package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"io"
	"net/http"
	"strings"
	"time"
)

const browserBridgeURL = "http://127.0.0.1:17373/api"

func registerBrowser(r *registry.Registry) {
	schema := registry.ObjectSchema(map[string]any{
		"action": map[string]any{"type": "string", "enum": []string{"tabs", "open", "navigate", "state", "click", "type", "scroll"}},
		"tabId":  registry.Integer("optional browser tab id"), "url": registry.String("URL for open/navigate"),
		"selector": registry.String("optional CSS selector"), "text": registry.String("visible text for click or literal text for type"),
		"index": registry.Integer("element index from state"), "x": registry.Integer("horizontal scroll pixels"), "y": registry.Integer("vertical scroll pixels"),
	}, []string{"action"})
	must(r.Add(registry.Tool{Name: "browser_use", Description: "Control a connected Chromium/Brave tab semantically through the CyCom Browser Bridge extension. Supports tab listing/navigation, DOM state, click, type and scroll.", InputSchema: schema, Source: "core", Handler: browserUse}))
}

func browserUse(ctx context.Context, raw json.RawMessage) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, browserBridgeURL, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("browser bridge unavailable: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("browser bridge HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
