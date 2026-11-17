//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

func TestShellQuotePreservesArguments(t *testing.T) {
	if got := shellQuote("a'b c"); got != "'a'\\''b c'" {
		t.Fatalf("shellQuote=%q", got)
	}
}

func TestNormalizeCyShellMachineActionRestoresCapturedState(t *testing.T) {
	cases := []struct{ action, value, wantAction, wantValue string }{
		{"wifi.toggle", "enabled", "wifi.enable", ""}, {"wifi.toggle", "disabled", "wifi.disable", ""},
		{"wifi.disable", "enabled", "wifi.enable", ""}, {"bluetooth.toggle", "false", "bluetooth.disable", ""},
		{"bluetooth.enable", "false", "bluetooth.disable", ""}, {"audio.mute.toggle", "true", "audio.mute.set", "true"},
		{"mic.mute.toggle", "false", "mic.mute.set", "false"}, {"audio.volume.set", "42", "audio.volume.set", "42"},
	}
	for _, tc := range cases {
		a, v := normalizeCyShellMachineAction(tc.action, tc.value)
		if a != tc.wantAction || v != tc.wantValue {
			t.Errorf("normalize(%q,%q)=(%q,%q), want (%q,%q)", tc.action, tc.value, a, v, tc.wantAction, tc.wantValue)
		}
	}
}

func fakeCyShellSession(t *testing.T, handler func(string) string) (sessionbridge.Client, <-chan string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "session.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	commands := make(chan string, 32)
	var mu sync.Mutex
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req struct {
					Action string `json:"action"`
					Exec   struct {
						Command string `json:"command"`
					} `json:"exec"`
				}
				if json.NewDecoder(c).Decode(&req) != nil {
					return
				}
				switch req.Action {
				case "status":
					_ = json.NewEncoder(c).Encode(map[string]any{"ok": true, "status": map[string]any{"pid": 42, "uid": 1000, "desktop": "CyShell", "session_type": "wayland"}})
				case "exec":
					mu.Lock()
					commands <- req.Exec.Command
					mu.Unlock()
					out := handler(req.Exec.Command)
					_ = json.NewEncoder(c).Encode(map[string]any{"ok": true, "exec": map[string]any{"exit_code": 0, "stdout": out}})
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return sessionbridge.NewClient(socket), commands
}

func TestCyShellSettingsSetUsesSettingsDataTypeAndReadback(t *testing.T) {
	value := true
	client, commands := fakeCyShellSession(t, func(command string) string {
		switch {
		case strings.Contains(command, "show -p MainPID"):
			return "4321"
		case strings.Contains(command, "'call' 'settings' 'get' 'darkMode'"):
			if value {
				return "true"
			}
			return "false"
		case strings.Contains(command, "'call' 'settings' 'set' 'darkMode' 'false'"):
			value = false
			return "SETTINGS_SET"
		default:
			return ""
		}
	})
	result, err := cyshellSettingsSet(context.Background(), client, "darkMode", "false")
	if err != nil {
		t.Fatal(err)
	}
	if result["verified"] != true || result["value"] != false {
		t.Fatalf("unexpected SettingsData result: %#v", result)
	}
	seen := []string{<-commands, <-commands, <-commands, <-commands, <-commands, <-commands, <-commands, <-commands, <-commands}
	if !strings.Contains(seen[0], "systemctl --user start 'cyshell-settings-surface.service'") {
		t.Fatalf("settings surface not started: %q", seen[0])
	}
	if !strings.Contains(seen[2], "'call' 'settings' 'get' 'darkMode'") {
		t.Fatalf("initial SettingsData read missing: %q", seen[2])
	}
	if !strings.Contains(seen[5], "'call' 'settings' 'set' 'darkMode' 'false'") {
		t.Fatalf("SettingsData.set not invoked with JSON boolean: %q", seen[5])
	}
}

func TestCyShellSettingsSearchUsesMainShellSearchService(t *testing.T) {
	client, commands := fakeCyShellSession(t, func(command string) string {
		if strings.Contains(command, "show -p MainPID") {
			return "9876"
		}
		if strings.Contains(command, "'call' 'settings' 'search'") {
			return `[{"key":"darkMode","title":"Dark mode"}]`
		}
		return ""
	})
	result, err := cyshellSettingsSearch(context.Background(), client, "dark mode", 10)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["backend"] != "cyshell-settings-ipc" {
		t.Fatalf("wrong backend: %#v", m)
	}
	first := <-commands
	if !strings.Contains(first, "show -p MainPID") {
		t.Fatalf("search should use main shell directly, not start settings surface: %q", first)
	}
	second := <-commands
	if !strings.Contains(second, "'call' 'settings' 'search' 'dark mode' '10'") {
		t.Fatalf("wrong CyShell search IPC: %q", second)
	}
}

func TestCyShellSessionDoesNotFallBackToKWinWithoutBridge(t *testing.T) {
	t.Setenv("XDG_CURRENT_DESKTOP", "CyShell")
	t.Setenv("DESKTOP_SESSION", "cyshell")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	client := sessionbridge.NewClient(filepath.Join(t.TempDir(), "missing-session.sock"))
	state, err := cyshellDesktopState(context.Background(), nil, client)
	if err != nil {
		t.Fatal(err)
	}
	m := state.(map[string]any)
	if m["desktop"] != "CyShell" || m["compositor"] != "Labwc" || m["session_bridge"] != false {
		t.Fatalf("CyShell session was misidentified: %#v", m)
	}
	if _, err := workspaceListNative(context.Background(), client); err == nil || !strings.Contains(err.Error(), "refusing to query KWin") {
		t.Fatalf("expected fail-closed workspace behavior, got %v", err)
	}
}
