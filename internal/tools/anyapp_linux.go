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
	"sync"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

const defaultAnyAppBackend = "/opt/codex-desktop/resources/plugins/openai-bundled/plugins/computer-use/bin/codex-computer-use-linux"

type anyAppToolSpec struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type anyAppRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type anyAppRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *anyAppRPCError `json:"error,omitempty"`
	Method  string          `json:"method,omitempty"`
}

type anyAppCallResult struct {
	Content           []map[string]any `json:"content"`
	StructuredContent any              `json:"structuredContent"`
	IsError           bool             `json:"isError"`
}

type anyAppClient struct {
	mu      sync.Mutex
	binary  string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	decoder *json.Decoder
	nextID  uint64
	envSig  string
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
			Name:        name,
			Title:       spec.Title,
			Description: description,
			InputSchema: spec.InputSchema,
			Annotations: spec.Annotations,
			Source:      "core:anyapp",
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
	if c.cmd != nil && c.cmd.Process != nil && c.envSig == sig {
		return nil
	}
	c.stopLocked()

	cmd := exec.Command(c.binary, "mcp")
	cmd.Env = env
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("Any App stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("Any App stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start Any App backend: %w", err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.decoder = json.NewDecoder(stdout)
	c.envSig = sig
	c.nextID = 0

	if _, err := c.rpcLocked(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "CyComAgent-AnyApp",
			"version": "1",
		},
	}); err != nil {
		c.stopLocked()
		return fmt.Errorf("initialize Any App backend: %w", err)
	}
	if err := c.writeLocked(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}); err != nil {
		c.stopLocked()
		return fmt.Errorf("notify Any App initialized: %w", err)
	}
	return nil
}

func (c *anyAppClient) rpcLocked(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.cmd == nil || c.stdin == nil || c.decoder == nil {
		return nil, errors.New("Any App backend is not running")
	}
	c.nextID++
	id := c.nextID
	if err := c.writeLocked(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		c.stopLocked()
		return nil, fmt.Errorf("write Any App %s: %w", method, err)
	}

	type decoded struct {
		resp anyAppRPCResponse
		err  error
	}
	ch := make(chan decoded, 1)
	decoder := c.decoder
	go func() {
		for {
			var resp anyAppRPCResponse
			if err := decoder.Decode(&resp); err != nil {
				ch <- decoded{err: err}
				return
			}
			if len(resp.ID) == 0 {
				continue
			}
			var got uint64
			if err := json.Unmarshal(resp.ID, &got); err != nil || got != id {
				continue
			}
			ch <- decoded{resp: resp}
			return
		}
	}()

	select {
	case <-ctx.Done():
		c.stopLocked()
		return nil, ctx.Err()
	case out := <-ch:
		if out.err != nil {
			c.stopLocked()
			return nil, fmt.Errorf("read Any App %s: %w", method, out.err)
		}
		if out.resp.Error != nil {
			return nil, fmt.Errorf("Any App RPC %s failed (%d): %s", method, out.resp.Error.Code, out.resp.Error.Message)
		}
		return out.resp.Result, nil
	}
}

func (c *anyAppClient) writeLocked(value any) error {
	if c.stdin == nil {
		return io.ErrClosedPipe
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

func (c *anyAppClient) stopLocked() {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	c.cmd = nil
	c.stdin = nil
	c.decoder = nil
	c.envSig = ""
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
