package audit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/trace"
)

func TestLoggerTailRecordsSuccessAndError(t *testing.T) {
	l, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l.AfterCall(context.Background(), "alpha", json.RawMessage(`{"x":1}`), 12*time.Millisecond, nil)
	l.AfterCall(context.Background(), "beta", json.RawMessage(`{"secret":"not-stored"}`), 7*time.Millisecond, errors.New("boom"))
	events, err := l.Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].Tool != "beta" || events[0].OK {
		t.Fatalf("unexpected newest event: %+v", events[0])
	}
	if events[0].ArgsSHA256 == "" {
		t.Fatal("args hash missing")
	}
	if events[0].Error != "boom" {
		t.Fatalf("unexpected error %q", events[0].Error)
	}
}

func TestConcurrentIdenticalCallsKeepDistinctActiveIDs(t *testing.T) {
	l, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"command":"same","reason":"parallel test"}`)
	ctxA := trace.WithCallID(trace.WithTraceID(context.Background(), "trace-a"), "call-a")
	ctxB := trace.WithCallID(trace.WithTraceID(context.Background(), "trace-b"), "call-b")
	if err := l.BeforeCall(ctxA, "process_exec", args); err != nil {
		t.Fatal(err)
	}
	if err := l.BeforeCall(ctxB, "process_exec", args); err != nil {
		t.Fatal(err)
	}
	if got := len(l.Active()); got != 2 {
		t.Fatalf("active calls = %d, want 2", got)
	}
	l.AfterCall(ctxA, "process_exec", args, time.Millisecond, nil)
	active := l.Active()
	if len(active) != 1 || active[0].ID != "call-b" || active[0].TraceID != "trace-b" {
		t.Fatalf("remaining active call = %#v", active)
	}
	l.AfterCall(ctxB, "process_exec", args, time.Millisecond, nil)
	events, err := l.Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	seen := map[string]bool{}
	for _, event := range events {
		if event.CallID == "" || event.TraceID == "" {
			t.Fatalf("missing correlation IDs: %+v", event)
		}
		seen[event.CallID] = true
	}
	if !seen["call-a"] || !seen["call-b"] {
		t.Fatalf("call IDs not retained: %#v", seen)
	}
}

func TestAuditDoesNotPersistRawArguments(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"api_key":"sensitive-test-value","reason":"redaction test"}`)
	ctx := trace.WithCallID(trace.WithTraceID(context.Background(), "trace-safe"), "call-safe")
	l.AfterCall(ctx, "test_tool", args, time.Millisecond, nil)
	data, err := os.ReadFile(filepath.Join(dir, "audit", time.Now().UTC().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sensitive-test-value") || strings.Contains(string(data), "api_key") {
		t.Fatal("raw arguments leaked into audit log")
	}
}

func TestTailReadsLegacyEventsWithoutCorrelationFields(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"time":"2026-01-02T03:04:05Z","tool":"legacy_tool","args_sha256":"abc","duration_ms":4,"ok":true}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-01-02.jsonl"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := TailDir(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Tool != "legacy_tool" {
		t.Fatalf("legacy event not loaded: %#v", events)
	}
	if events[0].TraceID != "" || events[0].CallID != "" {
		t.Fatalf("legacy event unexpectedly has correlation IDs: %+v", events[0])
	}
}
