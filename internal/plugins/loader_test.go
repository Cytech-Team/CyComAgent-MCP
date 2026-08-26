package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func TestLoadAndReload(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "echo.json")
	write := func(name string) {
		data := `{"name":"` + name + `","description":"echo","command":"/bin/cat","input_schema":{"type":"object","properties":{},"additionalProperties":true}}`
		if err := os.WriteFile(manifest, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("echo_one")
	r := registry.New()
	m := New(dir)
	loaded, err := m.LoadInto(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || !r.Exists("echo_one") {
		t.Fatalf("loaded=%v", loaded)
	}
	out, err := r.Call(context.Background(), "echo_one", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["x"].(float64) != 1 {
		t.Fatalf("out=%#v", out)
	}
	write("echo_two")
	loaded, err = m.ReloadInto(r)
	if err != nil {
		t.Fatal(err)
	}
	if r.Exists("echo_one") || !r.Exists("echo_two") || len(loaded) != 1 {
		t.Fatalf("reload=%v", loaded)
	}
}
