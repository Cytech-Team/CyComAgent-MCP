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
	t.Helper()
	r := registry.New()
	if err := r.Add(registry.Tool{Name: "echo", Description: "echo", InputSchema: registry.ObjectSchema(map[string]any{"message": registry.String("message")}, []string{"message"}), Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		return v, nil
	}}); err != nil {
		t.Fatal(err)
	}
	return &Server{Name: "test", Version: "1", Registry: r}
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
	rr := modernRequest(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"message":"hi"`) {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
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
