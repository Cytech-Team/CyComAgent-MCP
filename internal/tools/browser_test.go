package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/browserbridge"
)

type browserTransport func(*http.Request) (*http.Response, error)

func (f browserTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBrowserMutationIsNotRetried(t *testing.T) {
	for _, action := range browserActions {
		if action == "tabs" || action == "state" || action == "screenshot" {
			continue
		}
		for _, status := range []int{0, 502, 503, 504} {
			t.Run(action+"/"+http.StatusText(status), func(t *testing.T) {
				calls := 0
				c := &http.Client{Transport: browserTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.URL.String() != browserbridge.APIURL {
						t.Fatalf("wrong endpoint: %s", r.URL)
					}
					if status == 0 {
						return nil, errors.New("response lost after dispatch")
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("uncertain result")), Header: make(http.Header)}, nil
				})}
				raw, _ := json.Marshal(map[string]string{"action": action})
				_, err := callBrowserBridgeWithRetry(context.Background(), c, raw)
				if err == nil || calls != 1 {
					t.Fatalf("err=%v calls=%d; mutation must be sent only once", err, calls)
				}
			})
		}
	}
}

func TestBrowserObservationCanRetry(t *testing.T) {
	for _, action := range []string{"tabs", "state"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			c := &http.Client{Transport: browserTransport(func(*http.Request) (*http.Response, error) {
				calls++
				status, body := http.StatusOK, `{"ok":true,"result":{}}`
				if calls == 1 {
					status, body = http.StatusServiceUnavailable, "extension reconnecting"
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			raw, _ := json.Marshal(map[string]string{"action": action})
			if _, err := callBrowserBridgeWithRetry(context.Background(), c, raw); err != nil || calls != 2 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestBrowserScreenshotDoesNotRetryAfterRetryableFailure(t *testing.T) {
	calls := 0
	c := &http.Client{Transport: browserTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("extension reconnecting")),
			Header:     make(http.Header),
		}, nil
	})}
	raw, _ := json.Marshal(map[string]string{"action": "screenshot"})
	if _, err := callBrowserBridgeWithRetry(context.Background(), c, raw); err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d; screenshot must not repeat because capture can focus a tab", err, calls)
	}
}

func TestBrowserClientPayloadBounds(t *testing.T) {
	calls := 0
	c := &http.Client{Transport: browserTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", browserbridge.MaxResponseBytes+1)))}, nil
	})}
	if _, retry, err := callBrowserBridge(context.Background(), c, json.RawMessage(strings.Repeat("x", browserbridge.MaxRequestBytes+1))); err == nil || retry || calls != 0 {
		t.Fatalf("oversized request: err=%v retry=%v calls=%d", err, retry, calls)
	}
	if _, retry, err := callBrowserBridge(context.Background(), c, json.RawMessage(`{"action":"screenshot"}`)); err == nil || retry || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response: err=%v retry=%v", err, retry)
	}
}

func TestBrowserOpenObservationRespectsActiveOption(t *testing.T) {
	tests := []struct {
		name        string
		request     string
		wantActions []string
	}{
		{
			name:        "inactive open preserves background tab",
			request:     `{"action":"open","url":"https://example.com","active":false}`,
			wantActions: []string{"open"},
		},
		{
			name:        "open defaults to active and observes",
			request:     `{"action":"open","url":"https://example.com"}`,
			wantActions: []string{"open", "screenshot"},
		},
		{
			name:        "explicit active open observes",
			request:     `{"action":"open","url":"https://example.com","active":true}`,
			wantActions: []string{"open", "screenshot"},
		},
		{
			name:        "explicit screenshot remains available",
			request:     `{"action":"screenshot","active":false}`,
			wantActions: []string{"screenshot"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var actions []string
			c := &http.Client{Transport: browserTransport(func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read bridge request: %v", err)
				}
				var request struct {
					Action string `json:"action"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Fatalf("decode bridge request: %v", err)
				}
				actions = append(actions, request.Action)
				response := `{"ok":true,"result":{"tabId":41}}`
				if request.Action == "screenshot" {
					response = `{"ok":true,"result":{"data":"ZmFrZQ==","mimeType":"image/png"}}`
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(response)),
					Header:     make(http.Header),
				}, nil
			})}

			result, err := browserUseWithClient(context.Background(), json.RawMessage(tt.request), c)
			if err != nil {
				t.Fatalf("browserUseWithClient: %v", err)
			}
			if len(actions) != len(tt.wantActions) {
				t.Fatalf("bridge actions = %v, want %v", actions, tt.wantActions)
			}
			for i := range tt.wantActions {
				if actions[i] != tt.wantActions[i] {
					t.Fatalf("bridge actions = %v, want %v", actions, tt.wantActions)
				}
			}
			if tt.name == "inactive open preserves background tab" {
				resultMap, ok := result.(map[string]any)
				if !ok || resultMap["ok"] != true {
					t.Fatalf("inactive open did not return the action result: %#v", result)
				}
			}
		})
	}
}

func TestBrowserExtensionFailureDoesNotReportActionCompleted(t *testing.T) {
	calls := 0
	c := &http.Client{Transport: browserTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error":"Explicit target not found"}`))}, nil
	})}
	_, err := callBrowserBridgeWithRetry(context.Background(), c, json.RawMessage(`{"action":"click","text":"Missing"}`))
	if err == nil || !strings.Contains(err.Error(), "Explicit target not found") || calls != 1 {
		t.Fatalf("extension failure: err=%v calls=%d", err, calls)
	}
}
