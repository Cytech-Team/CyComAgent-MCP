package remotejobs

import (
	"context"
	"fmt"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
)

// LaunchAsJob connects the descriptor-bound remote launcher to the durable Job
// store. The Manager commits its launching intent before Launch can start a
// child. Launch owns process-group timeout/output-limit handling and waits for
// the child; the Manager then persists the terminal result. An ambiguous launch
// is recorded as launch-unknown and is never automatically replayed.
//
// This is an execution primitive, not a Federation transport handler or a
// dispatch coordinator. The caller must already have authenticated the peer,
// verified the signed request, reconciled the durable intent, and acquired the
// receiver policy lease for the complete dispatch critical section.
func LaunchAsJob(ctx context.Context, manager *jobs.Manager, localJobID string, policy ExecutionPolicy, callerNodeID string, callerTrusted bool, request ProcessRequest, executionContext, sessionID, requestID string, authorization jobs.LifecycleAuthorization) (*jobs.Job, error) {
	if manager == nil {
		return nil, ErrJobManagerRequired
	}
	if !validID.MatchString(callerNodeID) || !validID.MatchString(requestID) {
		return nil, fmt.Errorf("remote caller and request IDs are required")
	}
	return manager.StartStructuredWithIDAuthorized(authorization, localJobID, request.Executable, request.WorkingDir, request.Environment, executionContext, sessionID, callerNodeID, requestID, func() (jobs.StructuredResult, error) {
		result, err := Launch(ctx, policy, callerNodeID, callerTrusted, request)
		return jobs.StructuredResult{
			ExitCode: result.ExitCode,
			Stdout:   result.Stdout,
			Stderr:   result.Stderr,
		}, err
	})
}
