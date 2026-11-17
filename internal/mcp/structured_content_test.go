package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

// TestStructuredContentIsAlwaysAnObject covers handlers that naturally return a
// list or scalar. MCP declares structuredContent to be an object, so without
// normalization a strict client rejects the whole result and the tool looks
// broken even though the handler succeeded.
func TestStructuredContentIsAlwaysAnObject(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"map", map[string]any{"ok": true}},
		{"slice", []any{map[string]any{"id": "one"}}},
		{"scalar", "done"},
		{"nil", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := registry.New()
			if err := r.Add(registry.Tool{
				Name:        "probe",
				Description: "structured content probe",
				InputSchema: registry.ObjectSchema(nil, nil),
				Handler:     func(context.Context, json.RawMessage) (any, error) { return tc.value, nil },
			}); err != nil {
				t.Fatal(err)
			}
			s := &Server{Name: "test", Version: "1", Registry: r}
			rr := modernRequest(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"probe","arguments":{"reason":"test"}}}`)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var envelope struct {
				Result struct {
					StructuredContent json.RawMessage `json:"structuredContent"`
				} `json:"result"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			raw := strings.TrimSpace(string(envelope.Result.StructuredContent))
			if raw == "" {
				t.Fatal("structuredContent missing")
			}
			if raw[0] != '{' {
				t.Fatalf("structuredContent is not an object: %s", raw)
			}
			var decoded map[string]any
			if err := json.Unmarshal(envelope.Result.StructuredContent, &decoded); err != nil {
				t.Fatalf("structuredContent not decodable as object: %v", err)
			}
			if tc.name == "slice" {
				if _, ok := decoded["items"]; !ok {
					t.Fatalf("list payload not reachable: %#v", decoded)
				}
			}
		})
	}
}
