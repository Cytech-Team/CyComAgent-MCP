package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPatchAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(p, []byte("hello world"), 0640); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"path": p, "edits": []map[string]any{{"old": "world", "new": "cycom"}}})
	if _, err := fsPatch(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "hello cycom" {
		t.Fatalf("got %q", b)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0640 {
		t.Fatalf("mode=%o", st.Mode().Perm())
	}
}
