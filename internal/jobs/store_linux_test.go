//go:build linux && !android

package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJobStoreRemainsPinnedAfterJobsPathIsReplaced(t *testing.T) {
	stateDir := t.TempDir()
	m, err := New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	pinnedPath := m.dir + ".pinned"
	if err := os.Rename(m.dir, pinnedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(m.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "pinned-directory"
	job := &Job{ID: id, Status: "exited", LogFile: filepath.Join(m.dir, id+".log"), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := m.writeJobAtomic(job); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("record escaped pinned directory into replacement path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pinnedPath, id+".json")); err != nil {
		t.Fatalf("record missing from pinned directory: %v", err)
	}
	if _, err := m.ReadSnapshot(id); err != nil {
		t.Fatalf("read through pinned directory failed: %v", err)
	}
}

func TestJobStoreRejectsLoosePermissionRecord(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "loose-record"
	path := filepath.Join(m.dir, id+".json")
	data := []byte(`{"id":"loose-record","status":"exited"}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadSnapshot(id); err == nil {
		t.Fatal("expected group/world-readable record to be rejected")
	}
}
