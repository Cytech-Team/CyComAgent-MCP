//go:build linux

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

const (
	defaultAnyAppBackend     = "/opt/codex-desktop/resources/plugins/openai-bundled/plugins/computer-use/bin/codex-computer-use-linux"
	defaultAnyAppCallTimeout = 20 * time.Second
)

type anyAppToolSpec struct {
	Name         string         `json:"name"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
}

type anyAppCallResult struct {
	Content           []map[string]any `json:"content"`
	StructuredContent any              `json:"structuredContent"`
	IsError           bool             `json:"isError"`
}

type anyAppClient struct {
	stdioMCPState
	binary string
}

func registerAnyApp(r *registry.Registry) {
	binary, ok := anyAppBackendPath()
	if !ok {
		return
	}
	specs, err := discoverAnyAppTools(binary)
	if err != nil || len(specs) == 0 {
		return
	}
	client := &anyAppClient{binary: binary}
	for _, spec := range specs {
		spec := spec
		if spec.Name == "" {
			continue
		}
		name := "anyapp_" + spec.Name
		description := "Any App (Linux): " + spec.Description
		if spec.Description == "" {
			description = "Any App (Linux) tool backed by codex-computer-use-linux."
		}
		if spec.InputSchema == nil {
			spec.InputSchema = registry.ObjectSchema(nil, nil)
		}
		_ = r.Add(registry.Tool{
			Name:         name,
			Title:        spec.Title,
			Description:  description,
			InputSchema:  spec.InputSchema,
			OutputSchema: spec.OutputSchema,
			Annotations:  spec.Annotations,
			Source:       "core:anyapp",
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				return client.callTool(ctx, spec.Name, raw)
			},
		})
	}
}

func anyAppBackendPath() (string, bool) {
	candidates := []string{}
	if p := strings.TrimSpace(os.Getenv("CYCOM_ANYAPP_BACKEND")); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		"/usr/local/lib/cycomagent/computer-use/codex-computer-use-linux",
		defaultAnyAppBackend,
		"/usr/lib/chatgpt/resources/plugins/openai-bundled/plugins/computer-use/bin/codex-computer-use-linux",
	)
	if p, err := exec.LookPath("codex-computer-use-linux"); err == nil {
		candidates = append(candidates, p)
	}
	seen := map[string]bool{}
	for _, p := range candidates {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

func discoverAnyAppTools(binary string) ([]anyAppToolSpec, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &anyAppClient{binary: binary}
	client.mu.Lock()
	defer client.mu.Unlock()
	if err := client.ensureStartedLocked(ctx); err != nil {
		return nil, err
	}
	defer client.stopLocked()
	result, err := client.rpcLocked(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var listed struct {
		Tools []anyAppToolSpec `json:"tools"`
	}
	if err := json.Unmarshal(result, &listed); err != nil {
		return nil, fmt.Errorf("decode Any App tools/list: %w", err)
	}
	sort.Slice(listed.Tools, func(i, j int) bool { return listed.Tools[i].Name < listed.Tools[j].Name })
	return listed.Tools, nil
}

func (c *anyAppClient) callTool(ctx context.Context, tool string, raw json.RawMessage) (any, error) {
	// Desktop backends can block indefinitely when a compositor removes or
	// reconfigures outputs (for example physical -> headless-only). Bound every
	// call so a wedged capture/portal cannot stall CyComAgent or its tunnel.
	callCtx, cancel := context.WithTimeout(ctx, defaultAnyAppCallTimeout)
	defer cancel()
	ctx = callCtx
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var arguments any
	if err := decodeAnyAppJSON(raw, &arguments); err != nil {
		return nil, fmt.Errorf("decode Any App arguments: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureStartedLocked(ctx); err != nil {
		return nil, err
	}
	result, err := c.rpcLocked(ctx, "tools/call", map[string]any{
		"name":      tool,
		"arguments": arguments,
	})
	if err != nil {
		return nil, err
	}
	var call anyAppCallResult
	if err := decodeAnyAppJSON(result, &call); err != nil {
		return nil, fmt.Errorf("decode Any App tool result: %w", err)
	}
	if call.IsError {
		return nil, errors.New(anyAppContentError(call.Content))
	}
	structured := call.StructuredContent
	if structured == nil {
		structured = anyAppStructuredFromContent(call.Content)
	}
	if structured == nil {
		structured = map[string]any{"ok": true, "backend": "codex-computer-use-linux", "tool": tool}
	}
	return registry.RichResult{Structured: structured, Content: call.Content}, nil
}

// Keep KWin's uint64 window IDs exact across the MCP bridge. Decoding
// arbitrary JSON through float64 silently rounds IDs above 2^53.
func decodeAnyAppJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("unexpected trailing JSON value")
	}
	return nil
}

func anyAppStructuredFromContent(content []map[string]any) any {
	for i := len(content) - 1; i >= 0; i-- {
		if content[i]["type"] != "text" {
			continue
		}
		text, ok := content[i]["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		var value any
		if decodeAnyAppJSON([]byte(text), &value) == nil {
			return value
		}
	}
	return nil
}

func anyAppContentError(content []map[string]any) string {
	for _, item := range content {
		if item["type"] == "text" {
			if text, ok := item["text"].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return "Any App backend returned an MCP tool error"
}

func (c *anyAppClient) ensureStartedLocked(ctx context.Context) error {
	env := desktopEnvironment()
	sig := anyAppDesktopEnvSignature(env) + "\x00" + anyAppBinaryFingerprint(c.binary)
	return c.stdioMCPState.ensureStartedLocked(ctx, c.binary, []string{"mcp"}, env, sig, "Any App", "CyComAgent-AnyApp")
}

func (c *anyAppClient) rpcLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	result, err := c.stdioMCPState.rpcLocked(ctx, method, params, "Any App")
	if err == nil {
		return result, nil
	}
	// Preserve Any App's established behavior: provider-level JSON-RPC errors
	// are surfaced to the outer dispatcher as ordinary tool errors. The shared
	// stdio seam retains error data for bridges that explicitly forward the
	// protocol error, such as agent-workspace-linux.
	var rpcErr *registry.JSONRPCError
	if errors.As(err, &rpcErr) {
		return nil, fmt.Errorf("Any App RPC %s failed (%d): %s", method, rpcErr.Code, rpcErr.Message)
	}
	return nil, err
}

func anyAppDesktopEnvSignature(env []string) string {
	wanted := map[string]bool{
		"DISPLAY": true, "WAYLAND_DISPLAY": true, "XDG_RUNTIME_DIR": true,
		"DBUS_SESSION_BUS_ADDRESS": true, "XAUTHORITY": true,
		"XDG_CURRENT_DESKTOP": true, "XDG_SESSION_TYPE": true,
	}
	values := make([]string, 0, len(wanted))
	for _, entry := range env {
		if i := strings.IndexByte(entry, '='); i > 0 && wanted[entry[:i]] {
			values = append(values, entry)
		}
	}
	sort.Strings(values)
	return strings.Join(values, "\x00")
}

func anyAppBinaryFingerprint(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
}

func anyAppInstalledBackend() (string, bool) {
	return anyAppBackendPath()
}

func anyAppToolPrefixCount(r *registry.Registry) int {
	count := 0
	for _, tool := range r.List() {
		if strings.HasPrefix(tool.Name, "anyapp_") {
			count++
		}
	}
	return count
}

func anyAppBackendBase(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}
