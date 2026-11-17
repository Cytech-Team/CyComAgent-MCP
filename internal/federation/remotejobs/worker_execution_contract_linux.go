//go:build linux

package remotejobs

import (
	"fmt"
	"time"
)

// WorkerExecutionContract is the broker's normalized, non-serialized handoff
// description. It contains only receiver-derived identity/unit/sandbox data
// and the already-authorized command values. It deliberately carries no file
// descriptor numbers, paths supplied by the caller, or systemd command line.
type WorkerExecutionContract struct {
	UnitName           string
	Profile            WorkerServiceProfile
	Argv               []string
	Environment        map[string]string
	Timeout            time.Duration
	MaxOutputBytes     int64
	InputRef           string
	ExecutableHash     string
	ExecutableSnapshot *ExecutableSnapshot
}

// BuildWorkerExecutionContract validates the durable authorization evidence
// and returns an immutable-by-convention launch description. It performs no
// IPC, filesystem mutation, account lookup, systemd operation, or process start.
func BuildWorkerExecutionContract(auth BrokerAuthorization, req BrokerStartRequest, workerUID, workerGID uint32, socketValidation *BrokerSocketValidation) (WorkerExecutionContract, error) {
	var out WorkerExecutionContract
	if err := ValidateBrokerStartRequest(req); err != nil {
		return out, err
	}
	if auth.Executable == nil || auth.WorkingDirectory == nil {
		return out, fmt.Errorf("pinned executable and working-directory descriptors are required")
	}
	if auth.Record.Status != "accepted" || auth.Record.RequestID != req.RequestID || auth.Record.PolicyRevision != req.PolicyRevision || auth.PolicyRevision != req.PolicyRevision {
		return out, fmt.Errorf("durable authorization does not match broker request")
	}
	if auth.Record.CallerNodeID == "" || auth.Record.LocalJobID == "" {
		return out, fmt.Errorf("durable authorization identity is incomplete")
	}
	exeInfo, err := auth.Executable.Stat()
	if err != nil || !exeInfo.Mode().IsRegular() || exeInfo.Mode()&0111 == 0 {
		return out, fmt.Errorf("authorized executable descriptor is not executable")
	}
	cwdInfo, err := auth.WorkingDirectory.Stat()
	if err != nil || !cwdInfo.IsDir() {
		return out, fmt.Errorf("authorized working-directory descriptor is invalid")
	}
	snapshot, err := CreateExecutableSnapshot(auth.Executable, req.ExecutableDigest)
	if err != nil {
		return out, fmt.Errorf("create authorized executable snapshot: %w", err)
	}
	if socketValidation == nil || socketValidation.parent == nil || socketValidation.socket == nil {
		return out, fmt.Errorf("pinned root-broker socket validation is required")
	}
	profile, err := BuildWorkerServiceProfile(workerUID, workerGID, time.Duration(req.TimeoutMillis)*time.Millisecond, socketValidation.Path)
	if err != nil {
		return out, err
	}
	unit, err := ExecutionUnitName(auth.Record.LocalJobID, req.RequestID)
	if err != nil {
		return out, err
	}
	argv := append([]string(nil), req.Argv...)
	env := make(map[string]string, len(req.Environment))
	for k, v := range req.Environment {
		env[k] = v
	}
	return WorkerExecutionContract{
		UnitName: unit, Profile: profile, Argv: argv, Environment: env,
		Timeout:        time.Duration(req.TimeoutMillis) * time.Millisecond,
		MaxOutputBytes: req.MaxOutputBytes, InputRef: req.InputRef,
		ExecutableHash: req.ExecutableDigest, ExecutableSnapshot: snapshot,
	}, nil
}
