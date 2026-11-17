package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func TestRemoteJobCallerToolsRequireExplicitConfirmation(t *testing.T) {
	r := registry.New()
	// A nil target manager ensures any accidental transport attempt fails. The
	// confirmation gate must reject before touching the Federation client.
	registerRemoteJobCaller(r, federation.New(nil, nil))
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"fleet_remote_job_submit", map[string]any{"target": "peer", "request_id": "req-1", "payload": "{}", "confirm": false, "reason": "test"}, "confirm=true is required to reserve"},
		{"fleet_remote_job_status", map[string]any{"target": "peer", "request_id": "req-1", "payload": "{}", "confirm": false, "reason": "test"}, "confirm=true is required to query"},
		{"fleet_remote_job_reconcile", map[string]any{"target": "peer", "request_id": "req-1", "payload": "{}", "confirm": false, "reason": "test"}, "confirm=true is required to inspect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.Call(context.Background(), tc.name, raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got err=%v want substring %q", err, tc.want)
			}
		})
	}
}
