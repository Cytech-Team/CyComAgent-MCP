package workflows

import (
	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"testing"
	"time"
)

func TestWorkflowDAGAndExecution(t *testing.T) {
	jm, e := jobs.New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(t.TempDir(), jm)
	if e != nil {
		t.Fatal(e)
	}
	w, e := m.Create("test", "sess", []Step{{ID: "a", Command: "true"}, {ID: "b", Command: "true", DependsOn: []string{"a"}}})
	if e != nil {
		t.Fatal(e)
	}
	w, e = m.Advance(w.ID, 2)
	if e != nil {
		t.Fatal(e)
	}
	if w.Steps[0].Status != "running" || w.Steps[1].Status != "pending" {
		t.Fatalf("unexpected initial dispatch: %+v", w.Steps)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w, e = m.Get(w.ID)
		if e != nil {
			t.Fatal(e)
		}
		if w.Steps[0].Status == "succeeded" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	w, e = m.Advance(w.ID, 2)
	if e != nil {
		t.Fatal(e)
	}
	if w.Steps[1].Status != "running" {
		t.Fatalf("dependent step not dispatched: %+v", w.Steps)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w, e = m.Get(w.ID)
		if e != nil {
			t.Fatal(e)
		}
		if w.Status == "succeeded" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("workflow did not complete: %+v", w)
}
func TestWorkflowRejectsCycles(t *testing.T) {
	jm, _ := jobs.New(t.TempDir())
	m, _ := New(t.TempDir(), jm)
	if _, e := m.Create("cycle", "", []Step{{ID: "a", Command: "true", DependsOn: []string{"b"}}, {ID: "b", Command: "true", DependsOn: []string{"a"}}}); e == nil {
		t.Fatal("cycle accepted")
	}
}

func TestDispatchReservationRecoversWithoutRetry(t *testing.T) {
	state := t.TempDir()
	jm, e := jobs.New(state)
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(state, jm)
	if e != nil {
		t.Fatal(e)
	}
	w, e := m.Create("recovery", "", []Step{{ID: "one", Command: "true"}})
	if e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	w.Steps[0].Status = "dispatching"
	if e = m.persist(w); e != nil {
		t.Fatal(e)
	}
	m.mu.Unlock()
	m2, e := New(state, jm)
	if e != nil {
		t.Fatal(e)
	}
	got, e := m2.Get(w.ID)
	if e != nil {
		t.Fatal(e)
	}
	if got.Steps[0].Status != "interrupted" {
		t.Fatalf("status=%s", got.Steps[0].Status)
	}
	if _, e = m2.Advance(w.ID, 1); e != nil {
		t.Fatal(e)
	}
	got, e = m2.Get(w.ID)
	if e != nil {
		t.Fatal(e)
	}
	if got.Steps[0].Status != "interrupted" {
		t.Fatalf("interrupted step was retried: %s", got.Steps[0].Status)
	}
}

func TestCancelStopsRunningJobAndPendingSteps(t *testing.T) {
	state := t.TempDir()
	jm, e := jobs.New(state)
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(state, jm)
	if e != nil {
		t.Fatal(e)
	}
	w, e := m.Create("cancel", "", []Step{{ID: "long", Command: "sleep 30"}, {ID: "later", Command: "true", DependsOn: []string{"long"}}})
	if e != nil {
		t.Fatal(e)
	}
	w, e = m.Advance(w.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	if w.Steps[0].Status != "running" {
		t.Fatalf("not running: %+v", w.Steps)
	}
	w, e = m.Cancel(w.ID)
	if e != nil {
		t.Fatal(e)
	}
	if w.Steps[1].Status != "cancelled" {
		t.Fatalf("pending step not cancelled: %+v", w.Steps)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w, e = m.Get(w.ID)
		if e != nil {
			t.Fatal(e)
		}
		if w.Status == "cancelled" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("workflow did not settle after cancellation: %+v", w)
}

func TestRetryRequiresExplicitActionAndTerminalJob(t *testing.T) {
	state := t.TempDir()
	jm, e := jobs.New(state)
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(state, jm)
	if e != nil {
		t.Fatal(e)
	}
	w, e := m.Create("retry", "", []Step{{ID: "bad", Command: "exit 7"}})
	if e != nil {
		t.Fatal(e)
	}
	w, e = m.Advance(w.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w, e = m.Get(w.ID)
		if e != nil {
			t.Fatal(e)
		}
		if w.Steps[0].Status == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if w.Steps[0].Status != "failed" {
		t.Fatalf("step not failed: %+v", w.Steps)
	}
	w, e = m.RetryStep(w.ID, "bad")
	if e != nil {
		t.Fatal(e)
	}
	if w.Steps[0].Status != "pending" || w.Steps[0].JobID != "" {
		t.Fatalf("retry did not reset step: %+v", w.Steps[0])
	}
	w, e = m.Advance(w.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	if w.Steps[0].Status != "running" {
		t.Fatalf("retry did not dispatch: %+v", w.Steps[0])
	}
}

func TestRetryRejectsAmbiguousDispatch(t *testing.T) {
	state := t.TempDir()
	jm, _ := jobs.New(state)
	m, _ := New(state, jm)
	w, _ := m.Create("ambiguous", "", []Step{{ID: "one", Command: "true"}})
	m.mu.Lock()
	w.Steps[0].Status = "interrupted"
	w.Steps[0].Error = "ambiguous"
	_ = m.persist(w)
	m.mu.Unlock()
	if _, e := m.RetryStep(w.ID, "one"); e == nil {
		t.Fatal("ambiguous dispatch was retried")
	}
}

func TestCancelledStepsNeverProduceSucceededWorkflow(t *testing.T) {
	jm, err := jobs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(t.TempDir(), jm)
	if err != nil {
		t.Fatal(err)
	}
	w := &Workflow{ID: "wf-all-cancelled", Status: "running", Steps: []Step{{ID: "a", Status: "cancelled"}, {ID: "b", Status: "cancelled"}}}
	m.reconcile(w)
	if w.Status != "cancelled" {
		t.Fatalf("all-cancelled workflow status=%q, want cancelled", w.Status)
	}
}
