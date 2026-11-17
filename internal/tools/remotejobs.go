package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation/remotejobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

type remoteJobProofInput struct {
	CallerNodeID string `json:"caller_node_id"`
	RequestID    string `json:"request_id"`
	Payload      string `json:"payload"`
	Signature    string `json:"signature"`
}

func verifyRemoteJobProof(identity *nodeidentity.Manager, raw json.RawMessage, purpose string) (remoteJobProofInput, error) {
	var in remoteJobProofInput
	if err := decode(raw, &in); err != nil {
		return in, err
	}
	sig, err := base64.RawStdEncoding.DecodeString(in.Signature)
	if err != nil {
		return in, fmt.Errorf("invalid request signature encoding")
	}
	if err := remotejobs.VerifyRequestForPurpose(identity, purpose, in.CallerNodeID, in.RequestID, []byte(in.Payload), sig); err != nil {
		return in, fmt.Errorf("remote job %s authentication failed: %w", purpose, err)
	}
	return in, nil
}

// registerRemoteJobs exposes only authenticated reservation, status and
// read-only evidence inspection. It intentionally has no process-launch API.
func registerRemoteJobs(r *registry.Registry, ledger *remotejobs.Ledger, identity *nodeidentity.Manager, jobsManager *jobs.Manager) {
	if ledger == nil || identity == nil {
		return
	}
	proofSchema := func() map[string]any {
		return registry.ObjectSchema(map[string]any{
			"caller_node_id": registry.String("Original caller Node ID"),
			"request_id":     registry.String("Stable original request ID"),
			"payload":        registry.String("Exact original UTF-8 payload covered by the signature"),
			"signature":      registry.String("Base64 RawStdEncoding Ed25519 request proof"),
		}, []string{"caller_node_id", "request_id", "payload", "signature"})
	}

	must(r.Add(registry.Tool{
		Name:        "federation_remote_job_reserve",
		Description: "Authenticate a Federation Remote Job request and durably reserve its idempotency key. This endpoint never launches a process.",
		InputSchema: proofSchema(),
		Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		Source:      "core",
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			in, err := verifyRemoteJobProof(identity, raw, "reserve")
			if err != nil {
				return nil, err
			}
			rec, created, err := ledger.Reserve(in.CallerNodeID, in.RequestID, in.Payload)
			if err != nil {
				return nil, err
			}
			return map[string]any{"record": rec, "created": created, "execution": "not_started"}, nil
		},
	}))

	must(r.Add(registry.Tool{
		Name:        "federation_remote_job_status",
		Description: "Authenticate the original caller and return the durable status for its exact Remote Job request. Read-only; does not execute or reconcile processes.",
		InputSchema: proofSchema(),
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		Source:      "core",
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			in, err := verifyRemoteJobProof(identity, raw, "status")
			if err != nil {
				return nil, err
			}
			rec, err := ledger.Get(in.CallerNodeID, in.RequestID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"record": rec, "execution": "not_started_by_reservation_api"}, nil
		},
	}))

	if jobsManager == nil {
		return
	}
	must(r.Add(registry.Tool{
		Name:        "federation_remote_job_reconcile",
		Description: "Read-only evidence inspection for an authenticated Remote Job. Compares the durable ledger record with the local Job Manager record; never changes state or replays work.",
		InputSchema: proofSchema(),
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		Source:      "core",
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			in, err := verifyRemoteJobProof(identity, raw, "reconcile")
			if err != nil {
				return nil, err
			}
			rec, err := ledger.Get(in.CallerNodeID, in.RequestID)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"record": rec, "job_evidence": "not_linked", "state_changed": false, "replayed": false, "local_job_id": rec.LocalJobID, "policy_revision": rec.PolicyRevision}
			if rec.LocalJobID == "" {
				out["job_evidence_status"] = "local_job_not_bound"
				return out, nil
			}
			job, err := jobsManager.Get(rec.LocalJobID)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					out["job_evidence_status"] = "bound_job_missing"
					return out, nil
				}
				return nil, fmt.Errorf("read bound local job evidence: %w", err)
			}
			if job.ID != rec.LocalJobID {
				return nil, fmt.Errorf("local job evidence ID does not match immutable ledger binding")
			}
			evidence := map[string]any{"id": job.ID, "status": job.Status, "created_at": job.CreatedAt, "updated_at": job.UpdatedAt, "execution_context": job.ExecutionContext, "ledger_status": rec.Status, "policy_revision": rec.PolicyRevision}
			if job.ExitCode != nil {
				evidence["exit_code"] = *job.ExitCode
			}
			out["job_evidence"] = evidence
			out["job_evidence_status"] = "found"
			return out, nil
		},
	}))
}
