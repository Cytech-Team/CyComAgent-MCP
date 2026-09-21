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
