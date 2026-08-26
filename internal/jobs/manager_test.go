package jobs

import (
	"testing"
	"time"
)

func TestJobPersistsAndCompletes(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.Start("printf hello", "", "/bin/sh", nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := m.Get(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Status == "exited" {
			text, _, err := m.Tail(j.ID, 20, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if text != "hello" {
				t.Fatalf("log=%q", text)
			}
			m2, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m2.Get(j.ID); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not complete")
}
