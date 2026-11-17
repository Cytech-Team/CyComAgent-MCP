package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

func registerNativeDesktopCompatibility(r *registry.Registry, session sessionbridge.Client) {
	readOnly := map[string]any{"readOnlyHint": true}
	must(r.Add(registry.Tool{Name: "cyshell_desktop_state", Description: "Return authoritative desktop readiness, preferring CyShell native shell state when the CyShell login session is active.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: readOnly, Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
		return cyshellDesktopState(ctx, nil, session)
	}}))
	must(r.Add(registry.Tool{Name: "shell_get_state", Description: "Read active desktop session, compositor, output, and workspace state. Returns the live CyShell agent state when the CyShell shell surface is active.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: readOnly, Handler: func(ctx context.Context, _ json.RawMessage) (any, error) { return shellGetState(ctx, nil, session) }}))
	must(r.Add(registry.Tool{Name: "workspace_list", Description: "List virtual desktops from CyShell WorkspaceModel when CyShell is active, otherwise use KWin.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: readOnly, Handler: func(ctx context.Context, _ json.RawMessage) (any, error) { return workspaceListNative(ctx, session) }}))
	must(r.Add(registry.Tool{Name: "workspace_focus", Description: "Activate one CyShell or KWin virtual desktop by ID, index, or exact name.", InputSchema: registry.ObjectSchema(map[string]any{"selector": map[string]any{"type": "object", "properties": map[string]any{"id": registry.String("KWin desktop UUID"), "idx": registry.Integer("zero-based desktop index"), "name": registry.String("exact desktop name"), "objectId": registry.Integer("zero-based desktop index"), "output": registry.String("optional output name")}}}, []string{"selector"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		return workspaceFocusNative(ctx, raw, session)
	}}))
	must(r.Add(registry.Tool{Name: "desktop_query", Description: "Search the focused third-party application's AT-SPI accessibility tree. CyShell shell UI is queried through its native agent IPC when active.", InputSchema: registry.ObjectSchema(map[string]any{"query": registry.String("semantic text to find"), "scope": map[string]any{"type": "string", "enum": []string{"auto", "shell", "app"}}, "max_results": registry.Integer("maximum matches per scope")}, []string{"query"}), Source: "core", Annotations: readOnly, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		return desktopQueryNative(ctx, raw, session)
	}}))
	must(r.Add(registry.Tool{Name: "shell_query_ui", Description: "Query the active desktop's accessible UI tree. On KDE this searches the focused application through AT-SPI; CyShell surfaces are queried through their native IPC when active.", InputSchema: registry.ObjectSchema(map[string]any{"query": registry.String("text matched against semantic UI nodes"), "max_nodes": registry.Integer("maximum nodes")}, nil), Source: "core", Annotations: readOnly, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		return shellQueryUINative(ctx, raw, session)
	}}))
}

func runDesktopCommand(ctx context.Context, name string, args ...string) (string, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is unavailable", name)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = desktopEnvironment()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

var desktopTupleRE = regexp.MustCompile(`\(uss\)\s+(\d+),\s+"([^"]+)",\s+"([^"]*)"`)

func kwinWorkspaces(ctx context.Context) ([]map[string]any, string, error) {
	current, err := runDesktopCommand(ctx, "qdbus6", "org.kde.KWin", "/VirtualDesktopManager", "org.freedesktop.DBus.Properties.Get", "org.kde.KWin.VirtualDesktopManager", "current")
	if err != nil {
		return nil, "", err
	}
	current = strings.TrimSpace(current)
	literal, err := runDesktopCommand(ctx, "qdbus6", "--literal", "org.kde.KWin", "/VirtualDesktopManager", "org.freedesktop.DBus.Properties.Get", "org.kde.KWin.VirtualDesktopManager", "desktops")
	if err != nil {
		return nil, current, err
	}
	matches := desktopTupleRE.FindAllStringSubmatch(literal, -1)
	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		idx, _ := strconv.Atoi(m[1])
		out = append(out, map[string]any{"index": idx, "id": m[2], "name": m[3], "active": m[2] == current})
	}
	if len(out) == 0 {
		return nil, current, fmt.Errorf("KWin returned no parseable virtual desktops: %s", literal)
	}
	return out, current, nil
}
func workspaceList(ctx context.Context, _ json.RawMessage) (any, error) {
	items, current, err := kwinWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"backend": "kwin-dbus", "current_id": current, "workspaces": items}, nil
}
func workspaceFocus(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Selector struct {
			ID       string `json:"id"`
			Idx      *int   `json:"idx"`
			Name     string `json:"name"`
			ObjectID *int   `json:"objectId"`
			Output   string `json:"output"`
		} `json:"selector"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	items, _, err := kwinWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	var selected map[string]any
	for _, item := range items {
		match := false
		if in.Selector.ID != "" {
			match = item["id"] == in.Selector.ID
		}
		if in.Selector.Name != "" {
			match = item["name"] == in.Selector.Name
		}
		if in.Selector.Idx != nil {
			match = item["index"] == *in.Selector.Idx
		}
		if in.Selector.ObjectID != nil {
			match = item["index"] == *in.Selector.ObjectID
		}
		if match {
			selected = item
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("workspace selector did not match a KWin virtual desktop")
	}
	id := fmt.Sprint(selected["id"])
	if _, err := runDesktopCommand(ctx, "qdbus6", "org.kde.KWin", "/VirtualDesktopManager", "org.freedesktop.DBus.Properties.Set", "org.kde.KWin.VirtualDesktopManager", "current", id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "backend": "kwin-dbus", "workspace": selected}, nil
}
func workspaceListNative(ctx context.Context, session sessionbridge.Client) (any, error) {
	if _, err := cyshellNativeState(ctx, session); err == nil {
		raw, err := cyshellIPC(ctx, session, "shell", "agent", "workspaces")
		if err != nil {
			return nil, err
		}
		parsed, err := parseCyShellIPCJSON(raw)
		if err != nil {
			return nil, err
		}
		snapshot, ok := parsed.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("CyShell workspace snapshot has an invalid response shape")
		}
		return map[string]any{"backend": "cyshell-workspace-model-ipc", "available": snapshot["available"], "compositor": snapshot["compositor"], "workspaces": snapshot["workspaces"]}, nil
	}
	if isCyShellSessionEnvironment() {
		return nil, fmt.Errorf("CyShell is the active shell, but its authenticated session bridge is unavailable; refusing to query KWin as a substitute")
	}
	return workspaceList(ctx, nil)
}
func workspaceFocusNative(ctx context.Context, raw json.RawMessage, session sessionbridge.Client) (any, error) {
	if _, err := cyshellNativeState(ctx, session); err == nil {
		var in struct {
			Selector map[string]any `json:"selector"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(in.Selector)
		result, err := cyshellIPC(ctx, session, "shell", "agent", "workspaceFocus", string(b))
		if err != nil {
			return nil, err
		}
		if strings.Contains(result, "NOT_FOUND") || strings.Contains(result, "INVALID") {
			return nil, fmt.Errorf("CyShell workspace focus rejected selector: %s", result)
		}
		return map[string]any{"ok": true, "backend": "cyshell-workspace-model-ipc", "result": result}, nil
	}
	if isCyShellSessionEnvironment() {
		return nil, fmt.Errorf("CyShell is the active shell, but its authenticated session bridge is unavailable; refusing to focus a KWin workspace")
	}
	return workspaceFocus(ctx, raw)
}
func desktopQueryNative(ctx context.Context, raw json.RawMessage, session sessionbridge.Client) (any, error) {
	var in struct {
		Query      string `json:"query"`
		Scope      string `json:"scope"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Scope == "shell" {
		if _, err := cyshellNativeState(ctx, session); err != nil {
			return nil, err
		}
		return cyshellQueryUI(ctx, session, in.Query)
	}
	return desktopQuery(ctx, raw)
}
func shellQueryUINative(ctx context.Context, raw json.RawMessage, session sessionbridge.Client) (any, error) {
	var in struct {
		Query    string `json:"query"`
		MaxNodes int    `json:"max_nodes"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if _, err := cyshellNativeState(ctx, session); err == nil {
		return cyshellQueryUI(ctx, session, in.Query)
	}
	if isCyShellSessionEnvironment() {
		return nil, fmt.Errorf("CyShell is the active shell, but its authenticated session bridge is unavailable; refusing to query a different desktop UI")
	}
	return queryFocusedApp(ctx, in.Query, in.MaxNodes, true)
}

func windowControlNative(ctx context.Context, raw json.RawMessage, session sessionbridge.Client) (any, error) {
	if _, err := cyshellNativeState(ctx, session); err == nil {
		var in struct {
			Action   string         `json:"action"`
			Selector map[string]any `json:"selector"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return cyshellWindowAction(ctx, session, in.Action, in.Selector)
	}
	if isCyShellSessionEnvironment() {
		return nil, fmt.Errorf("CyShell is the active shell, but its authenticated session bridge is unavailable; refusing to control a KWin window")
	}
	return windowControl(ctx, raw)
}

func cyshellDesktopState(ctx context.Context, _ json.RawMessage, session sessionbridge.Client) (any, error) {
	if native, err := cyshellNativeState(ctx, session); err == nil {
		return native, nil
	}
	if isCyShellSessionEnvironment() {
		return map[string]any{"desktop": "CyShell", "compositor": "Labwc", "cyshell_builtin": map[string]any{"available": false, "reason": "CyShell session bridge is unavailable; the Labwc autostart supervisor must be running"}, "session_bridge": session.Available()}, nil
	}
	items, current, wsErr := kwinWorkspaces(ctx)
	out := map[string]any{"desktop": "KDE Plasma", "compositor": "KWin", "cyshell_builtin": map[string]any{"available": false, "reason": "CyShell shell surfaces are not active in this graphical session"}, "session_bridge": session.Available()}
	if wsErr == nil {
		out["workspaces"] = items
		out["current_workspace_id"] = current
	} else {
		out["workspace_error"] = wsErr.Error()
	}
	if output, err := runDesktopCommand(ctx, "qdbus6", "org.kde.KWin", "/KWin", "org.kde.KWin.activeOutputName"); err == nil {
		out["active_output"] = output
	}
	return out, nil
}
func shellGetState(ctx context.Context, _ json.RawMessage, session sessionbridge.Client) (any, error) {
	state, err := cyshellDesktopState(ctx, nil, session)
	if err != nil {
		return nil, err
	}
	m := state.(map[string]any)
	if m["desktop"] == "CyShell" {
		m["shell"] = "CyShell"
		available := false
		if state, ok := m["cyshell_builtin"].(map[string]any); ok {
			available = state["available"] == true
		}
		m["cyshell_settings_available"] = available
	} else {
		m["shell"] = "KDE Plasma"
		m["cyshell_settings_available"] = false
	}
	return m, nil
}
func desktopQuery(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Query      string `json:"query"`
		Scope      string `json:"scope"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Scope == "shell" {
		return map[string]any{"backend": "kde-atspi", "matches": []any{}, "note": "CyShell shell surfaces are unavailable; this session runs KDE Plasma"}, nil
	}
	return queryFocusedApp(ctx, in.Query, in.MaxResults, false)
}
func shellQueryUI(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Query    string `json:"query"`
		MaxNodes int    `json:"max_nodes"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	return queryFocusedApp(ctx, in.Query, in.MaxNodes, true)
}
func queryFocusedApp(ctx context.Context, query string, max int, shellShape bool) (any, error) {
	binary, ok := anyAppBackendPath()
	if !ok {
		return nil, fmt.Errorf("Any App accessibility backend unavailable")
	}
	client := &anyAppClient{binary: binary}
	args := map[string]any{"include_screenshot": false, "verbose": false, "max_nodes": max}
	result, err := client.callTool(ctx, "get_app_state", mustDesktopJSON(args))
	if err != nil {
		return nil, err
	}
	var structured any
	if rich, ok := result.(registry.RichResult); ok {
		structured = rich.Structured
	} else {
		structured = result
	}
	if m, ok := structured.(map[string]any); ok {
		if tree, ok := m["accessibility_tree"].([]any); ok && strings.TrimSpace(query) != "" {
			q := strings.ToLower(query)
			filtered := make([]any, 0, len(tree))
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
	if shellShape {
		return map[string]any{"backend": "kde-atspi", "shell_available": false, "application": structured}, nil
	}
	return map[string]any{"backend": "kde-atspi", "matches": structured}, nil
}
func mustDesktopJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
