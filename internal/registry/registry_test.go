package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestRegistryAddListCall(t *testing.T) {
	var handlerArgs map[string]any
	r := New()
	err := r.Add(Tool{Name: "echo", Description: "echo", InputSchema: ObjectSchema(nil, nil), Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		if err := json.Unmarshal(raw, &handlerArgs); err != nil {
			return nil, err
		}
		return handlerArgs, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.List(); len(got) != 1 || got[0].Name != "echo" {
		t.Fatalf("unexpected list: %#v", got)
	}
	out, err := r.Call(context.Background(), "echo", json.RawMessage(`{"x":1,"reason":"test echo"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["x"].(float64) != 1 {
		t.Fatalf("unexpected output: %#v", out)
	}
	if _, ok := m["reason"]; ok {
		t.Fatalf("reason leaked to handler: %#v", m)
	}
}

func TestRegistryReasonSchemaPreservesRequiredFields(t *testing.T) {
	nativeRequired := []string{"command"}
	nativeProperties := map[string]any{"command": String("command")}
	nativeSchema := ObjectSchema(nativeProperties, nativeRequired)
	decodedSchema := map[string]any{}
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`), &decodedSchema); err != nil {
		t.Fatal(err)
	}

	r := New()
	for _, tool := range []Tool{
		{Name: "native", InputSchema: nativeSchema, Handler: func(context.Context, json.RawMessage) (any, error) { return nil, nil }},
		{Name: "decoded", InputSchema: decodedSchema, Handler: func(context.Context, json.RawMessage) (any, error) { return nil, nil }},
	} {
		if err := r.Add(tool); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok := nativeProperties["reason"]; ok {
		t.Fatal("Add mutated the caller's properties map")
	}
	if len(nativeRequired) != 1 {
		t.Fatalf("Add mutated the caller's required slice: %#v", nativeRequired)
	}

	tools := r.List()
	if len(tools) != 2 {
		t.Fatalf("tools=%#v", tools)
	}
	for _, tool := range tools {
		properties, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties=%T", tool.Name, tool.InputSchema["properties"])
		}
		reason, ok := properties["reason"].(map[string]any)
		if !ok || reason["type"] != "string" {
			t.Fatalf("%s reason schema=%#v", tool.Name, properties["reason"])
		}
		required := requiredNames(t, tool.InputSchema["required"])
		if !containsName(required, "reason") {
			t.Fatalf("%s required=%#v", tool.Name, required)
		}
		if tool.Name == "native" && !containsName(required, "command") {
			t.Fatalf("native required field lost: %#v", required)
		}
		if tool.Name == "decoded" && !containsName(required, "path") {
			t.Fatalf("decoded required field lost: %#v", required)
		}
	}

	// Upsert must apply the same normalization, including to a decoded JSON
	// schema, without duplicating an existing reason requirement.
	if err := r.Upsert(Tool{Name: "decoded", InputSchema: decodedSchema, Handler: func(context.Context, json.RawMessage) (any, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	var decodedRequired []any
	for _, tool := range r.List() {
		if tool.Name == "decoded" {
			var ok bool
			decodedRequired, ok = tool.InputSchema["required"].([]any)
			if !ok {
				t.Fatalf("decoded required type=%T", tool.InputSchema["required"])
			}
		}
	}
	if len(decodedRequired) != 2 || decodedRequired[0] != "path" || decodedRequired[1] != "reason" {
		t.Fatalf("decoded required after Upsert=%#v", decodedRequired)
	}
}

func TestRegistryClonesOutputSchema(t *testing.T) {
	outputProperties := map[string]any{
		"backend": map[string]any{"type": "string"},
	}
	outputRequired := []string{"backend"}
	outputSchema := map[string]any{
		"type":       "object",
		"properties": outputProperties,
		"required":   outputRequired,
	}

	r := New()
	if err := r.Add(Tool{
		Name:         "output",
		InputSchema:  ObjectSchema(nil, nil),
		OutputSchema: outputSchema,
		Handler:      func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}

	// Mutating the caller-owned schema after registration must not alter the
	// opaque schema retained by the registry.
	outputProperties["mutated"] = map[string]any{"type": "boolean"}
	outputRequired[0] = "mutated"
	tool := r.List()[0]
	properties, ok := tool.OutputSchema["properties"].(map[string]any)
	if !ok || properties["backend"] == nil || properties["mutated"] != nil {
		t.Fatalf("output properties were not cloned: %#v", tool.OutputSchema["properties"])
	}
	required, ok := tool.OutputSchema["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "backend" {
		t.Fatalf("output required was not cloned: %#v", tool.OutputSchema["required"])
	}

	// Upsert applies the same cloning behavior and keeps the output schema
	// opaque; no input-reason normalization should be applied to it.
	upsertOutput := map[string]any{
		"type":       "object",
		"properties": map[string]any{"ready": map[string]any{"type": "boolean"}},
	}
	if err := r.Upsert(Tool{
		Name:         "output",
		InputSchema:  ObjectSchema(nil, nil),
		OutputSchema: upsertOutput,
		Handler:      func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	upsertOutput["properties"].(map[string]any)["changed"] = map[string]any{"type": "string"}
	tool = r.List()[0]
	properties, ok = tool.OutputSchema["properties"].(map[string]any)
	if !ok || properties["ready"] == nil || properties["changed"] != nil {
		t.Fatalf("Upsert output schema was not cloned: %#v", tool.OutputSchema)
	}
}

func TestRegistryRejectsMalformedRequiredSchema(t *testing.T) {
	for name, required := range map[string]any{
		"scalar":  "command",
		"null":    nil,
		"member":  []any{"command", 1},
		"boolean": []any{"command", false},
	} {
		r := New()
		err := r.Add(Tool{
			Name:        name,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "required": required},
			Handler:     func(context.Context, json.RawMessage) (any, error) { return nil, nil },
		})
		if err == nil || err.Error() != "invalid tool input schema: required must be an array of strings" {
			t.Errorf("%s error=%v", name, err)
		}
		if r.Exists(name) {
			t.Errorf("%s was registered despite malformed required schema", name)
		}
		upsertName := name + "-upsert"
		if err := r.Upsert(Tool{
			Name:        upsertName,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "required": required},
			Handler:     func(context.Context, json.RawMessage) (any, error) { return nil, nil },
		}); err == nil || err.Error() != "invalid tool input schema: required must be an array of strings" {
			t.Errorf("%s Upsert error=%v", name, err)
		}
		if r.Exists(upsertName) {
			t.Errorf("%s was Upserted despite malformed required schema", name)
		}
	}
}

func TestRegistryRejectsDuplicateRequiredNames(t *testing.T) {
	duplicateError := "invalid tool input schema: required entries must be unique"
	cases := map[string]any{
		"native":         []string{"command", "command"},
		"native-reason":  []string{"reason", "reason"},
		"decoded":        []any{"path", "path"},
		"decoded-reason": []any{"reason", "reason"},
	}
	for name, required := range cases {
		t.Run(name, func(t *testing.T) {
			newTool := func(toolName string) Tool {
				return Tool{
					Name:        toolName,
					InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "required": required},
					Handler:     func(context.Context, json.RawMessage) (any, error) { return nil, nil },
				}
			}

			r := New()
			if err := r.Add(newTool(name)); err == nil || err.Error() != duplicateError {
				t.Fatalf("Add error=%v", err)
			}
			if r.Exists(name) {
				t.Fatal("duplicate required schema was registered by Add")
			}

			upsertName := name + "-upsert"
			if err := r.Upsert(newTool(upsertName)); err == nil || err.Error() != duplicateError {
				t.Fatalf("Upsert error=%v", err)
			}
			if r.Exists(upsertName) {
				t.Fatal("duplicate required schema was registered by Upsert")
			}
		})
	}
}

type recordingInterceptor struct {
	before json.RawMessage
	after  json.RawMessage
}

func (i *recordingInterceptor) BeforeCall(_ context.Context, _ string, args json.RawMessage) error {
	i.before = append(json.RawMessage(nil), args...)
	return nil
}

func (i *recordingInterceptor) AfterCall(_ context.Context, _ string, args json.RawMessage, _ time.Duration, _ error) {
	i.after = append(json.RawMessage(nil), args...)
}

func TestRegistryReasonValidationAndDispatch(t *testing.T) {
	var handlerCalls int
	var gotHandler map[string]any
	r := New()
	interceptor := &recordingInterceptor{}
	r.AddInterceptor(interceptor)
	if err := r.Add(Tool{Name: "run", InputSchema: ObjectSchema(map[string]any{"command": String("command")}, []string{"command"}), Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		handlerCalls++
		if err := json.Unmarshal(raw, &gotHandler); err != nil {
			return nil, err
		}
		return gotHandler, nil
	}}); err != nil {
		t.Fatal(err)
	}

	for _, args := range []string{
		`{"command":"echo"}`,
		`{"command":"echo","reason":"   "}`,
		`{"command":"echo","reason":42}`,
		`{"command":"echo","reason":null}`,
	} {
		if _, err := r.Call(context.Background(), "run", json.RawMessage(args)); err == nil || err.Error() != "tool reason is required" {
			t.Errorf("args=%s error=%v, want exact reason error", args, err)
		}
	}
	if handlerCalls != 0 {
		t.Fatalf("handler called for invalid reasons: %d", handlerCalls)
	}

	out, err := r.Call(context.Background(), "run", json.RawMessage(`{"command":"echo","reason":"inspect","nested":{"reason":"keep"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if handlerCalls != 1 {
		t.Fatalf("handler calls=%d", handlerCalls)
	}
	if gotHandler["command"] != "echo" {
		t.Fatalf("handler args=%#v", gotHandler)
	}
	if _, ok := gotHandler["reason"]; ok {
		t.Fatalf("top-level reason leaked to handler: %#v", gotHandler)
	}
	nested, ok := gotHandler["nested"].(map[string]any)
	if !ok || nested["reason"] != "keep" {
		t.Fatalf("nested reason changed: %#v", gotHandler)
	}
	if outMap, ok := out.(map[string]any); !ok || outMap["command"] != "echo" {
		t.Fatalf("output=%#v", out)
	}

	for label, raw := range map[string]json.RawMessage{"before": interceptor.before, "after": interceptor.after} {
		var args map[string]any
		if err := json.Unmarshal(raw, &args); err != nil {
			t.Fatalf("%s interceptor args=%s: %v", label, raw, err)
		}
		if args["reason"] != "inspect" {
			t.Fatalf("%s interceptor did not receive reason: %#v", label, args)
		}
	}
}

func TestRegistryRejectsDuplicateTopLevelReason(t *testing.T) {
	var handlerCalls int
	var interceptorCalls int
	r := New()
	r.AddInterceptor(&countingInterceptor{calls: &interceptorCalls})
	if err := r.Add(Tool{Name: "echo", Handler: func(context.Context, json.RawMessage) (any, error) {
		handlerCalls++
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}

	_, err := r.Call(context.Background(), "echo", json.RawMessage(`{"reason":"first","reason":"second"}`))
	if err == nil || err.Error() != "tool reason is duplicated" {
		t.Fatalf("duplicate reason error=%v", err)
	}
	if handlerCalls != 0 || interceptorCalls != 0 {
		t.Fatalf("duplicate reason reached consumers: handler=%d interceptors=%d", handlerCalls, interceptorCalls)
	}

	// Duplicate-looking keys nested under another argument are outside the
	// cross-cutting contract and remain part of the tool payload.
	_, err = r.Call(context.Background(), "echo", json.RawMessage(`{"reason":"outer","nested":{"reason":"inner"}}`))
	if err != nil {
		t.Fatalf("nested reason rejected: %v", err)
	}
}

type countingInterceptor struct{ calls *int }

func (i *countingInterceptor) BeforeCall(context.Context, string, json.RawMessage) error {
	*i.calls++
	return nil
}

func (i *countingInterceptor) AfterCall(context.Context, string, json.RawMessage, time.Duration, error) {
}

func requiredNames(t *testing.T, value any) []string {
	t.Helper()
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			name, ok := value.(string)
			if !ok {
				t.Fatalf("required member=%T (%v)", value, value)
			}
			out = append(out, name)
		}
		return out
	default:
		t.Fatalf("required type=%T", value)
		return nil
	}
}

func containsName(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRegistryReasonErrorIsStable(t *testing.T) {
	r := New()
	if err := r.Add(Tool{Name: "noop", Handler: func(context.Context, json.RawMessage) (any, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{nil, []byte(`null`), []byte(`[]`), []byte(`{"reason":true}`)} {
		_, err := r.Call(context.Background(), "noop", raw)
		if err == nil || fmt.Sprint(err) != "tool reason is required" {
			t.Errorf("raw=%s error=%v", raw, err)
		}
	}
}
