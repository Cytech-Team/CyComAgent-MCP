package embed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestEmbeddedRuntimeUsesNormalPolicyAndRegistry(t *testing.T) {
	rt, err := New(Config{Version: "test", StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Version() != "test" {
		t.Fatalf("Version() = %q", rt.Version())
	}
	tools := rt.ListTools()
	if len(tools) == 0 {
		t.Fatal("expected embedded runtime tools")
	}
	_, err = rt.Call(context.Background(), "system_info", json.RawMessage(`{"reason":"embedded runtime test"}`))
	if err != nil {
		t.Fatalf("system_info through embedded runtime: %v", err)
	}
}


func TestEmbeddedRuntimeCanRegisterHostTool(t *testing.T) {
	rt, err := New(Config{Version: "test", StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.RegisterTool(Tool{
		Name:        "shell_ping",
		Description: "test host tool",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		Source:      "host:test",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"pong": true}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	value, err := rt.Call(context.Background(), "shell_ping", json.RawMessage(`{"reason":"host integration test"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, ok := value.(map[string]any)
	if !ok || result["pong"] != true {
		t.Fatalf("unexpected result: %#v", value)
	}
	rt.RemoveSource("host:test")
	if _, err := rt.Call(context.Background(), "shell_ping", json.RawMessage(`{"reason":"after removal"}`)); err == nil {
		t.Fatal("expected removed host tool to be unavailable")
	}
}
