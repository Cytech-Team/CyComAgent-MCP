//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

const (
	cyShellShellUnit    = "cyshell-shell-ui.service"
	cyShellSettingsUnit = "cyshell-settings-surface.service"
)

func isCyShellSessionEnvironment() bool {
	for _, entry := range desktopEnvironment() {
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch k {
		case "XDG_CURRENT_DESKTOP", "XDG_SESSION_DESKTOP", "DESKTOP_SESSION", "CYSHELL_LABWC_BRIDGE":
			value := strings.ToLower(v)
			if strings.Contains(value, "cyshell") || strings.Contains(value, "labwc") {
				return true
			}
		}
	}
	return false
}

func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }

func cyshellSessionStatus(ctx context.Context, session sessionbridge.Client) (sessionbridge.Status, error) {
	if !session.Available() {
		return sessionbridge.Status{}, fmt.Errorf("CyShell native IPC requires the authenticated graphical login-session bridge")
	}
	status, err := session.Status(ctx)
	if err != nil {
		return sessionbridge.Status{}, err
	}
	return status, nil
}

func cyshellUnitPID(ctx context.Context, session sessionbridge.Client, unit string) (int, error) {
	res, err := session.Exec(ctx, sessionbridge.ExecRequest{Command: "systemctl --user show -p MainPID --value " + shellQuote(unit), TimeoutSeconds: 5, MaxOutputBytes: 1024})
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("query CyShell surface %s: %s", unit, strings.TrimSpace(res.Stderr))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("CyShell surface %s is not running", unit)
	}
	return pid, nil
}

func cyshellIPC(ctx context.Context, session sessionbridge.Client, surface, target, method string, args ...string) (string, error) {
	if _, err := cyshellSessionStatus(ctx, session); err != nil {
		return "", err
	}
	unit := cyShellShellUnit
	if surface == "settings" {
		unit = cyShellSettingsUnit
	}
	// Settings is an independent surface and can be started on demand. The
	// shell surface is deliberately not replaced by KDE System Settings.
	if surface == "settings" {
		start, err := session.Exec(ctx, sessionbridge.ExecRequest{Command: "systemctl --user start " + shellQuote(unit), TimeoutSeconds: 8, MaxOutputBytes: 2048})
		if err != nil {
			return "", fmt.Errorf("start CyShell Settings surface: %w", err)
		}
		if start.ExitCode != 0 {
			return "", fmt.Errorf("start CyShell Settings surface: %s", strings.TrimSpace(start.Stderr))
		}
	}
	pid, err := cyshellUnitPID(ctx, session, unit)
	if err != nil {
		return "", err
	}
	parts := []string{"qs", "ipc", "--pid", strconv.Itoa(pid), "call", target, method}
	for _, arg := range args {
		parts = append(parts, arg)
	}
	quoted := make([]string, len(parts))
	for i, v := range parts {
		quoted[i] = shellQuote(v)
	}
	call, err := session.Exec(ctx, sessionbridge.ExecRequest{Command: strings.Join(quoted, " "), TimeoutSeconds: 10, MaxOutputBytes: 1 << 20})
	if err != nil {
		return "", fmt.Errorf("CyShell IPC %s.%s: %w", target, method, err)
	}
	if call.ExitCode != 0 {
		return "", fmt.Errorf("CyShell IPC %s.%s failed: %s", target, method, strings.TrimSpace(call.Stderr))
	}
	result := strings.TrimSpace(call.Stdout)
	if strings.Contains(result, "SETTINGS_INVALID_KEY") || strings.Contains(result, "SETTINGS_UNKNOWN_KEY") || strings.Contains(result, "SETTINGS_SET_FAILURE") || strings.Contains(result, "SETTINGS_OPEN_FAILED") || strings.Contains(result, "CONTROL_CENTER_OPEN_FAILED") || strings.Contains(result, "CONTROL_CENTER_HIDE_FAILED") || strings.Contains(result, "CONTROL_CENTER_TOGGLE_FAILED") {
		return "", fmt.Errorf("CyShell rejected %s.%s: %s", target, method, result)
	}
	return result, nil
}

func cyshellNativeState(ctx context.Context, session sessionbridge.Client) (map[string]any, error) {
	status, err := cyshellSessionStatus(ctx, session)
	if err != nil {
		return nil, err
	}
	shellPID, err := cyshellUnitPID(ctx, session, cyShellShellUnit)
	if err != nil {
		return nil, fmt.Errorf("CyShell shell surface is not running: %w", err)
	}
	raw, err := cyshellIPC(ctx, session, "shell", "agent", "state")
	if err != nil {
		return nil, err
	}
	parsed, err := parseCyShellIPCJSON(raw)
	if err != nil {
		return nil, err
	}
	out, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CyShell agent state has an invalid response shape")
	}
	out["desktop"] = "CyShell"
	out["session_bridge"] = true
	out["session"] = status
	out["cyshell_builtin"] = map[string]any{"available": true, "shell_surface_pid": shellPID, "settings_surface": "on-demand"}
	if snapshot, ok := out["workspaces"].(map[string]any); ok {
		out["workspace_snapshot"] = snapshot
		out["workspaces"] = snapshot["workspaces"]
		out["workspace_available"] = snapshot["available"]
	}
	return out, nil
}

func cyshellSettingsSearch(ctx context.Context, session sessionbridge.Client, query string, max int) (any, error) {
	if max <= 0 {
		max = 15
	}
	if max > 50 {
		max = 50
	}
	result, err := cyshellIPC(ctx, session, "shell", "settings", "search", query, strconv.Itoa(max))
	if err != nil {
		return nil, err
	}
	var parsed any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return map[string]any{"backend": "cyshell-settings-ipc", "raw": result}, nil
	}
	return map[string]any{"backend": "cyshell-settings-ipc", "results": parsed}, nil
}

func cyshellOpenSettings(ctx context.Context, session sessionbridge.Client, page string) (any, error) {
	method, args := "open", []string{}
	if strings.TrimSpace(page) != "" {
		method = "openWithTab"
		args = []string{page}
	}
	result, err := cyshellIPC(ctx, session, "settings", "settings", method, args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "backend": "cyshell-settings-ipc", "page": page, "result": result}, nil
}

func cyshellSettingsGet(ctx context.Context, session sessionbridge.Client, key string) (any, error) {
	raw, err := cyshellIPC(ctx, session, "settings", "settings", "get", key)
	if err != nil {
		return nil, err
	}
	return parseCyShellIPCJSON(raw)
}
func cyshellSettingsSet(ctx context.Context, session sessionbridge.Client, key, value string) (map[string]any, error) {
	current, err := cyshellSettingsGet(ctx, session, key)
	if err != nil {
		return nil, err
	}
	var next any
	switch current.(type) {
	case bool:
		parsed, e := strconv.ParseBool(strings.TrimSpace(value))
		if e != nil {
			return nil, fmt.Errorf("CyShell setting %s requires true or false", key)
		}
		next = parsed
	case float64:
		parsed, e := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if e != nil {
			return nil, fmt.Errorf("CyShell setting %s requires a number", key)
		}
		next = parsed
	case string:
		next = value
	default:
		return nil, fmt.Errorf("CyShell setting %s is not a supported scalar setting", key)
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	result, err := cyshellIPC(ctx, session, "settings", "settings", "set", key, string(encoded))
	if err != nil {
		return nil, err
	}
	if result != "SETTINGS_SET" {
		return nil, fmt.Errorf("CyShell SettingsData did not accept %s: %s", key, result)
	}
	updated, err := cyshellSettingsGet(ctx, session, key)
	if err != nil {
		return nil, err
	}
	if fmt.Sprint(updated) != fmt.Sprint(next) {
		return nil, fmt.Errorf("CyShell setting %s did not read back the requested value", key)
	}
	return map[string]any{"ok": true, "backend": "cyshell-settingsdata-ipc", "key": key, "value": updated, "result": result, "verified": true}, nil
}

func cyshellControlCenter(ctx context.Context, session sessionbridge.Client, action, section string) (any, error) {
	method, args := "open", []string{}
	switch action {
	case "open":
		if section != "" {
			method = "openWith"
			args = []string{section}
		}
	case "close":
		method = "hide"
	case "toggle":
		method = "toggle"
	default:
		return nil, fmt.Errorf("unsupported CyShell Control Center action %q", action)
	}
	result, err := cyshellIPC(ctx, session, "shell", "control-center", method, args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "backend": "cyshell-control-center-ipc", "section": section, "result": result}, nil
}

func cyshellQueryUI(ctx context.Context, session sessionbridge.Client, query string) (any, error) {
	raw, err := cyshellIPC(ctx, session, "shell", "agent", "queryUi", query)
	if err != nil {
		return nil, err
	}
	parsed, err := parseCyShellIPCJSON(raw)
	if err != nil {
		return nil, err
	}
	return map[string]any{"backend": "cyshell-agent-query-ui", "matches": parsed}, nil
}
func cyshellWindowAction(ctx context.Context, session sessionbridge.Client, action string, selector map[string]any) (any, error) {
	b, _ := json.Marshal(selector)
	result, err := cyshellIPC(ctx, session, "shell", "agent", "windowAction", action, string(b))
	if err != nil {
		return nil, err
	}
	if strings.Contains(result, "NOT_FOUND") || strings.Contains(result, "UNSUPPORTED") {
		return nil, fmt.Errorf("CyShell window action rejected selector: %s", result)
	}
	return map[string]any{"ok": true, "backend": "cyshell-agent-window-ipc", "result": result}, nil
}

func cyshellMachineSnapshot(ctx context.Context, session sessionbridge.Client) (map[string]any, error) {
	state, err := cyshellNativeState(ctx, session)
	if err != nil {
		return nil, err
	}
	machine, ok := state["machine"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CyShell agent state has no machine snapshot")
	}
	return machine, nil
}
func nestedMap(value any, keys ...string) (map[string]any, error) {
	current, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CyShell machine state is missing %s", strings.Join(keys, "."))
	}
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("CyShell machine state is missing %s", strings.Join(keys, "."))
		}
		current = next
	}
	return current, nil
}
func cyshellMachineRead(ctx context.Context, session sessionbridge.Client, action string) (string, error) {
	machine, err := cyshellMachineSnapshot(ctx, session)
	if err != nil {
		return "", err
	}
	var value any
	switch action {
	case "wifi.toggle", "wifi.enable", "wifi.disable":
		m, e := nestedMap(machine, "network", "wifi")
		if e != nil {
			return "", e
		}
		value = m["enabled"]
		if value == true {
			return "enabled", nil
		}
		return "disabled", nil
	case "bluetooth.toggle", "bluetooth.enable", "bluetooth.disable":
		m, e := nestedMap(machine, "bluetooth")
		if e != nil {
			return "", e
		}
		value = m["enabled"]
		if value == true {
			return "true", nil
		}
		return "false", nil
	case "audio.volume.set":
		m, e := nestedMap(machine, "audio", "output")
		if e != nil {
			return "", e
		}
		value = m["volume"]
	case "mic.volume.set":
		m, e := nestedMap(machine, "audio", "input")
		if e != nil {
			return "", e
		}
		value = m["volume"]
	case "audio.mute.toggle", "audio.mute.set":
		m, e := nestedMap(machine, "audio", "output")
		if e != nil {
			return "", e
		}
		value = m["muted"]
	case "mic.mute.toggle", "mic.mute.set":
		m, e := nestedMap(machine, "audio", "input")
		if e != nil {
			return "", e
		}
		value = m["muted"]
	case "brightness.set":
		m, e := nestedMap(machine, "brightness")
		if e != nil {
			return "", e
		}
		value = m["level"]
	case "power.profile.set":
		m, e := nestedMap(machine, "powerProfile")
		if e != nil {
			return "", e
		}
		value = m["profile"]
	default:
		return "", fmt.Errorf("unsupported CyShell machine action %q", action)
	}
	if value == nil {
		return "", fmt.Errorf("CyShell machine state has no value for %s", action)
	}
	return fmt.Sprint(value), nil
}
func normalizeCyShellMachineAction(action, value string) (string, string) {
	switch action {
	case "wifi.toggle":
		if value == "enabled" {
			return "wifi.enable", ""
		}
		if value == "disabled" {
			return "wifi.disable", ""
		}
	case "wifi.enable", "wifi.disable":
		if value == "enabled" {
			return "wifi.enable", ""
		}
		if value == "disabled" {
			return "wifi.disable", ""
		}
	case "bluetooth.toggle", "bluetooth.enable", "bluetooth.disable":
		if value == "true" {
			return "bluetooth.enable", ""
		}
		if value == "false" {
			return "bluetooth.disable", ""
		}
	case "audio.mute.toggle", "audio.mute.set":
		if value == "true" || value == "false" {
			return "audio.mute.set", value
		}
	case "mic.mute.toggle", "mic.mute.set":
		if value == "true" || value == "false" {
			return "mic.mute.set", value
		}
	}
	return action, value
}
func cyshellMachineApply(ctx context.Context, session sessionbridge.Client, action, value string) error {
	action, value = normalizeCyShellMachineAction(action, value)
	result, err := cyshellIPC(ctx, session, "shell", "agent", "machineAction", action, value)
	if err != nil {
		return err
	}
	if strings.Contains(result, "FAILED") || strings.Contains(result, "INVALID") || strings.Contains(result, "UNSUPPORTED") {
		return fmt.Errorf("CyShell machine action %s failed: %s", action, result)
	}
	return nil
}

func parseCyShellIPCJSON(raw string) (any, error) {
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("CyShell returned invalid JSON: %w", err)
	}
	return out, nil
}
