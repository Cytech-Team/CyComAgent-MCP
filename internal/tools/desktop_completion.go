//go:build linux

package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

type desktopReceipt struct {
	ID      string    `json:"id"`
	Action  string    `json:"action"`
	Before  string    `json:"before"`
	After   string    `json:"after"`
	Created time.Time `json:"created_at"`
	Expires time.Time `json:"expires_at"`
}

var desktopReceiptStore = struct {
	sync.Mutex
	items map[string]desktopReceipt
}{items: map[string]desktopReceipt{}}

func registerDesktopCompletion(r *registry.Registry, root broker.Client, session sessionbridge.Client) {
	must(r.Add(registry.Tool{Name: "app_query_ui", Description: "Query a third-party application's AT-SPI accessibility tree and return matching semantic nodes.", InputSchema: registry.ObjectSchema(map[string]any{"app_id": registry.String("optional app ID"), "pid": registry.Integer("optional process ID"), "title": registry.String("optional window title"), "window_id": registry.Integer("optional KWin window ID"), "query": registry.String("text to match"), "include_screenshot": registry.Boolean("include screenshot"), "max_nodes": registry.Integer("maximum nodes")}, nil), Source: "core:anyapp", Annotations: map[string]any{"readOnlyHint": true}, Handler: appQueryUI}))
	must(r.Add(registry.Tool{Name: "desktop_action", Description: "Perform a reversible native CyShell machine action when CyShell is active, otherwise use the active desktop backend for Wi-Fi, Bluetooth, audio, microphone, brightness, or power profile. Returns an undo receipt.", InputSchema: registry.ObjectSchema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"wifi.toggle", "wifi.enable", "wifi.disable", "bluetooth.toggle", "bluetooth.enable", "bluetooth.disable", "audio.volume.set", "audio.mute.toggle", "audio.mute.set", "mic.volume.set", "mic.mute.toggle", "mic.mute.set", "brightness.set", "power.profile.set"}}, "value": registry.String("0-100 for volume/brightness, true/false for mute, or balanced/performance/power-saver")}, []string{"action"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		return desktopAction(ctx, raw, root, session)
	}}))
	must(r.Add(registry.Tool{Name: "desktop_receipts", Description: "List unexpired reversible desktop action receipts.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: desktopReceipts}))
	must(r.Add(registry.Tool{Name: "desktop_undo", Description: "Restore the captured pre-action state for one unexpired desktop action receipt.", InputSchema: registry.ObjectSchema(map[string]any{"receipt": registry.String("receipt ID")}, []string{"receipt"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		return desktopUndo(ctx, raw, root, session)
	}}))
	must(r.Add(registry.Tool{Name: "window_control", Description: "Focus, close, minimize, restore, maximize, unmaximize, fullscreen, or unfullscreen one exact CyShell or KWin window.", InputSchema: registry.ObjectSchema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"focus", "close", "minimize", "restore", "maximize", "unmaximize", "fullscreen", "unfullscreen"}}, "selector": map[string]any{"type": "object", "properties": map[string]any{"index": registry.Integer("window index"), "pid": registry.Integer("process ID"), "appId": registry.String("application ID"), "title": registry.String("exact title"), "titleContains": registry.String("title substring")}}}, []string{"action", "selector"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		return windowControlNative(ctx, raw, session)
	}}))
	must(r.Add(registry.Tool{Name: "shell_control_center", Description: "Open, close, or toggle the native CyShell Control Center through its control-center IPC target.", InputSchema: registry.ObjectSchema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"open", "close", "toggle"}}, "section": registry.String("optional CyShell Control Center section")}, []string{"action"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Action  string `json:"action"`
			Section string `json:"section"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return cyshellControlCenter(ctx, session, in.Action, in.Section)
	}}))
	must(r.Add(registry.Tool{Name: "shell_launcher", Description: "Open, close, toggle, or search the native CyShell launcher through its launcher IPC target.", InputSchema: registry.ObjectSchema(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"open", "close", "toggle", "search"}}, "query": registry.String("search text")}, []string{"action"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) { return shellLauncher(ctx, raw, session) }}))
	must(r.Add(registry.Tool{Name: "shell_open_settings", Description: "Open the native CyShell Settings surface, optionally at a CyShell Settings tab ID.", InputSchema: registry.ObjectSchema(map[string]any{"page": registry.String("CyShell Settings tab ID")}, nil), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Page string `json:"page"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return cyshellOpenSettings(ctx, session, in.Page)
	}}))
	must(r.Add(registry.Tool{Name: "shell_settings_search", Description: "Search CyShell SettingsSearchService and return native translated/ranked SettingsData results.", InputSchema: registry.ObjectSchema(map[string]any{"query": registry.String("settings topic"), "max_results": registry.Integer("maximum results")}, []string{"query"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Query      string `json:"query"`
			MaxResults int    `json:"max_results"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return cyshellSettingsSearch(ctx, session, in.Query, in.MaxResults)
	}}))
	must(r.Add(registry.Tool{Name: "shell_settings_get", Description: "Read an exact CyShell SettingsData key through the native Settings IPC API.", InputSchema: registry.ObjectSchema(map[string]any{"key": registry.String("exact CyShell SettingsData key")}, []string{"key"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Key string `json:"key"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return cyshellSettingsGet(ctx, session, in.Key)
	}}))
	must(r.Add(registry.Tool{Name: "shell_settings_set", Description: "Set a type-checked scalar CyShell SettingsData value through SettingsData.set and verify it by reading it back.", InputSchema: registry.ObjectSchema(map[string]any{"key": registry.String("exact CyShell SettingsData key"), "value": registry.String("new scalar value")}, []string{"key", "value"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return cyshellSettingsSet(ctx, session, in.Key, in.Value)
	}}))
}

func desktopExec(ctx context.Context, name string, args ...string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("required desktop utility %s is not installed", name)
	}
	c := exec.CommandContext(ctx, p, args...)
	c.Env = desktopEnvironment()
	b, err := c.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(b)), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}
func parsePercent(v string) (int, error) {
	n, e := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(v, "%")))
	if e != nil || n < 0 || n > 100 {
		return 0, fmt.Errorf("value must be an integer from 0 to 100")
	}
	return n, nil
}
func machineRead(ctx context.Context, action string, session sessionbridge.Client) (string, error) {
	if value, err := cyshellMachineRead(ctx, session, action); err == nil {
		return value, nil
	} else if _, nativeErr := cyshellNativeState(ctx, session); nativeErr == nil {
		return "", err
	}
	switch action {
	case "wifi.toggle", "wifi.enable", "wifi.disable":
		s, e := desktopExec(ctx, "nmcli", "-t", "-f", "WIFI", "general")
		return strings.TrimSpace(s), e
	case "bluetooth.toggle", "bluetooth.enable", "bluetooth.disable":
		s, e := desktopExec(ctx, "bluetoothctl", "show")
		if e != nil {
			return "", e
		}
		if strings.Contains(s, "Powered: yes") {
			return "true", nil
		}
		return "false", nil
	case "audio.volume.set":
		s, e := desktopExec(ctx, "wpctl", "get-volume", "@DEFAULT_AUDIO_SINK@")
		if e != nil {
			return "", e
		}
		return volumePercent(s), nil
	case "audio.mute.toggle", "audio.mute.set":
		s, e := desktopExec(ctx, "pactl", "get-sink-mute", "@DEFAULT_SINK@")
		if e != nil {
			return "", e
		}
		if strings.HasSuffix(s, "yes") {
			return "true", nil
		}
		return "false", nil
	case "mic.volume.set":
		s, e := desktopExec(ctx, "wpctl", "get-volume", "@DEFAULT_AUDIO_SOURCE@")
		if e != nil {
			return "", e
		}
		return volumePercent(s), nil
	case "mic.mute.toggle", "mic.mute.set":
		s, e := desktopExec(ctx, "pactl", "get-source-mute", "@DEFAULT_SOURCE@")
		if e != nil {
			return "", e
		}
		if strings.HasSuffix(s, "yes") {
			return "true", nil
		}
		return "false", nil
	case "brightness.set":
		s, e := desktopExec(ctx, "qdbus6", "org.kde.org_kde_powerdevil", "/org/kde/ScreenBrightness/display4", "org.freedesktop.DBus.Properties.Get", "org.kde.ScreenBrightness.Display", "Brightness")
		if e != nil {
			return "", e
		}
		n, e := strconv.Atoi(strings.TrimSpace(s))
		if e != nil {
			return "", e
		}
		m, me := desktopExec(ctx, "qdbus6", "org.kde.org_kde_powerdevil", "/org/kde/ScreenBrightness/display4", "org.freedesktop.DBus.Properties.Get", "org.kde.ScreenBrightness.Display", "MaxBrightness")
		if me != nil {
			return "", me
		}
		mx, _ := strconv.Atoi(strings.TrimSpace(m))
		if mx < 1 {
			return "", fmt.Errorf("PowerDevil reports invalid maximum brightness")
		}
		return strconv.Itoa(n * 100 / mx), nil
	case "power.profile.set":
		return desktopExec(ctx, "powerprofilesctl", "get")
	default:
		return "", fmt.Errorf("unsupported machine action %q", action)
	}
}
func volumePercent(s string) string {
	f := regexp.MustCompile(`Volume:\s*([0-9.]+)`).FindStringSubmatch(s)
	if len(f) < 2 {
		return "0"
	}
	n, _ := strconv.ParseFloat(f[1], 64)
	return strconv.Itoa(int(n*100 + 0.5))
}
func machineApply(ctx context.Context, action, value string, root broker.Client, session sessionbridge.Client) error {
	if _, err := cyshellNativeState(ctx, session); err == nil {
		return cyshellMachineApply(ctx, session, action, value)
	}
	switch action {
	case "wifi.toggle", "wifi.enable", "wifi.disable":
		target := value
		if target == "" {
			old, e := machineRead(ctx, action, session)
			if e != nil {
				return e
			}
			if old == "enabled" {
				target = "disabled"
			} else {
				target = "enabled"
			}
		}
		if target == "enabled" {
			return desktopExecWithRootFallback(ctx, root, "/usr/bin/nmcli", "radio", "wifi", "on")
		}
		return desktopExecWithRootFallback(ctx, root, "/usr/bin/nmcli", "radio", "wifi", "off")
	case "bluetooth.toggle", "bluetooth.enable", "bluetooth.disable":
		target := value
		if target == "" {
			old, e := machineRead(ctx, action, session)
			if e != nil {
				return e
			}
			if old == "true" {
				target = "false"
			} else {
				target = "true"
			}
		}
		if target == "true" {
			_, e := desktopExec(ctx, "bluetoothctl", "power", "on")
			return e
		}
		_, e := desktopExec(ctx, "bluetoothctl", "power", "off")
		return e
	case "audio.volume.set", "mic.volume.set":
		n, e := parsePercent(value)
		if e != nil {
			return e
		}
		node := "@DEFAULT_AUDIO_SINK@"
		if action == "mic.volume.set" {
			node = "@DEFAULT_AUDIO_SOURCE@"
		}
		_, e = desktopExec(ctx, "wpctl", "set-volume", node, fmt.Sprintf("%d%%", n))
		return e
	case "audio.mute.toggle", "audio.mute.set", "mic.mute.toggle", "mic.mute.set":
		target := value
		if (action == "audio.mute.toggle" || action == "mic.mute.toggle") && target == "" {
			old, e := machineRead(ctx, action, session)
			if e != nil {
				return e
			}
			if old == "true" {
				target = "false"
			} else {
				target = "true"
			}
		}
		if target != "true" && target != "false" {
			return fmt.Errorf("mute value must be true or false")
		}
		kind := "sink"
		if strings.HasPrefix(action, "mic.") {
			kind = "source"
		}
		if target == "true" {
			_, e := desktopExec(ctx, "pactl", "set-"+kind+"-mute", "@DEFAULT_"+strings.ToUpper(kind)+"@", "1")
			return e
		}
		_, e := desktopExec(ctx, "pactl", "set-"+kind+"-mute", "@DEFAULT_"+strings.ToUpper(kind)+"@", "0")
		return e
	case "brightness.set":
		n, e := parsePercent(value)
		if e != nil {
			return e
		}
		m, e := desktopExec(ctx, "qdbus6", "org.kde.org_kde_powerdevil", "/org/kde/ScreenBrightness/display4", "org.freedesktop.DBus.Properties.Get", "org.kde.ScreenBrightness.Display", "MaxBrightness")
		if e != nil {
			return e
		}
		mx, e := strconv.Atoi(strings.TrimSpace(m))
		if e != nil {
			return e
		}
		_, e = desktopExec(ctx, "qdbus6", "org.kde.org_kde_powerdevil", "/org/kde/ScreenBrightness/display4", "org.kde.ScreenBrightness.Display.SetBrightness", strconv.Itoa(mx*n/100), "0")
		return e
	case "power.profile.set":
		if value != "balanced" && value != "performance" && value != "power-saver" {
			return fmt.Errorf("power profile must be balanced, performance, or power-saver")
		}
		return desktopExecWithRootFallback(ctx, root, "/usr/bin/powerprofilesctl", "set", value)
	}
	return fmt.Errorf("unsupported action %q", action)
}
func desktopExecWithRootFallback(ctx context.Context, root broker.Client, binary string, args ...string) error {
	if _, err := desktopExec(ctx, binary, args...); err == nil {
		return nil
	} else {
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "not authorized") && !strings.Contains(lower, "not authorized to perform") && !strings.Contains(lower, "permission denied") {
			return err
		}
	}
	if !root.Available() {
		return fmt.Errorf("desktop action requires authorization, but the configured root broker is unavailable")
	}
	command := strings.Join(append([]string{binary}, args...), " ")
	response, err := root.Exec(ctx, broker.ExecRequest{Command: command, TimeoutSeconds: 15, MaxOutputBytes: 4096})
	if err != nil {
		return err
	}
	if response.ExitCode != 0 {
		return fmt.Errorf("privileged desktop action failed (exit %d): %s", response.ExitCode, strings.TrimSpace(response.Stderr))
	}
	return nil
}

func desktopAction(ctx context.Context, raw json.RawMessage, root broker.Client, session sessionbridge.Client) (any, error) {
	var in struct {
		Action string `json:"action"`
		Value  string `json:"value"`
	}
	if e := decode(raw, &in); e != nil {
		return nil, e
	}
	before, e := machineRead(ctx, in.Action, session)
	if e != nil {
		return nil, e
	}
	value := in.Value
	switch in.Action {
	case "wifi.enable":
		value = "enabled"
	case "wifi.disable":
		value = "disabled"
	case "bluetooth.enable":
		value = "true"
	case "bluetooth.disable":
		value = "false"
	case "wifi.toggle", "bluetooth.toggle", "audio.mute.toggle", "mic.mute.toggle":
		value = ""
	}
	if e = machineApply(ctx, in.Action, value, root, session); e != nil {
		return nil, e
	}
	after, e := machineRead(ctx, in.Action, session)
	if e != nil {
		return map[string]any{"ok": true, "verified": false, "verification_error": e.Error()}, nil
	}
	var b [12]byte
	if _, e = rand.Read(b[:]); e != nil {
		return nil, e
	}
	id := hex.EncodeToString(b[:])
	rec := desktopReceipt{ID: id, Action: in.Action, Before: fmt.Sprint(before), After: fmt.Sprint(after), Created: time.Now(), Expires: time.Now().Add(10 * time.Minute)}
	desktopReceiptStore.Lock()
	desktopReceiptStore.items[id] = rec
	desktopReceiptStore.Unlock()
	return map[string]any{"ok": true, "backend": "native-desktop", "action": in.Action, "before": before, "after": after, "receipt": id, "expires_at": rec.Expires.Format(time.RFC3339)}, nil
}
func desktopReceipts(_ context.Context, _ json.RawMessage) (any, error) {
	now := time.Now()
	desktopReceiptStore.Lock()
	defer desktopReceiptStore.Unlock()
	out := []desktopReceipt{}
	for id, r := range desktopReceiptStore.items {
		if now.After(r.Expires) {
			delete(desktopReceiptStore.items, id)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return map[string]any{"count": len(out), "receipts": out}, nil
}
func desktopUndo(ctx context.Context, raw json.RawMessage, root broker.Client, session sessionbridge.Client) (any, error) {
	var in struct {
		Receipt string `json:"receipt"`
	}
	if e := decode(raw, &in); e != nil {
		return nil, e
	}
	desktopReceiptStore.Lock()
	rec, ok := desktopReceiptStore.items[in.Receipt]
	desktopReceiptStore.Unlock()
	if !ok || time.Now().After(rec.Expires) {
		return nil, fmt.Errorf("receipt not found or expired")
	}
	if e := machineApply(ctx, rec.Action, rec.Before, root, session); e != nil {
		return nil, e
	}
	after, e := machineRead(ctx, rec.Action, session)
	if e != nil {
		return nil, e
	}
	if fmt.Sprint(after) != rec.Before {
		return nil, fmt.Errorf("rollback verification failed: expected %s, got %s", rec.Before, after)
	}
	desktopReceiptStore.Lock()
	delete(desktopReceiptStore.items, in.Receipt)
	desktopReceiptStore.Unlock()
	return map[string]any{"ok": true, "rolled_back": rec.Action, "restored": after}, nil
}

func appQueryUI(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		AppID             string      `json:"app_id"`
		PID               int         `json:"pid"`
		Title             string      `json:"title"`
		WindowID          json.Number `json:"window_id"`
		Query             string      `json:"query"`
		IncludeScreenshot bool        `json:"include_screenshot"`
		MaxNodes          int         `json:"max_nodes"`
	}
	if e := decode(raw, &in); e != nil {
		return nil, e
	}
	binary, ok := anyAppBackendPath()
	if !ok {
		return nil, fmt.Errorf("Any App accessibility backend unavailable")
	}
	args := map[string]any{"include_screenshot": in.IncludeScreenshot, "verbose": false, "max_nodes": in.MaxNodes}
	if in.AppID != "" {
		args["app_id"] = in.AppID
	}
	if in.PID > 0 {
		args["pid"] = in.PID
	}
	if in.Title != "" {
		args["title"] = in.Title
	}
	if in.WindowID != "" {
		args["window_id"] = in.WindowID
	}
	client := &anyAppClient{binary: binary}
	result, e := client.callTool(ctx, "get_app_state", mustDesktopJSON(args))
	if e != nil {
		return nil, e
	}
	var rich registry.RichResult
	if v, ok := result.(registry.RichResult); ok {
		rich = v
	} else {
		rich.Structured = result
	}
	if m, ok := rich.Structured.(map[string]any); ok {
		if tree, ok := m["accessibility_tree"].([]any); ok && in.Query != "" {
			q := strings.ToLower(in.Query)
			filtered := []any{}
			for _, node := range tree {
				b, _ := json.Marshal(node)
				if strings.Contains(strings.ToLower(string(b)), q) {
					filtered = append(filtered, node)
				}
			}
			m["accessibility_tree"] = filtered
			m["matched_nodes"] = len(filtered)
		}
	}
	return rich, nil
}

func shellLauncher(ctx context.Context, raw json.RawMessage, session sessionbridge.Client) (any, error) {
	var in struct {
		Action string `json:"action"`
		Query  string `json:"query"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	method, args := "open", []string{}
	switch in.Action {
	case "open":
		method = "open"
	case "close":
		method = "close"
	case "toggle":
		method = "toggle"
	case "search":
		if strings.TrimSpace(in.Query) == "" {
			return nil, fmt.Errorf("query is required for search")
		}
		method = "openQuery"
		args = []string{in.Query}
	default:
		return nil, fmt.Errorf("unsupported CyShell launcher action %q", in.Action)
	}
	result, err := cyshellIPC(ctx, session, "shell", "launcher", method, args...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "backend": "cyshell-launcher-ipc", "action": in.Action, "result": result}, nil
}

func errText(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

func listKDEWindows(ctx context.Context) ([]map[string]any, error) {
	binary, ok := anyAppBackendPath()
	if !ok {
		return nil, fmt.Errorf("window backend unavailable")
	}
	client := &anyAppClient{binary: binary}
	v, e := client.callTool(ctx, "list_windows", json.RawMessage(`{}`))
	if e != nil {
		return nil, e
	}
	var data any
	if rr, ok := v.(registry.RichResult); ok {
		data = rr.Structured
	} else {
		data = v
	}
	m, _ := data.(map[string]any)
	if m == nil {
		return nil, fmt.Errorf("invalid window list response")
	}
	a, _ := m["windows"].([]any)
	out := []map[string]any{}
	for _, x := range a {
		if row, ok := x.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out, nil
}
func closeKDEWindow(ctx context.Context, title string) (any, error) {
	return runKWinWindowScript(ctx, "close", map[string]any{"titleContains": title})
}
func windowControl(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Action   string `json:"action"`
		Selector struct {
			Index         *int   `json:"index"`
			PID           *int   `json:"pid"`
			AppID         string `json:"appId"`
			Title         string `json:"title"`
			TitleContains string `json:"titleContains"`
		} `json:"selector"`
	}
	if e := decode(raw, &in); e != nil {
		return nil, e
	}
	if in.Selector.Index == nil && in.Selector.PID == nil && in.Selector.AppID == "" && in.Selector.Title == "" && in.Selector.TitleContains == "" {
		return nil, fmt.Errorf("a window selector is required")
	}
	if in.Action != "focus" && in.Action != "close" && in.Action != "minimize" && in.Action != "restore" && in.Action != "maximize" && in.Action != "unmaximize" && in.Action != "fullscreen" && in.Action != "unfullscreen" {
		return nil, fmt.Errorf("unsupported window action")
	}
	windows, err := listKDEWindows(ctx)
	if err != nil {
		return nil, err
	}
	matches := []map[string]any{}
	for i, w := range windows {
		match := true
		if in.Selector.Index != nil {
			match = match && i == *in.Selector.Index
		}
		if in.Selector.PID != nil {
			match = match && intValue(w["pid"]) == *in.Selector.PID
		}
		if in.Selector.AppID != "" {
			match = match && fmt.Sprint(w["app_id"]) == in.Selector.AppID
		}
		if in.Selector.Title != "" {
			match = match && fmt.Sprint(w["title"]) == in.Selector.Title
		}
		if in.Selector.TitleContains != "" {
			match = match && strings.Contains(strings.ToLower(fmt.Sprint(w["title"])), strings.ToLower(in.Selector.TitleContains))
		}
		if match {
			matches = append(matches, w)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("window selector matched %d windows; exactly one is required", len(matches))
	}
	selected := matches[0]
	if in.Action == "focus" {
		binary, ok := anyAppBackendPath()
		if !ok {
			return nil, fmt.Errorf("Any App window activation backend unavailable")
		}
		client := &anyAppClient{binary: binary}
		args := map[string]any{"reason": "KWin window_control focus"}
		if pid := intValue(selected["pid"]); pid > 0 {
			args["pid"] = pid
		}
		if app := fmt.Sprint(selected["app_id"]); app != "<nil>" && app != "" {
			args["app_id"] = app
		}
		if title := fmt.Sprint(selected["title"]); title != "<nil>" {
			args["title"] = title
		}
		if id, ok := selected["window_id"]; ok {
			args["window_id"] = id
		}
		result, err := client.callTool(ctx, "activate_window", mustDesktopJSON(args))
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	selector := map[string]any{"pid": intValue(selected["pid"])}
	return runKWinWindowScript(ctx, in.Action, selector)
}
func intValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	}
	i, _ := strconv.Atoi(fmt.Sprint(v))
	return i
}
func runKWinWindowScript(ctx context.Context, action string, selector map[string]any) (any, error) {
	windows, err := listKDEWindows(ctx)
	if err != nil {
		return nil, err
	}
	matches := []map[string]any{}
	for i, w := range windows {
		match := true
		if v, ok := selector["pid"]; ok {
			match = match && intValue(w["pid"]) == intValue(v)
		}
		if v, ok := selector["index"]; ok {
			match = match && i == intValue(v)
		}
		if v, ok := selector["appId"]; ok {
			match = match && fmt.Sprint(w["app_id"]) == fmt.Sprint(v)
		}
		if v, ok := selector["title"]; ok {
			match = match && fmt.Sprint(w["title"]) == fmt.Sprint(v)
		}
		if v, ok := selector["titleContains"]; ok {
			match = match && strings.Contains(strings.ToLower(fmt.Sprint(w["title"])), strings.ToLower(fmt.Sprint(v)))
		}
		if match {
			matches = append(matches, w)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("window selector matched %d windows; exactly one is required", len(matches))
	}
	pid := intValue(matches[0]["pid"])
	if pid <= 0 {
		return nil, fmt.Errorf("selected window has no usable process ID")
	}
	selector = map[string]any{"pid": pid}
	sel, _ := json.Marshal(selector)
	act, _ := json.Marshal(action)
	script := `var wanted=` + string(sel) + `; var action=` + string(act) + `; var wins=workspace.windowList(); var hits=[]; for (var i=0;i<wins.length;i++){var w=wins[i]; var ok=true; if(wanted.index!==undefined) ok=ok&&(i===wanted.index); if(wanted.pid!==undefined) ok=ok&&(w.pid===wanted.pid); if(wanted.appId!==undefined) ok=ok&&(w.desktopFileName===wanted.appId||w.resourceClass===wanted.appId||w.resourceName===wanted.appId); if(wanted.title!==undefined) ok=ok&&(w.caption===wanted.title); if(wanted.titleContains!==undefined) ok=ok&&w.caption.toLowerCase().indexOf(wanted.titleContains.toLowerCase())>=0; if(ok) hits.push(w); } if(hits.length===1){var w=hits[0]; if(action==='focus') workspace.activeWindow=w; else if(action==='close') w.closeWindow(); else if(action==='minimize') w.minimized=true; else if(action==='restore') {w.minimized=false;w.activate();} else if(action==='maximize') w.setMaximize(true,true); else if(action==='unmaximize') w.setMaximize(false,false); else if(action==='fullscreen') w.fullScreen=true; else if(action==='unfullscreen') w.fullScreen=false; }`
	f, e := os.CreateTemp("", "cycom-kwin-*.js")
	if e != nil {
		return nil, e
	}
	path := f.Name()
	defer os.Remove(path)
	if _, e = f.WriteString(script); e != nil {
		f.Close()
		return nil, e
	}
	f.Close()
	plugin := "cycomagent_action_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	out, e := desktopExec(ctx, "qdbus6", "org.kde.KWin", "/Scripting", "org.kde.kwin.Scripting.loadScript", path, plugin)
	if e != nil {
		return nil, e
	}
	loaded, checkErr := desktopExec(ctx, "qdbus6", "org.kde.KWin", "/Scripting", "org.kde.kwin.Scripting.isScriptLoaded", plugin)
	if checkErr != nil || strings.TrimSpace(loaded) != "true" {
		return map[string]any{"ok": false, "backend": "kwin-scripting", "action": action, "error": "KWin did not register the action script", "load_result": strings.TrimSpace(out)}, nil
	}
	_, _ = desktopExec(ctx, "qdbus6", "org.kde.KWin", "/Scripting", "org.kde.kwin.Scripting.start")
	time.Sleep(350 * time.Millisecond)
	_, _ = desktopExec(ctx, "qdbus6", "org.kde.KWin", "/Scripting", "org.kde.kwin.Scripting.unloadScript", plugin)
	verified := false
	if action == "minimize" || action == "restore" || action == "close" {
		updated, listErr := listKDEWindows(ctx)
		if listErr != nil {
			return nil, listErr
		}
		found := false
		hidden := false
		for _, w := range updated {
			if intValue(w["pid"]) == pid {
				found = true
				hidden = w["hidden"] == true
				break
			}
		}
		switch action {
		case "minimize":
			verified = found && hidden
		case "restore":
			verified = found && !hidden
		case "close":
			verified = !found
		}
		if !verified {
			return map[string]any{"ok": false, "backend": "kwin-scripting", "action": action, "verified": false, "error": "KWin accepted the script but the requested window state was not confirmed"}, nil
		}
	} else {
		verified = true
	}
	return map[string]any{"ok": true, "backend": "kwin-scripting", "action": action, "selector": selector, "script_id": strings.TrimSpace(out), "script_loaded": true, "verified": verified, "note": "KWin loaded the script and the requested window operation was accepted"}, nil
}
