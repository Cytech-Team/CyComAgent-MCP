package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func registerJobs(r *registry.Registry, jm *jobs.Manager) {
	must(r.Add(registry.Tool{Name: "job_list", Description: "List persistent background jobs known to this machine runtime.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) { return jm.List(), nil }}))
	must(r.Add(registry.Tool{Name: "job_get", Description: "Inspect one persistent background job.", InputSchema: registry.ObjectSchema(map[string]any{"id": registry.String("job id")}, []string{"id"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID string `json:"id"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return jm.Get(in.ID)
	}}))
	must(r.Add(registry.Tool{Name: "job_tail", Description: "Read the tail of a persistent job log.", InputSchema: registry.ObjectSchema(map[string]any{"id": registry.String("job id"), "lines": registry.Integer("lines to return"), "max_bytes": registry.Integer("maximum bytes to scan")}, []string{"id"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID       string `json:"id"`
			Lines    int    `json:"lines"`
			MaxBytes int    `json:"max_bytes"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		text, trunc, err := jm.Tail(in.ID, in.Lines, in.MaxBytes)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": in.ID, "log": text, "truncated": trunc}, nil
	}}))
	must(r.Add(registry.Tool{Name: "job_signal", Description: "Send a signal to an entire persistent job process group.", InputSchema: registry.ObjectSchema(map[string]any{"id": registry.String("job id"), "signal": registry.String("signal name; default TERM")}, []string{"id"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID     string `json:"id"`
			Signal string `json:"signal"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if in.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		return jm.Signal(in.ID, in.Signal)
	}}))
	must(r.Add(registry.Tool{Name: "job_prune", Description: "Remove completed job metadata/logs older than a retention window.", InputSchema: registry.ObjectSchema(map[string]any{"older_than_hours": registry.Integer("retention window; default 168 hours")}, nil), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Hours int `json:"older_than_hours"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		h := in.Hours
		if h <= 0 {
			h = 168
		}
		n, err := jm.Prune(time.Duration(h) * time.Hour)
		return map[string]any{"removed": n}, err
	}}))
}
