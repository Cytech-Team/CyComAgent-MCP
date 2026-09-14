//go:build linux

package tools

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAnyAppCallTimeoutIsBounded(t *testing.T) {
	if defaultAnyAppCallTimeout <= 0 || defaultAnyAppCallTimeout > 30*time.Second {
		t.Fatalf("unexpected Any App timeout: %v", defaultAnyAppCallTimeout)
	}
}

func TestDesktopEnvironmentHasRuntimeFallback(t *testing.T) {
	env := desktopEnvironment()
	joined := strings.Join(env, "\n")
	if os.Getenv("XDG_RUNTIME_DIR") != "" && !strings.Contains(joined, "XDG_RUNTIME_DIR=") {
		t.Fatal("desktop environment dropped XDG_RUNTIME_DIR")
	}
}
