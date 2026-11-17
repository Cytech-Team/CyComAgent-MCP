// Package trace carries correlation identifiers across a single runtime
// invocation without coupling the registry to a particular observability sink.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
	"time"
)

type contextKey uint8

const (
	traceIDKey contextKey = iota + 1
	callIDKey
	sessionIDKey
)

var fallbackCounter atomic.Uint64

// NewID returns a random, opaque identifier suitable for logs and correlation.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	// crypto/rand failures are exceptionally rare; retain uniqueness for
	// observability rather than making tool execution unavailable.
	return hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000"))) + "-" + itoa(fallbackCounter.Add(1))
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func WithTraceID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		id = NewID()
	}
	return context.WithValue(ctx, traceIDKey, id)
}

func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(traceIDKey).(string)
	return id
}

func WithCallID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		id = NewID()
	}
	return context.WithValue(ctx, callIDKey, id)
}

func CallID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(callIDKey).(string)
	return id
}

// WithSessionID associates a runtime Agent session with this invocation.
func WithSessionID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionIDKey, id)
}
func SessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(sessionIDKey).(string)
	return id
}
