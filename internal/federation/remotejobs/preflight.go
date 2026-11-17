package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

// HashProcessRequest binds every execution-affecting field, including timeout
// and environment, using the DispatchRequest's canonical JSON representation.
func HashProcessRequest(process ProcessRequest) (string, error) {
	data, err := json.Marshal(process)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// DispatchPreflightInput contains the exact signed bytes received by the
// receiver. CallerNodeID must be the authenticated Federation peer identity.
type DispatchPreflightInput struct {
	CallerNodeID string
	RequestID    string
	Payload      []byte
	Signature    []byte
}

// DispatchPreflightReport is authorization evidence only. It does not reserve,
// transition, create a Job, call Launcher, or start any process.
type DispatchPreflightReport struct {
	CallerNodeID   string `json:"caller_node_id"`
	RequestID      string `json:"request_id"`
	RemoteJobID    string `json:"remote_job_id"`
	LocalJobID     string `json:"local_job_id"`
	PolicyRevision uint64 `json:"policy_revision"`
	Executable     string `json:"executable"`
	WorkingDir     string `json:"working_dir"`
	Reconciliation string `json:"reconciliation"`
}

// HashProcessArgs returns the stable SHA-256 binding used by ExecutionRecord.
// JSON encoding preserves argument order and distinguishes empty from absent.
func HashProcessArgs(args []string) (string, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// PreflightDispatch verifies the purpose-separated Ed25519 proof, pins the
// exact policy revision with an inter-process lease, checks receiver policy,
// and reconciles all durable stores. It is strictly read-only with respect to
// the job lifecycle; the lease is released before returning on every path.
func PreflightDispatch(identity *nodeidentity.Manager, policyStore *PolicyStore, ledger *Ledger, records *ExecutionRecordStore, manager *jobs.Manager, input DispatchPreflightInput) (report DispatchPreflightReport, retErr error) {
	if identity == nil || policyStore == nil || ledger == nil || records == nil || manager == nil {
		return DispatchPreflightReport{}, fmt.Errorf("identity, policy store, ledger, execution record store, and job manager are required")
	}
	if !validID.MatchString(input.CallerNodeID) || !validID.MatchString(input.RequestID) || len(input.Signature) == 0 {
		return DispatchPreflightReport{}, fmt.Errorf("authenticated caller, request ID, and signature are required")
	}
	if err := VerifyRequestForPurpose(identity, "dispatch", input.CallerNodeID, input.RequestID, input.Payload, input.Signature); err != nil {
		return DispatchPreflightReport{}, fmt.Errorf("dispatch proof rejected: %w", err)
	}
	request, err := DecodeDispatchRequest(input.Payload)
	if err != nil {
		return DispatchPreflightReport{}, err
	}
	lease, err := policyStore.AcquireLease(request.PolicyVersion)
	if err != nil {
		return DispatchPreflightReport{}, fmt.Errorf("cannot pin receiver policy: %w", err)
	}
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil && retErr == nil {
			report = DispatchPreflightReport{}
			retErr = fmt.Errorf("release policy lease: %w", releaseErr)
		}
	}()

	policy := lease.Policy
	executable, workingDir, err := policy.validateAndResolve(input.CallerNodeID, true, request.Process)
	if err != nil {
		return DispatchPreflightReport{}, fmt.Errorf("receiver execution policy rejected dispatch: %w", err)
	}
	argsHash, err := HashProcessArgs(request.Process.Args)
	if err != nil {
		return DispatchPreflightReport{}, fmt.Errorf("hash process arguments: %w", err)
	}
	processHash, err := HashProcessRequest(request.Process)
	if err != nil {
		return DispatchPreflightReport{}, fmt.Errorf("hash process request: %w", err)
	}

	record, err := records.Get(input.CallerNodeID, input.RequestID)
	if err != nil {
		return DispatchPreflightReport{}, fmt.Errorf("load immutable execution intent: %w", err)
	}
	if record.CallerNodeID != input.CallerNodeID || record.RequestID != input.RequestID || record.PolicyRevision != lease.Revision || record.Executable != executable || record.WorkingDir != workingDir || record.ArgsHash != argsHash || record.ProcessHash != processHash {
		return DispatchPreflightReport{}, fmt.Errorf("signed dispatch does not match immutable execution intent")
	}
	reconciliation, err := ReconcileExecution(ledger, records, manager, input.CallerNodeID, input.RequestID)
	if err != nil {
		return DispatchPreflightReport{}, fmt.Errorf("three-store reconciliation failed: %w", err)
	}
	if reconciliation.State != "consistent_prepared" || reconciliation.JobFound {
		return DispatchPreflightReport{}, fmt.Errorf("dispatch requires consistent_prepared with no Job record; observed %s", reconciliation.State)
	}
	return DispatchPreflightReport{
		CallerNodeID: input.CallerNodeID, RequestID: input.RequestID,
		RemoteJobID: record.RemoteJobID, LocalJobID: record.LocalJobID,
		PolicyRevision: lease.Revision, Executable: executable,
		WorkingDir: workingDir, Reconciliation: reconciliation.State,
	}, nil
}
