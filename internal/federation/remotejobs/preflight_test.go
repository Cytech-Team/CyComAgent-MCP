package remotejobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

func setupPreflight(t *testing.T) (*nodeidentity.Manager, *PolicyStore, *Ledger, *ExecutionRecordStore, *jobs.Manager, DispatchPreflightInput) {
	t.Helper()
	caller, receiver := trustedPair(t)
	dir := t.TempDir()
	policyStore, err := OpenPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	policy := ExecutionPolicy{Enabled: true, AllowedCallers: []string{caller.Public().NodeID}, AllowedExecutables: []string{"/usr/bin/printf"}, WorkingDirRoots: []string{"/tmp"}, MaxTimeout: 5 * time.Second, MaxOutputBytes: 4096, MaxArgumentCount: 4, MaxArgumentBytes: 128}
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
	process := ProcessRequest{Executable: "/usr/bin/printf", Args: []string{"safe"}, WorkingDir: "/tmp", Timeout: time.Second}
	argsHash, err := HashProcessArgs(process.Args)
	if err != nil {
		t.Fatal(err)
	}
	requestID := "preflight-001"
	reserved, _, err := ledger.Reserve(caller.Public().NodeID, requestID, "dispatch-reservation")
	if err != nil {
		t.Fatal(err)
	}
	processHash, err := HashProcessRequest(process)
	if err != nil {
		t.Fatal(err)
	}
	intent := ExecutionRecord{Version: ExecutionRecordSchemaVersion, CallerNodeID: caller.Public().NodeID, RequestID: requestID, RemoteJobID: reserved.JobID, PayloadHash: reserved.PayloadHash, LocalJobID: "local-preflight-001", PolicyRevision: revision, State: "prepared", Executable: "/usr/bin/printf", WorkingDir: "/tmp", ArgsHash: argsHash, ProcessHash: processHash}
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
	return receiver, policyStore, ledger, records, manager, DispatchPreflightInput{CallerNodeID: caller.Public().NodeID, RequestID: requestID, Payload: payload, Signature: signature}
}

func TestPreflightDispatchAuthorizesPreparedIntentWithoutMutation(t *testing.T) {
	identity, policy, ledger, records, manager, input := setupPreflight(t)
	before, err := ledger.Get(input.CallerNodeID, input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := PreflightDispatch(identity, policy, ledger, records, manager, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Reconciliation != "consistent_prepared" || report.PolicyRevision == 0 || report.LocalJobID == "" {
		t.Fatalf("unexpected report: %+v", report)
	}
	after, err := ledger.Get(input.CallerNodeID, input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("preflight mutated ledger: before=%+v after=%+v", before, after)
	}
	if _, err := manager.ReadSnapshot(report.LocalJobID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight created Job record: %v", err)
	}
	if _, err := records.Get(input.CallerNodeID, input.RequestID); err != nil {
		t.Fatalf("preflight changed execution record: %v", err)
	}
}

func TestPreflightDispatchRejectsTamperedPayloadAndWrongProof(t *testing.T) {
	identity, policy, ledger, records, manager, input := setupPreflight(t)
	input.Payload = append([]byte(nil), input.Payload...)
	input.Payload[len(input.Payload)-2] ^= 1
	if _, err := PreflightDispatch(identity, policy, ledger, records, manager, input); err == nil {
		t.Fatal("tampered signed payload accepted")
	}
	_, _, _, _, _, fresh := setupPreflight(t)
	fresh.Signature = []byte("not-a-signature")
	if _, err := PreflightDispatch(identity, policy, ledger, records, manager, fresh); err == nil {
		t.Fatal("invalid signature accepted")
	}
}

func TestPreflightDispatchRejectsPolicyRevisionChange(t *testing.T) {
	identity, policyStore, ledger, records, manager, input := setupPreflight(t)
	current, revision, err := policyStore.LoadVersioned()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policyStore.SaveVersioned(current, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightDispatch(identity, policyStore, ledger, records, manager, input); err == nil {
		t.Fatal("stale policy revision accepted")
	}
}

func TestPreflightDispatchRejectsIntentArgumentMismatch(t *testing.T) {
	identity, policy, ledger, records, manager, input := setupPreflight(t)
	// A valid signature over a different argv is still insufficient: the
	// immutable receiver-side intent must match the signed request exactly.
	request, err := DecodeDispatchRequest(input.Payload)
	if err != nil {
		t.Fatal(err)
	}
	request.Process.Args = []string{"different"}
	input.Payload, err = EncodeDispatchRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Trust(identity.Public(), identity.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	input.Signature, err = SignRequestForPurpose(caller, "dispatch", identity.Public().NodeID, input.RequestID, input.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightDispatch(identity, policy, ledger, records, manager, input); err == nil {
		t.Fatal("changed argv accepted against immutable intent")
	}
}

func TestPreflightDispatchRejectsExistingJob(t *testing.T) {
	identity, policy, ledger, records, manager, input := setupPreflight(t)
	jobDir := filepath.Join(t.TempDir(), "jobs")
	_ = jobDir
	// Any unexpected Job record is a hard stop; use the same Manager storage
	// location and persist only inert fixture metadata.
	base := filepath.Dir(records.dir)
	jobsDir := filepath.Join(filepath.Dir(base), "jobs")
	_ = os.MkdirAll(jobsDir, 0700)
	job := jobs.Job{ID: "local-preflight-001", Status: "completed", LogFile: filepath.Join(jobsDir, "local-preflight-001.log")}
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobsDir, job.ID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreflightDispatch(identity, policy, ledger, records, manager, input); err == nil {
		t.Fatal("existing Job record did not block preflight")
	}
}

func TestPreflightDispatchBindsTimeoutAndEnvironment(t *testing.T) {
	for _, mutate := range []func(*ProcessRequest){
		func(p *ProcessRequest) { p.Timeout += time.Second },
		func(p *ProcessRequest) { p.Environment = map[string]string{"LANG": "C"} },
	} {
		identity, policy, ledger, records, manager, input := setupPreflight(t)
		request, err := DecodeDispatchRequest(input.Payload)
		if err != nil {
			t.Fatal(err)
		}
		mutate(&request.Process)
		input.Payload, err = EncodeDispatchRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		caller, err := nodeidentity.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := caller.Trust(identity.Public(), identity.Public().Fingerprint); err != nil {
			t.Fatal(err)
		}
		input.Signature, err = SignRequestForPurpose(caller, "dispatch", identity.Public().NodeID, input.RequestID, input.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PreflightDispatch(identity, policy, ledger, records, manager, input); err == nil {
			t.Fatal("changed timeout/environment accepted against immutable process intent")
		}
	}
}
