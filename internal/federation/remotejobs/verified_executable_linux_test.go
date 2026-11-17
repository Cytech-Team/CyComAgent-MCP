//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestVerifiedExecutableArtifactRetainsExactDescriptorIdentity(t *testing.T) {
	data := []byte("#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	stagePath := filepath.Join(t.TempDir(), "stage")
	if err := os.Mkdir(stagePath, 0711); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	uid := uint32(os.Getuid())
	stage, err := StageExecutableSnapshot(snapshot, dir, uid, uid+1)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	info, err := stage.File.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	record := ExecutableArtifactRecord{
		Name: stage.Name, Digest: snapshot.Digest(), Size: info.Size(),
		BrokerUID: uid, WorkerUID: uid + 1, Device: uint64(st.Dev), Inode: st.Ino,
	}
	capability, err := OpenVerifiedExecutableArtifact(dir, record)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	duplicate, err := capability.DuplicateReadOnlyDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Close()
	if err := os.Rename(filepath.Join(stagePath, stage.Name), filepath.Join(stagePath, "renamed")); err != nil {
		t.Fatal(err)
	}
	if err := capability.Verify(); err != nil {
		t.Fatalf("pinned descriptor should remain bound to original inode after rename: %v", err)
	}
	if _, err := capability.Record(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifiedExecutableArtifactRejectsReplacementAndTamper(t *testing.T) {
	data := []byte("immutable executable bytes")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	stagePath := filepath.Join(t.TempDir(), "stage")
	if err := os.Mkdir(stagePath, 0711); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	uid := uint32(os.Getuid())
	stage, err := StageExecutableSnapshot(snapshot, dir, uid, uid+1)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	info, _ := stage.File.Stat()
	st := info.Sys().(*syscall.Stat_t)
	record := ExecutableArtifactRecord{Name: stage.Name, Digest: snapshot.Digest(), Size: info.Size(), BrokerUID: uid, WorkerUID: uid + 1, Device: uint64(st.Dev), Inode: st.Ino}
	if err := os.Remove(filepath.Join(stagePath, stage.Name)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagePath, stage.Name), data, 0555); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVerifiedExecutableArtifact(dir, record); err == nil {
		t.Fatal("replacement inode accepted")
	}
}
