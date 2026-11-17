package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLifecycleHierarchyAndRecovery(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := m.Create("root", "agent", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.Create("child", "agent", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Transition(root.ID, Closed); err == nil {
		t.Fatal("parent closed with active child")
	}
	if _, err = m.Touch(child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Transition(child.ID, Closed); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Transition(root.ID, Active); err != nil {
		t.Fatal(err)
	}
	m2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m2.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Interrupted {
		t.Fatalf("active session recovered as %q", got.Status)
	}
	gotChild, err := m2.Get(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotChild.Status != Closed {
		t.Fatalf("closed child recovered as %q", gotChild.Status)
	}
}
func TestTraceAttachAndDefensiveCopies(t *testing.T) {
	m, _ := New(t.TempDir())
	s, _ := m.Create("x", "owner", "")
	s, err := m.AttachTrace(s.ID, "trace-1")
	if err != nil {
		t.Fatal(err)
	}
	s.TraceIDs[0] = "mutated"
	got, _ := m.Get(s.ID)
	if got.TraceIDs[0] != "trace-1" {
		t.Fatal("returned session aliases manager state")
	}
	if _, err = m.AttachTrace(s.ID, "trace-1"); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 1 {
		t.Fatal("unexpected session count")
	}
}
func TestCorruptStoreFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil {
		t.Fatal("expected corrupt store error")
	}
}

func TestPruneDryRunAndLeafOnlySafety(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := m.Create("parent", "agent", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.Create("child", "agent", parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Transition(child.ID, Closed); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Transition(parent.ID, Closed); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	m.mu.Lock()
	p := m.sessions[parent.ID]
	p.ClosedAt = &old
	m.sessions[parent.ID] = p
	c := m.sessions[child.ID]
	c.ClosedAt = &old
	m.sessions[child.ID] = c
	if err = m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	plan := m.PlanPrune(cutoff, nil)
	if len(plan.Candidates) != 1 || plan.Candidates[0] != child.ID {
		t.Fatalf("expected only leaf child candidate, got %+v", plan)
	}
	if _, err = m.Get(parent.ID); err != nil {
		t.Fatal("dry-run mutated parent", err)
	}
	if _, err = m.Prune(plan.Candidates, cutoff, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Get(child.ID); err == nil {
		t.Fatal("child was not pruned")
	}
	got, err := m.Get(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Children) != 0 {
		t.Fatalf("parent retains dangling child reference: %+v", got.Children)
	}
	plan = m.PlanPrune(cutoff, nil)
	if len(plan.Candidates) != 1 || plan.Candidates[0] != parent.ID {
		t.Fatalf("parent should become candidate on next pass: %+v", plan)
	}
}

func TestPruneProtectsLiveWorkAndRechecksBeforeDelete(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Create("protected", "agent", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Transition(s.ID, Closed); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-72 * time.Hour)
	m.mu.Lock()
	v := m.sessions[s.ID]
	v.ClosedAt = &old
	m.sessions[s.ID] = v
	if err = m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	plan := m.PlanPrune(cutoff, map[string]bool{s.ID: true})
	if len(plan.Candidates) != 0 || len(plan.Protected) != 1 {
		t.Fatalf("protected session appeared as candidate: %+v", plan)
	}
	if _, err = m.Prune([]string{s.ID}, cutoff, map[string]bool{s.ID: true}); err == nil {
		t.Fatal("prune ignored active-work protection")
	}
	if _, err = m.Get(s.ID); err != nil {
		t.Fatal("protected session was deleted")
	}
}
