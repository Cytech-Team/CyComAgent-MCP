package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJobLockRejectsSymlink(t *testing.T) {
	stateDir := t.TempDir()
	m, err := New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	id := "lock-symlink"
	if err := m.writeJobAtomic(&Job{
		ID: id, Status: "exited", LogFile: filepath.Join(m.dir, id+".log"),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(m.dir, ".locks", id+".lock")
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := m.updateJob(id, func(j *Job) (bool, error) {
		j.Status = "failed"
		return true, nil
	}); err == nil {
		t.Fatal("expected symlinked lock file to be rejected")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "untouched" {
		t.Fatalf("lock sentinel was modified: %q", got)
	}
}

func TestJobRecordReadRejectsSymlink(t *testing.T) {
	stateDir := t.TempDir()
	m, err := New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel.json")
	if err := os.WriteFile(sentinel, []byte(`{"id":"record-symlink","status":"exited"}`), 0600); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(m.dir, "record-symlink.json")
	if err := os.Symlink(sentinel, record); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadSnapshot("record-symlink"); err == nil {
		t.Fatal("expected symlinked Job record to be rejected")
	}
}

func TestJobTailRejectsSymlinkAndTamperedLogPath(t *testing.T) {
	stateDir := t.TempDir()
	m, err := New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	id := "tail-symlink"
	logPath := filepath.Join(m.dir, id+".log")
	if err := os.WriteFile(logPath, []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.writeJobAtomic(&Job{
		ID: id, Status: "exited", LogFile: logPath,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, logPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Tail(id, 10, 1024); err == nil {
		t.Fatal("expected symlinked log to be rejected")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "private" {
		t.Fatalf("log sentinel was modified: %q", got)
	}
}
