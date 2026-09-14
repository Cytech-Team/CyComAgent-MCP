package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Handler func(context.Context, json.RawMessage) (any, error)

// RichResult lets a generic tool return native MCP content (for example an
// image) while still exposing structured metadata. Most tools can simply
// return any JSON-marshalable value.
type RichResult struct {
	Structured any              `json:"structured"`
	Content    []map[string]any `json:"content"`
	IsError    bool             `json:"isError"`

	// ContentPresent and StructuredPresent distinguish an omitted provider
	// field from an explicit empty/null field when a rich result is forwarded.
	// Existing callers can leave these false and retain the registry's normal
	// success-result fallback behavior.
	ContentPresent    bool `json:"-"`
	StructuredPresent bool `json:"-"`

	// Extra carries provider-defined result fields that CyComAgent does not
	// interpret. Raw messages preserve their exact JSON representation,
	// including native blocks and large numeric values.
	Extra map[string]json.RawMessage `json:"-"`
}

// JSONRPCError is an upstream JSON-RPC protocol error. The bridge keeps the
// provider's code, message, and opaque data together so an outer MCP server
// can surface the same error without reducing it to a text-only result.
type JSONRPCError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *JSONRPCError) Error() string {
	if e == nil {
		return "JSON-RPC error"
	}
	return fmt.Sprintf("JSON-RPC error (%d): %s", e.Code, e.Message)
}

type Tool struct {
	Name         string         `json:"name"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
	Handler      Handler        `json:"-"`
	Source       string         `json:"-"`
}

type Interceptor interface {
	BeforeCall(context.Context, string, json.RawMessage) error
	AfterCall(context.Context, string, json.RawMessage, time.Duration, error)
}

type Registry struct {
	mu           sync.RWMutex
	tools        map[string]Tool
	interceptors []Interceptor
}

func New() *Registry { return &Registry{tools: make(map[string]Tool)} }

func (r *Registry) AddInterceptor(i Interceptor) {
	if i == nil {
		return
	}
	r.mu.Lock()
	r.interceptors = append(r.interceptors, i)
	r.mu.Unlock()
}

func (r *Registry) Add(t Tool) error {
	if t.Name == "" || t.Handler == nil {
		return fmt.Errorf("tool name and handler are required")
	}
	if t.InputSchema == nil {
		t.InputSchema = ObjectSchema(nil, nil)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

func (r *Registry) Upsert(t Tool) error {
	if t.Name == "" || t.Handler == nil {
		return fmt.Errorf("tool name and handler are required")
	}
	if t.InputSchema == nil {
		t.InputSchema = ObjectSchema(nil, nil)
	}
	r.mu.Lock()
	r.tools[t.Name] = t
	r.mu.Unlock()
	return nil
}

func (r *Registry) RemoveSource(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, t := range r.tools {
		if t.Source == source {
			delete(r.tools, name)
		}
	}
}

func (r *Registry) RemoveSourcePrefix(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, t := range r.tools {
		if strings.HasPrefix(t.Source, prefix) {
			delete(r.tools, name)
		}
	}
}

func (r *Registry) List() []Tool {
	r.mu.RLock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		t.Handler = nil
		out = append(out, t)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage) (value any, callErr error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	interceptors := append([]Interceptor{}, r.interceptors...)
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	for _, i := range interceptors {
		if err := i.BeforeCall(ctx, name, args); err != nil {
			for _, done := range interceptors {
				done.AfterCall(ctx, name, args, 0, err)
			}
			return nil, err
		}
	}
	start := time.Now()
	value, callErr = t.Handler(ctx, args)
	d := time.Since(start)
	for _, i := range interceptors {
		i.AfterCall(ctx, name, args, d, callErr)
	}
	return value, callErr
}

func (r *Registry) Exists(name string) bool {
	r.mu.RLock()
	_, ok := r.tools[name]
	r.mu.RUnlock()
	return ok
}

func ObjectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	s := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func String(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func Boolean(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func Integer(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func StringArray(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       map[string]any{"type": "string"},
	}
}
