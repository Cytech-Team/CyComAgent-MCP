//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentWorkspaceTargetNormalization(t *testing.T) {
	for input, want := range map[string]string{
		"":                "auto",
		"auto":            "auto",
		"current":         "current",
		"agent_workspace": "agent_workspace",
		"workspace":       "agent_workspace",
		"headless":        "agent_workspace",
	} {
		got, err := normalizeDesktopTarget(input)
		if err != nil || got != want {
			t.Fatalf("normalizeDesktopTarget(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := normalizeDesktopTarget("bogus"); err == nil {
		t.Fatal("invalid target should fail")
	}

	t.Setenv("CYCOM_AGENT_WORKSPACE_AUTO", "0")
	if agentWorkspaceAutoEnabled() {
		t.Fatal("workspace auto preference should be disabled")
	}
	t.Setenv("CYCOM_AGENT_WORKSPACE_AUTO", "1")
	if !agentWorkspaceAutoEnabled() {
		t.Fatal("workspace auto preference should be enabled")
	}
}

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

func TestCycomHeadlessEnvironmentMasksPhysicalSessionBus(t *testing.T) {
	display := fakeHeadlessDesktop(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/12345/bus")
	env, gotDisplay, err := cycomHeadlessEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if gotDisplay != display {
		t.Fatalf("display=%q; want %q", gotDisplay, display)
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "DBUS_SESSION_BUS_ADDRESS=") {
			if value := strings.TrimPrefix(entry, "DBUS_SESSION_BUS_ADDRESS="); value != isolatedAgentWorkspaceBusAddress {
				t.Fatalf("physical session bus leaked through isolated desktop environment: %q", value)
			}
			return
		}
	}
	t.Fatal("isolated desktop environment omitted explicit unreachable session bus address")
}

func fakeUnavailableHeadlessDesktop(t *testing.T) {
	t.Helper()
	runtimeDir := t.TempDir()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "wayland-display"), []byte("wayland-unavailable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("CYCOM_AI_DESKTOP_STATE", stateDir)
	t.Setenv("CYCOM_AGENT_WORKSPACE_AUTO", "1")
	// Keep physical-session discovery and adapters out of these tests. The
	// fake values are deliberately unusable and PATH has no desktop tools.
	t.Setenv("DISPLAY", ":physical-test")
	t.Setenv("WAYLAND_DISPLAY", "wayland-physical-test")
	t.Setenv("PATH", t.TempDir())
}

func TestAutoDesktopCaptureFailsClosedWhenWorkspaceUnavailable(t *testing.T) {
	fakeUnavailableHeadlessDesktop(t)
	path := filepath.Join(t.TempDir(), "must-not-capture.png")
	raw, _ := json.Marshal(map[string]any{"target": "auto", "output_path": path})
	if _, err := desktopCapture(context.Background(), raw, t.TempDir()); err == nil || !strings.Contains(err.Error(), "Agent Workspace is unavailable") {
		t.Fatalf("auto capture error = %v; want clear Agent Workspace unavailable error", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("capture output exists after unavailable-workspace error: stat err=%v", err)
	}
}

func TestAutoDesktopInputFailsClosedWhenWorkspaceUnavailable(t *testing.T) {
	fakeUnavailableHeadlessDesktop(t)
	raw, _ := json.Marshal(map[string]any{"target": "auto", "action": "click", "button": 1})
	if _, err := desktopInput(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "Agent Workspace is unavailable") {
		t.Fatalf("auto input error = %v; want clear Agent Workspace unavailable error", err)
	}
}

func TestAnyAppAutoIsolationFailsClosedWhenWorkspaceUnavailable(t *testing.T) {
	fakeUnavailableHeadlessDesktop(t)
	physicalEnv := []string{"DISPLAY=:physical-test", "WAYLAND_DISPLAY=wayland-physical-test", "PATH=/physical/bin"}
	got, err := anyAppIsolatedDesktopEnv(physicalEnv)
	if err == nil || !strings.Contains(err.Error(), "Agent Workspace is unavailable") {
		t.Fatalf("Any App auto route error = %v; want clear unavailable-workspace error", err)
	}
	if got != nil {
		t.Fatalf("Any App returned a physical desktop environment after isolation failed: %#v", got)
	}
}

func TestDesktopTargetRequiresWorkspaceExceptExplicitOptOut(t *testing.T) {
	fakeUnavailableHeadlessDesktop(t)
	if _, _, _, _, err := resolveDesktopTarget("auto"); err == nil || !strings.Contains(err.Error(), "Agent Workspace is unavailable") {
		t.Fatalf("auto target error = %v; want unavailable-workspace error", err)
	}
	if _, _, _, _, err := resolveDesktopTarget("agent_workspace"); err == nil {
		t.Fatal("explicit Agent Workspace target should fail when its socket is unavailable")
	}
	target, _, _, workspace, err := resolveDesktopTarget("current")
	if err != nil || target != "current" || workspace {
		t.Fatalf("explicit current target = (%q, workspace=%t, %v)", target, workspace, err)
	}

	t.Setenv("CYCOM_AGENT_WORKSPACE_AUTO", "0")
	target, _, _, workspace, err = resolveDesktopTarget("auto")
	if err != nil || target != "current" || workspace {
		t.Fatalf("operator opt-out auto target = (%q, workspace=%t, %v)", target, workspace, err)
	}
	physicalEnv := []string{"DISPLAY=:physical-test", "PATH=/physical/bin"}
	got, err := anyAppIsolatedDesktopEnv(physicalEnv)
	if err != nil || strings.Join(got, "\x00") != strings.Join(physicalEnv, "\x00") {
		t.Fatalf("Any App operator opt-out env = %#v, %v; want original physical env", got, err)
	}
}
