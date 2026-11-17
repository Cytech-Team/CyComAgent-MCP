//go:build linux

package remotejobs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateStateStoreUsesPinnedDirectoryAfterRename(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "metadata")
	store, err := openPrivateStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	moved := filepath.Join(base, "metadata-moved")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := store.write("entry.json", []byte(`{"ok":true}`), false); err != nil {
		t.Fatal(err)
	}
	got, err := store.read("entry.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("unexpected record: %s", got)
	}
	if _, err := os.Stat(filepath.Join(path, "entry.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old path unexpectedly resolved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, "entry.json")); err != nil {
		t.Fatalf("pinned directory did not receive write: %v", err)
	}
}

func TestPrivateStateStoreRejectsSymlinkRecordAndPrivateModeViolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata")
	store, err := openPrivateStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(path, "linked.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read("linked.json"); err == nil {
		t.Fatal("symlink record was accepted")
	}
	if err := store.write("loose.json", []byte(`{}`), false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(path, "loose.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read("loose.json"); err == nil {
		t.Fatal("group/world-readable record was accepted")
	}
}

func TestPrivateStateStoreCreateOnlyAndReplaceWrites(t *testing.T) {
	store, err := openPrivateStateStore(filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	if err := store.write("entry.json", []byte("one"), false); err != nil {
		t.Fatal(err)
	}
	if err := store.write("entry.json", []byte("two"), false); !os.IsExist(err) {
		t.Fatalf("second create-only write error = %v", err)
	}
	if err := store.write("entry.json", []byte("two"), true); err != nil {
		t.Fatal(err)
	}
	got, err := store.read("entry.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two" {
		t.Fatalf("record = %q", got)
	}
}

func TestPrivateStateStoreEntriesCanBeEnumeratedRepeatedly(t *testing.T) {
	store, err := openPrivateStateStore(filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	if err := store.write("one.json", []byte("one"), false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		entries, err := store.entries()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range entries {
			if entry.Name() == "one.json" {
				found = true
			}
		}
		if !found {
			t.Fatalf("enumeration %d did not return one.json", i)
		}
	}
}
