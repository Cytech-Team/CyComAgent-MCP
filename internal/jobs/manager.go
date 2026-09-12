package jobs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
)

type Job struct {
	ID               string            `json:"id"`
	Command          string            `json:"command"`
	Cwd              string            `json:"cwd,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Shell            string            `json:"shell"`
	PID              int               `json:"pid"`
	PGID             int               `json:"pgid,omitempty"`
	Status           string            `json:"status"`
	ExitCode         *int              `json:"exit_code,omitempty"`
	LogFile          string            `json:"log_file"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	Error            string            `json:"error,omitempty"`
	ExecutionContext string            `json:"execution_context,omitempty"`
}

type Manager struct {
	dir  string
	mu   sync.RWMutex
	jobs map[string]*Job
	seq  atomic.Uint64
}

func New(stateDir string) (*Manager, error) {
	dir := filepath.Join(stateDir, "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{dir: dir, jobs: make(map[string]*Job)}
	if err := m.recover(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Start(command, cwd, shell string, env map[string]string) (*Job, error) {
	return m.StartWithContext(command, cwd, shell, env, "service")
}

func (m *Manager) StartWithContext(command, cwd, shell string, env map[string]string, executionContext string) (*Job, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command is required")
	}
	if shell == "" {
		shell = platform.DefaultShell()
	}
	if strings.TrimSpace(executionContext) == "" {
		executionContext = "service"
	}
	id := fmt.Sprintf("job-%d-%d", time.Now().UnixMilli(), m.seq.Add(1))
	logFile := filepath.Join(m.dir, id+".log")
	logf, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(shell, "-lc", command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = mergedEnv(env)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return nil, err
	}
	pgid, _ := syscall.Getpgid(cmd.Process.Pid)
	j := m.recordStartedJob(id, command, cwd, shell, env, executionContext, logFile, cmd.Process.Pid, pgid)

	go func() {
		err := cmd.Wait()
		_ = logf.Close()
		m.finishJob(id, err)
	}()
	return clone(j), nil
}

// StartExternal registers a process launched by another execution domain (for
// example the graphical session bridge). The durable job store still owns the
// metadata, logs and signalling surface even though the runtime is not the
// process parent and therefore cannot recover an exact exit status.
func (m *Manager) StartExternal(command, cwd, shell string, env map[string]string, executionContext string, launch func(logFile string) (pid, pgid int, err error)) (*Job, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command is required")
	}
	if launch == nil {
		return nil, fmt.Errorf("external job launcher is required")
	}
	if shell == "" {
		shell = platform.DefaultShell()
	}
	if strings.TrimSpace(executionContext) == "" {
		executionContext = "user"
	}
	id := fmt.Sprintf("job-%d-%d", time.Now().UnixMilli(), m.seq.Add(1))
	logFile := filepath.Join(m.dir, id+".log")
	// Create the log with the same private mode before handing the path to the
	// session bridge. This also fails early if the state directory is unusable.
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	pid, pgid, err := launch(logFile)
	if err != nil {
		_ = os.Remove(logFile)
		return nil, err
	}
	if pid <= 0 {
		_ = os.Remove(logFile)
		return nil, fmt.Errorf("external launcher returned invalid pid %d", pid)
	}
	j := m.recordStartedJob(id, command, cwd, shell, env, executionContext, logFile, pid, pgid)
	go m.monitorExternal(id, pid)
	return clone(j), nil
}

func (m *Manager) recordStartedJob(id, command, cwd, shell string, env map[string]string, executionContext, logFile string, pid, pgid int) *Job {
	now := time.Now().UTC()
	j := &Job{
		ID: id, Command: command, Cwd: cwd, Env: env, Shell: shell,
		PID: pid, PGID: pgid, Status: "running", LogFile: logFile,
		CreatedAt: now, UpdatedAt: now, ExecutionContext: executionContext,
	}
	m.mu.Lock()
	m.jobs[id] = j
	_ = m.persistLocked(j)
	m.mu.Unlock()
	return j
}

func (m *Manager) finishJob(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.jobs[id]
	if current == nil {
		return
	}
	current.UpdatedAt = time.Now().UTC()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
			current.Error = err.Error()
		}
	}
	current.ExitCode = &code
	if current.Status == "terminating" {
		current.Status = "terminated"
	} else {
		current.Status = "exited"
	}
	_ = m.persistLocked(current)
}

func (m *Manager) monitorExternal(id string, pid int) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if processAlive(pid) {
			continue
		}
		m.mu.Lock()
		j := m.jobs[id]
		if j != nil && (j.Status == "running" || j.Status == "running-recovered" || j.Status == "terminating") {
			code := -1
			j.ExitCode = &code
			if j.Status == "terminating" {
				j.Status = "terminated"
			} else {
				j.Status = "exited-external"
			}
			j.UpdatedAt = time.Now().UTC()
			_ = m.persistLocked(j)
		}
		m.mu.Unlock()
		return
	}
}

func (m *Manager) List() []*Job {
	m.mu.RLock()
	out := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, clone(j))
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (m *Manager) Get(id string) (*Job, error) {
	m.mu.RLock()
	j := m.jobs[id]
	m.mu.RUnlock()
	if j == nil {
		return nil, os.ErrNotExist
	}
	return clone(j), nil
}

func (m *Manager) Signal(id, signal string) (*Job, error) {
	sig, err := parseSignal(signal)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	j := m.jobs[id]
	if j == nil {
		m.mu.Unlock()
		return nil, os.ErrNotExist
	}
	target := j.PID
	if j.PGID > 0 {
		target = -j.PGID
	}
	if sig == syscall.SIGTERM || sig == syscall.SIGKILL || sig == syscall.SIGINT {
		j.Status = "terminating"
	}
	j.UpdatedAt = time.Now().UTC()
	_ = m.persistLocked(j)
	m.mu.Unlock()
	if err := syscall.Kill(target, sig); err != nil {
		return nil, err
	}
	return m.Get(id)
}

func (m *Manager) Tail(id string, lines, maxBytes int) (string, bool, error) {
	j, err := m.Get(id)
	if err != nil {
		return "", false, err
	}
	if lines <= 0 {
		lines = 200
	}
	if maxBytes <= 0 || maxBytes > 2<<20 {
		maxBytes = 256 << 10
	}
	f, err := os.Open(j.LogFile)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	start := st.Size() - int64(maxBytes)
	truncated := start > 0
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", false, err
	}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64<<10), 2<<20)
	ring := make([]string, 0, lines)
	for scan.Scan() {
		if len(ring) == lines {
			copy(ring, ring[1:])
			ring[len(ring)-1] = scan.Text()
		} else {
			ring = append(ring, scan.Text())
		}
	}
	if err := scan.Err(); err != nil {
		return "", truncated, err
	}
	return strings.Join(ring, "\n"), truncated, nil
}

func (m *Manager) Prune(olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		olderThan = 7 * 24 * time.Hour
	}
	cutoff := time.Now().Add(-olderThan)
	removed := 0
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, j := range m.jobs {
		if strings.HasPrefix(j.Status, "running") || j.Status == "terminating" || j.UpdatedAt.After(cutoff) {
			continue
		}
		_ = os.Remove(j.LogFile)
		_ = os.Remove(filepath.Join(m.dir, id+".json"))
		delete(m.jobs, id)
		removed++
	}
	return removed, nil
}

func (m *Manager) recover() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.dir, e.Name()))
		if err != nil {
			continue
		}
		var j Job
		if json.Unmarshal(data, &j) != nil || j.ID == "" {
			continue
		}
		if j.Status == "running" || j.Status == "terminating" || j.Status == "running-recovered" {
			if processAlive(j.PID) {
				j.Status = "running-recovered"
			} else {
				j.Status = "exited-while-runtime-offline"
				code := -1
				j.ExitCode = &code
			}
			j.UpdatedAt = time.Now().UTC()
		}
		c := j
		m.jobs[j.ID] = &c
		_ = m.persistLocked(&c)
		if c.Status == "running-recovered" {
			go m.monitorRecovered(c.ID, c.PID)
		}
	}
	return nil
}

func (m *Manager) monitorRecovered(id string, pid int) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if processAlive(pid) {
			continue
		}
		m.mu.Lock()
		j := m.jobs[id]
		if j != nil && j.Status == "running-recovered" {
			code := -1
			j.ExitCode = &code
			j.Status = "exited-after-recovery"
			j.UpdatedAt = time.Now().UTC()
			_ = m.persistLocked(j)
		}
		m.mu.Unlock()
		return
	}
}

func (m *Manager) persistLocked(j *Job) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.dir, j.ID+".json")
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func clone(j *Job) *Job {
	if j == nil {
		return nil
	}
	c := *j
	if j.Env != nil {
		c.Env = make(map[string]string, len(j.Env))
		for k, v := range j.Env {
			c.Env[k] = v
		}
	}
	return &c
}

func parseSignal(s string) (syscall.Signal, error) {
	switch strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(s), "SIG")) {
	case "", "TERM":
		return syscall.SIGTERM, nil
	case "INT":
		return syscall.SIGINT, nil
	case "HUP":
		return syscall.SIGHUP, nil
	case "KILL":
		return syscall.SIGKILL, nil
	case "USR1":
		return syscall.SIGUSR1, nil
	case "USR2":
		return syscall.SIGUSR2, nil
	default:
		return 0, fmt.Errorf("unsupported signal %q", s)
	}
}

func mergedEnv(extra map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
