package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/trace"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/workflows"
)

func registerWorkflows(r *registry.Registry, m *workflows.Manager) {
	must(r.Add(registry.Tool{Name: "workflow_create", Description: "Create a durable workflow DAG. Steps run only in the service execution domain; each step starts only after all dependencies succeed. Creation does not execute commands.", InputSchema: registry.ObjectSchema(map[string]any{"name": registry.String("workflow name"), "steps": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "items": map[string]any{"type": "object", "properties": map[string]any{"id": registry.String("unique step ID"), "command": registry.String("shell command"), "cwd": registry.String("working directory"), "shell": registry.String("shell executable"), "depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []string{"id", "command"}}}}, []string{"name", "steps"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Name  string           `json:"name"`
			Steps []workflows.Step `json:"steps"`
		}
		if e := decode(raw, &in); e != nil {
			return nil, e
		}
		return m.Create(in.Name, trace.SessionID(ctx), in.Steps)
	}}))
	must(r.Add(registry.Tool{Name: "workflow_list", Description: "List durable workflows and their step states.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) {
		v, e := m.List()
		return map[string]any{"workflows": v}, e
	}}))
	must(r.Add(registry.Tool{Name: "workflow_get", Description: "Inspect a workflow and reconcile its linked Job states.", InputSchema: registry.ObjectSchema(map[string]any{"id": registry.String("workflow ID")}, []string{"id"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID string `json:"id"`
		}
		if e := decode(raw, &in); e != nil {
			return nil, e
		}
		if in.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		return m.Get(in.ID)
	}}))
	must(r.Add(registry.Tool{Name: "workflow_advance", Description: "Dispatch dependency-ready pending steps. Defaults to one concurrent step. Does not retry steps with existing Job IDs.", InputSchema: registry.ObjectSchema(map[string]any{"id": registry.String("workflow ID"), "max_parallel": registry.Integer("maximum concurrent steps, 1-16; default 1")}, []string{"id"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID  string `json:"id"`
			Max int    `json:"max_parallel"`
		}
		if e := decode(raw, &in); e != nil {
			return nil, e
		}
		if in.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		return m.Advance(in.ID, in.Max)
	}}))
	must(r.Add(registry.Tool{Name: "workflow_cancel", Description: "Cancel a workflow: prevents further dispatch, marks pending steps cancelled, and sends TERM to active Job process groups. Inspect status afterward; this does not force-kill processes.", InputSchema: registry.ObjectSchema(map[string]any{"id": registry.String("workflow ID")}, []string{"id"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID string `json:"id"`
		}
		if e := decode(raw, &in); e != nil {
			return nil, e
		}
		if in.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		return m.Cancel(in.ID)
	}}))
	must(r.Add(registry.Tool{Name: "workflow_retry_step", Description: "Explicitly reset one failed, cancelled, or safely-interrupted step for a later workflow_advance. Requires successful dependencies and a terminal linked Job. This action may execute the step again when advanced.", InputSchema: registry.ObjectSchema(map[string]any{"id": registry.String("workflow ID"), "step_id": registry.String("step ID to retry")}, []string{"id", "step_id"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			ID     string `json:"id"`
			StepID string `json:"step_id"`
		}
		if e := decode(raw, &in); e != nil {
			return nil, e
		}
		if in.ID == "" || in.StepID == "" {
			return nil, fmt.Errorf("id and step_id are required")
		}
		return m.RetryStep(in.ID, in.StepID)
	}}))

}
