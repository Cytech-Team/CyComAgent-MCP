//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnyAppStructuredFromContent(t *testing.T) {
	got := anyAppStructuredFromContent([]map[string]any{
		{"type": "image", "data": "AA==", "mimeType": "image/png"},
		{"type": "text", "text": `{"width":1920,"source":"xdg-desktop-portal"}`},
	})
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("got %T, want map", got)
	}
	if m["source"] != "xdg-desktop-portal" {
		t.Fatalf("source=%v", m["source"])
	}
}

func TestAnyAppContentError(t *testing.T) {
	got := anyAppContentError([]map[string]any{{"type": "text", "text": "boom"}})
	if got != "boom" {
		t.Fatalf("got %q", got)
	}
}

func TestAnyAppPreservesKWinWindowID(t *testing.T) {
	const id = "14944722567147409204"
	source := []byte(`{"window_id":` + id + `}`)
	var args any
	if err := decodeAnyAppJSON(source, &args); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(args)
	if err != nil || string(encoded) != string(source) {
		t.Fatalf("window ID changed: %s (%v)", encoded, err)
	}
	content := anyAppStructuredFromContent([]map[string]any{{"type": "text", "text": string(source)}})
	encoded, err = json.Marshal(content)
	if err != nil || string(encoded) != string(source) {
		t.Fatalf("content ID changed: %s (%v)", encoded, err)
	}
	var call anyAppCallResult
	if err := decodeAnyAppJSON([]byte(`{"structuredContent":{"window_id":`+id+`}}`), &call); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(call.StructuredContent)
	if err != nil || !strings.Contains(string(encoded), id) {
		t.Fatalf("structured ID changed: %s", encoded)
	}
}

func TestAnyAppJSONRejectsTrailingValues(t *testing.T) {
	var value any
	if err := decodeAnyAppJSON([]byte(`{} {}`), &value); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	if err := decodeAnyAppJSON([]byte(`{} garbage`), &value); err == nil {
		t.Fatal("accepted trailing garbage")
	}
	if err := decodeAnyAppJSON([]byte("{} \n"), &value); err != nil {
		t.Fatal(err)
	}
}

func TestAnyAppDesktopEnvSignature(t *testing.T) {
	first := anyAppDesktopEnvSignature([]string{"DISPLAY=:1", "WAYLAND_DISPLAY=wayland-0", "IGNORED=a"})
	reordered := anyAppDesktopEnvSignature([]string{"IGNORED=b", "WAYLAND_DISPLAY=wayland-0", "DISPLAY=:1"})
	if first != reordered {
		t.Fatal("signature depends on order or unrelated variables")
	}
	changed := anyAppDesktopEnvSignature([]string{"DISPLAY=:1", "WAYLAND_DISPLAY=wayland-1"})
	if first == changed {
		t.Fatal("session change not detected")
	}
}

func TestAnyAppBackendPathOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CYCOM_ANYAPP_BACKEND", path)
	got, ok := anyAppBackendPath()
	if !ok || got != path {
		t.Fatalf("got %q, %v; want explicit override", got, ok)
	}
}

// The subprocess speaks just enough MCP to verify the bridge without a desktop,
// portal permissions, or a separately installed computer-use helper.
func TestAnyAppHelperProcess(t *testing.T) {
	if os.Getenv("CYCOM_ANYAPP_TEST_PROCESS") != "1" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				os.Exit(0)
			}
			os.Exit(2)
		}
		if len(req.ID) == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "test helper", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				os.Exit(3)
			}
			if params.Name == "wait" {
				time.Sleep(time.Hour)
			}
			if params.Name == "error" {
				result = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "expected helper failure"}}}
			} else {
				result = map[string]any{
					"structuredContent": params.Arguments,
					"content": []any{
						map[string]any{"type": "image", "mimeType": "image/png", "data": "AA=="},
						map[string]any{"type": "text", "text": string(params.Arguments)},
					},
				}
			}
		default:
			os.Exit(4)
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			os.Exit(5)
		}
	}
}

func anyAppTestBackend(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "helper")
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
	script := "#!/bin/sh\nexec " + quoted + " -test.run=^TestAnyAppHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CYCOM_ANYAPP_TEST_PROCESS", "1")
	return wrapper
}

func TestAnyAppTransportRoundTrip(t *testing.T) {
	binary := anyAppTestBackend(t)
	specs, err := discoverAnyAppTools(binary)
	if err != nil || len(specs) != 1 || specs[0].Name != "echo" {
		t.Fatalf("discovery: %v, %v", specs, err)
	}
	client := &anyAppClient{binary: binary}
	defer client.stopLocked()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const source = `{"window_id":14944722567147409204,"text":"literal"}`
	result, err := client.callTool(ctx, "echo", json.RawMessage(source))
	if err != nil {
		t.Fatal(err)
	}
	rich, ok := result.(registry.RichResult)
	if !ok || len(rich.Content) != 2 {
		t.Fatalf("expected native rich result, got %#v", result)
	}
	if rich.Content[0]["type"] != "image" || rich.Content[0]["data"] != "AA==" {
		t.Fatal("image content changed")
	}
	encoded, err := json.Marshal(rich.Structured)
	if err != nil || !strings.Contains(string(encoded), "14944722567147409204") {
		t.Fatalf("ID rounded across transport: %s", encoded)
	}
	pid := client.cmd.Process.Pid
	_, err = client.callTool(ctx, "echo", json.RawMessage(`{}`))
	if err != nil || client.cmd.Process.Pid != pid {
		t.Fatal("persistent helper not reused", err)
	}
	_, err = client.callTool(ctx, "error", json.RawMessage(`{}`))
	if err == nil || err.Error() != "expected helper failure" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnyAppCancellationStopsHelper(t *testing.T) {
	client := &anyAppClient{binary: anyAppTestBackend(t)}
	defer client.stopLocked()
	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initCancel()
	if _, err := client.callTool(initCtx, "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := client.callTool(ctx, "wait", json.RawMessage(`{}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if client.cmd != nil || client.stdin != nil || client.decoder != nil {
		t.Fatal("helper state retained after cancellation")
	}
	if _, err := client.callTool(initCtx, "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
}
