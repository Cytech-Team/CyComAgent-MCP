package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/audit"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/policy"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func registerGovernance(r *registry.Registry, a *audit.Logger, p *policy.Engine) {
	must(r.Add(registry.Tool{Name: "audit_tail", Description: "Read recent structured tool-call audit events. Arguments are represented by SHA-256 rather than copied into logs to avoid silently persisting secrets.", InputSchema: registry.ObjectSchema(map[string]any{"limit": registry.Integer("event count; default 100, max 1000")}, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Limit int `json:"limit"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		events, err := a.Tail(in.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"events": events, "count": len(events)}, nil
	}}))
	must(r.Add(registry.Tool{Name: "audit_prune", Description: "Prune old structured audit log files.", InputSchema: registry.ObjectSchema(map[string]any{"older_than_days": registry.Integer("retention; default 30 days")}, nil), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Days int `json:"older_than_days"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if in.Days <= 0 {
			in.Days = 30
		}
		n, err := a.Prune(time.Duration(in.Days) * 24 * time.Hour)
		return map[string]any{"removed": n}, err
	}}))
	must(r.Add(registry.Tool{Name: "policy_get", Description: "Inspect the currently loaded runtime policy and policy-file path.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) {
		return map[string]any{"file": p.File(), "policy": p.Snapshot()}, nil
	}}))
	must(r.Add(registry.Tool{Name: "policy_reload", Description: "Reload policy.json from disk without restarting the runtime.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Handler: func(context.Context, json.RawMessage) (any, error) {
		if err := p.Reload(); err != nil {
			return nil, err
		}
		return map[string]any{"reloaded": true, "file": p.File(), "policy": p.Snapshot()}, nil
	}}))
}
