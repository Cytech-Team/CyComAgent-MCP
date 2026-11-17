package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func registerRemoteJobCaller(r *registry.Registry, client *federation.Client) {
	if client == nil {
		return
	}
	must(r.Add(registry.Tool{
		Name:        "fleet_remote_job_submit",
		Description: "Submit a signed, idempotent Remote Job reservation to a paired node. This phase only reserves the request; it does not launch a process. Requires explicit confirm=true.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"target":     registry.String("exact paired SSH target name"),
			"request_id": registry.String("stable caller-generated idempotency key; reuse this exact ID when checking or retrying"),
			"payload":    registry.String("exact UTF-8 request payload; currently stored but never executed"),
			"confirm":    registry.Boolean("explicit approval to reserve this remote request"),
		}, []string{"target", "request_id", "payload", "confirm"}),
		Source: "core",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Target    string `json:"target"`
				RequestID string `json:"request_id"`
				Payload   string `json:"payload"`
				Confirm   bool   `json:"confirm"`
			}
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			if !in.Confirm {
				return nil, fmt.Errorf("confirm=true is required to reserve a remote job request")
			}
			if strings.TrimSpace(in.Target) == "" || strings.TrimSpace(in.RequestID) == "" {
				return nil, fmt.Errorf("target and request_id are required")
			}
			return client.SubmitRemoteJobReservation(ctx, in.Target, in.RequestID, in.Payload)
		},
	}))
	must(r.Add(registry.Tool{
		Name:        "fleet_remote_job_status",
		Description: "Query a signed Remote Job reservation on a paired node using the original request ID and exact original payload. Read-only; never executes or reconciles work.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"target":     registry.String("exact paired SSH target name"),
			"request_id": registry.String("original stable request ID"),
			"payload":    registry.String("exact original UTF-8 payload used at submission"),
			"confirm":    registry.Boolean("explicit approval to query the remote job status"),
		}, []string{"target", "request_id", "payload", "confirm"}),
		Source:      "core",
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Target    string `json:"target"`
				RequestID string `json:"request_id"`
				Payload   string `json:"payload"`
				Confirm   bool   `json:"confirm"`
			}
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			if !in.Confirm {
				return nil, fmt.Errorf("confirm=true is required to query remote job status")
			}
			if strings.TrimSpace(in.Target) == "" || strings.TrimSpace(in.RequestID) == "" {
				return nil, fmt.Errorf("target and request_id are required")
			}
			return client.RemoteJobStatus(ctx, in.Target, in.RequestID, in.Payload)
		},
	}))
	must(r.Add(registry.Tool{
		Name:        "fleet_remote_job_reconcile",
		Description: "Request read-only reconciliation evidence from a paired node for an existing Remote Job. Never changes ledger state or replays a process.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"target":     registry.String("exact paired SSH target name"),
			"request_id": registry.String("original stable request ID"),
			"payload":    registry.String("exact original UTF-8 payload used at submission"),
			"confirm":    registry.Boolean("explicit approval to inspect remote reconciliation evidence"),
		}, []string{"target", "request_id", "payload", "confirm"}),
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		Source:      "core",
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in struct {
				Target    string `json:"target"`
				RequestID string `json:"request_id"`
				Payload   string `json:"payload"`
				Confirm   bool   `json:"confirm"`
			}
			if err := decode(raw, &in); err != nil {
				return nil, err
			}
			if !in.Confirm {
				return nil, fmt.Errorf("confirm=true is required to inspect remote reconciliation evidence")
			}
			if strings.TrimSpace(in.Target) == "" || strings.TrimSpace(in.RequestID) == "" {
				return nil, fmt.Errorf("target and request_id are required")
			}
			return client.RemoteJobReconcile(ctx, in.Target, in.RequestID, in.Payload)
		},
	}))

}
