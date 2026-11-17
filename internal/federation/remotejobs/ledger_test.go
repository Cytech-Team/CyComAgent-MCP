package remotejobs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReserveIsIdempotentAndRejectsPayloadMutation(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := l.Reserve("node-a", "request-1", `{"command":"echo hi"}`)
	if err != nil || !created {
		t.Fatalf("first reserve: created=%v err=%v", created, err)
	}
	if !strings.HasPrefix(first.JobID, "rjob-") || first.JobID == "rjob-" {
		t.Fatalf("missing deterministic reserved job ID: %q", first.JobID)
	}
	second, created, err := l.Reserve("node-a", "request-1", `{"command":"echo hi"}`)
	if err != nil || created || first.PayloadHash != second.PayloadHash || first.CreatedAt != second.CreatedAt {
		t.Fatalf("duplicate reserve mismatch: %#v %#v %v %v", first, second, created, err)
	}
	if _, _, err := l.Reserve("node-a", "request-1", `{"command":"rm -rf /"}`); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected payload conflict, got %v", err)
	}
}

func TestConcurrentReserveCreatesExactlyOneRecord(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	errs := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fresh, err := l.Reserve("node-a", "same-key", "payload")
			mu.Lock()
			defer mu.Unlock()
			if fresh {
				created++
			}
			if err != nil {
				errs++
			}
		}()
	}
	wg.Wait()
	if created != 1 || errs != 0 {
		t.Fatalf("created=%d errors=%d", created, errs)
	}
}

func TestRecoveryMarksDispatchingUnknownAndNeverRequeues(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Reserve("node-a", "request-2", "payload"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Transition("node-a", "request-2", "dispatching", "", ""); err != nil {
		t.Fatal(err)
	}
	// Simulate a dead owner by persisting a PID that cannot exist.
	if err := l.withLock(func() error {
		r := l.records[recordKey("node-a", "request-2")]
		r.OwnerPID = 2147483647
		return l.persist(r)
	}); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := l2.Get("node-a", "request-2")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "unknown" {
		t.Fatalf("recovered status=%q", rec.Status)
	}
	if _, err := l2.Transition("node-a", "request-2", "dispatching", "", ""); !errors.Is(err, ErrTransition) {
		t.Fatalf("unknown must not dispatch again: %v", err)
	}
	if _, err := l2.Transition("node-a", "request-2", "completed", "", "reconciled"); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionPersistsJobAndTerminalState(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Reserve("node-a", "request-3", "payload"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"dispatching", "running", "completed"} {
		if _, err := l.Transition("node-a", "request-3", status, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	l2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := l2.Get("node-a", "request-3")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || !strings.HasPrefix(got.JobID, "rjob-") {
		t.Fatalf("unexpected record: %#v", got)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "federation", "remote-jobs"))
	if err != nil {
		t.Fatal(err)
	}
	jsonFiles := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			jsonFiles++
		}
	}
	if jsonFiles != 1 {
		t.Fatalf("ledger JSON files=%d entries=%v", jsonFiles, entries)
	}
}

func TestTerminalStatesCannotBeReopened(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Reserve("node-a", "request-4", "payload"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"dispatching", "failed"} {
		if _, err := l.Transition("node-a", "request-4", status, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Transition("node-a", "request-4", "running", "job-2", ""); !errors.Is(err, ErrTransition) {
		t.Fatalf("terminal record reopened: %v", err)
	}
}

func TestLedgerSubprocessHelper(t *testing.T) {
	dir := os.Getenv("CYCOM_LEDGER_HELPER_DIR")
	if dir == "" {
		return
	}
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := l.Reserve("node-subprocess", "shared-request", "identical-payload")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		fmt.Fprint(os.Stdout, "created")
	} else {
		fmt.Fprint(os.Stdout, "existing")
	}
}

func TestCrossProcessReserveIsSerialized(t *testing.T) {
	dir := t.TempDir()
	const workers = 24
	var wg sync.WaitGroup
	outputs := make(chan string, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestLedgerSubprocessHelper$")
			cmd.Env = append(os.Environ(), "CYCOM_LEDGER_HELPER_DIR="+dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				errs <- fmt.Errorf("helper: %w: %s", err, out)
				return
			}
			value := strings.TrimSpace(string(out))
			if strings.HasPrefix(value, "created") {
				outputs <- "created"
			} else if strings.HasPrefix(value, "existing") {
				outputs <- "existing"
			} else {
				errs <- fmt.Errorf("unexpected helper output %q", value)
			}
		}()
	}
	wg.Wait()
	close(outputs)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	created := 0
	existing := 0
	for out := range outputs {
		switch out {
		case "created":
			created++
		case "existing":
			existing++
		default:
			t.Fatalf("unexpected helper output %q", out)
		}
	}
	if created != 1 || existing != workers-1 {
		t.Fatalf("created=%d existing=%d", created, existing)
	}
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := l.Get("node-subprocess", "shared-request")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "accepted" {
		t.Fatalf("unexpected persisted status %q", rec.Status)
	}
}

func TestRecoveryRejectsReusedOwnerPID(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Reserve("node-a", "pid-reuse", "payload"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Transition("node-a", "pid-reuse", "dispatching", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := l.withLock(func() error {
		r := l.records[recordKey("node-a", "pid-reuse")]
		r.OwnerStartTicks++ // Same PID, but a different process incarnation.
		return l.persist(r)
	}); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := l2.Get("node-a", "pid-reuse")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "unknown" {
		t.Fatalf("PID reuse not detected, status=%q", rec.Status)
	}
}

func TestLedgerCrashAfterDispatchIntentRecoversUnknownWithoutReplay(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLedgerCrashDispatchHelper$")
	cmd.Env = append(os.Environ(), "CYCOM_LEDGER_CRASH_DIR="+dir)
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 93 {
		t.Fatalf("dispatch helper did not crash after durable intent: %v", err)
	}
	// Opening a fresh ledger models a receiver Runtime restart.
	recovered, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := recovered.Get("node-crash-caller", "dispatch-crash")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "unknown" {
		t.Fatalf("recovered status=%q, want unknown", rec.Status)
	}
	if rec.OwnerPID != 0 {
		t.Fatalf("stale dispatch owner retained: %d", rec.OwnerPID)
	}
	if _, err := recovered.Transition(rec.CallerNodeID, rec.RequestID, "dispatching", "", ""); !errors.Is(err, ErrTransition) {
		t.Fatalf("recovered request was re-dispatchable: %v", err)
	}
	// Same signed reservation retry is still idempotent and cannot reset state.
	retry, created, err := recovered.Reserve(rec.CallerNodeID, rec.RequestID, "crash-payload")
	if err != nil || created || retry.Status != "unknown" {
		t.Fatalf("retry changed recovery state: created=%v record=%#v err=%v", created, retry, err)
	}
}

func TestLedgerCrashDispatchHelper(t *testing.T) {
	dir := os.Getenv("CYCOM_LEDGER_CRASH_DIR")
	if dir == "" {
		return
	}
	l, err := Open(dir)
	if err != nil {
		os.Exit(90)
	}
	if _, _, err := l.Reserve("node-crash-caller", "dispatch-crash", "crash-payload"); err != nil {
		os.Exit(91)
	}
	if _, err := l.Transition("node-crash-caller", "dispatch-crash", "dispatching", "", ""); err != nil {
		os.Exit(92)
	}
	os.Exit(93) // abrupt process exit while durable dispatching intent exists
}

func TestLocalJobBindingIsDurableImmutableAndPreDispatchOnly(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reserved, _, err := l.Reserve("node-bind", "request-bind", "payload")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := l.BindLocalJob("node-bind", "request-bind", reserved.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.LocalJobID != reserved.JobID {
		t.Fatalf("local binding=%q", bound.LocalJobID)
	}
	again, err := l.BindLocalJob("node-bind", "request-bind", reserved.JobID)
	if err != nil || again.LocalJobID != reserved.JobID {
		t.Fatalf("idempotent bind: %#v %v", again, err)
	}
	if _, err := l.BindLocalJob("node-bind", "request-bind", "different-local-id"); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutable binding accepted: %v", err)
	}
	if _, err := l.Transition("node-bind", "request-bind", "dispatching", reserved.JobID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := l.BindLocalJob("node-bind", "request-bind", reserved.JobID); err == nil {
		t.Fatal("binding after dispatch accepted")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get("node-bind", "request-bind")
	if err != nil || got.LocalJobID != reserved.JobID {
		t.Fatalf("binding not durable: %#v %v", got, err)
	}
}

func TestPrepareDispatchAtomicallyFreezesBindingAndPolicy(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = l.Reserve("node-prepare", "request-prepare", "payload")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.BindExecutionMetadata("node-prepare", "request-prepare", "receiver-local-job", 7); err != nil {
		t.Fatal(err)
	}
	prepared, err := l.PrepareDispatch("node-prepare", "request-prepare", "receiver-local-job", 7)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "dispatching" || prepared.LocalJobID != "receiver-local-job" || prepared.PolicyRevision != 7 || prepared.OwnerPID <= 0 || prepared.OwnerStartTicks == 0 || prepared.OwnerBootID == "" {
		t.Fatalf("incomplete durable dispatch intent: %#v", prepared)
	}
	if _, err := l.PrepareDispatch("node-prepare", "request-prepare", "receiver-local-job", 7); !errors.Is(err, ErrTransition) {
		t.Fatalf("dispatch re-entry accepted: %v", err)
	}
	if _, err := l.PrepareDispatch("node-prepare", "request-prepare", "receiver-local-job", 8); !errors.Is(err, ErrTransition) {
		t.Fatalf("policy revision changed after dispatch: %v", err)
	}
	// Model a dead dispatcher owner without killing the test process.
	if err := l.withLock(func() error {
		key := recordKey("node-prepare", "request-prepare")
		dead := l.records[key]
		dead.OwnerPID = 2147483647
		return l.persist(dead)
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get("node-prepare", "request-prepare")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unknown" || got.LocalJobID != "receiver-local-job" || got.PolicyRevision != 7 {
		t.Fatalf("recovery lost immutable intent: %#v", got)
	}
}
