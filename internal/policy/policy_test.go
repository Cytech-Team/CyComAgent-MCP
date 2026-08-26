package policy

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestFullModeAndRuntimeReload(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().Mode != "full" {
		t.Fatalf("default mode=%q", e.Snapshot().Mode)
	}
	if err := e.BeforeCall(context.Background(), "process_exec", json.RawMessage(`{"privileged":true,"timeout_seconds":10}`)); err != nil {
		t.Fatalf("full mode should allow: %v", err)
	}

	cfg := e.Snapshot()
	cfg.Mode = "readonly"
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(e.File(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := e.BeforeCall(context.Background(), "fs_write", json.RawMessage(`{"path":"x","content":"y"}`)); err == nil {
		t.Fatal("readonly should deny fs_write")
	}
	if err := e.BeforeCall(context.Background(), "fs_read", json.RawMessage(`{"path":"x"}`)); err != nil {
		t.Fatalf("readonly should allow fs_read: %v", err)
	}
}

func TestTimeoutLimit(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.Snapshot()
	cfg.MaxExecTimeoutSeconds = 5
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(e.File(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := e.BeforeCall(context.Background(), "process_exec", json.RawMessage(`{"command":"sleep 1","timeout_seconds":6}`)); err == nil {
		t.Fatal("expected timeout policy rejection")
	}
}

func TestSensitiveAndroidRequiresExplicitOptIn(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().AllowSensitiveAndroid {
		t.Fatal("sensitive Android capabilities must default to disabled")
	}
	if err := e.BeforeCall(context.Background(), "assistant_sms_send", json.RawMessage(`{"numbers":["123"],"text":"x"}`)); err == nil {
		t.Fatal("expected sensitive Android capability to be denied by default")
	}
	cfg := e.Snapshot()
	cfg.AllowSensitiveAndroid = true
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(e.File(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := e.BeforeCall(context.Background(), "assistant_sms_send", json.RawMessage(`{"numbers":["123"],"text":"x"}`)); err != nil {
		t.Fatalf("explicit opt-in should allow policy stage: %v", err)
	}
}
