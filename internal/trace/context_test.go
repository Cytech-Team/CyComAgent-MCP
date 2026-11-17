package trace

import (
	"context"
	"testing"
)

func TestTraceAndCallIDsAreIndependentAndPropagated(t *testing.T) {
	ctx := WithTraceID(context.Background(), "trace-1")
	ctx = WithCallID(ctx, "call-1")
	if got := TraceID(ctx); got != "trace-1" {
		t.Fatalf("trace id = %q", got)
	}
	if got := CallID(ctx); got != "call-1" {
		t.Fatalf("call id = %q", got)
	}
	if TraceID(context.Background()) != "" || CallID(context.Background()) != "" {
		t.Fatal("IDs leaked into an unrelated context")
	}
}

func TestNewIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 32 || seen[id] {
			t.Fatalf("invalid or duplicate ID %q", id)
		}
		seen[id] = true
	}
}
