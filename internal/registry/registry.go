package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

const reasonDescription = "Required: concise user-visible reason why ChatGPT is invoking this tool now"

var (
	errInvalidRequired   = errors.New("invalid tool input schema: required must be an array of strings")
	errDuplicateRequired = errors.New("invalid tool input schema: required entries must be unique")
)

// cloneSchemaValue copies the JSON-shaped values used by input schemas. The
// registry adds a cross-cutting property at registration time, so it should
// not mutate a schema map (or its properties/required slices) owned by a
// caller, such as a plugin manifest.
func cloneSchemaValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, nested := range value {
			out[key] = cloneSchemaValue(nested)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, nested := range value {
			out[i] = cloneSchemaValue(nested)
		}
		return out
	case []string:
		return append([]string(nil), value...)
	default:
		return value
	}
}

func cloneSchema(s map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	return cloneSchemaValue(s).(map[string]any)
}

func appendReasonRequired(required any) (any, error) {
	switch values := required.(type) {
	case nil:
		return nil, errInvalidRequired
	case []string:
		out := append([]string(nil), values...)
		seen := make(map[string]struct{}, len(out))
		for _, value := range out {
			if _, ok := seen[value]; ok {
				return nil, errDuplicateRequired
			}
			seen[value] = struct{}{}
		}
		if _, ok := seen["reason"]; ok {
			return out, nil
		}
		return append(out, "reason"), nil
	case []any:
		out := append([]any(nil), values...)
		seen := make(map[string]struct{}, len(out))
		for _, value := range out {
			text, ok := value.(string)
			if !ok {
				return nil, errInvalidRequired
			}
			if _, ok := seen[text]; ok {
				return nil, errDuplicateRequired
			}
			seen[text] = struct{}{}
		}
		if _, ok := seen["reason"]; ok {
			return out, nil
		}
		return append(out, "reason"), nil
	default:
		return nil, errInvalidRequired
	}
}

func requireReasonSchema(s map[string]any) (map[string]any, error) {
	out := cloneSchema(s)
	if out == nil {
		out = ObjectSchema(nil, nil)
	}
	props, _ := out["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props["reason"] = String(reasonDescription)
	out["properties"] = props
	required, exists := out["required"]
	if !exists {
		out["required"] = []string{"reason"}
		return out, nil
	}
	normalized, err := appendReasonRequired(required)
	if err != nil {
		return nil, err
	}
	out["required"] = normalized
	return out, nil
}

func stripReason(args json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil {
		return args
	}
	delete(obj, "reason")
	b, err := json.Marshal(obj)
	if err != nil {
		return args
	}
	return b
}

func validateReason(args json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	tok, err := dec.Token()
	if err != nil {
		return errors.New("tool reason is required")
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return errors.New("tool reason is required")
	}

	var reason json.RawMessage
	found := false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return errors.New("tool reason is required")
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("tool reason is required")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return errors.New("tool reason is required")
		}
		if key == "reason" {
			if found {
				return errors.New("tool reason is duplicated")
			}
			found = true
			reason = value
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return errors.New("tool reason is required")
	}
	// Reject trailing JSON values. json.Unmarshal used by the previous path
	// rejected them too, and accepting them would make duplicate detection
	// dependent on decoder behavior.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("tool reason is required")
	}
	if !found {
		return errors.New("tool reason is required")
	}
	var text string
	if err := json.Unmarshal(reason, &text); err != nil || strings.TrimSpace(text) == "" {
		return errors.New("tool reason is required")
	}
	return nil
}

func (r *Registry) Add(t Tool) error {
	if t.Name == "" || t.Handler == nil {
		return fmt.Errorf("tool name and handler are required")
	}
	if t.InputSchema == nil {
		t.InputSchema = ObjectSchema(nil, nil)
	}
	schema, err := requireReasonSchema(t.InputSchema)
	if err != nil {
		return err
	}
	t.InputSchema = schema
	if t.OutputSchema != nil {
		t.OutputSchema = cloneSchema(t.OutputSchema)
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
	schema, err := requireReasonSchema(t.InputSchema)
	if err != nil {
		return err
	}
	t.InputSchema = schema
	if t.OutputSchema != nil {
		t.OutputSchema = cloneSchema(t.OutputSchema)
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
	if err := validateReason(args); err != nil {
		return nil, err
	}
	for _, i := range interceptors {
		if err := i.BeforeCall(ctx, name, args); err != nil {
			for _, done := range interceptors {
				done.AfterCall(ctx, name, args, 0, err)
			}
			return nil, err
		}
	}
	handlerArgs := stripReason(args)
	start := time.Now()
	value, callErr = t.Handler(ctx, handlerArgs)
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
