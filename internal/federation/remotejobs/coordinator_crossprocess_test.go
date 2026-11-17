package remotejobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

type coordinatorProcessFixture struct {
	CallerNodeID string `json:"caller_node_id"`
	RequestID    string `json:"request_id"`
	Payload      []byte `json:"payload"`
	Signature    []byte `json:"signature"`
}

func TestCoordinatorCrossProcessHelper(t *testing.T) {
	dir := os.Getenv("CYCOM_COORDINATOR_RACE_DIR")
	if dir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, "race-fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture coordinatorProcessFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	identity, err := nodeidentity.New(filepath.Join(dir, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := OpenPolicyStore(dir)
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
	coordinator := &DispatchCoordinator{Identity: identity, PolicyStore: policy, Ledger: ledger, Records: records, Jobs: manager}
	_, err = coordinator.Dispatch(context.Background(), DispatchPreflightInput{
		CallerNodeID: fixture.CallerNodeID,
		RequestID:    fixture.RequestID,
		Payload:      fixture.Payload,
		Signature:    fixture.Signature,
	}, "cross-process-test", "")
	if err != nil {
		fmt.Fprint(os.Stdout, "rejected")
		return
	}
	fmt.Fprint(os.Stdout, "success")
}

func TestDispatchCoordinatorCrossProcessDuplicateIsExecutedAtMostOnce(t *testing.T) {
	requireSystemdScope(t)
	if testing.Short() {
		t.Skip("spawns independent test processes")
	}
	dir := t.TempDir()
	caller, err := nodeidentity.New(filepath.Join(dir, "caller"))
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := nodeidentity.New(filepath.Join(dir, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Trust(caller.Public(), caller.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Trust(receiver.Public(), receiver.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}

	policyStore, err := OpenPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "work")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	executable := "/usr/bin/printf"
	if _, err := os.Stat(executable); err != nil {
		t.Skipf("fixture executable unavailable: %v", err)
	}
	policy := ExecutionPolicy{
		Enabled:            true,
		AllowedCallers:     []string{caller.Public().NodeID},
		AllowedExecutables: []string{executable},
		WorkingDirRoots:    []string{root},
		MaxTimeout:         5 * time.Second,
		MaxOutputBytes:     1024,
		MaxArgumentCount:   2,
		MaxArgumentBytes:   64,
	}
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
	requestID := "cross-process-coordinator-001"
	process := ProcessRequest{Executable: executable, Args: []string{"cross-process-once"}, WorkingDir: root, Timeout: time.Second}
	payload, err := EncodeDispatchRequest(DispatchRequest{Version: 1, PolicyVersion: revision, Process: process})
	if err != nil {
		t.Fatal(err)
	}
	reserved, created, err := ledger.Reserve(caller.Public().NodeID, requestID, "cross-process-payload")
	if err != nil || !created {
		t.Fatalf("reserve fixture: created=%v err=%v", created, err)
	}
	argsHash, err := HashProcessArgs(process.Args)
	if err != nil {
		t.Fatal(err)
	}
	processHash, err := HashProcessRequest(process)
	if err != nil {
		t.Fatal(err)
	}
	intent := ExecutionRecord{
		Version:        ExecutionRecordSchemaVersion,
		CallerNodeID:   caller.Public().NodeID,
		RequestID:      requestID,
		RemoteJobID:    reserved.JobID,
		PayloadHash:    reserved.PayloadHash,
		LocalJobID:     "local-cross-process-coordinator-001",
		PolicyRevision: revision,
		State:          "prepared",
		Executable:     executable,
		WorkingDir:     root,
		ArgsHash:       argsHash,
		ProcessHash:    processHash,
	}
	if _, _, err := PrepareExecutionIntent(ledger, records, intent); err != nil {
		t.Fatal(err)
	}
	signature, err := SignRequestForPurpose(caller, "dispatch", receiver.Public().NodeID, requestID, payload)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := json.Marshal(coordinatorProcessFixture{
		CallerNodeID: caller.Public().NodeID,
		RequestID:    requestID,
		Payload:      payload,
		Signature:    signature,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "race-fixture.json"), fixture, 0600); err != nil {
		t.Fatal(err)
	}

	const workers = 12
	var wg sync.WaitGroup
	outputs := make(chan string, workers)
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			cmd := exec.Command(os.Args[0], "-test.run=^TestCoordinatorCrossProcessHelper$")
			cmd.Env = append(os.Environ(), "CYCOM_COORDINATOR_RACE_DIR="+dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				errs <- fmt.Errorf("coordinator helper: %w: %s", err, out)
				return
			}
			value := strings.TrimSpace(string(out))
			if strings.HasPrefix(value, "success") {
				outputs <- "success"
			} else if strings.HasPrefix(value, "rejected") {
				outputs <- "rejected"
			} else {
				errs <- fmt.Errorf("unexpected coordinator helper output %q", value)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(outputs)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	successes, rejected := 0, 0
	for value := range outputs {
		switch value {
		case "success":
			successes++
		case "rejected":
			rejected++
		}
	}
	if successes != 1 || rejected != workers-1 {
		t.Fatalf("independent Coordinator processes: successes=%d rejected=%d; want exactly one success", successes, rejected)
	}
	finalLedger, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := finalLedger.Get(caller.Public().NodeID, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "completed" {
		t.Fatalf("final Ledger status=%q, want completed", record.Status)
	}
	finalJobs, err := jobs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := finalJobs.ReadSnapshot(intent.LocalJobID)
	if err != nil {
		t.Fatal(err)
	}
	output, _, err := finalJobs.Tail(intent.LocalJobID, 10, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "exited" || output != "cross-process-once" {
		t.Fatalf("unexpected single durable Job result: status=%q output=%q", job.Status, output)
	}
}
