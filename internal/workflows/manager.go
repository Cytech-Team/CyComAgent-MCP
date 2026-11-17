package workflows

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
)

type Step struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Cwd       string    `json:"cwd,omitempty"`
	Shell     string    `json:"shell,omitempty"`
	DependsOn []string  `json:"depends_on,omitempty"`
	Status    string    `json:"status"`
	JobID     string    `json:"job_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Workflow struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SessionID string    `json:"session_id,omitempty"`
	Status    string    `json:"status"`
	Steps     []Step    `json:"steps"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Manager struct {
	dir   string
	mu    sync.Mutex
	items map[string]*Workflow
	jobs  *jobs.Manager
}

func New(stateDir string, jm *jobs.Manager) (*Manager, error) {
	d := filepath.Join(stateDir, "workflows")
	if err := os.MkdirAll(d, 0700); err != nil {
		return nil, err
	}
	m := &Manager{dir: d, items: map[string]*Workflow{}, jobs: jm}
	es, err := os.ReadDir(d)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d, e.Name()))
		if err != nil {
			return nil, err
		}
		var w Workflow
		if err = json.Unmarshal(b, &w); err != nil {
			return nil, fmt.Errorf("workflow %s: %w", e.Name(), err)
		}
		if w.ID == "" {
			return nil, fmt.Errorf("workflow %s has no id", e.Name())
		}
		m.items[w.ID] = &w
	}
	return m, nil
}
func (m *Manager) Create(name, sessionID string, steps []Step) (*Workflow, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	if len(steps) == 0 || len(steps) > 100 {
		return nil, fmt.Errorf("workflow must contain 1 to 100 steps")
	}
	ids := map[string]bool{}
	for i := range steps {
		s := &steps[i]
		s.ID = strings.TrimSpace(s.ID)
		if s.ID == "" || ids[s.ID] {
			return nil, fmt.Errorf("step IDs must be non-empty and unique")
		}
		ids[s.ID] = true
		if strings.TrimSpace(s.Command) == "" {
			return nil, fmt.Errorf("step %s command is required", s.ID)
		}
		s.Status = "pending"
	}
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			if !ids[dep] || dep == s.ID {
				return nil, fmt.Errorf("step %s has invalid dependency %s", s.ID, dep)
			}
		}
	}
	if hasCycle(steps) {
		return nil, fmt.Errorf("workflow dependency graph contains a cycle")
	}
	now := time.Now().UTC()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	w := &Workflow{ID: "wf-" + hex.EncodeToString(raw[:]), Name: name, SessionID: sessionID, Status: "pending", Steps: steps, CreatedAt: now, UpdatedAt: now}
	for i := range w.Steps {
		w.Steps[i].CreatedAt = now
		w.Steps[i].UpdatedAt = now
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[w.ID] = w
	if err := m.persist(w); err != nil {
		delete(m.items, w.ID)
		return nil, err
	}
	return clone(w), nil
}
func hasCycle(steps []Step) bool {
	deps := map[string][]string{}
	for _, s := range steps {
		deps[s.ID] = s.DependsOn
	}
	state := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		if state[id] == 1 {
			return true
		}
		if state[id] == 2 {
			return false
		}
		state[id] = 1
		for _, d := range deps[id] {
			if visit(d) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for id := range deps {
		if visit(id) {
			return true
		}
	}
	return false
}
func (m *Manager) Get(id string) (*Workflow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.items[id]
	if w == nil {
		return nil, os.ErrNotExist
	}
	m.reconcile(w)
	if err := m.persist(w); err != nil {
		return nil, err
	}
	return clone(w), nil
}
func (m *Manager) List() ([]*Workflow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Workflow, 0, len(m.items))
	for _, w := range m.items {
		m.reconcile(w)
		if err := m.persist(w); err != nil {
			return nil, err
		}
		out = append(out, clone(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Advance dispatches only dependency-ready pending steps. It never retries a step
// with an existing JobID and launches commands in the service execution domain.
func (m *Manager) Advance(id string, maxParallel int) (*Workflow, error) {
	if maxParallel <= 0 {
		maxParallel = 1
	}
	if maxParallel > 16 {
		maxParallel = 16
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.items[id]
	if w == nil {
		return nil, os.ErrNotExist
	}
	m.reconcile(w)
	running := 0
	for _, s := range w.Steps {
		if s.Status == "running" {
			running++
		}
	}
	for i := range w.Steps {
		if running >= maxParallel {
			break
		}
		s := &w.Steps[i]
		if s.Status != "pending" || s.JobID != "" {
			continue
		}
		ready := true
		for _, d := range s.DependsOn {
			if stepStatus(w, d) != "succeeded" {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		// Persist a dispatch reservation before starting the process. If the
		// runtime crashes in the launch window, recovery marks it interrupted
		// instead of risking duplicate execution.
		s.Status = "dispatching"
		s.UpdatedAt = time.Now().UTC()
		if err := m.persist(w); err != nil {
			s.Status = "pending"
			return clone(w), err
		}
		j, err := m.jobs.StartWithSession(s.Command, s.Cwd, s.Shell, nil, "service", w.SessionID)
		if err != nil {
			s.Status = "failed"
			s.Error = err.Error()
			s.UpdatedAt = time.Now().UTC()
			continue
		}
		s.JobID = j.ID
		s.Status = "running"
		s.UpdatedAt = time.Now().UTC()
		running++
	}
	m.reconcile(w)
	if err := m.persist(w); err != nil {
		return nil, err
	}
	return clone(w), nil
}
func (m *Manager) reconcile(w *Workflow) {
	now := time.Now().UTC()
	for i := range w.Steps {
		s := &w.Steps[i]
		if s.Status == "dispatching" && s.JobID == "" {
			s.Status = "interrupted"
			s.Error = "runtime stopped during dispatch; automatic retry is disabled"
			s.UpdatedAt = now
			continue
		}
		if s.Status != "running" || s.JobID == "" {
			continue
		}
		j, err := m.jobs.Get(s.JobID)
		if err != nil {
			s.Status = "interrupted"
			s.Error = "linked Job is missing; automatic retry is disabled"
			s.UpdatedAt = now
			continue
		}
		switch j.Status {
		case "exited", "exited-external", "exited-while-runtime-offline", "exited-after-recovery":
			if j.ExitCode != nil && *j.ExitCode == 0 {
				s.Status = "succeeded"
			} else {
				s.Status = "failed"
				s.Error = fmt.Sprintf("Job finished with exit code %v", j.ExitCode)
			}
			s.UpdatedAt = now
		case "terminated":
			s.Status = "cancelled"
			s.UpdatedAt = now
		}
	}
	failed := false
	cancelled := true
	interrupted := false
	allSucceeded := true
	running := false
	for _, s := range w.Steps {
		if s.Status != "succeeded" {
			allSucceeded = false
		}
		switch s.Status {
		case "failed":
			failed = true
			cancelled = false
		case "interrupted":
			failed = true
			interrupted = true
			cancelled = false
		case "cancelled":
		case "running", "dispatching":
			running = true
			cancelled = false
		case "pending":
			cancelled = false
		}
	}
	if w.Status == "cancelling" && !running {
		if interrupted {
			w.Status = "interrupted"
		} else {
			w.Status = "cancelled"
		}
	} else if w.Status == "cancelling" {
		// Keep cancellation in progress until all process groups reach terminal state.
	} else if failed {
		w.Status = "failed"
	} else if allSucceeded {
		w.Status = "succeeded"
	} else if running {
		w.Status = "running"
	} else if cancelled {
		w.Status = "cancelled"
	} else {
		w.Status = "pending"
	}
	w.UpdatedAt = now
}
func stepStatus(w *Workflow, id string) string {
	for _, s := range w.Steps {
		if s.ID == id {
			return s.Status
		}
	}
	return ""
}
func (m *Manager) persist(w *Workflow) error {
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(m.dir, w.ID+".json")
	tmp := p + ".tmp"
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
func clone(w *Workflow) *Workflow {
	b, _ := json.Marshal(w)
	var c Workflow
	_ = json.Unmarshal(b, &c)
	return &c
}

// Cancel prevents further dispatch, marks pending steps cancelled, and sends
// TERM to each currently running Job. The Workflow remains cancelling until
// reconciliation confirms that process groups have stopped.
func (m *Manager) Cancel(id string) (*Workflow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.items[id]
	if w == nil {
		return nil, os.ErrNotExist
	}
	m.reconcile(w)
	if w.Status == "succeeded" || w.Status == "failed" || w.Status == "cancelled" || w.Status == "interrupted" {
		return clone(w), fmt.Errorf("workflow is already terminal: %s", w.Status)
	}
	w.Status = "cancelling"
	var signalErrors []string
	for i := range w.Steps {
		s := &w.Steps[i]
		switch s.Status {
		case "pending":
			s.Status = "cancelled"
			s.UpdatedAt = time.Now().UTC()
		case "dispatching":
			s.Status = "interrupted"
			s.Error = "cancellation requested during dispatch; execution outcome is ambiguous"
			s.UpdatedAt = time.Now().UTC()
		case "running":
			if s.JobID == "" {
				s.Status = "interrupted"
				s.Error = "running step has no Job ID; cancellation outcome is ambiguous"
				s.UpdatedAt = time.Now().UTC()
				continue
			}
			if _, err := m.jobs.Signal(s.JobID, "TERM"); err != nil {
				signalErrors = append(signalErrors, s.ID+": "+err.Error())
			}
		}
	}
	m.reconcile(w)
	if err := m.persist(w); err != nil {
		return nil, err
	}
	if len(signalErrors) > 0 {
		return clone(w), fmt.Errorf("workflow cancellation partially signalled: %s", strings.Join(signalErrors, "; "))
	}
	return clone(w), nil
}

// RetryStep is an explicit operator action. It only resets a failed/cancelled
// step or an interrupted step with a known Job ID; dependency gating is applied
// again by Advance. No Job is automatically replayed.
func (m *Manager) RetryStep(id, stepID string) (*Workflow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.items[id]
	if w == nil {
		return nil, os.ErrNotExist
	}
	m.reconcile(w)
	if w.Status == "cancelling" {
		return nil, fmt.Errorf("workflow cancellation is still in progress")
	}
	var target *Step
	for i := range w.Steps {
		if w.Steps[i].ID == stepID {
			target = &w.Steps[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("step %s not found", stepID)
	}
	if target.Status != "failed" && target.Status != "cancelled" && target.Status != "interrupted" {
		return nil, fmt.Errorf("step %s is not retryable from status %s", stepID, target.Status)
	}
	if target.Status == "interrupted" && target.JobID == "" {
		return nil, fmt.Errorf("step %s has ambiguous dispatch outcome and cannot be retried safely", stepID)
	}
	for _, dep := range target.DependsOn {
		if stepStatus(w, dep) != "succeeded" {
			return nil, fmt.Errorf("dependency %s is not successful", dep)
		}
	}
	if target.JobID != "" {
		j, err := m.jobs.Get(target.JobID)
		if err == nil && (j.Status == "running" || j.Status == "running-recovered" || j.Status == "terminating" || j.Status == "terminating-recovered") {
			return nil, fmt.Errorf("linked Job %s is still active", target.JobID)
		}
	}
	target.Status = "pending"
	target.JobID = ""
	target.Error = ""
	target.UpdatedAt = time.Now().UTC()
	w.Status = "pending"
	w.UpdatedAt = time.Now().UTC()
	if err := m.persist(w); err != nil {
		return nil, err
	}
	return clone(w), nil
}
