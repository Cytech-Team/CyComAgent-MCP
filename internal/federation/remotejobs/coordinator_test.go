//go:build linux

package remotejobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
	"testing"
	"time"
)

func TestDispatchCoordinatorExecutesAndPersistsAllThreeStores(t *testing.T) {
	requireSystemdScope(t)
	identity, policy, ledger, records, manager, input := setupPreflight(t)
	coordinator := &DispatchCoordinator{Identity: identity, PolicyStore: policy, Ledger: ledger, Records: records, Jobs: manager}
	job, err := coordinator.Dispatch(context.Background(), input, "service", "session-coordinator")
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.Status != "exited" || job.ExitCode == nil || *job.ExitCode != 0 {
		t.Fatalf("unexpected Job: %#v", job)
	}
	lr, err := ledger.Get(input.CallerNodeID, input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if lr.Status != "completed" || lr.LocalJobID != job.ID {
		t.Fatalf("unexpected Ledger: %#v", lr)
	}
	report, err := ReconcileExecution(ledger, records, manager, input.CallerNodeID, input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "consistent_completed" {
		t.Fatalf("three-store reconciliation: %+v", report)
	}
	if _, err := coordinator.Dispatch(context.Background(), input, "service", "session-coordinator"); err == nil {
		t.Fatal("completed dispatch replay was accepted")
	}
}

func TestDispatchCoordinatorPersistsUnknownAndRefusesReplayOnTimeout(t *testing.T) {
	requireSystemdScope(t)
	caller, receiver := trustedPair(t)
	dir := t.TempDir()
	policyStore, err := OpenPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := execLookPath("sleep")
	if err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	policy := ExecutionPolicy{Enabled: true, AllowedCallers: []string{caller.Public().NodeID}, AllowedExecutables: []string{exe}, WorkingDirRoots: []string{root}, MaxTimeout: time.Second, MaxOutputBytes: 1024, MaxArgumentCount: 2, MaxArgumentBytes: 64}
	revision, err := policyStore.SaveVersioned(policy, 0)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	records, err := OpenExecutionRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	process := ProcessRequest{Executable: exe, Args: []string{"5"}, WorkingDir: root, Timeout: 100 * time.Millisecond}
	argsHash, _ := HashProcessArgs(process.Args)
	processHash, _ := HashProcessRequest(process)
	requestID := "coordinator-timeout"
	reserved, _, err := ledger.Reserve(caller.Public().NodeID, requestID, "timeout-payload")
	if err != nil {
		t.Fatal(err)
	}
	intent := ExecutionRecord{Version: ExecutionRecordSchemaVersion, CallerNodeID: caller.Public().NodeID, RequestID: requestID, RemoteJobID: reserved.JobID, PayloadHash: reserved.PayloadHash, LocalJobID: "local-coordinator-timeout", PolicyRevision: revision, State: "prepared", Executable: exe, WorkingDir: root, ArgsHash: argsHash, ProcessHash: processHash}
	if _, _, err := PrepareExecutionIntent(ledger, records, intent); err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeDispatchRequest(DispatchRequest{Version: 1, PolicyVersion: revision, Process: process})
	if err != nil {
		t.Fatal(err)
	}
	signature, err := SignRequestForPurpose(caller, "dispatch", receiver.Public().NodeID, requestID, payload)
	if err != nil {
		t.Fatal(err)
	}
	input := DispatchPreflightInput{CallerNodeID: caller.Public().NodeID, RequestID: requestID, Payload: payload, Signature: signature}
	coordinator := &DispatchCoordinator{Identity: receiver, PolicyStore: policyStore, Ledger: ledger, Records: records, Jobs: manager}
	job, err := coordinator.Dispatch(context.Background(), input, "service", "")
	if err == nil || job == nil || job.Status != "launch-unknown" {
		t.Fatalf("timeout must be unknown: job=%#v err=%v", job, err)
	}
	lr, err := ledger.Get(input.CallerNodeID, input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if lr.Status != "unknown" {
		t.Fatalf("Ledger did not persist unknown: %#v", lr)
	}
	if _, err := coordinator.Dispatch(context.Background(), input, "service", ""); err == nil {
		t.Fatal("unknown dispatch was replayed")
	}
}

func TestDispatchCoordinatorRejectsUnauthenticatedAndMalformedRequestsWithoutMutation(t *testing.T) {
	identity, policy, ledger, records, manager, input := setupPreflight(t)
	coordinator := &DispatchCoordinator{Identity: identity, PolicyStore: policy, Ledger: ledger, Records: records, Jobs: manager}
	before, err := ledger.Get(input.CallerNodeID, input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	bad := input
	bad.CallerNodeID = "forged-caller"
	if _, err := coordinator.Dispatch(context.Background(), bad, "service", ""); err == nil {
		t.Fatal("forged caller accepted")
	}
	bad = input
	bad.Payload = []byte(`{"version":1,"policy_version":1,"process":{},"unexpected":true}`)
	if _, err := coordinator.Dispatch(context.Background(), bad, "service", ""); err == nil {
		t.Fatal("malformed signed envelope accepted")
	}
	after, err := ledger.Get(input.CallerNodeID, input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("rejected request mutated Ledger: before=%#v after=%#v", before, after)
	}
	if _, err := manager.ReadSnapshot("local-preflight-001"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected request created Job: %v", err)
	}
}

func TestDispatchCoordinatorEmitsDurableCrashBoundariesInOrder(t *testing.T) {
	requireSystemdScope(t)
	identity, policy, ledger, records, manager, input := setupPreflight(t)
	coordinator := &DispatchCoordinator{Identity: identity, PolicyStore: policy, Ledger: ledger, Records: records, Jobs: manager}
	var stages []string
	coordinator.crashHook = func(stage string) { stages = append(stages, stage) }
	job, err := coordinator.Dispatch(context.Background(), input, "service", "")
	if err != nil || job == nil || job.Status != "exited" {
		t.Fatalf("dispatch failed: job=%#v err=%v", job, err)
	}
	want := []string{"after-ledger-dispatching", "after-job-terminal", "after-ledger-terminal"}
	if len(stages) != len(want) {
		t.Fatalf("crash boundaries=%v want %v", stages, want)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Fatalf("crash boundaries=%v want %v", stages, want)
		}
	}
}

type coordinatorCrashFixture struct {
	CallerNodeID string `json:"caller_node_id"`
	RequestID    string `json:"request_id"`
	Payload      []byte `json:"payload"`
	Signature    []byte `json:"signature"`
}

func TestDispatchCoordinatorCrashRecoverySubprocess(t *testing.T) {
	if os.Getenv("CYCOM_COORDINATOR_CRASH_HELPER") == "1" {
		dir := os.Getenv("CYCOM_COORDINATOR_CRASH_DIR")
		stage := os.Getenv("CYCOM_COORDINATOR_CRASH_STAGE")
		var fixture coordinatorCrashFixture
		data, err := os.ReadFile(filepath.Join(dir, "crash-fixture.json"))
		if err != nil || json.Unmarshal(data, &fixture) != nil {
			os.Exit(91)
		}
		identity, err := nodeidentity.New(filepath.Join(dir, "receiver"))
		if err != nil {
			os.Exit(92)
		}
		policy, err := OpenPolicyStore(dir)
		if err != nil {
			os.Exit(93)
		}
		ledger, err := Open(dir)
		if err != nil {
			os.Exit(94)
		}
		records, err := OpenExecutionRecordStore(dir)
		if err != nil {
			os.Exit(95)
		}
		manager, err := jobs.New(dir)
		if err != nil {
			os.Exit(96)
		}
		coordinator := &DispatchCoordinator{Identity: identity, PolicyStore: policy, Ledger: ledger, Records: records, Jobs: manager}
		coordinator.crashHook = func(at string) {
			if at == stage {
				os.Exit(97)
			}
		}
		_, _ = coordinator.Dispatch(context.Background(), DispatchPreflightInput{CallerNodeID: fixture.CallerNodeID, RequestID: fixture.RequestID, Payload: fixture.Payload, Signature: fixture.Signature}, "service", "")
		os.Exit(98)
	}
	requireSystemdScope(t)
	if runtime.GOOS != "linux" {
		t.Skip("descriptor-bound execution is Linux-only")
	}
	for _, stage := range []string{"after-ledger-dispatching", "after-job-terminal", "after-ledger-terminal"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			caller, err := nodeidentity.New(filepath.Join(dir, "caller"))
			if err != nil {
				t.Fatal(err)
			}
			receiver, err := nodeidentity.New(filepath.Join(dir, "receiver"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := caller.Trust(receiver.Public(), receiver.Public().Fingerprint); err != nil {
				t.Fatal(err)
			}
			if _, err := receiver.Trust(caller.Public(), caller.Public().Fingerprint); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, "work")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			policy, err := OpenPolicyStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			revision, err := policy.SaveVersioned(ExecutionPolicy{Enabled: true, AllowedCallers: []string{caller.Public().NodeID}, AllowedExecutables: []string{"/usr/bin/printf"}, WorkingDirRoots: []string{root}, MaxTimeout: time.Second, MaxOutputBytes: 1024, MaxArgumentCount: 2, MaxArgumentBytes: 128}, 0)
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			records, err := OpenExecutionRecordStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			process := ProcessRequest{Executable: "/usr/bin/printf", Args: []string{"crash-safe"}, WorkingDir: root, Timeout: time.Second}
			payload, err := EncodeDispatchRequest(DispatchRequest{Version: 1, PolicyVersion: revision, Process: process})
			if err != nil {
				t.Fatal(err)
			}
			requestID := "coordinator-crash-" + strings.ReplaceAll(stage, "-", "_")
			reserved, _, err := ledger.Reserve(caller.Public().NodeID, requestID, "payload-"+requestID)
			if err != nil {
				t.Fatal(err)
			}
			argsHash, _ := HashProcessArgs(process.Args)
			processHash, _ := HashProcessRequest(process)
			intent := ExecutionRecord{Version: ExecutionRecordSchemaVersion, CallerNodeID: caller.Public().NodeID, RequestID: requestID, RemoteJobID: reserved.JobID, PayloadHash: reserved.PayloadHash, LocalJobID: "local-" + requestID, PolicyRevision: revision, State: "prepared", Executable: process.Executable, WorkingDir: root, ArgsHash: argsHash, ProcessHash: processHash}
			if _, _, err := PrepareExecutionIntent(ledger, records, intent); err != nil {
				t.Fatal(err)
			}
			signature, err := SignRequestForPurpose(caller, "dispatch", receiver.Public().NodeID, requestID, payload)
			if err != nil {
				t.Fatal(err)
			}
			encodedFixture, _ := json.Marshal(coordinatorCrashFixture{CallerNodeID: caller.Public().NodeID, RequestID: requestID, Payload: payload, Signature: signature})
			fixture := coordinatorCrashFixture{CallerNodeID: caller.Public().NodeID, RequestID: requestID, Payload: payload, Signature: signature}
			if err := os.WriteFile(filepath.Join(dir, "crash-fixture.json"), encodedFixture, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestDispatchCoordinatorCrashRecoverySubprocess$")
			cmd.Env = append(os.Environ(), "CYCOM_COORDINATOR_CRASH_HELPER=1", "CYCOM_COORDINATOR_CRASH_DIR="+dir, "CYCOM_COORDINATOR_CRASH_STAGE="+stage)
			if err := cmd.Run(); err == nil {
				t.Fatal("helper did not exit at requested crash boundary")
			} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 97 {
				t.Fatalf("helper exit=%v, want injected exit 97", err)
			}
			ledger2, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			jobs2, err := jobs.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			lr, err := ledger2.Get(fixture.CallerNodeID, fixture.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			job, jobErr := jobs2.ReadSnapshot(intent.LocalJobID)
			switch stage {
			case "after-ledger-dispatching":
				if lr.Status != "unknown" {
					t.Fatalf("Ledger status=%s, want recovered unknown", lr.Status)
				}
				if !errors.Is(jobErr, os.ErrNotExist) {
					t.Fatalf("Job unexpectedly exists: %#v err=%v", job, jobErr)
				}
			case "after-job-terminal":
				if lr.Status != "unknown" {
					t.Fatalf("Ledger status=%s, want recovered unknown", lr.Status)
				}
				if jobErr != nil || job.Status != "exited" {
					t.Fatalf("terminal Job missing: %#v err=%v", job, jobErr)
				}
			case "after-ledger-terminal":
				if lr.Status != "completed" {
					t.Fatalf("Ledger status=%s, want completed", lr.Status)
				}
				if jobErr != nil || job.Status != "exited" {
					t.Fatalf("terminal Job missing: %#v err=%v", job, jobErr)
				}
			}
			receiver2, err := nodeidentity.New(filepath.Join(dir, "receiver"))
			if err != nil {
				t.Fatal(err)
			}
			policy2, err := OpenPolicyStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			records2, err := OpenExecutionRecordStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			coordinator2 := &DispatchCoordinator{Identity: receiver2, PolicyStore: policy2, Ledger: ledger2, Records: records2, Jobs: jobs2}
			if _, err := coordinator2.Dispatch(context.Background(), DispatchPreflightInput{CallerNodeID: fixture.CallerNodeID, RequestID: fixture.RequestID, Payload: fixture.Payload, Signature: fixture.Signature}, "service", ""); err == nil {
				t.Fatal("crashed dispatch was replayed")
			}
		})
	}
}

func TestDispatchCoordinatorConcurrentDuplicateRequestExecutesAtMostOnce(t *testing.T) {
	requireSystemdScope(t)
	identity, policy, ledger, _, manager, input := setupPreflight(t)
	stateDir := filepath.Dir(filepath.Dir(policy.path))
	const callers = 12
	start := make(chan struct{})
	type outcome struct {
		jobID string
		err   error
	}
	results := make(chan outcome, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Reopen every durable store so contenders have independent in-memory
			// mutexes and file descriptors. The only serialization shared between
			// them is the OS-level lock protocol used by production processes.
			policyStore, err := OpenPolicyStore(stateDir)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			ledgerStore, err := Open(stateDir)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			recordStore, err := OpenExecutionRecordStore(stateDir)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			jobStore, err := jobs.New(stateDir)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			coordinator := &DispatchCoordinator{Identity: identity, PolicyStore: policyStore, Ledger: ledgerStore, Records: recordStore, Jobs: jobStore}
			job, err := coordinator.Dispatch(context.Background(), input, "service", "")
			id := ""
			if job != nil {
				id = job.ID
			}
			results <- outcome{jobID: id, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes, failures := 0, 0
	jobID := ""
	for result := range results {
		if result.err == nil {
			successes++
			if result.jobID == "" {
				t.Fatal("successful dispatch returned no Job ID")
			}
			if jobID != "" && jobID != result.jobID {
				t.Fatalf("duplicate executions produced Job IDs %q and %q", jobID, result.jobID)
			}
			jobID = result.jobID
		} else {
			failures++
		}
	}
	if successes != 1 || failures != callers-1 {
		t.Fatalf("dispatch outcomes: successes=%d failures=%d", successes, failures)
	}
	lr, err := ledger.Get(input.CallerNodeID, input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if lr.Status != "completed" || lr.LocalJobID != jobID {
		t.Fatalf("unexpected final Ledger: %#v", lr)
	}
	job, err := manager.ReadSnapshot(jobID)
	if err != nil || job.Status != "exited" {
		t.Fatalf("unexpected final Job: %#v err=%v", job, err)
	}
}
