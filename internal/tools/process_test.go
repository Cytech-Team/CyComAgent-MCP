package tools

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

func TestProcessExecTimeoutKillsProcessGroup(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	raw, _ := json.Marshal(map[string]any{"command": "sleep 30 & echo $! > '" + pidfile + "'; wait", "timeout_seconds": 1, "shell": "/bin/bash"})
	out, err := processExec(context.Background(), raw, broker.Client{}, sessionbridge.NewClient(filepath.Join(t.TempDir(), "missing-session.sock")))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["timed_out"] != true {
		t.Fatalf("out=%#v", m)
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("child pid %d survived process group timeout", pid)
}

func TestProcessExecStdin(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"command": "cat", "stdin": "hello-stdin"})
	out, err := processExec(context.Background(), raw, broker.Client{}, sessionbridge.NewClient(filepath.Join(t.TempDir(), "missing-session.sock")))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if strings.TrimSpace(m["stdout"].(string)) != "hello-stdin" {
		t.Fatalf("out=%#v", m)
	}
}

func TestResolveExecutionContext(t *testing.T) {
	tests := []struct {
		name             string
		requested        string
		command          string
		privileged       bool
		desktopAvailable bool
		want             string
		wantErr          bool
	}{
		{name: "auto service", command: "printf ok", want: "service"},
		{name: "auto desktop", command: "powerprofilesctl set balanced", desktopAvailable: true, want: "desktop"},
		{name: "explicit desktop", requested: "desktop", command: "my-gui", desktopAvailable: true, want: "desktop"},
		{name: "explicit user", requested: "user", command: "id", desktopAvailable: true, want: "user"},
		{name: "desktop without bridge", requested: "desktop", command: "my-gui", wantErr: true},
		{name: "auto privileged", command: "id", privileged: true, want: "system"},
		{name: "system requires privileged", requested: "system", command: "id", wantErr: true},
		{name: "desktop cannot be privileged", requested: "desktop", command: "id", privileged: true, desktopAvailable: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveExecutionContext(tt.requested, tt.command, tt.privileged, tt.desktopAvailable)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got context %q; expected error", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestExecutionContextNeedsBridgeProbe(t *testing.T) {
	if executionContextNeedsBridgeProbe("service", "powerprofilesctl get", false) {
		t.Fatal("explicit service should not probe desktop bridge")
	}
	if executionContextNeedsBridgeProbe("", "git status", false) {
		t.Fatal("ordinary auto service command should not probe desktop bridge")
	}
	if !executionContextNeedsBridgeProbe("", "powerprofilesctl get", false) {
		t.Fatal("recognized auto desktop command should probe desktop bridge")
	}
	if !executionContextNeedsBridgeProbe("desktop", "custom-gui", false) {
		t.Fatal("explicit desktop should probe desktop bridge")
	}
	if executionContextNeedsBridgeProbe("desktop", "custom-gui", true) {
		t.Fatal("privileged request should be rejected/routed before desktop probing")
	}
}

func TestLooksLikeDesktopCommand(t *testing.T) {
	for _, command := range []string{
		"noctalia --daemon",
		"powerprofilesctl set balanced",
		"notify-send hello",
		"wl-copy hello",
		"gdbus call --session --dest org.example",
		"wlrctl pointer move 10 20",
		"whydotool mousemove --absolute -x 10 -y 20",
	} {
		if !looksLikeDesktopCommand(command) {
			t.Fatalf("expected desktop routing for %q", command)
		}
	}
	if looksLikeDesktopCommand("git status") {
		t.Fatal("headless command was classified as desktop")
	}
}

func fakeHeadlessDesktop(t *testing.T) string {
	t.Helper()
	runtimeDir := t.TempDir()
	stateDir := t.TempDir()
	display := "wayland-test"
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, display))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.WriteFile(filepath.Join(stateDir, "wayland-display"), []byte(display+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("CYCOM_AI_DESKTOP_STATE", stateDir)
	return display
}

func TestProcessDesktopTarget(t *testing.T) {
	display := fakeHeadlessDesktop(t)
	t.Setenv("CYCOM_AGENT_WORKSPACE_AUTO", "1")
	route, err := prepareProcessDesktopRoute("", "wlrctl pointer move 10 20", "auto", map[string]string{"KEEP": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if route.Target != "agent_workspace" || route.WaylandDisplay != display {
		t.Fatalf("unexpected auto route: %#v", route)
	}
	if route.Env["KEEP"] != "yes" {
		t.Fatalf("caller environment was not preserved: %#v", route.Env)
	}

	route, err = prepareProcessDesktopRoute("current", "wlrctl pointer move 10 20", "auto", nil)
	if err != nil || route.Target != "current" {
		t.Fatalf("explicit current route = %#v, %v", route, err)
	}

	route, err = prepareProcessDesktopRoute("auto", "git status", "auto", nil)
	if err != nil || route.Target != "" {
		t.Fatalf("non-input command should keep normal routing: %#v, %v", route, err)
	}

	if _, err := prepareProcessDesktopRoute("bogus", "wlrctl pointer move 1 2", "auto", nil); err == nil {
		t.Fatal("unsupported desktop target should fail")
	}

	t.Setenv("CYCOM_AGENT_WORKSPACE_AUTO", "0")
	route, err = prepareProcessDesktopRoute("auto", "wlrctl pointer move 10 20", "auto", nil)
	if err != nil || route.Target != "current" {
		t.Fatalf("disabled workspace preference should route current: %#v, %v", route, err)
	}

	route, err = prepareProcessDesktopRoute("headless", "wlrctl pointer move 10 20", "auto", nil)
	if err != nil || route.Target != "agent_workspace" {
		t.Fatalf("legacy headless alias should force Agent Workspace: %#v, %v", route, err)
	}
}

func TestProcessAutoDesktopRoutingHonorsExecutionContext(t *testing.T) {
	display := fakeHeadlessDesktop(t)
	t.Setenv("CYCOM_AGENT_WORKSPACE_AUTO", "1")

	for _, command := range []string{"notify-send hello", "xdg-open https://example.test", "wl-copy text", "noctalia-shell"} {
		route, err := prepareProcessDesktopRoute("auto", command, "auto", nil)
		if err != nil || route.Target != "agent_workspace" || route.WaylandDisplay != display {
			t.Errorf("auto desktop command %q route = %#v, %v; want isolated Agent Workspace", command, route, err)
		}
	}

	route, err := prepareProcessDesktopRoute("auto", "notify-send hello", "service", nil)
	if err != nil || route.Target != "" {
		t.Fatalf("explicit service non-input route = %#v, %v; want service context", route, err)
	}

	for _, context := range []string{"desktop", "user"} {
		route, err = prepareProcessDesktopRoute("auto", "notify-send hello", context, nil)
		if err != nil || route.Target != "" {
			t.Errorf("explicit %s context route = %#v, %v; want login-session bridge semantics", context, route, err)
		}
	}

	route, err = prepareProcessDesktopRoute("auto", "wlrctl pointer move 10 20", "service", nil)
	if err != nil || route.Target != "agent_workspace" {
		t.Fatalf("explicit service input route = %#v, %v; want existing isolated input handling", route, err)
	}
	for _, command := range []string{
		"whydotool --force-portal click 1",
		"/tmp/test-bin/whydotool --force-portal click 1",
		"env TEST=1 /tmp/test-bin/whydotool --force-portal click 1",
	} {
		route, err = prepareProcessDesktopRoute("auto", command, "service", nil)
		if err != nil || route.Target != "agent_workspace" || route.WaylandDisplay != display {
			t.Errorf("explicit service route for %q = %#v, %v; want isolated Agent Workspace", command, route, err)
		}
	}

	for _, command := range []string{
		"ydotool --socket-path=/tmp/ydotool.sock click 0xC0",
		"/tmp/test-bin/xdotool --clearmodifiers key Return",
	} {
		if _, err := prepareProcessDesktopRoute("auto", command, "service", nil); err == nil || !strings.Contains(err.Error(), "refusing global input injector") {
			t.Errorf("auto service route for %q = %v; want global-injector refusal", command, err)
		}
	}
}

func TestProcessExecAutoInputFailsClosedBeforeCommandRuns(t *testing.T) {
	fakeUnavailableHeadlessDesktop(t)
	sentinel := filepath.Join(t.TempDir(), "command-ran")
	command := "printf ran > '" + sentinel + "'; wlrctl pointer move 1 2"
	raw, _ := json.Marshal(map[string]any{
		"command": command, "shell": "/bin/sh", "execution_context": "service", "desktop_target": "auto",
	})
	_, err := processExec(context.Background(), raw, broker.Client{}, sessionbridge.NewClient(filepath.Join(t.TempDir(), "missing-session.sock")))
	if err == nil || !strings.Contains(err.Error(), "Agent Workspace is unavailable") {
		t.Fatalf("process_exec auto route error = %v; want clear Agent Workspace unavailable error", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("process command ran after auto workspace routing failed: stat err=%v", err)
	}
}

func TestProcessExecAutoOptionedInputFailsClosedBeforeCommandRuns(t *testing.T) {
	fakeUnavailableHeadlessDesktop(t)
	for _, inputCommand := range []string{
		"ydotool --socket-path=/tmp/ydotool.sock click 0xC0",
		"/tmp/test-bin/xdotool --clearmodifiers key Return",
		"whydotool --force-portal click 1",
		"/tmp/test-bin/whydotool --force-portal click 1",
		"env TEST=1 /tmp/test-bin/whydotool --force-portal click 1",
	} {
		t.Run(inputCommand, func(t *testing.T) {
			sentinel := filepath.Join(t.TempDir(), "command-ran")
			command := "printf ran > '" + sentinel + "'; " + inputCommand
			raw, _ := json.Marshal(map[string]any{
				"command": command, "shell": "/bin/sh", "execution_context": "service", "desktop_target": "auto",
			})
			_, err := processExec(context.Background(), raw, broker.Client{}, sessionbridge.NewClient(filepath.Join(t.TempDir(), "missing-session.sock")))
			if err == nil || !strings.Contains(err.Error(), "Agent Workspace is unavailable") {
				t.Fatalf("process_exec auto route error = %v; want clear Agent Workspace unavailable error", err)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("desktop input command ran after auto workspace routing failed: stat err=%v", err)
			}
		})
	}
}

func TestProcessExecAutoDesktopCommandFailsClosedBeforeCommandRuns(t *testing.T) {
	fakeUnavailableHeadlessDesktop(t)
	sentinel := filepath.Join(t.TempDir(), "command-ran")
	command := "printf ran > '" + sentinel + "'; notify-send test"
	raw, _ := json.Marshal(map[string]any{
		"command": command, "shell": "/bin/sh", "execution_context": "auto", "desktop_target": "auto",
	})
	_, err := processExec(context.Background(), raw, broker.Client{}, sessionbridge.NewClient(filepath.Join(t.TempDir(), "missing-session.sock")))
	if err == nil || !strings.Contains(err.Error(), "Agent Workspace is unavailable") {
		t.Fatalf("process_exec auto desktop route error = %v; want clear Agent Workspace unavailable error", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("desktop-routed process command ran after auto workspace routing failed: stat err=%v", err)
	}
}

func TestLooksLikeDesktopInputCommand(t *testing.T) {
	for _, command := range []string{
		"wlrctl pointer move 10 20",
		"/usr/bin/wlrctl keyboard type hello",
		"whydotool mousemove --absolute -x 1 -y 2",
		"whydotool --force-portal click 1",
		"/usr/bin/whydotool --force-portal click 1",
		"env TEST=1 /usr/bin/whydotool --force-portal click 1",
		"wtype -k Return",
		"ydotool mousemove --absolute -x 1 -y 2",
		"ydotool --socket-path=/tmp/ydotool.sock click 0xC0",
		"/tmp/test-bin/xdotool --clearmodifiers key Return",
		"env CYCOM_TEST=1 /tmp/test-bin/xdotool key Return",
		"sudo -u nobody -- /tmp/test-bin/ydotool --socket-path=/tmp/ydotool.sock click 0xC0",
		"sh -c 'xdotool click 1'",
		"sh -c 'whydotool --force-portal click 1'",
		"xdotool click 1",
	} {
		if !looksLikeDesktopInputCommand(command) {
			t.Fatalf("expected input classification for %q", command)
		}
	}
	for _, command := range []string{"git status", "wlrctl window focus foo", "grim /tmp/a.png"} {
		if looksLikeDesktopInputCommand(command) {
			t.Fatalf("unexpected input classification for %q", command)
		}
	}
	if looksLikeDesktopInputCommand("printf xdotool") {
		t.Fatal("input classifier treated an argument as a global input injector invocation")
	}
}

func TestHeadlessProcessEnv(t *testing.T) {
	display := fakeHeadlessDesktop(t)
	env, gotDisplay, err := headlessProcessEnv(map[string]string{
		"WAYLAND_DISPLAY":          "wayland-physical",
		"DISPLAY":                  ":0",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/12345/bus",
		"KEEP":                     "yes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotDisplay != display || env["WAYLAND_DISPLAY"] != display {
		t.Fatalf("wrong isolated display: display=%q env=%#v", gotDisplay, env)
	}
	if env["DISPLAY"] != "" {
		t.Fatalf("physical X11 display leaked into headless route: %#v", env)
	}
	if env["DBUS_SESSION_BUS_ADDRESS"] != isolatedAgentWorkspaceBusAddress {
		t.Fatalf("physical session D-Bus leaked into headless route: %#v", env)
	}
	if env["XDG_CURRENT_DESKTOP"] != "labwc-ai" || env["CYCOM_AI_DESKTOP"] != "1" || env["CYCOM_ANYAPP_ISOLATED"] != "1" {
		t.Fatalf("missing isolated desktop markers: %#v", env)
	}
	if env["KEEP"] != "yes" {
		t.Fatalf("caller environment was not preserved: %#v", env)
	}
}

func TestRejectUnsafeHeadlessInput(t *testing.T) {
	for _, command := range []string{
		"ydotool mousemove --absolute -x 1 -y 2",
		"ydotool --socket-path=/tmp/ydotool.sock click 0xC0",
		"/usr/bin/ydotool click 0xC0",
		"/tmp/test-bin/xdotool --clearmodifiers key Return",
		"sudo -u nobody -- /tmp/test-bin/ydotool --socket-path=/tmp/ydotool.sock click 0xC0",
		"xdotool mousemove 1 2",
		"xdotool key Return",
	} {
		if !unsafeGlobalHeadlessInputCommand(command) {
			t.Fatalf("expected unsafe global injector for %q", command)
		}
		if _, err := prepareProcessDesktopRoute("agent_workspace", command, "auto", nil); err == nil {
			t.Fatalf("Agent Workspace route should reject %q", command)
		}
	}
	for _, command := range []string{
		"wlrctl pointer move 1 2",
		"whydotool mousemove --absolute -x 1 -y 2",
		"wtype -k Return",
	} {
		if unsafeGlobalHeadlessInputCommand(command) {
			t.Fatalf("compositor-scoped injector was rejected: %q", command)
		}
	}
}
