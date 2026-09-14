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

func TestDesktopSessionEnvScoreIsCompositorAgnostic(t *testing.T) {
	uid := os.Getuid()
	wayland := map[string]string{
		"WAYLAND_DISPLAY":     "wayland-custom",
		"XDG_RUNTIME_DIR":     "/definitely/not/a/socket",
		"XDG_CURRENT_DESKTOP": "SomeFutureDesktop",
	}
	x11 := map[string]string{"DISPLAY": ":42", "DESKTOP_SESSION": "custom-x11"}
	plain := map[string]string{"XDG_CURRENT_DESKTOP": "not-enough-alone"}
	if desktopSessionEnvScore(wayland, uid) <= desktopSessionEnvScore(plain, uid) {
		t.Fatal("generic Wayland session should outrank a non-graphical environment")
	}
	if desktopSessionEnvScore(x11, uid) <= desktopSessionEnvScore(plain, uid) {
		t.Fatal("generic X11 session should outrank a non-graphical environment")
	}
}
