package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/browserbridge"
	"github.com/coder/websocket"
)

func TestListenAddrRejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:17373", ":17373", "[::]:17373", "192.168.1.5:17373", "localhost:17373", "evil.example:17373", "127.0.0.1:0", "127.0.0.1:70000", "127.0.0.1"} {
		if got, err := listenAddr(addr); err == nil {
			t.Errorf("accepted %q as %q", addr, got)
		}
	}
	for _, addr := range []string{"", "127.0.0.1:17373", "[::1]:17373", "127.0.0.2:17373"} {
		if _, err := listenAddr(addr); err != nil {
			t.Errorf("rejected %q: %v", addr, err)
		}
	}
}

func TestAPIRejectsInvalidAndOversizedRequestBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"action":"tabs"} {"action":"open"}`, 400},
		{`null`, 400},
		{`[]`, 400},
		{`{"action":"type","text":"` + strings.Repeat("x", browserbridge.MaxRequestBytes) + `"}`, 413},
		{`{"action":"tabs"}` + strings.Repeat(" ", browserbridge.MaxRequestBytes), 413},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		api(w, req)
		if w.Code != tc.status {
			t.Fatalf("got %d (%s), want %d", w.Code, w.Body.String(), tc.status)
		}
	}
}

func TestAPIRejectsBrowserOriginsAndNonJSONBeforeDispatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/extension", ext)
	mux.HandleFunc("/api", api)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/extension", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"chrome-extension://test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	t.Cleanup(func() {
		mu.Lock()
		active := conn
		conn = nil
		lastSeen = time.Time{}
		mu.Unlock()
		if active != nil {
			_ = active.CloseNow()
		}
	})

	dispatched := make(chan map[string]any, 8)
	go func() {
		for {
			_, payload, readErr := c.Read(ctx)
			if readErr != nil {
				return
			}
			var request map[string]any
			if json.Unmarshal(payload, &request) != nil {
				return
			}
			dispatched <- request
			id, _ := request["id"].(string)
			ack, _ := json.Marshal(response{ID: id, OK: true, Result: map[string]any{"action": request["action"]}})
			if c.Write(ctx, websocket.MessageText, ack) != nil {
				return
			}
		}
	}()

	tests := []struct {
		name        string
		origin      *string
		contentType *string
		status      int
	}{
		{name: "remote Origin", origin: ptr("https://attacker.example"), contentType: ptr("application/json"), status: http.StatusForbidden},
		{name: "null Origin", origin: ptr("null"), contentType: ptr("application/json"), status: http.StatusForbidden},
		{name: "empty Origin header", origin: ptr(""), contentType: ptr("application/json"), status: http.StatusForbidden},
		{name: "text plain", contentType: ptr("text/plain"), status: http.StatusUnsupportedMediaType},
		{name: "form encoded", contentType: ptr("application/x-www-form-urlencoded"), status: http.StatusUnsupportedMediaType},
		{name: "missing content type", status: http.StatusUnsupportedMediaType},
		{name: "malformed content type", contentType: ptr("application/json; charset=\"unterminated"), status: http.StatusUnsupportedMediaType},
		{name: "originless json with parameters", contentType: ptr("Application/JSON; charset=utf-8"), status: http.StatusOK},
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api", strings.NewReader(`{"action":"tabs"}`))
			if err != nil {
				t.Fatal(err)
			}
			if tc.origin != nil {
				req.Header.Set("Origin", *tc.origin)
			}
			if tc.contentType != nil {
				req.Header.Set("Content-Type", *tc.contentType)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, body %q; want %d", resp.StatusCode, body, tc.status)
			}
		})
	}

	select {
	case request := <-dispatched:
		if request["action"] != "tabs" {
			t.Fatalf("dispatched unexpected action: %#v", request)
		}
	default:
		t.Fatal("originless JSON request did not reach extension")
	}
	select {
	case request := <-dispatched:
		t.Fatalf("rejected request reached extension: %#v", request)
	default:
	}
}

func ptr(value string) *string { return &value }

func TestExtensionLargeResponseAndDuplicateDoesNotBlock(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/extension", ext)
	mux.HandleFunc("/api", api)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/extension", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"chrome-extension://test"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	t.Cleanup(func() {
		mu.Lock()
		if conn != nil {
			_ = conn.CloseNow()
		}
		conn = nil
		lastSeen = time.Time{}
		mu.Unlock()
	})
	extErrors := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			_, b, err := c.Read(ctx)
			if err != nil {
				extErrors <- err
				return
			}
			var q map[string]any
			if err = json.Unmarshal(b, &q); err != nil {
				extErrors <- err
				return
			}
			payload, _ := json.Marshal(map[string]any{"id": q["id"], "ok": true, "result": map[string]any{"data": strings.Repeat("x", 100000)}})
			for j := 0; j < 3; j++ {
				if err = c.Write(ctx, websocket.MessageText, payload); err != nil {
					extErrors <- err
					return
				}
			}
		}
		extErrors <- nil
	}()
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api", strings.NewReader(`{"action":"screenshot"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("status %d, body %s, err %v", resp.StatusCode, b, err)
		}
		var result response
		if err = json.Unmarshal(b, &result); err != nil || !result.OK {
			t.Fatalf("invalid response: %v", err)
		}
		if len(result.Result.(map[string]any)["data"].(string)) != 100000 {
			t.Fatal("large response truncated")
		}
	}
	if err := <-extErrors; err != nil {
		t.Fatal(err)
	}
}

func TestExtensionRejectsResponseAboveLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(ext))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	payload := []byte(strings.Repeat("x", browserbridge.MaxResponseBytes+1))
	_ = c.Write(ctx, websocket.MessageText, payload)
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("expected payload limit close, got %v", err)
	}
}
