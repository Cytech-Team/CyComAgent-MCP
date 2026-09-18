package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func testServer(t *testing.T) *Server {
	return testServerWithCompatibility(t, false)
}

func testServerWithCompatibility(t *testing.T, compatibility bool) *Server {
	t.Helper()
	r := registry.New()
	if err := r.Add(registry.Tool{Name: "echo", Description: "echo", InputSchema: registry.ObjectSchema(map[string]any{"message": registry.String("message")}, []string{"message"}), Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		return v, nil
	}}); err != nil {
		t.Fatal(err)
	}
	return &Server{Name: "test", Version: "1", Registry: r, CompatMissingReason: compatibility}
}

func modernRequest(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", ProtocolLatest)
	rr := httptest.NewRecorder()
	s.HTTPHandler("").ServeHTTP(rr, req)
	return rr
}

func TestDiscoverModern(t *testing.T) {
	s := testServer(t)
	rr := modernRequest(t, s, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["error"] != nil {
		t.Fatalf("unexpected response: %s", rr.Body.String())
	}
}
func TestToolsCallModern(t *testing.T) {
	s := testServer(t)
	rr := modernRequest(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi","reason":"echo the supplied message"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"message":"hi"`) {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"reason"`) {
		t.Fatalf("reason leaked through the handler: %s", rr.Body.String())
	}
}

func TestToolsListAdvertisesRequiredReason(t *testing.T) {
	s := testServer(t)
	rr := modernRequest(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Result.Tools) != 1 || envelope.Result.Tools[0].Name != "echo" {
		t.Fatalf("tools=%#v", envelope.Result.Tools)
	}
	schema := envelope.Result.Tools[0].InputSchema
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T", schema["properties"])
	}
	reason, ok := properties["reason"].(map[string]any)
	if !ok || reason["type"] != "string" {
		t.Fatalf("reason schema=%#v", properties["reason"])
	}
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("required=%T", schema["required"])
	}
	if !containsRequired(required, "message") || !containsRequired(required, "reason") {
		t.Fatalf("required=%#v", required)
	}
}

func TestToolsListEmitsOptionalOutputSchema(t *testing.T) {
	r := registry.New()
	for _, tool := range []registry.Tool{
		{
			Name:        "with_output",
			Description: "tool with output schema",
			InputSchema: registry.ObjectSchema(nil, nil),
			OutputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"ready": map[string]any{"type": "boolean"}},
				"required":   []string{"ready"},
			},
			Handler: func(context.Context, json.RawMessage) (any, error) { return map[string]any{"ready": true}, nil },
		},
		{
			Name:        "without_output",
			Description: "tool without output schema",
			InputSchema: registry.ObjectSchema(nil, nil),
			Handler:     func(context.Context, json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil },
		},
	} {
		if err := r.Add(tool); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Name: "test", Version: "1", Registry: r}
	rr := modernRequest(t, s, `{"jsonrpc":"2.0","id":9,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Result struct {
			Tools []struct {
				Name         string         `json:"name"`
				OutputSchema map[string]any `json:"outputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Result.Tools) != 2 {
		t.Fatalf("tools=%#v", envelope.Result.Tools)
	}
	for _, tool := range envelope.Result.Tools {
		switch tool.Name {
		case "with_output":
			if tool.OutputSchema == nil || tool.OutputSchema["type"] != "object" {
				t.Fatalf("output schema missing: %#v", tool.OutputSchema)
			}
		case "without_output":
			if tool.OutputSchema != nil {
				t.Fatalf("nil output schema was emitted: %#v", tool.OutputSchema)
			}
		default:
			t.Fatalf("unexpected tool %q", tool.Name)
		}
	}
}

func TestToolsCallWithoutReasonReturnsToolError(t *testing.T) {
	s := testServer(t)
	rr := modernRequest(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"isError":true`) || !strings.Contains(rr.Body.String(), `tool reason is required`) {
		t.Fatalf("unexpected error response: %s", rr.Body.String())
	}
}

func TestToolsCallStrictModeRejectsMissingReason(t *testing.T) {
	s := testServer(t)
	s.Strict = true
	req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", ProtocolLatest)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "echo")
	rr := httptest.NewRecorder()
	s.HTTPHandler("").ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `tool reason is required`) {
		t.Fatalf("strict missing reason response: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestToolsCallCompatibilityInjectsFallback(t *testing.T) {
	s := testServerWithCompatibility(t, true)
	rr := modernRequest(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"message":"hi"`) {
		t.Fatalf("compatibility response: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"reason"`) {
		t.Fatalf("fallback reason leaked to handler: %s", rr.Body.String())
	}
}

func TestToolsCallTranslatesOutOfBandReason(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantMessage bool
	}{
		{
			name:        "params",
			body:        `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","reason":"supplied beside arguments","arguments":{"message":"hi"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			wantMessage: true,
		},
		{
			name:        "meta",
			body:        `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"},"_meta":{"reason":"supplied in metadata","io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			wantMessage: true,
		},
		{
			name: "omitted-arguments",
			body: `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","reason":"supplied with omitted arguments","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := modernRequest(t, testServer(t), tc.body)
			if rr.Code != http.StatusOK || (tc.wantMessage && !strings.Contains(rr.Body.String(), `"message":"hi"`)) {
				t.Fatalf("translated reason response: status=%d body=%s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), `"reason"`) {
				t.Fatalf("translated reason leaked to handler: %s", rr.Body.String())
			}
		})
	}
}

func TestToolsCallCompatibilityPreservesInvalidReason(t *testing.T) {
	cases := []string{
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi","reason":"   "},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","reason":"   ","arguments":{"message":"hi"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
	}
	for _, body := range cases {
		rr := modernRequest(t, testServerWithCompatibility(t, true), body)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `tool reason is required`) {
			t.Fatalf("invalid reason response: status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
}

func TestToolsCallCompatibilityDoesNotConvertNonObjectArguments(t *testing.T) {
	s := testServerWithCompatibility(t, true)
	rr := modernRequest(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"echo","arguments":[],"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `tool reason is required`) {
		t.Fatalf("non-object compatibility response: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func containsRequired(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func TestProtocolHeaderMismatch(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`))
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	rr := httptest.NewRecorder()
	s.HTTPHandler("").ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}
func TestLegacySSEProbe(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.HTTPHandler("").ServeHTTP(rr, req); close(done) }()
	cancel()
	<-done
	if !strings.Contains(rr.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type=%q", rr.Header().Get("Content-Type"))
	}
	b, _ := io.ReadAll(rr.Result().Body)
	if !strings.Contains(string(b), "legacy SSE probe") {
		t.Fatalf("body=%q", string(b))
	}
}

func TestTokenProtectsGetAndPost(t *testing.T) {
	s := testServer(t)
	h := s.HTTPHandler("secret")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(method, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s expected 401 got %d", method, rr.Code)
		}
	}
}

func TestOriginValidationRejectsRemoteWebOrigin(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	s.HTTPHandler("").ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestOriginValidationAllowsLoopbackOrigin(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Origin", "http://127.0.0.1:3000")
	rr := httptest.NewRecorder()
	s.HTTPHandler("").ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}
