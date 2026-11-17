package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInspectorV2Page(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/inspector", nil)
	rr := httptest.NewRecorder()
	inspectorV2Page(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatal("HTML Content-Type missing")
	}
	for _, needle := range []string{"Live Inspector", "id=\"event-rows\"", "id=\"pause-btn\"", "id=\"tool-filter\"", "id=\"clear-filters\"", "/inspector/assets/inspector.js"} {
		if !strings.Contains(rr.Body.String(), needle) {
			t.Errorf("UI missing %q", needle)
		}
	}
}

func TestInspectorV2AssetRoutes(t *testing.T) {
	tests := []struct {
		path        string
		expected    int
		contentType string
		contains    string
	}{
		{"/inspector/assets/inspector.css", 200, "text/css", ".sidebar"},
		{"/inspector/assets/inspector.js", 200, "text/javascript", "function refresh"},
		{"/inspector/assets/missing.js", 404, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rr := httptest.NewRecorder()
			inspectorV2Asset(rr, req)
			if rr.Code != tt.expected {
				t.Fatalf("status=%d want=%d", rr.Code, tt.expected)
			}
			if tt.expected == 200 {
				if !strings.Contains(rr.Header().Get("Content-Type"), tt.contentType) {
					t.Errorf("content type=%q", rr.Header().Get("Content-Type"))
				}
				if !strings.Contains(rr.Body.String(), tt.contains) {
					t.Errorf("body missing %q", tt.contains)
				}
				if tt.path == "/inspector/assets/inspector.js" {
					for _, needle := range []string{"TRACE ID", "CALL ID", "Trace ID", "Call ID"} {
						if !strings.Contains(rr.Body.String(), needle) {
							t.Errorf("Inspector JS missing %q", needle)
						}
					}
				}
			}
		})
	}
}

func TestInspectorV2RejectsMutatingMethods(t *testing.T) {
	rr := httptest.NewRecorder()
	inspectorV2Page(rr, httptest.NewRequest(http.MethodPost, "/inspector", strings.NewReader("{}")))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", rr.Code)
	}
}
