package mcp

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

const (
	ProtocolLatest = "2026-07-28"
	ProtocolLegacy = "2025-11-25"
	maxBodyBytes   = 4 << 20
)

type Server struct {
	Name         string
	Version      string
	Instructions string
	Registry     *registry.Registry
	Strict       bool

	requests  atomic.Uint64
	toolCalls atomic.Uint64
	toolErrs  atomic.Uint64
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type metaEnvelope struct {
	Meta map[string]json.RawMessage `json:"_meta"`
}

func (s *Server) Metrics() map[string]uint64 {
	return map[string]uint64{
		"mcp_requests_total":    s.requests.Load(),
		"mcp_tool_calls_total":  s.toolCalls.Load(),
		"mcp_tool_errors_total": s.toolErrs.Load(),
	}
}

func (s *Server) HTTPHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validOrigin(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if token != "" && !validToken(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPost:
			s.handlePOST(w, r)
		case http.MethodGet:
			// Compatibility for clients/tunnels that still probe the removed
			// legacy GET/SSE endpoint. This is deliberately a probe stream,
			// not a stateful MCP session transport.
			s.serveLegacyProbe(w, r)
		default:
			w.Header().Set("Allow", "POST, GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func validToken(r *http.Request, token string) bool {
	provided := r.Header.Get("X-CyCom-Token")
	if provided == "" {
		provided = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	if len(provided) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

// validOrigin follows the Streamable HTTP DNS-rebinding requirement. Native
// tunnel/CLI clients normally omit Origin. If a browser supplies one, the
// origin must itself be loopback; remote web pages must not be able to drive a
// localhost computer-control runtime.
func validOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	host := strings.TrimSpace(u.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handlePOST(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeHTTPError(w, nil, http.StatusBadRequest, -32700, "invalid request body", err.Error())
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(data, &req); err != nil {
		s.writeHTTPError(w, nil, http.StatusBadRequest, -32700, "parse error", err.Error())
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		s.writeHTTPError(w, req.ID, http.StatusBadRequest, -32600, "invalid request", nil)
		return
	}

	modern, version, verr := s.detectProtocol(r, req)
	if verr != nil {
		s.writeHTTPError(w, req.ID, http.StatusBadRequest, -32020, verr.Error(), nil)
		return
	}
	_ = version

	if s.Strict && modern {
		if got := r.Header.Get("Mcp-Method"); got == "" || got != req.Method {
			s.writeHTTPError(w, req.ID, http.StatusBadRequest, -32020, "Mcp-Method header mismatch", nil)
			return
		}
		if req.Method == "tools/call" {
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if got := r.Header.Get("Mcp-Name"); got == "" || got != p.Name {
				s.writeHTTPError(w, req.ID, http.StatusBadRequest, -32020, "Mcp-Name header mismatch", nil)
				return
			}
		}
	}

	if len(req.ID) == 0 || string(req.ID) == "null" {
		// Notifications do not receive JSON-RPC responses.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, rpcErr := s.dispatch(r.Context(), req, modern)
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
	if rpcErr != nil {
		resp.Result = nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if modern && rpcErr != nil && rpcErr.Code == -32601 {
		w.WriteHeader(http.StatusNotFound)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) detectProtocol(r *http.Request, req rpcRequest) (bool, string, error) {
	header := r.Header.Get("MCP-Protocol-Version")
	var env metaEnvelope
	_ = json.Unmarshal(req.Params, &env)
	var bodyVersion string
	if raw := env.Meta["io.modelcontextprotocol/protocolVersion"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &bodyVersion)
	}
	modern := header == ProtocolLatest || bodyVersion == ProtocolLatest
	if modern {
		if header == "" {
			if s.Strict {
				return true, bodyVersion, errors.New("missing MCP-Protocol-Version header")
			}
			header = bodyVersion
		}
		if bodyVersion == "" {
			if s.Strict {
				return true, header, errors.New("missing request _meta protocol version")
			}
			bodyVersion = header
		}
		if header != bodyVersion {
			return true, header, errors.New("MCP-Protocol-Version header does not match request _meta")
		}
		if header != ProtocolLatest {
			return true, header, fmt.Errorf("unsupported protocol version %s", header)
		}
		return true, header, nil
	}
	if header != "" && !supportedLegacy(header) {
		return false, header, fmt.Errorf("unsupported protocol version %s", header)
	}
	if header == "" {
		header = ProtocolLegacy
	}
	return false, header, nil
}

func supportedLegacy(v string) bool {
	switch v {
	case ProtocolLegacy, "2025-06-18", "2025-03-26":
		return true
	default:
		return false
	}
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest, modern bool) (any, *rpcError) {
	switch req.Method {
	case "server/discover":
		if !modern {
			// Discovery is harmless for legacy probes and allows clients to upgrade.
		}
		return s.withMeta(map[string]any{
			"resultType":        "complete",
			"supportedVersions": []string{ProtocolLatest, ProtocolLegacy, "2025-06-18", "2025-03-26"},
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      s.Instructions,
			"ttlMs":             300000,
			"cacheScope":        "public",
		}), nil

	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := p.ProtocolVersion
		if !supportedLegacy(v) {
			v = ProtocolLegacy
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
			"instructions":    s.Instructions,
		}, nil

	case "tools/list":
		list := s.Registry.List()
		tools := make([]map[string]any, 0, len(list))
		for _, t := range list {
			item := map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			}
			if t.Title != "" {
				item["title"] = t.Title
			}
			if len(t.Annotations) > 0 {
				item["annotations"] = t.Annotations
			}
			tools = append(tools, item)
		}
		out := map[string]any{"tools": tools}
		if modern {
			out["resultType"] = "complete"
			out["ttlMs"] = 30000
			out["cacheScope"] = "public"
			return s.withMeta(out), nil
		}
		return out, nil

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid tools/call parameters"}
		}
		if !s.Registry.Exists(p.Name) {
			return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
		}
		s.toolCalls.Add(1)
		value, err := s.Registry.Call(ctx, p.Name, p.Arguments)
		if err != nil {
			s.toolErrs.Add(1)
			out := map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			}
			if modern {
				out["resultType"] = "complete"
				return s.withMeta(out), nil
			}
			return out, nil
		}
		var content []map[string]any
		structured := value
		if rich, ok := value.(registry.RichResult); ok {
			structured = rich.Structured
			content = rich.Content
		}
		if len(content) == 0 {
			text, _ := json.Marshal(structured)
			content = []map[string]any{{"type": "text", "text": string(text)}}
		}
		out := map[string]any{
			"content":           content,
			"structuredContent": structured,
			"isError":           false,
		}
		if modern {
			out["resultType"] = "complete"
			return s.withMeta(out), nil
		}
		return out, nil

	case "ping":
		if modern {
			return nil, &rpcError{Code: -32601, Message: "method not found in protocol 2026-07-28"}
		}
		return map[string]any{}, nil

	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (s *Server) withMeta(result map[string]any) map[string]any {
	result["_meta"] = map[string]any{
		"io.modelcontextprotocol/serverInfo": map[string]any{
			"name": s.Name, "version": s.Version,
		},
	}
	return result
}

func (s *Server) writeHTTPError(w http.ResponseWriter, id json.RawMessage, status, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message, Data: data},
	})
}

func (s *Server) serveLegacyProbe(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-CyCom-Compatibility", "legacy-sse-probe")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": CyComAgent legacy SSE probe compatibility\n\n")
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	deadline := time.NewTimer(60 * time.Second)
	defer ticker.Stop()
	defer deadline.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 64<<10), maxBodyBytes)
	enc := json.NewEncoder(out)
	for scan.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scan.Bytes()
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		modern := req.Method == "server/discover"
		if !modern {
			var env metaEnvelope
			_ = json.Unmarshal(req.Params, &env)
			var v string
			_ = json.Unmarshal(env.Meta["io.modelcontextprotocol/protocolVersion"], &v)
			modern = v == ProtocolLatest
		}
		result, rpcErr := s.dispatch(ctx, req, modern)
		resp := rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
		if rpcErr != nil {
			resp.Result = nil
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	return nil
}
