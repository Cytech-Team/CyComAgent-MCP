package remotejobs

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

// DispatchCoordinator owns the receiver-side dispatch critical section. It
// authenticates the exact signed envelope, pins policy, checks immutable intent
// and all durable stores, commits the one-way Ledger dispatch gate, then asks
// Job Manager to persist its launch intent before the descriptor-bound Launcher
// may start a child. It is deliberately not wired to a network/API handler.
type DispatchCoordinator struct {
	crashHook   func(string) // test-only subprocess fault injection; nil in production
	Identity    *nodeidentity.Manager
	PolicyStore *PolicyStore
	Ledger      *Ledger
	Records     *ExecutionRecordStore
	Jobs        *jobs.Manager
}

// Dispatch executes one already-received Federation request. CallerNodeID must
// come from the authenticated Federation transport, never from request JSON.
// The dispatch proof is separately verified for this purpose and request ID.
func (c *DispatchCoordinator) Dispatch(ctx context.Context, input DispatchPreflightInput, executionContext, sessionID string) (job *jobs.Job, retErr error) {
	if c == nil || c.Identity == nil || c.PolicyStore == nil || c.Ledger == nil || c.Records == nil || c.Jobs == nil {
		return nil, fmt.Errorf("dispatch coordinator dependencies are incomplete")
	}
	if !validID.MatchString(input.CallerNodeID) || !validID.MatchString(input.RequestID) || len(input.Signature) == 0 {
		return nil, fmt.Errorf("authenticated caller, request ID, and signature are required")
	}
	if err := VerifyRequestForPurpose(c.Identity, "dispatch", input.CallerNodeID, input.RequestID, input.Payload, input.Signature); err != nil {
		return nil, fmt.Errorf("dispatch proof rejected: %w", err)
	}
	request, err := DecodeDispatchRequest(input.Payload)
	if err != nil {
		return nil, err
	}

	if err := c.Jobs.SetLifecycleLockProvider(remoteJobLifecycleLockProvider{stateDir: filepath.Dir(filepath.Dir(c.Ledger.dir))}); err != nil {
		return nil, fmt.Errorf("configure Job Manager lifecycle lock: %w", err)
	}
	if err := c.Jobs.Recover(); err != nil {
		return nil, fmt.Errorf("recover Job Manager after lifecycle lock configuration: %w", err)
	}
	// The shared per-job lock is acquired before any store-local lock and held
	// through the irreversible dispatch gate, launcher call, and terminal Ledger
	// update. Recovery inspection uses the same lock. Legacy direct Ledger and
	// Launcher callers remain outside this protocol and are not cleanup-safe.
	err = c.Jobs.WithLifecycleAuthorization(input.CallerNodeID, input.RequestID, func(authorization jobs.LifecycleAuthorization) error {
		job, retErr = c.dispatchUnderLifecycleLock(ctx, input, request, executionContext, sessionID, authorization)
		return retErr
	})
	return job, err
}

func (c *DispatchCoordinator) dispatchUnderLifecycleLock(ctx context.Context, input DispatchPreflightInput, request DispatchRequest, executionContext, sessionID string, authorization jobs.LifecycleAuthorization) (job *jobs.Job, retErr error) {
	// Keep the exact policy revision locked through validation, durable gate,
	// process launch, and terminal Ledger update. SaveVersioned uses this lock.
	lease, err := c.PolicyStore.AcquireLease(request.PolicyVersion)
	if err != nil {
		return nil, fmt.Errorf("cannot pin receiver policy: %w", err)
	}
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil && retErr == nil {
			retErr = fmt.Errorf("release dispatch policy lease: %w", releaseErr)
		}
	}()

	policy := lease.Policy
	executable, workingDir, err := policy.validateAndResolve(input.CallerNodeID, true, request.Process)
	if err != nil {
		return nil, fmt.Errorf("receiver execution policy rejected dispatch: %w", err)
	}
	argsHash, err := HashProcessArgs(request.Process.Args)
	if err != nil {
		return nil, fmt.Errorf("hash process arguments: %w", err)
	}
	processHash, err := HashProcessRequest(request.Process)
	if err != nil {
		return nil, fmt.Errorf("hash process request: %w", err)
	}

	intent, err := c.Records.Get(input.CallerNodeID, input.RequestID)
	if err != nil {
		return nil, fmt.Errorf("load immutable execution intent: %w", err)
	}
	if intent.CallerNodeID != input.CallerNodeID || intent.RequestID != input.RequestID || intent.PolicyRevision != lease.Revision || intent.Executable != executable || intent.WorkingDir != workingDir || intent.ArgsHash != argsHash || intent.ProcessHash != processHash {
		return nil, fmt.Errorf("signed dispatch does not match immutable execution intent")
	}
	reconciliation, err := ReconcileExecution(c.Ledger, c.Records, c.Jobs, input.CallerNodeID, input.RequestID)
	if err != nil {
		return nil, fmt.Errorf("three-store reconciliation failed: %w", err)
	}
	if reconciliation.State != "consistent_prepared" || reconciliation.JobFound {
		return nil, fmt.Errorf("dispatch requires consistent_prepared with no Job record; observed %s", reconciliation.State)
	}

	// This is the irreversible no-replay gate. Any crash from this point onward
	// is reconciled as unknown; a second dispatch cannot pass the prepared check.
	if _, err := c.Ledger.PrepareDispatchUnderLifecycleLock(input.CallerNodeID, input.RequestID, intent.LocalJobID, lease.Revision); err != nil {
		return nil, fmt.Errorf("persist dispatch gate: %w", err)
	}
	c.crashAt("after-ledger-dispatching")

	job, launchErr := LaunchAsJob(ctx, c.Jobs, intent.LocalJobID, policy, input.CallerNodeID, true, request.Process, executionContext, sessionID, input.RequestID, authorization)
	if launchErr != nil {
		// Never roll back dispatching to accepted. A Job may have started even if
		// its result could not be durably committed, so ambiguity is terminal.
		if _, transitionErr := c.Ledger.TransitionUnderLifecycleLock(input.CallerNodeID, input.RequestID, "unknown", "", "structured launch outcome requires reconciliation"); transitionErr != nil {
			return job, fmt.Errorf("launch failed (%v); persist unknown Ledger state: %w", launchErr, transitionErr)
		}
		return job, fmt.Errorf("dispatch outcome unknown; automatic replay prohibited: %w", launchErr)
	}
	c.crashAt("after-job-terminal")
	if job == nil {
		if _, transitionErr := c.Ledger.TransitionUnderLifecycleLock(input.CallerNodeID, input.RequestID, "unknown", "", "Job Manager returned no durable result"); transitionErr != nil {
			return nil, fmt.Errorf("Job Manager returned no result; persist unknown Ledger state: %w", transitionErr)
		}
		return nil, fmt.Errorf("Job Manager returned no durable result; dispatch outcome unknown")
	}

	status := "unknown"
	message := "Job result is not a recognized terminal state"
	switch job.Status {
	case "exited":
		status, message = "completed", ""
	case "failed":
		status, message = "failed", job.Error
	case "launch-unknown":
		status, message = "unknown", "Job launch outcome requires reconciliation"
	}
	if _, err := c.Ledger.TransitionUnderLifecycleLock(input.CallerNodeID, input.RequestID, status, "", message); err != nil {
		return job, fmt.Errorf("persist terminal Ledger state for Job %s: %w", job.ID, err)
	}
	c.crashAt("after-ledger-terminal")
	if status == "unknown" {
		return job, fmt.Errorf("dispatch completed with an unknown outcome; automatic replay prohibited")
	}
	return job, nil
}

func (c *DispatchCoordinator) crashAt(stage string) {
	if c.crashHook != nil {
		c.crashHook(stage)
	}
}
