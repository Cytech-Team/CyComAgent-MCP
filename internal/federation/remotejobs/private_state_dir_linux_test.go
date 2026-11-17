//go:build linux

package remotejobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateStateDirCreatesPrivateOwnedDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "federation", "registry")
	if err := ensurePrivateStateDir(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("mode=%#o, want 0700", info.Mode().Perm())
	}
}

func TestEnsurePrivateStateDirRejectsSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateStateDir(filepath.Join(link, "nested")); err == nil {
		t.Fatal("symlink ancestor was accepted")
	}
	finalLink := filepath.Join(root, "final-link")
	if err := os.Symlink(real, finalLink); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateStateDir(finalLink); err == nil {
		t.Fatal("final symlink was accepted")
	}
}
