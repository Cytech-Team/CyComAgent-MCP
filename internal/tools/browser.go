package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/browserbridge"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

const browserBridgeURL = browserbridge.APIURL

var browserActions = []string{
	"tabs", "open", "switch", "close", "navigate", "back", "forward", "reload",
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
		"action": map[string]any{"type": "string", "enum": browserActions},
		"tabId":  registry.Integer("optional browser tab id; defaults to the active tab"),
		"url":    registry.String("URL for open/navigate"),
		"active": map[string]any{"type": "boolean", "description": "whether a newly opened tab should become active; defaults true"},

		// Semantic targeting remains available, but clicks are executed as real
		// CDP pointer input at the resolved element center rather than element.click().
		"selector":   registry.String("optional CSS selector for semantic targeting"),
		"text":       registry.String("visible text for semantic click targeting, or literal text for type"),
		"index":      registry.Integer("element index from state; requires snapshotId from the same state"),
		"snapshotId": registry.String("state snapshot identity required when targeting an observed index"),
		"frameId":    registry.Integer("semantic pointer frame; only the top frame (0) is supported; use viewport coordinates for child frames"),

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
		"keys": registry.StringArray("keypress keys/modifiers, for example [\"CTRL\",\"A\"]"),
		"ms":   registry.Integer("wait duration in milliseconds; maximum 15000"),
	}, []string{"action"})

	must(r.Add(registry.Tool{
		Name:        "browser_use",
		Description: "Control the user's connected browser through the CyCom extension with a computer-use-style visual loop. The extension can inspect and enumerate real browser tabs, capture screenshots and page state, navigate, and send real pointer or keyboard input. Actions can change page state and may focus a tab or browser window, including during capture; this is not an isolated browser. Pointer/keyboard actions automatically return a post-action screenshot so the model can visually verify the result.",
		InputSchema: schema,
		Source:      "core",
		Handler:     browserUse,
	}))
}

func browserUse(ctx context.Context, raw json.RawMessage) (any, error) {
	return browserUseWithClient(ctx, raw, &http.Client{Timeout: 25 * time.Second})
}

func browserUseWithClient(ctx context.Context, raw json.RawMessage, c *http.Client) (any, error) {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode browser arguments: %w", err)
	}
	action, _ := input["action"].(string)
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}

	out, err := callBrowserBridgeWithRetry(ctx, c, raw)
	if err != nil {
		return nil, err
	}

	if action == "screenshot" {
		return browserScreenshotRich("Browser screenshot", out, nil)
	}
	if !browserRequestNeedsObservation(input) {
		return out, nil
	}

	tabID := browserRequestedTabID(input)
	if resolved := browserTabIDFromResult(out); resolved != 0 {
		tabID = resolved
	}
	shotReq := map[string]any{"action": "screenshot"}
	if tabID != 0 {
		shotReq["tabId"] = tabID
	}
	shotRaw, _ := json.Marshal(shotReq)
	shot, shotErr := callBrowserBridgeWithRetry(ctx, c, shotRaw)
	if shotErr != nil {
		return map[string]any{
			"action_result":     out,
			"observation_error": shotErr.Error(),
		}, nil
	}
	return browserScreenshotRich("Browser action completed; post-action visual observation", shot, out)
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

func browserRequestNeedsObservation(input map[string]any) bool {
	action, _ := input["action"].(string)
	if action == "open" {
		if active, specified := input["active"].(bool); specified && !active {
			return false
		}
	}
	return browserActionNeedsObservation(action)
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
	var input struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode browser arguments: %w", err)
	}
	// Screenshot capture can focus the user's browser, so only tabs and state can repeat.
	safeToRetry := input.Action == "tabs" || input.Action == "state"
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		out, retry, err := callBrowserBridge(ctx, c, raw)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !safeToRetry || !retry || attempt == 2 {
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
	if len(raw) > browserbridge.MaxRequestBytes {
		return nil, false, fmt.Errorf("browser request exceeds %d bytes", browserbridge.MaxRequestBytes)
	}
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
		return nil, true, fmt.Errorf("browser bridge request failed; action outcome may be unknown: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, browserbridge.MaxResponseBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > browserbridge.MaxResponseBytes {
		return nil, false, fmt.Errorf("browser response exceeds %d bytes; action outcome may be unknown", browserbridge.MaxResponseBytes)
	}
	if resp.StatusCode >= 300 {
		retry := resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout
		return nil, retry, fmt.Errorf("browser bridge HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false, err
	}
	envelope, ok := out.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("browser bridge response is not an object")
	}
	succeeded, ok := envelope["ok"].(bool)
	if !ok {
		return nil, false, fmt.Errorf("browser bridge response has no status")
	}
	if !succeeded {
		return nil, false, fmt.Errorf("browser action failed: %v", envelope["error"])
	}
	return out, false, nil
}
