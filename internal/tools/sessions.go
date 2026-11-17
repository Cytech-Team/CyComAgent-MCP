package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessions"
	"time"
)

func registerSessions(r *registry.Registry, manager *sessions.Manager, jobManager *jobs.Manager) {
	must(r.Add(registry.Tool{Name: "session_create", Description: "Create a durable Agent session. Supports optional parent_id for child-agent hierarchy.", InputSchema: registry.ObjectSchema(map[string]any{"name": registry.String("optional session label"), "owner": registry.String("optional agent or owner label"), "parent_id": registry.String("optional parent session ID")}, nil), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Name     string `json:"name"`
			Owner    string `json:"owner"`
			ParentID string `json:"parent_id"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return manager.Create(in.Name, in.Owner, in.ParentID)
	}}))
	must(r.Add(registry.Tool{Name: "session_list", Description: "List durable Agent sessions and their lifecycle metadata.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) {
		return map[string]any{"sessions": manager.List()}, nil
	}}))
	must(r.Add(registry.Tool{Name: "session_get", Description: "Get one durable Agent session by ID.", InputSchema: registry.ObjectSchema(map[string]any{"session_id": registry.String("session ID")}, []string{"session_id"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID string `json:"session_id"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return manager.Get(in.ID)
	}}))
	must(r.Add(registry.Tool{Name: "session_touch", Description: "Mark a non-terminal session active and update its last activity timestamp.", InputSchema: registry.ObjectSchema(map[string]any{"session_id": registry.String("session ID")}, []string{"session_id"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID string `json:"session_id"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return manager.Touch(in.ID)
	}}))
	must(r.Add(registry.Tool{Name: "session_transition", Description: "Move a session to active, idle, expired, or closed. Closing/expiring a parent requires all child sessions to be terminal.", InputSchema: registry.ObjectSchema(map[string]any{"session_id": registry.String("session ID"), "status": registry.String("active, idle, expired, or closed")}, []string{"session_id", "status"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID     string `json:"session_id"`
			Status string `json:"status"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return manager.Transition(in.ID, in.Status)
	}}))
	must(r.Add(registry.Tool{Name: "session_prune", Description: "Review or prune old terminal leaf sessions. Dry-run is the default; deletion requires explicit dry_run=false. Active Job-linked sessions and parents with retained children are protected. Audit and Trace stores are never deleted.", InputSchema: registry.ObjectSchema(map[string]any{"retention_days": registry.Integer("minimum age in days; must be at least 1"), "dry_run": map[string]any{"type": "boolean", "description": "Defaults to true; set false only after reviewing the candidate list"}}, []string{"retention_days"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			RetentionDays int   `json:"retention_days"`
			DryRun        *bool `json:"dry_run"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if in.RetentionDays < 1 || in.RetentionDays > 36500 {
			return nil, fmt.Errorf("retention_days must be between 1 and 36500")
		}
		dry := true
		if in.DryRun != nil {
			dry = *in.DryRun
		}
		cutoff := time.Now().UTC().Add(-time.Duration(in.RetentionDays) * 24 * time.Hour)
		protected := map[string]bool{}
		for _, j := range jobManager.List() {
			if j.SessionID != "" && j.Status != "exited" && j.Status != "terminated" && j.Status != "exited-external" && j.Status != "exited-while-runtime-offline" && j.Status != "exited-after-recovery" {
				protected[j.SessionID] = true
			}
		}
		plan := manager.PlanPrune(cutoff, protected)
		if dry {
			return map[string]any{"dry_run": true, "retention_days": in.RetentionDays, "candidate_count": len(plan.Candidates), "plan": plan, "deleted": []string{}}, nil
		}
		removed, err := manager.Prune(plan.Candidates, cutoff, protected)
		if err != nil {
			return nil, err
		}
		return map[string]any{"dry_run": false, "retention_days": in.RetentionDays, "candidate_count": len(plan.Candidates), "plan": plan, "deleted": removed}, nil
	}}))
	must(r.Add(registry.Tool{Name: "session_attach_trace", Description: "Associate a trace ID with a non-terminal Agent session.", InputSchema: registry.ObjectSchema(map[string]any{"session_id": registry.String("session ID"), "trace_id": registry.String("trace ID")}, []string{"session_id", "trace_id"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID      string `json:"session_id"`
			TraceID string `json:"trace_id"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return manager.AttachTrace(in.ID, in.TraceID)
	}}))
}
