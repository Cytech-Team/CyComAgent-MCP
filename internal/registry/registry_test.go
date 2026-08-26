package registry

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRegistryAddListCall(t *testing.T) {
	r := New()
	err := r.Add(Tool{Name: "echo", Description: "echo", InputSchema: ObjectSchema(nil, nil), Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		return v, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.List(); len(got) != 1 || got[0].Name != "echo" {
		t.Fatalf("unexpected list: %#v", got)
	}
	out, err := r.Call(context.Background(), "echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["x"].(float64) != 1 {
		t.Fatalf("unexpected output: %#v", out)
	}
}
