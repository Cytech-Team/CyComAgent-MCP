//go:build linux

package remotejobs

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
)

func sampleExecutionIntent() ExecutionRecord {
	return ExecutionRecord{Version: ExecutionRecordSchemaVersion, CallerNodeID: "node-caller", RequestID: "request-intent-1", RemoteJobID: "remote-job-1", PayloadHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LocalJobID: "local-job-1", PolicyRevision: 7, State: "prepared", Executable: "/usr/bin/printf", WorkingDir: "/tmp", ArgsHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ProcessHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}
}

func TestExecutionRecordIntentIsDurableImmutableAndCreateOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleExecutionIntent()
	got, created, err := s.CreateIntent(want)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("durable timestamps missing")
	}
	got2, created, err := s.CreateIntent(want)
	if err != nil || created || !sameExecutionIntent(got, got2) || !got.CreatedAt.Equal(got2.CreatedAt) {
		t.Fatalf("idempotent create mismatch: %#v %#v %v %v", got, got2, created, err)
	}
	changed := want
	changed.PolicyRevision++
	if _, _, err := s.CreateIntent(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected immutable policy conflict, got %v", err)
	}
	s2, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := s2.Get(want.CallerNodeID, want.RequestID)
	if err != nil || !sameExecutionIntent(got, reopened) || !got.CreatedAt.Equal(reopened.CreatedAt) {
		t.Fatalf("reopen mismatch: %#v %v", reopened, err)
	}
	info, err := os.Stat(s.path(want.CallerNodeID, want.RequestID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("record permissions=%o", info.Mode().Perm())
	}
}

func TestExecutionRecordRejectsCorruptAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleExecutionIntent()
	if _, _, err := s.CreateIntent(want); err != nil {
		t.Fatal(err)
	}
	path := s.path(want.CallerNodeID, want.RequestID)
	legacy := want
	legacy.Version = 1
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacyData, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(want.CallerNodeID, want.RequestID); err == nil {
		t.Fatal("legacy v1 record must fail closed because it predates complete process binding")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(want.CallerNodeID, want.RequestID); err == nil {
		t.Fatal("unknown fields must fail closed")
	}
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(want.CallerNodeID, want.RequestID); err == nil {
		t.Fatal("corrupt record must fail closed")
	}
}

func TestExecutionRecordDirectoryIsPrivate(t *testing.T) {
	s, err := OpenExecutionRecordStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(s.path("node-caller", "request-intent-1")))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions=%o", info.Mode().Perm())
	}
}

func TestPrepareExecutionIntentCoordinatesStoresWithoutDispatch(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "payload-for-intent")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash = reserved.PayloadHash
	intent.RemoteJobID, intent.LocalJobID = reserved.JobID, "receiver-local-job-1"
	if state, err := InspectExecutionConsistency(ledger, store, intent.CallerNodeID, intent.RequestID); err != nil || state != "execution_record_missing" {
		t.Fatalf("initial state=%q err=%v", state, err)
	}
	got, created, err := PrepareExecutionIntent(ledger, store, intent)
	if err != nil || !created {
		t.Fatalf("prepare: created=%v err=%v", created, err)
	}
	if !sameExecutionIntent(got, intent) {
		t.Fatalf("intent changed: %#v", got)
	}
	bound, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Status != "accepted" || bound.LocalJobID != intent.LocalJobID || bound.PolicyRevision != intent.PolicyRevision {
		t.Fatalf("ledger binding unexpectedly dispatchable or mismatched: %#v", bound)
	}
	if state, err := InspectExecutionConsistency(ledger, store, intent.CallerNodeID, intent.RequestID); err != nil || state != "consistent_prepared" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if _, _, err := PrepareExecutionIntent(ledger, store, intent); err != nil {
		t.Fatalf("idempotent prepare: %v", err)
	}
	changed := intent
	changed.ArgsHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, _, err := PrepareExecutionIntent(ledger, store, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected changed intent conflict, got %v", err)
	}
}

func TestPrepareExecutionIntentLeavesDetectableOrphanOnLedgerBindingConflict(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "payload-for-intent")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID, intent.LocalJobID = reserved.PayloadHash, reserved.JobID, "receiver-local-job-1"
	if _, err := ledger.BindExecutionMetadata(intent.CallerNodeID, intent.RequestID, "other-local-job", 7); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrepareExecutionIntent(ledger, store, intent); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected binding conflict, got %v", err)
	}
	if state, err := InspectExecutionConsistency(ledger, store, intent.CallerNodeID, intent.RequestID); err != nil || state != "binding_mismatch" {
		t.Fatalf("orphan state=%q err=%v", state, err)
	}
}

func TestPrepareExecutionIntentCrashAfterFirstStoreIsDetectableAndNeverDispatches(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "payload-for-intent")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID, intent.LocalJobID = reserved.PayloadHash, reserved.JobID, "receiver-local-job-1"
	crash := errors.New("simulated crash after execution record fsync")
	if _, _, err := prepareExecutionIntent(ledger, store, intent, func() error { return crash }); !errors.Is(err, crash) {
		t.Fatalf("expected injected crash, got %v", err)
	}
	// Reopen both stores to model process restart and ensure no hidden cache is used.
	ledger, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err = OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := InspectExecutionConsistency(ledger, store, intent.CallerNodeID, intent.RequestID)
	if err != nil || state != "ledger_binding_incomplete" {
		t.Fatalf("recovered state=%q err=%v", state, err)
	}
	lr, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if lr.Status != "accepted" || lr.LocalJobID != "" || lr.PolicyRevision != 0 {
		t.Fatalf("crash unexpectedly advanced ledger: %#v", lr)
	}
	if _, err := ledger.PrepareDispatch(intent.CallerNodeID, intent.RequestID, intent.LocalJobID, intent.PolicyRevision); err == nil {
		t.Fatal("dispatch must not proceed while execution record is unbound")
	}
}

func TestPrepareExecutionIntentConcurrentSameRequestIsSerialized(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "payload-for-intent")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID, intent.LocalJobID = reserved.PayloadHash, reserved.JobID, "receiver-local-job-1"
	const callers = 16
	start := make(chan struct{})
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { <-start; _, _, e := PrepareExecutionIntent(ledger, store, intent); errs <- e }()
	}
	close(start)
	for i := 0; i < callers; i++ {
		if e := <-errs; e != nil {
			t.Errorf("concurrent prepare: %v", e)
		}
	}
	if state, err := InspectExecutionConsistency(ledger, store, intent.CallerNodeID, intent.RequestID); err != nil || state != "consistent_prepared" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	bound, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Status != "accepted" || bound.LocalJobID != intent.LocalJobID || bound.PolicyRevision != intent.PolicyRevision {
		t.Fatalf("unexpected ledger after concurrency: %#v", bound)
	}
}

func TestPrepareExecutionIntentCrashSubprocessHelper(t *testing.T) {
	crashMode := os.Getenv("REMOTEJOBS_CRASH_HELPER") == "1"
	concurrentMode := os.Getenv("REMOTEJOBS_CONCURRENT_HELPER") == "1"
	boundCrashMode := os.Getenv("REMOTEJOBS_BOUND_CRASH_HELPER") == "1"
	lockMode := os.Getenv("REMOTEJOBS_LOCK_HELPER") == "1"
	if !crashMode && !concurrentMode && !boundCrashMode && !lockMode {
		return
	}
	dir := os.Getenv("REMOTEJOBS_STATE_DIR")
	if lockMode {
		lockPath := filepath.Join(dir, "coordinator-kill-test.lock")
		err := withFileLock(lockPath, func() error {
			if err := os.WriteFile(os.Getenv("REMOTEJOBS_LOCK_READY"), []byte("locked"), 0o600); err != nil {
				return err
			}
			select {}
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID, intent.LocalJobID = reserved.PayloadHash, reserved.JobID, "receiver-local-job-crash"
	if boundCrashMode {
		intent.LocalJobID = "receiver-local-job-bound-crash"
		if _, _, err := store.CreateIntent(intent); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.BindExecutionMetadata(intent.CallerNodeID, intent.RequestID, intent.LocalJobID, intent.PolicyRevision); err != nil {
			t.Fatal(err)
		}
		os.Exit(78)
	}
	if crashMode {
		_, _, _ = prepareExecutionIntent(ledger, store, intent, func() error { os.Exit(77); return nil })
		t.Fatal("crash hook did not terminate helper")
	}
	intent.LocalJobID = "receiver-local-job-multiprocess"
	if _, _, err := PrepareExecutionIntent(ledger, store, intent); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareExecutionIntentRealSubprocessCrashLeavesOrphan(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	if _, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "payload-for-intent"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrepareExecutionIntentCrashSubprocessHelper$")
	cmd.Env = append(os.Environ(), "REMOTEJOBS_CRASH_HELPER=1", "REMOTEJOBS_STATE_DIR="+dir)
	output, runErr := cmd.CombinedOutput()
	if exitErr, ok := runErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 77 {
		t.Fatalf("helper did not crash at boundary: err=%v output=%s", runErr, output)
	}
	ledger, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err = OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := InspectExecutionConsistency(ledger, store, intent.CallerNodeID, intent.RequestID)
	if err != nil || state != "ledger_binding_incomplete" {
		t.Fatalf("after real process death state=%q err=%v", state, err)
	}
	rec, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "accepted" || rec.LocalJobID != "" {
		t.Fatalf("ledger advanced after process death: %#v", rec)
	}
	if _, err := ledger.PrepareDispatch(intent.CallerNodeID, intent.RequestID, "receiver-local-job-crash", 7); err == nil {
		t.Fatal("dispatch gate accepted orphaned intent")
	}
}

func TestPrepareExecutionIntentConcurrentProcessesShareCoordinatorLock(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Reserve("node-caller", "request-intent-1", "payload-for-intent"); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	cmds := make([]*exec.Cmd, 0, workers)
	for i := 0; i < workers; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPrepareExecutionIntentCrashSubprocessHelper$")
		cmd.Env = append(os.Environ(), "REMOTEJOBS_CONCURRENT_HELPER=1", "REMOTEJOBS_STATE_DIR="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("worker failed: %v", err)
		}
	}
	ledger, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := InspectExecutionConsistency(ledger, store, "node-caller", "request-intent-1")
	if err != nil || state != "consistent_prepared" {
		t.Fatalf("multi-process state=%q err=%v", state, err)
	}
	rec, err := ledger.Get("node-caller", "request-intent-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "accepted" || rec.LocalJobID != "receiver-local-job-multiprocess" {
		t.Fatalf("unexpected multi-process ledger: %#v", rec)
	}
}

func TestPrepareExecutionIntentCrashAfterLedgerBindingIsConsistentAndNotDispatched(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	if _, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "payload-for-intent"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrepareExecutionIntentCrashSubprocessHelper$")
	cmd.Env = append(os.Environ(), "REMOTEJOBS_BOUND_CRASH_HELPER=1", "REMOTEJOBS_STATE_DIR="+dir)
	if output, err := cmd.CombinedOutput(); !isExitCode(err, 78) {
		t.Fatalf("helper did not exit after binding: err=%v output=%s", err, output)
	}
	ledger, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := InspectExecutionConsistency(ledger, store, intent.CallerNodeID, intent.RequestID)
	if err != nil || state != "consistent_prepared" {
		t.Fatalf("reopened state=%q err=%v", state, err)
	}
	rec, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "accepted" || rec.LocalJobID != "receiver-local-job-bound-crash" {
		t.Fatalf("unexpected recovered ledger: %#v", rec)
	}
	if rec.Status != "accepted" {
		t.Fatalf("recovery must not auto-advance to dispatching: %q", rec.Status)
	}
}

func isExitCode(err error, code int) bool {
	exitErr, ok := err.(*exec.ExitError)
	return ok && exitErr.ExitCode() == code
}

func TestCoordinatorLockReleasedWhenHolderIsKilled(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "holder-ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrepareExecutionIntentCrashSubprocessHelper$")
	cmd.Env = append(os.Environ(), "REMOTEJOBS_LOCK_HELPER=1", "REMOTEJOBS_STATE_DIR="+dir, "REMOTEJOBS_LOCK_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if i == 199 {
			t.Fatal("child did not acquire coordinator lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected killed lock holder")
	}
	acquired := make(chan error, 1)
	go func() {
		acquired <- withFileLock(filepath.Join(dir, "coordinator-kill-test.lock"), func() error { return nil })
	}()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("lock not reusable after holder death: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flock remained held after holder was killed")
	}
}

func TestPrepareExecutionIntentAcceptsSameStateDirectoryThroughSymlinkAlias(t *testing.T) {
	base := t.TempDir()
	canonical := filepath.Join(base, "state")
	alias := filepath.Join(base, "state-alias")
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}
	ledger, err := Open(canonical)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(alias)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "payload-through-alias")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID = reserved.PayloadHash, reserved.JobID
	if _, created, err := PrepareExecutionIntent(ledger, store, intent); err != nil || !created {
		t.Fatalf("same physical state directory via symlink should coordinate: created=%v err=%v", created, err)
	}
	if state, err := InspectExecutionConsistency(ledger, store, intent.CallerNodeID, intent.RequestID); err != nil || state != "consistent_prepared" {
		t.Fatalf("consistency=%q err=%v", state, err)
	}
}

func TestExecutionRecordLocalJobIDResolvesToDurableJobManagerRecord(t *testing.T) {
	dir := t.TempDir()
	intent := sampleExecutionIntent()
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.CreateIntent(intent); err != nil || !created {
		t.Fatalf("create execution intent: created=%v err=%v", created, err)
	}
	jobDir := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: intent.LocalJobID, Command: "not-executed-test-fixture", Shell: "/bin/sh", Status: "completed", LogFile: filepath.Join(jobDir, intent.LocalJobID+".log")}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, intent.LocalJobID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := manager.Get(record.LocalJobID)
	if err != nil {
		t.Fatalf("LocalJobID %q did not resolve in Job Manager: %v", record.LocalJobID, err)
	}
	if resolved.ID != record.LocalJobID {
		t.Fatalf("resolved Job ID=%q, execution record LocalJobID=%q", resolved.ID, record.LocalJobID)
	}
	if resolved.Status != "completed" {
		t.Fatalf("fixture status changed during read-only recovery: %q", resolved.Status)
	}
	if _, err := manager.Get(record.LocalJobID + "-missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unbound LocalJobID should not resolve, got %v", err)
	}
}

func TestReconcileExecutionReportsMissingJobWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "reconcile-missing-job")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID = reserved.PayloadHash, reserved.JobID
	if _, _, err := PrepareExecutionIntent(ledger, store, intent); err != nil {
		t.Fatal(err)
	}
	before, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ReconcileExecution(ledger, store, manager, intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "consistent_prepared" || report.JobFound {
		t.Fatalf("unexpected report: %+v", report)
	}
	after, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("read-only reconciliation mutated Ledger: before=%+v after=%+v", before, after)
	}
	if _, err := store.Get(intent.CallerNodeID, intent.RequestID); err != nil {
		t.Fatalf("reconciliation removed execution record: %v", err)
	}
}

func TestReconcileExecutionReportsAllThreeStoresConsistent(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "reconcile-consistent")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID = reserved.PayloadHash, reserved.JobID
	if _, _, err := PrepareExecutionIntent(ledger, store, intent); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(dir, "jobs")
	job := jobs.Job{ID: intent.LocalJobID, Command: "fixture-only", Shell: "/bin/sh", Status: "completed", LogFile: filepath.Join(jobDir, intent.LocalJobID+".log")}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, intent.LocalJobID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := ReconcileExecution(ledger, store, manager, intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "job_exists_before_dispatch" || !report.JobFound || report.JobStatus != "completed" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReconcileExecutionFailsClosedOnCorruptJobSnapshot(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "corrupt-job")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID = reserved.PayloadHash, reserved.JobID
	if _, _, err := PrepareExecutionIntent(ledger, store, intent); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(dir, "jobs")
	if err := os.WriteFile(filepath.Join(jobDir, intent.LocalJobID+".json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileExecution(ledger, store, manager, intent.CallerNodeID, intent.RequestID); err == nil {
		t.Fatal("corrupt Job metadata must fail closed")
	}
}

func TestReconcileExecutionRejectsUnknownExecutionRecordState(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "unknown-exec-state")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID = reserved.PayloadHash, reserved.JobID
	if _, _, err := PrepareExecutionIntent(ledger, store, intent); err != nil {
		t.Fatal(err)
	}
	path := store.path(intent.CallerNodeID, intent.RequestID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted ExecutionRecord
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	persisted.State = "unexpected-state"
	data, err = json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileExecution(ledger, store, manager, intent.CallerNodeID, intent.RequestID); err == nil {
		t.Fatal("strict record decoder must reject unknown execution state")
	}
}

func TestReconcileExecutionRejectsAcceptedLedgerWithIncompleteBinding(t *testing.T) {
	dir := t.TempDir()
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	reserved, _, err := ledger.Reserve(intent.CallerNodeID, intent.RequestID, "incomplete-binding")
	if err != nil {
		t.Fatal(err)
	}
	intent.PayloadHash, intent.RemoteJobID = reserved.PayloadHash, reserved.JobID
	if _, _, err := store.CreateIntent(intent); err != nil {
		t.Fatal(err)
	}
	report, err := ReconcileExecution(ledger, store, manager, intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "ledger_binding_incomplete" || report.JobFound {
		t.Fatalf("incomplete accepted binding must fail closed: %+v", report)
	}
	unchanged, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != "accepted" || unchanged.LocalJobID != "" || unchanged.PolicyRevision != 0 {
		t.Fatalf("read-only reconciliation changed incomplete ledger: %+v", unchanged)
	}
}
