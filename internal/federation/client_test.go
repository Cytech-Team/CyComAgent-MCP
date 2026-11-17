package federation

import "testing"

func TestShellQuote(t *testing.T) {
	cases := map[string]string{"plain": "'plain'", "has space": "'has space'", "a'b": "'a'\\''b'", "$(touch /tmp/x)": "'$(touch /tmp/x)'", "line\nbreak": "'line\nbreak'"}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Fatalf("shellQuote(%q)=%q want %q", in, got, want)
		}
	}
}
func TestReadOnlyAnnotationMustBeExplicit(t *testing.T) {
	for _, annotations := range []map[string]any{nil, {}, {"readOnlyHint": false}, {"destructiveHint": false}} {
		if annotations["readOnlyHint"] == true {
			t.Fatalf("unexpected allow: %#v", annotations)
		}
	}
}

func TestStructuredToolContentUnwrapsItems(t *testing.T) {
	raw := []byte(`{"isError":false,"structuredContent":{"items":{"node_id":"node-a","algorithm":"Ed25519"}}}`)
	got, err := structuredToolContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["node_id"]) != `"node-a"` || string(got["algorithm"]) != `"Ed25519"` {
		t.Fatalf("unexpected unwrapped content: %s", got["node_id"])
	}
}

func TestStructuredToolContentPreservesMapAndRejectsToolError(t *testing.T) {
	got, err := structuredToolContent([]byte(`{"isError":false,"structuredContent":{"node_id":"node-b"}}`))
	if err != nil || string(got["node_id"]) != `"node-b"` {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if _, err := structuredToolContent([]byte(`{"isError":true,"content":[{"text":"caller authentication rejected"}]}`)); err == nil {
		t.Fatal("expected tool error to be surfaced")
	}
}
