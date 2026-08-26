package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/state"
)

func registerState(r *registry.Registry, st *state.Store) {
	must(r.Add(registry.Tool{Name: "state_get", Description: "Read an explicit persistent runtime value. State is application-level and survives MCP/tunnel/runtime restarts.", InputSchema: registry.ObjectSchema(map[string]any{"namespace": registry.String("state namespace"), "key": registry.String("state key")}, []string{"namespace", "key"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Namespace string `json:"namespace"`
			Key       string `json:"key"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		var value any
		if err := st.Get(in.Namespace, in.Key, &value); err != nil {
			return nil, err
		}
		return map[string]any{"namespace": in.Namespace, "key": in.Key, "value": value}, nil
	}}))
	must(r.Add(registry.Tool{Name: "state_put", Description: "Persist an explicit JSON value under a namespace/key. Use this for durable handles or machine/workspace state, not hidden MCP session state.", InputSchema: registry.ObjectSchema(map[string]any{"namespace": registry.String("state namespace"), "key": registry.String("state key"), "value": map[string]any{}}, []string{"namespace", "key", "value"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Namespace string          `json:"namespace"`
			Key       string          `json:"key"`
			Value     json.RawMessage `json:"value"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if len(in.Value) == 0 {
			return nil, fmt.Errorf("value is required")
		}
		var value any
		if err := json.Unmarshal(in.Value, &value); err != nil {
			return nil, err
		}
		if err := st.Put(in.Namespace, in.Key, value); err != nil {
			return nil, err
		}
		return map[string]any{"namespace": in.Namespace, "key": in.Key, "stored": true}, nil
	}}))
	must(r.Add(registry.Tool{Name: "state_delete", Description: "Delete one explicit persistent runtime value.", InputSchema: registry.ObjectSchema(map[string]any{"namespace": registry.String("state namespace"), "key": registry.String("state key")}, []string{"namespace", "key"}), Source: "core", Annotations: map[string]any{"destructiveHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Namespace string `json:"namespace"`
			Key       string `json:"key"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := st.Delete(in.Namespace, in.Key); err != nil {
			return nil, err
		}
		return map[string]any{"namespace": in.Namespace, "key": in.Key, "deleted": true}, nil
	}}))
	must(r.Add(registry.Tool{Name: "state_list", Description: "List keys in one persistent runtime namespace.", InputSchema: registry.ObjectSchema(map[string]any{"namespace": registry.String("state namespace")}, []string{"namespace"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Namespace string `json:"namespace"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		keys, err := st.List(in.Namespace)
		if err != nil {
			return nil, err
		}
		return map[string]any{"namespace": in.Namespace, "keys": keys}, nil
	}}))
}
