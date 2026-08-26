package state

import "testing"

func TestListAndDelete(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("ns", "a", map[string]any{"v": 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("ns", "b", true); err != nil {
		t.Fatal(err)
	}
	keys, err := s.List("ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("keys=%v", keys)
	}
	if err := s.Delete("ns", "a"); err != nil {
		t.Fatal(err)
	}
	keys, err = s.List("ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "b" {
		t.Fatalf("keys=%v", keys)
	}
}
