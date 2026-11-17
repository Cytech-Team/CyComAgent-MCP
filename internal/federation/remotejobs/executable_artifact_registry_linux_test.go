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

func TestExecutableArtifactRegistryBindsPreparedLedgerAndInode(t *testing.T) {
	state := t.TempDir()
	ledger, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	records, err := OpenExecutionRecordStore(state)
	if err != nil {
		t.Fatal(err)
	}
	reserved, _, err := ledger.Reserve("node-caller", "request-artifact", "payload")
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	intent.RequestID = reserved.RequestID
	intent.RemoteJobID = reserved.JobID
	intent.PayloadHash = reserved.PayloadHash
	if _, _, err := PrepareExecutionIntent(ledger, records, intent); err != nil {
		t.Fatal(err)
	}
	data := []byte("#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	stageDir := filepath.Join(state, "stage")
	if err := os.Mkdir(stageDir, 0711); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(stageDir)
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
	registry, err := OpenExecutableArtifactRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	got, created, err := registry.Register(stage, snapshot, ledger, records, intent.CallerNodeID, intent.RequestID, uid, uid+1)
	if err != nil || !created {
		t.Fatalf("register created=%v err=%v", created, err)
	}
	if got.Name != stage.Name || got.Digest != snapshot.Digest() || got.Inode == 0 {
		t.Fatalf("unexpected artifact record: %#v", got)
	}
	status, err := InspectArtifactConsistency(registry, ledger, records, intent.CallerNodeID, intent.RequestID)
	if err != nil || status != "consistent_prepared_artifact" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	reopened, err := OpenExecutableArtifactRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil || persisted.Inode != got.Inode {
		t.Fatalf("reopen=%#v err=%v", persisted, err)
	}
	if _, created, err := registry.Register(stage, snapshot, ledger, records, intent.CallerNodeID, intent.RequestID, uid, uid+1); err != nil || created {
		t.Fatalf("idempotent register created=%v err=%v", created, err)
	}
	artifactPath := filepath.Join(stageDir, stage.Name)
	if err := stage.Retain(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifactPath); err != nil {
		t.Fatalf("retained artifact was removed: %v", err)
	}
	if _, err := ledger.Transition(intent.CallerNodeID, intent.RequestID, "failed", "", "test terminal"); err != nil {
		t.Fatal(err)
	}
	status, err = InspectArtifactConsistency(registry, ledger, records, intent.CallerNodeID, intent.RequestID)
	if err != nil || status != "terminal_ledger_recorded_unit_absence_still_required" {
		t.Fatalf("terminal status=%q err=%v", status, err)
	}
}

func TestExecutableArtifactRegistryRejectsUnpreparedAndImmutableMismatch(t *testing.T) {
	state := t.TempDir()
	ledger, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	records, err := OpenExecutionRecordStore(state)
	if err != nil {
		t.Fatal(err)
	}
	reserved, _, err := ledger.Reserve("node-caller", "request-artifact", "payload")
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	intent.RequestID = reserved.RequestID
	intent.RemoteJobID = reserved.JobID
	intent.PayloadHash = reserved.PayloadHash
	if _, _, err := PrepareExecutionIntent(ledger, records, intent); err != nil {
		t.Fatal(err)
	}
	data := []byte("bin")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	stageDir := filepath.Join(state, "stage")
	if err := os.Mkdir(stageDir, 0711); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(stageDir)
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
	registry, err := OpenExecutableArtifactRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Register(stage, snapshot, ledger, records, intent.CallerNodeID, intent.RequestID, uid, uid+1); err != nil {
		t.Fatal(err)
	}
	changed := *snapshot
	changed.digest = hex.EncodeToString(make([]byte, 32))
	if _, _, err := registry.Register(stage, &changed, ledger, records, intent.CallerNodeID, intent.RequestID, uid, uid+1); err == nil {
		t.Fatal("accepted changed artifact digest")
	}
}

func TestVerifyRegisteredArtifactFileDetectsReplacementAndMissing(t *testing.T) {
	state := t.TempDir()
	data := []byte("artifact bytes")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	stageDir := filepath.Join(state, "stage")
	if err := os.Mkdir(stageDir, 0711); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	uid := uint32(os.Getuid())
	stage, err := StageExecutableSnapshot(snapshot, dir, uid, uid+1)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stage.File.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	record := ExecutableArtifactRecord{Name: stage.Name, Digest: snapshot.Digest(), Size: info.Size(), BrokerUID: uid, WorkerUID: uid + 1, Device: uint64(st.Dev), Inode: st.Ino}
	observation, err := VerifyRegisteredArtifactFile(dir, record)
	if err != nil || !observation.Present || !observation.IdentityMatches || !observation.SizeMatches || !observation.DigestMatches {
		t.Fatalf("observation=%#v err=%v", observation, err)
	}
	if err := stage.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(stageDir, record.Name)); err != nil {
		t.Fatal(err)
	}
	observation, err = VerifyRegisteredArtifactFile(dir, record)
	if err != nil || observation.Present {
		t.Fatalf("missing observation=%#v err=%v", observation, err)
	}
}
