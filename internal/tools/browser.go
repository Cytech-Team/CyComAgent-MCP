package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

const browserBridgeURL = "http://cycom-browser-native/api"

var browserActions = []string{
	"tabs", "open", "switch", "close", "finish", "navigate", "back", "forward", "reload",
	"state", "screenshot", "move", "click", "double_click", "drag", "type",
	"keypress", "scroll", "wait",
}

func registerBrowser(r *registry.Registry) {
	point := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"x": registry.Integer("x coordinate"),
			"y": registry.Integer("y coordinate"),
		},
		"required": []string{"x", "y"},
	}
	schema := registry.ObjectSchema(map[string]any{
		"action": map[string]any{"type": "string", "enum": browserActions, "description": "Use finish at the end of an AI browsing task to close its background Agent-owned tabs immediately. Idle background Agent tabs are also cleaned up after 3 minutes. Never close personal tabs."},
		"tabId":  registry.Integer("optional browser tab id; defaults to the active tab"),
		"url":    registry.String("URL for open/navigate"),
		"active": map[string]any{"type": "boolean", "description": "whether a newly opened tab should become active; defaults false to preserve user focus"},
		"focus":  map[string]any{"type": "boolean", "description": "explicitly focus the target tab before pointer/keyboard actions; defaults false"},

		// Semantic targeting remains available, but clicks are executed as real
		// CDP pointer input at the resolved element center rather than element.click().
		"selector": registry.String("optional CSS selector for semantic targeting"),
		"text":     registry.String("visible text for semantic click targeting, or literal text for type"),
		"index":    registry.Integer("element index from state/selector results"),

		// Visual computer-use coordinates.
		"x":      registry.Integer("viewport x coordinate"),
		"y":      registry.Integer("viewport y coordinate"),
		"button": map[string]any{"type": "string", "enum": []string{"left", "middle", "right"}, "description": "mouse button; default left"},
		"path": map[string]any{
			"type":        "array",
			"description": "drag path as viewport coordinate points",
			"minItems":    2,
			"items":       point,
		},
		"startX": registry.Integer("drag start x when path is omitted"),
		"startY": registry.Integer("drag start y when path is omitted"),
		"endX":   registry.Integer("drag end x when path is omitted"),
		"endY":   registry.Integer("drag end y when path is omitted"),
		"deltaX": registry.Integer("horizontal wheel delta"),
		"deltaY": registry.Integer("vertical wheel delta"),

		// Real keyboard input.
		"key":  registry.String("keypress key or chord, for example ENTER or CTRL+A"),
		"keys": registry.StringArray("modifier keys for pointer actions, or keys/modifiers for keypress; for example [\"CTRL\",\"A\"]"),
		"ms":   registry.Integer("wait duration in milliseconds; maximum 15000"),
	}, []string{"action"})

	must(r.Add(registry.Tool{
		Name:        "browser_use",
		Description: "Control the dedicated isolated CyShell browser with a computer-use-style visual loop. Supports non-focus-stealing CDP screenshots of background tabs, deep rendered state (full text, headings, links, forms, tables, images, frames, open shadow DOM and accessibility metadata), real CDP pointer input (move/click/double-click/drag/wheel with modifiers), real keyboard input, tabs and navigation. Pointer/keyboard actions automatically return a post-action screenshot so the model can visually verify the result. Agent-owned tabs stay in a CyCom tab group and do not take focus by default; use switch or active:true only on explicit request. ALWAYS call action=finish when a browsing task is complete to close Agent-owned background tabs right away; idle tabs are also cleaned up after 3 minutes. An active tab is preserved to avoid closing what the user is viewing.",
		InputSchema: schema,
		Source:      "core",
		Handler:     browserUse,
	}))
}

func browserUse(ctx context.Context, raw json.RawMessage) (any, error) {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode browser arguments: %w", err)
	}
	action, _ := input["action"].(string)
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}

	socketPath := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "cycom-browser-native", "api.sock")
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		socketPath = fmt.Sprintf("/run/user/%d/cycom-browser-native/api.sock", os.Getuid())
	}
	c := &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}}
	if action == "screenshot" {
		return browserHeadlessScreenshotRich(ctx, c, "Browser screenshot", browserRequestedTabID(input), nil)
	}

	out, err := callBrowserBridgeWithRetry(ctx, c, raw)
	if err != nil {
		return nil, err
	}
	if !browserActionNeedsObservation(action) {
		return out, nil
	}

	tabID := browserRequestedTabID(input)
	if resolved := browserTabIDFromResult(out); resolved != 0 {
		tabID = resolved
	}
	shot, shotErr := browserHeadlessScreenshotRich(ctx, c, "Browser action completed; post-action visual observation", tabID, out)
	if shotErr != nil {
		return map[string]any{
			"action_result":     out,
			"observation_error": shotErr.Error(),
		}, nil
	}
	return shot, nil
}

func browserActionNeedsObservation(action string) bool {
	switch action {
	case "open", "switch", "navigate", "back", "forward", "reload",
		"move", "click", "double_click", "drag", "type", "keypress", "scroll", "wait":
		return true
	default:
		return false
	}
}

func browserRequestedTabID(input map[string]any) int64 {
	switch v := input["tabId"].(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case int:
		return int64(v)
	case int64:
		return v
	default:
		return 0
	}
}

func browserTabIDFromResult(out any) int64 {
	env, ok := out.(map[string]any)
	if !ok {
		return 0
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		return 0
	}
	for _, key := range []string{"tabId", "id"} {
		switch v := result[key].(type) {
		case float64:
			return int64(v)
		case json.Number:
			n, _ := v.Int64()
			if n != 0 {
				return n
			}
		}
	}
	return 0
}

// browserHeadlessScreenshotRich captures the target via CDP without switching
// active tabs or foreground windows, including when the tab is in the background.
func browserHeadlessScreenshotRich(ctx context.Context, c *http.Client, label string, tabID int64, actionOut any) (any, error) {
	req := map[string]any{"action": "screenshot"}
	if tabID != 0 {
		req["tabId"] = tabID
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	shot, err := callBrowserBridgeWithRetry(ctx, c, raw)
	if err != nil {
		return nil, err
	}
	return browserScreenshotRich(label, shot, actionOut)
}

func browserScreenshotRich(label string, shotOut any, actionOut any) (any, error) {
	env, ok := shotOut.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("browser screenshot response has type %T", shotOut)
	}
	if okValue, exists := env["ok"]; exists {
		if okBool, isBool := okValue.(bool); isBool && !okBool {
			return nil, fmt.Errorf("browser screenshot failed: %v", env["error"])
		}
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("browser screenshot response has no result object")
	}
	data, _ := result["data"].(string)
	mime, _ := result["mimeType"].(string)
	if mime == "" {
		mime = "image/png"
	}
	if data == "" {
		return nil, fmt.Errorf("browser screenshot response has no image data")
	}

	meta := make(map[string]any, len(result))
	for k, v := range result {
		if k != "data" {
			meta[k] = v
		}
	}
	structured := any(meta)
	if actionOut != nil {
		structured = map[string]any{
			"action_result": actionOut,
			"observation":   meta,
		}
	}
	return registry.RichResult{
		Structured: structured,
		Content: []map[string]any{
			{"type": "text", "text": label},
			{"type": "image", "data": data, "mimeType": mime},
		},
	}, nil
}

func callBrowserBridgeWithRetry(ctx context.Context, c *http.Client, raw json.RawMessage) (any, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		out, retry, err := callBrowserBridge(ctx, c, raw)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !retry || attempt == 2 {
			return nil, err
		}
		delay := time.Duration(attempt+1) * 750 * time.Millisecond
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	return nil, lastErr
}

func callBrowserBridge(ctx context.Context, c *http.Client, raw json.RawMessage) (any, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, browserBridgeURL, strings.NewReader(string(raw)))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, true, fmt.Errorf("browser bridge unavailable: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode >= 300 {
		retry := resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout
		return nil, retry, fmt.Errorf("browser bridge HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false, err
	}
	return out, false, nil
}
