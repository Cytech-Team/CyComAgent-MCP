package remotejobs

import (
	"errors"
	"fmt"
	"os"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
)

// ReconciliationReport is a read-only snapshot of the three durable stores.
// It reports observations only; it never repairs, transitions, or launches.
type ReconciliationReport struct {
	State          string `json:"state"`
	CallerNodeID   string `json:"caller_node_id"`
	RequestID      string `json:"request_id"`
	RemoteJobID    string `json:"remote_job_id,omitempty"`
	LocalJobID     string `json:"local_job_id,omitempty"`
	LedgerStatus   string `json:"ledger_status,omitempty"`
	ExecutionState string `json:"execution_state,omitempty"`
	JobStatus      string `json:"job_status,omitempty"`
	JobFound       bool   `json:"job_found"`
}

// ReconcileExecution is deliberately read-only. The caller supplies an
// already-open Job Manager; opening one may run its normal startup recovery.
func ReconcileExecution(ledger *Ledger, store *ExecutionRecordStore, manager *jobs.Manager, caller, request string) (ReconciliationReport, error) {
	if ledger == nil || store == nil || manager == nil {
		return ReconciliationReport{}, fmt.Errorf("ledger, execution record store, and job manager are required")
	}
	report := ReconciliationReport{CallerNodeID: caller, RequestID: request}
	lr, le := ledger.Get(caller, request)
	er, ee := store.Get(caller, request)
	ledgerMissing := errors.Is(le, ErrNotFound)
	executionMissing := errors.Is(ee, os.ErrNotExist)
	if le != nil && !ledgerMissing {
		return ReconciliationReport{}, le
	}
	if ee != nil && !executionMissing {
		return ReconciliationReport{}, ee
	}
	if !ledgerMissing {
		report.RemoteJobID, report.LocalJobID, report.LedgerStatus = lr.JobID, lr.LocalJobID, lr.Status
	}
	if !executionMissing {
		report.ExecutionState = er.State
		if report.RemoteJobID == "" {
			report.RemoteJobID = er.RemoteJobID
		}
		if report.LocalJobID == "" {
			report.LocalJobID = er.LocalJobID
		}
	}
	if ledgerMissing && executionMissing {
		report.State = "both_missing"
		return report, nil
	}
	if ledgerMissing {
		report.State = "ledger_record_missing"
		return report, nil
	}
	if executionMissing {
		report.State = "execution_record_missing"
		return report, nil
	}
	if lr.PayloadHash != er.PayloadHash || lr.JobID != er.RemoteJobID {
		report.State = "binding_mismatch"
		return report, nil
	}
	if lr.Status == "accepted" && (lr.LocalJobID == "" || lr.PolicyRevision == 0) {
		report.State = "ledger_binding_incomplete"
		return report, nil
	}
	if lr.LocalJobID == "" || lr.LocalJobID != er.LocalJobID || lr.PolicyRevision == 0 || lr.PolicyRevision != er.PolicyRevision {
		report.State = "binding_mismatch"
		return report, nil
	}
	if er.State != "prepared" {
		report.State = "execution_record_state_unrecognized"
		return report, nil
	}
	// ExecutionRecord is an immutable prepared intent, not a mutable runtime
	// state machine. Ledger state therefore controls which Job states are
	// compatible. Reject impossible Ledger states before interpreting Job data.
	switch lr.Status {
	case "accepted":
		if lr.LocalJobID == "" || lr.PolicyRevision == 0 {
			report.State = "ledger_binding_incomplete"
			return report, nil
		}
	case "dispatching", "running", "completed", "failed", "unknown":
	default:
		report.State = "ledger_state_unrecognized"
		return report, nil
	}
	job, je := manager.ReadSnapshot(er.LocalJobID)
	if errors.Is(je, os.ErrNotExist) {
		// A prepared intent is expected to have no Job record yet: the Job
		// Manager persists its launching record only after the durable ledger
		// transition to dispatching. Missing Job data is therefore consistent
		// only while the ledger is still accepted.
		if lr.Status == "accepted" {
			report.State = "consistent_prepared"
		} else {
			report.State = "job_record_missing"
		}
		return report, nil
	}
	if je != nil {
		return ReconciliationReport{}, je
	}
	report.JobFound, report.JobStatus = true, job.Status
	if job.ID != er.LocalJobID {
		report.State = "job_id_mismatch"
		return report, nil
	}
	switch lr.Status {
	case "accepted":
		report.State = "job_exists_before_dispatch"
	case "dispatching":
		switch job.Status {
		case "launching", "running":
			report.State = "dispatching_job_present"
		case "launch-unknown":
			report.State = "job_launch_outcome_unknown"
		default:
			report.State = "dispatching_job_state_mismatch"
		}
	case "running":
		switch job.Status {
		case "running":
			report.State = "consistent_running"
		case "completed", "failed":
			report.State = "job_terminal_ledger_stale"
		case "launch-unknown":
			report.State = "job_launch_outcome_unknown"
		default:
			report.State = "running_job_state_mismatch"
		}
	case "completed":
		if job.Status == "completed" || job.Status == "exited" {
			report.State = "consistent_completed"
		} else {
			report.State = "completed_job_state_mismatch"
		}
	case "failed":
		if job.Status == "failed" {
			report.State = "consistent_failed"
		} else {
			report.State = "failed_job_state_mismatch"
		}
	case "unknown":
		// Recovery deliberately makes the outcome ambiguous. Even if a Job
		// snapshot exists, it is evidence only and must not authorize replay.
		report.State = "ledger_unknown"
	default:
		report.State = "ledger_state_unrecognized"
	}
	return report, nil
}
