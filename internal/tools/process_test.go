package tools

import (
	"context"
	"encoding/json"
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
	raw, _ := json.Marshal(map[string]any{"command": "sleep 30 & echo $! > '" + pidfile + "'; wait", "timeout_seconds": 1})
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
	} {
		if !looksLikeDesktopCommand(command) {
			t.Fatalf("expected desktop routing for %q", command)
		}
	}
	if looksLikeDesktopCommand("git status") {
		t.Fatal("headless command was classified as desktop")
	}
}
