package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

// registerDesktopContext provides the stable aggregate desktop context API.
// CyShell owns shell surfaces, windows, workspaces, and machine state through
// its native agent IPC; Any App remains the accessibility backend for ordinary
// third-party application content.
func registerDesktopContext(r *registry.Registry, session sessionbridge.Client) {
	must(r.Add(registry.Tool{
		Name:        "desktop_get_context",
		Description: "Return one structured desktop context containing shell surfaces, windows, workspaces, permissions, and optionally the focused third-party app and a screenshot.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"include_app":        registry.Boolean("include focused third-party application state"),
			"include_screenshot": registry.Boolean("include a screenshot with app state; requires screen capture permission"),
			"include_system":     registry.Boolean("include local system information"),
			"known_digest":       registry.String("optional digest from a previous response"),
			"known_sections":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		}, nil),
		Source:      "core",
		Annotations: map[string]any{"readOnlyHint": true},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
			return desktopGetContext(ctx, raw, session)
		},
	}))
}

func desktopGetContext(ctx context.Context, raw json.RawMessage, session sessionbridge.Client) (any, error) {
	var in struct {
		IncludeApp        bool              `json:"include_app"`
		IncludeScreenshot bool              `json:"include_screenshot"`
		IncludeSystem     bool              `json:"include_system"`
		KnownDigest       string            `json:"known_digest"`
		KnownSections     map[string]string `json:"known_sections"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	binary, ok := anyAppBackendPath()
	if !ok {
		return nil, fmt.Errorf("desktop context requires the Any App backend")
	}
	client := &anyAppClient{binary: binary}
	call := func(name string, args any) (any, []map[string]any, error) {
		b, _ := json.Marshal(args)
		v, err := client.callTool(ctx, name, b)
		if err != nil {
			return nil, nil, err
		}
		if rich, ok := v.(registry.RichResult); ok {
			return rich.Structured, rich.Content, nil
		}
		return v, nil, nil
	}
	var windows, focused, workspaces, surfaces any
	var err error
	var focusErr error
	var native map[string]any
	if state, nativeErr := cyshellNativeState(ctx, session); nativeErr == nil {
		native = state
		windows = state["windows"]
		focused = state["activeWindow"]
		workspaces = state["workspaces"]
		surfaces = state["surfaces"]
	} else if isCyShellSessionEnvironment() {
		native = map[string]any{"desktop": "CyShell", "compositor": "Labwc", "session_bridge": session.Available(), "cyshell_builtin": map[string]any{"available": false, "reason": "CyShell login-session bridge is unavailable; check the Labwc autostart supervisor"}}
		windows, _, err = call("list_windows", map[string]any{})
		if err != nil {
			return nil, fmt.Errorf("list desktop windows: %w", err)
		}
		focused, _, focusErr = call("focused_window", map[string]any{})
		workspaces = []any{}
		surfaces = []any{}
	} else {
		windows, _, err = call("list_windows", map[string]any{})
		if err != nil {
			return nil, fmt.Errorf("list desktop windows: %w", err)
		}
		focused, _, focusErr = call("focused_window", map[string]any{})
		workspaces = []any{}
		surfaces = []any{}
	}
	out := map[string]any{
		"shell_surfaces": surfaces,
		"windows":        windows,
		"workspaces":     workspaces,
		"permissions":    map[string]any{"backend": "cycomagent-desktop-context", "read_windows": true, "read_app_state": true, "capture_screen": in.IncludeScreenshot},
		"focused_window": focused,
		"context_digest": "",
	}
	if native != nil {
		out["shell_state"] = native
	}
	if focusErr != nil {
		out["focused_window_error"] = focusErr.Error()
	}
	if in.IncludeApp {
		var target map[string]any
		if m, ok := focused.(map[string]any); ok {
			target = m
			if nested, ok := m["focused_window"].(map[string]any); ok {
				target = nested
			}
		}
		args := map[string]any{}
		if target != nil {
			for _, key := range []string{"window_id", "pid", "app_id", "appId", "title", "wm_class"} {
				if v, ok := target[key]; ok && v != nil {
					args[key] = v
				}
			}
		}
		args["include_screenshot"] = in.IncludeScreenshot
		args["verbose"] = false
		app, content, appErr := call("get_app_state", args)
		if appErr != nil {
			out["app_error"] = appErr.Error()
		} else {
			out["app"] = app
			if len(content) > 0 {
				out["app_content"] = content
			}
		}
	}
	if in.IncludeSystem {
		env := make(map[string]string)
		for _, entry := range desktopEnvironment() {
			if k, v, ok := strings.Cut(entry, "="); ok {
				env[k] = v
			}
		}
		desktop := strings.TrimSpace(env["XDG_CURRENT_DESKTOP"])
		if desktop == "" {
			desktop = strings.TrimSpace(env["DESKTOP_SESSION"])
		}
		if native != nil {
			desktop = fmt.Sprint(native["desktop"])
		}
		out["system"] = map[string]any{"os": runtime.GOOS, "architecture": runtime.GOARCH, "desktop": desktop, "session_type": strings.TrimSpace(env["XDG_SESSION_TYPE"]), "compositor": func() string {
			if native != nil {
				return fmt.Sprint(native["compositor"])
			}
			return ""
		}()}
	}
	if in.KnownDigest != "" || len(in.KnownSections) > 0 {
		out["change_detection"] = map[string]any{"supported": false, "reason": "digest-based section caching is not yet implemented; a complete context was returned"}
	}
	return out, nil
}
