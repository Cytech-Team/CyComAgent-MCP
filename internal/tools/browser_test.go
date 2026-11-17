package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func TestBrowserUseSchemaExposesComputerActions(t *testing.T) {
	r := registry.New()
	registerBrowser(r)

	var tool registry.Tool
	found := false
	for _, candidate := range r.List() {
		if candidate.Name == "browser_use" {
			tool = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("browser_use tool not registered")
	}

	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T", tool.InputSchema["properties"])
	}
	action, ok := props["action"].(map[string]any)
	if !ok {
		t.Fatalf("action schema=%T", props["action"])
	}
	values, ok := action["enum"].([]string)
	if !ok {
		t.Fatalf("action enum=%T %#v", action["enum"], action["enum"])
	}

	have := make(map[string]bool, len(values))
	for _, value := range values {
		have[value] = true
	}
	for _, required := range []string{
		"tabs", "open", "switch", "close", "finish", "navigate", "back", "forward", "reload",
		"state", "screenshot", "move", "click", "double_click", "drag", "type",
		"keypress", "scroll", "wait",
	} {
		if !have[required] {
			t.Errorf("missing browser action %q", required)
		}
	}
}

func TestBrowserScreenshotRichProducesNativeImageContent(t *testing.T) {
	value, err := browserScreenshotRich("visual observation", map[string]any{
		"ok": true,
		"result": map[string]any{
			"tabId":    float64(42),
			"title":    "Example",
			"url":      "https://example.com/",
			"mimeType": "image/png",
			"data":     "YWJj",
			"width":    float64(1280),
			"height":   float64(720),
		},
	}, map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}

	rich, ok := value.(registry.RichResult)
	if !ok {
		t.Fatalf("result=%T", value)
	}
	if len(rich.Content) != 2 {
		t.Fatalf("content=%#v", rich.Content)
	}
	if got := rich.Content[1]["type"]; got != "image" {
		t.Fatalf("image content type=%v", got)
	}
	if got := rich.Content[1]["mimeType"]; got != "image/png" {
		t.Fatalf("mimeType=%v", got)
	}
	if got := rich.Content[1]["data"]; got != "YWJj" {
		t.Fatalf("image data=%v", got)
	}

	encoded, err := json.Marshal(rich.Structured)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" {
		t.Fatal("missing structured metadata")
	}
	if strings.Contains(string(encoded), "YWJj") {
		t.Fatal("raw image data leaked into structured metadata")
	}
}

func TestBrowserActionObservationPolicy(t *testing.T) {
	for _, action := range []string{
		"open", "switch", "navigate", "back", "forward", "reload",
		"move", "click", "double_click", "drag", "type", "keypress", "scroll", "wait",
	} {
		if !browserActionNeedsObservation(action) {
			t.Errorf("%s should request a post-action screenshot", action)
		}
	}
	for _, action := range []string{"tabs", "state", "screenshot", "close", "finish"} {
		if browserActionNeedsObservation(action) {
			t.Errorf("%s should not request an automatic post-action screenshot", action)
		}
	}
}

func TestBrowserTabIDFromResult(t *testing.T) {
	if got := browserTabIDFromResult(map[string]any{
		"result": map[string]any{"tabId": float64(123)},
	}); got != 123 {
		t.Fatalf("tab id=%d", got)
	}
	if got := browserTabIDFromResult(map[string]any{
		"result": map[string]any{"id": float64(456)},
	}); got != 456 {
		t.Fatalf("id=%d", got)
	}
}
