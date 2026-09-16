//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCycomHeadlessEnvironmentTargetsRecordedSocket(t *testing.T) {
	d := t.TempDir()
	runtime := filepath.Join(d, "run")
	state := filepath.Join(d, "state")
	os.MkdirAll(runtime, 0700)
	os.MkdirAll(state, 0700)
	// Environment parser requires a real socket, so absence must fail closed rather than falling back to main desktop.
	os.WriteFile(filepath.Join(state, "wayland-display"), []byte("wayland-ai\n"), 0600)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("CYCOM_AI_DESKTOP_STATE", state)
	if _, _, err := cycomHeadlessEnvironment(); err == nil {
		t.Fatal("expected missing isolated socket to fail closed")
	}
}
