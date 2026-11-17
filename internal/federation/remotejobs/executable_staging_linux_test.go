//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestStageExecutableSnapshotCreatesSealedBrokerOwnedFileAndCleansByDirFD(t *testing.T) {
	data := []byte("#!/bin/sh\necho immutable\n")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: append([]byte(nil), data...), digest: hex.EncodeToString(sum[:])}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0711); err != nil {
		t.Fatal(err)
	}
	fd, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	uid := uint32(os.Getuid())
	worker := uid + 1
	stage, err := StageExecutableSnapshot(snapshot, fd, uid, worker)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, stage.Name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0555 || info.Size() != int64(len(data)) {
		t.Fatalf("unexpected staged file metadata: %v", info)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("staged file remains after cleanup: %v", err)
	}
}

func TestStageExecutableSnapshotRejectsUnsafeDirectoryAndSameIdentity(t *testing.T) {
	data := []byte("binary")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	dir := t.TempDir()
	fd, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	uid := uint32(os.Getuid())
	if _, err := StageExecutableSnapshot(snapshot, fd, uid, uid); err == nil {
		t.Fatal("accepted shared broker/worker identity")
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := StageExecutableSnapshot(snapshot, fd, uid, uid+1); err == nil {
		t.Fatal("accepted writable staging directory")
	}
}

func TestStagedExecutableCleanupUsesPinnedDirectoryAfterRename(t *testing.T) {
	data := []byte("#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	parent := t.TempDir()
	dir := filepath.Join(parent, "stage")
	if err := os.Mkdir(dir, 0711); err != nil {
		t.Fatal(err)
	}
	fd, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	stage, err := StageExecutableSnapshot(snapshot, fd, uint32(os.Getuid()), uint32(os.Getuid()+1))
	if err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(parent, "renamed")
	if err := os.Rename(dir, renamed); err != nil {
		t.Fatal(err)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(renamed, stage.Name)); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not unlink from pinned directory: %v", err)
	}
}

func TestFindAbandonedExecutableStagesReportsExpiredOwnedArtifactsWithoutDeleting(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "stage")
	if err := os.Mkdir(dir, 0711); err != nil {
		t.Fatal(err)
	}
	fd, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	uid := uint32(os.Getuid())
	oldName := "exec-0123456789abcdef0123456789abcdef"
	oldPath := filepath.Join(dir, oldName)
	if err := os.WriteFile(oldPath, []byte("old"), 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oldPath, 0555); err != nil {
		t.Fatal(err)
	}
	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	oldStat := oldInfo.Sys().(*syscall.Stat_t)
	oldCreated := time.Unix(oldStat.Ctim.Sec, oldStat.Ctim.Nsec)
	if err := os.WriteFile(filepath.Join(dir, "unrelated"), []byte("keep"), 0555); err != nil {
		t.Fatal(err)
	}
	candidates, err := findAbandonedExecutableStagesAt(fd, uid, 26*time.Hour, oldCreated.Add(27*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != oldName {
		t.Fatalf("unexpected recovery candidates: %#v", candidates)
	}
	if _, err := os.Lstat(oldPath); err != nil {
		t.Fatalf("inspection unexpectedly deleted artifact: %v", err)
	}
	for _, keep := range []string{filepath.Join(dir, "unrelated")} {
		if _, err := os.Lstat(keep); err != nil {
			t.Fatalf("unexpectedly removed %s: %v", keep, err)
		}
	}
}

func TestFindAbandonedExecutableStagesRejectsUnsafeAge(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0711); err != nil {
		t.Fatal(err)
	}
	fd, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	if _, err := FindAbandonedExecutableStages(fd, uint32(os.Getuid()), 25*time.Hour); err == nil {
		t.Fatal("accepted cleanup age shorter than safety floor")
	}
}
