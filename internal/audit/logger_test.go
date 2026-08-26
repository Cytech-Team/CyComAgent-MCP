package audit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
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
