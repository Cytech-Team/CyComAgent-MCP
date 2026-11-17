package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMissionControlPageAndAssets(t *testing.T) {
	cases := []struct {
		path, contentType, contains string
		handler                     http.HandlerFunc
	}{
		{"/mission-control", "text/html", "Mission Control", missionControlPage},
		{"/mission-control/assets/app.css", "text/css", ".metrics", missionControlAsset},
		{"/mission-control/assets/app.js", "text/javascript", "/mission-control/api/snapshot", missionControlAsset},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			tc.handler(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Header().Get("Content-Type"), tc.contentType) {
				t.Fatalf("content type %q", rr.Header().Get("Content-Type"))
			}
			if !strings.Contains(rr.Body.String(), tc.contains) {
				t.Fatalf("missing %q", tc.contains)
			}
		})
	}
}

func TestMissionControlMethodsAndUnknownAssets(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		handler      http.HandlerFunc
		want         int
	}{
		{http.MethodPost, "/mission-control", missionControlPage, http.StatusMethodNotAllowed},
		{http.MethodGet, "/mission-control/assets/unknown.js", missionControlAsset, http.StatusNotFound},
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		tc.handler(rr, req)
		if rr.Code != tc.want {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.path, rr.Code, tc.want)
		}
	}
}

func TestValidDashboardToken(t *testing.T) {
	for _, tc := range []struct {
		name, header, value string
		want                bool
	}{
		{"custom header", "X-CyCom-Token", "secret", true},
		{"bearer", "Authorization", "Bearer secret", true},
		{"wrong", "X-CyCom-Token", "nope", false},
		{"missing", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			if got := validDashboardToken(req, "secret"); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
