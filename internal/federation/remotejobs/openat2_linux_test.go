//go:build linux

package remotejobs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenBeneathPinsOnlySafeDescendants(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(rootPath, "bin", "worker")
	if err := os.WriteFile(worker, []byte("worker"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(rootPath, "passwd-link")); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := OpenExecutableBeneath(root, "bin/worker")
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(worker, worker+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worker, []byte("replacement"), 0700); err != nil {
		t.Fatal(err)
	}
	after, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("opened descriptor did not remain bound to original inode")
	}
	_ = f.Close()
	for _, path := range []string{"../etc/passwd", "/etc/passwd", "passwd-link", "bin/../bin/worker", "bin//worker"} {
		if got, err := OpenBeneath(root, path, uint64(syscall.O_RDONLY), 0); err == nil {
			_ = got.Close()
			t.Fatalf("unsafe path %q was accepted", path)
		}
	}
}

func TestOpenDirectoryBeneathRejectsRegularFile(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if f, err := OpenDirectoryBeneath(root, "file"); err == nil {
		_ = f.Close()
		t.Fatal("regular file accepted as directory")
	}
}
