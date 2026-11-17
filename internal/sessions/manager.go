package sessions

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	Created     = "created"
	Active      = "active"
	Idle        = "idle"
	Expired     = "expired"
	Closed      = "closed"
	Interrupted = "interrupted"
)

var ErrNotFound = errors.New("session not found")

type Session struct {
	ID             string     `json:"id"`
	ParentID       string     `json:"parent_id,omitempty"`
	Children       []string   `json:"children,omitempty"`
	Name           string     `json:"name,omitempty"`
	Owner          string     `json:"owner,omitempty"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastActivityAt time.Time  `json:"last_activity_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	TraceIDs       []string   `json:"trace_ids,omitempty"`
}

type diskState struct {
	Version  int                `json:"version"`
	Sessions map[string]Session `json:"sessions"`
}
type PrunePlan struct {
	Before     time.Time         `json:"before"`
	Candidates []string          `json:"candidates"`
	Protected  []string          `json:"protected"`
	Blocked    map[string]string `json:"blocked"`
}

type Manager struct {
	mu       sync.RWMutex
	path     string
	sessions map[string]Session
}

func New(dir string) (*Manager, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("session state directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	m := &Manager{path: filepath.Join(dir, "sessions.json"), sessions: map[string]Session{}}
	data, err := os.ReadFile(m.path)
	if err == nil {
		var saved diskState
		if err = json.Unmarshal(data, &saved); err != nil {
			return nil, fmt.Errorf("decode session store: %w", err)
		}
		if saved.Sessions != nil {
			m.sessions = saved.Sessions
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// A runtime restart must not silently resurrect work as active.
	changed := false
	now := time.Now().UTC()
	for id, s := range m.sessions {
		if s.Status == Active || s.Status == Created {
			s.Status = Interrupted
			s.UpdatedAt = now
			m.sessions[id] = s
			changed = true
		}
	}
	if changed {
		if err = m.persistLocked(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) Create(name, owner, parentID string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name = strings.TrimSpace(name)
	owner = strings.TrimSpace(owner)
	if len(name) > 120 || len(owner) > 120 {
		return Session{}, fmt.Errorf("name and owner must be at most 120 characters")
	}
	var parent Session
	if parentID != "" {
		var ok bool
		parent, ok = m.sessions[parentID]
		if !ok {
			return Session{}, fmt.Errorf("parent: %w", ErrNotFound)
		}
		if terminal(parent.Status) {
			return Session{}, fmt.Errorf("parent session is terminal")
		}
	}
	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	s := Session{ID: id, ParentID: parentID, Name: name, Owner: owner, Status: Created, CreatedAt: now, UpdatedAt: now, LastActivityAt: now}
	m.sessions[id] = s
	if parentID != "" {
		parent.Children = append(parent.Children, id)
		parent.UpdatedAt = now
		m.sessions[parentID] = parent
	}
	if err = m.persistLocked(); err != nil {
		delete(m.sessions, id)
		if parentID != "" {
			parent.Children = remove(parent.Children, id)
			m.sessions[parentID] = parent
		}
		return Session{}, err
	}
	return clone(s), nil
}

func (m *Manager) Get(id string) (Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	return clone(s), nil
}
func (m *Manager) List() []Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, clone(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
func (m *Manager) Transition(id, status string) (Session, error) {
	if status != Active && status != Idle && status != Expired && status != Closed {
		return Session{}, fmt.Errorf("unsupported session status %q", status)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	if terminal(s.Status) {
		return Session{}, fmt.Errorf("session is terminal: %s", s.Status)
	}
	if status == Expired || status == Closed {
		for _, childID := range s.Children {
			child := m.sessions[childID]
			if !terminal(child.Status) {
				return Session{}, fmt.Errorf("session has non-terminal child %s", childID)
			}
		}
	}
	now := time.Now().UTC()
	s.Status = status
	s.UpdatedAt = now
	s.LastActivityAt = now
	if status == Closed || status == Expired {
		s.ClosedAt = &now
	}
	m.sessions[id] = s
	if err := m.persistLocked(); err != nil {
		return Session{}, err
	}
	return clone(s), nil
}
func (m *Manager) Touch(id string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	if terminal(s.Status) {
		return Session{}, fmt.Errorf("session is terminal")
	}
	now := time.Now().UTC()
	s.LastActivityAt = now
	s.UpdatedAt = now
	if s.Status == Created || s.Status == Idle || s.Status == Interrupted {
		s.Status = Active
	}
	m.sessions[id] = s
	if err := m.persistLocked(); err != nil {
		return Session{}, err
	}
	return clone(s), nil
}
func (m *Manager) AttachTrace(id, traceID string) (Session, error) {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" || len(traceID) > 256 {
		return Session{}, fmt.Errorf("valid trace ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	if terminal(s.Status) {
		return Session{}, fmt.Errorf("session is terminal")
	}
	for _, v := range s.TraceIDs {
		if v == traceID {
			return clone(s), nil
		}
	}
	s.TraceIDs = append(s.TraceIDs, traceID)
	s.UpdatedAt = time.Now().UTC()
	m.sessions[id] = s
	if err := m.persistLocked(); err != nil {
		return Session{}, err
	}
	return clone(s), nil
}
func (m *Manager) persistLocked() error {
	data, err := json.MarshalIndent(diskState{Version: 1, Sessions: m.sessions}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".sessions-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, m.path)
}
func terminal(s string) bool { return s == Closed || s == Expired }
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "ses_" + hex.EncodeToString(b[:]), nil
}
func clone(s Session) Session {
	s.Children = append([]string(nil), s.Children...)
	s.TraceIDs = append([]string(nil), s.TraceIDs...)
	if s.ClosedAt != nil {
		v := *s.ClosedAt
		s.ClosedAt = &v
	}
	return s
}
func remove(in []string, v string) []string {
	out := in[:0]
	for _, x := range in {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// PlanPrune selects only terminal leaf sessions older than before. Protected IDs
// are sessions referenced by live runtime work and are never candidates.
func (m *Manager) PlanPrune(before time.Time, protected map[string]bool) PrunePlan {
	m.mu.RLock()
	defer m.mu.RUnlock()
	plan := PrunePlan{Before: before.UTC(), Candidates: []string{}, Protected: []string{}, Blocked: map[string]string{}}
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := m.sessions[id]
		if !terminal(s.Status) {
			continue
		}
		stamp := s.ClosedAt
		if stamp == nil {
			v := s.UpdatedAt
			stamp = &v
		}
		if stamp.After(before) {
			continue
		}
		if protected[id] {
			plan.Protected = append(plan.Protected, id)
			plan.Blocked[id] = "referenced by active runtime work"
			continue
		}
		hasChild := false
		for _, childID := range s.Children {
			if _, ok := m.sessions[childID]; ok {
				hasChild = true
				break
			}
		}
		if hasChild {
			plan.Blocked[id] = "retained while child session metadata exists"
			continue
		}
		plan.Candidates = append(plan.Candidates, id)
	}
	return plan
}

// Prune removes exactly the reviewed candidate IDs, rechecking all safety
// conditions under the manager lock. It never cascades into child sessions.
func (m *Manager) Prune(ids []string, before time.Time, protected map[string]bool) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := make([]string, 0, len(ids))
	original := make(map[string]Session, len(m.sessions))
	for key, value := range m.sessions {
		original[key] = clone(value)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		s, ok := m.sessions[id]
		if !ok {
			continue
		}
		if !terminal(s.Status) {
			return removed, fmt.Errorf("session %s is not terminal", id)
		}
		stamp := s.ClosedAt
		if stamp == nil {
			v := s.UpdatedAt
			stamp = &v
		}
		if stamp.After(before) {
			return removed, fmt.Errorf("session %s is newer than retention cutoff", id)
		}
		if protected[id] {
			return removed, fmt.Errorf("session %s is protected by active work", id)
		}
		for _, childID := range s.Children {
			if _, ok := m.sessions[childID]; ok {
				return removed, fmt.Errorf("session %s still has retained child metadata", id)
			}
		}
		delete(m.sessions, id)
		if s.ParentID != "" {
			parent := m.sessions[s.ParentID]
			parent.Children = remove(parent.Children, id)
			parent.UpdatedAt = time.Now().UTC()
			m.sessions[s.ParentID] = parent
		}
		removed = append(removed, id)
	}
	if len(removed) > 0 {
		if err := m.persistLocked(); err != nil {
			m.sessions = original
			return nil, err
		}
	}
	return removed, nil
}
