//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAdmitWorkerReceivedExecutionBindsAllEvidence(t *testing.T) {
	data := []byte("#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	dirPath := filepath.Join(t.TempDir(), "stage")
	if err := os.Mkdir(dirPath, 0711); err != nil {
		t.Fatal(err)
	}
	stageDir, err := os.Open(dirPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stageDir.Close()
	brokerUID := uint32(os.Getuid())
	workerUID := brokerUID + 1
	stage, err := StageExecutableSnapshot(snapshot, stageDir, brokerUID, workerUID)
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
		Version: executableArtifactRegistryVersion, CallerNodeID: "node-a",
		RequestID: "request-a", RemoteJobID: "remote-a", LocalJobID: "local-a",
		PayloadHash: hex.EncodeToString(sum[:]), PolicyRevision: 7,
		Name: stage.Name, Digest: snapshot.Digest(), Size: info.Size(),
		BrokerUID: brokerUID, WorkerUID: workerUID, Device: uint64(st.Dev),
		Inode: st.Ino, CreatedAt: time.Now().UTC(),
	}
	capability, err := OpenVerifiedExecutableArtifact(stageDir, record)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	exe, err := capability.DuplicateReadOnlyDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := systemdStartRequest{
		Version: systemdStartProtocolVersion, CallerNodeID: record.CallerNodeID,
		RequestID: record.RequestID, RemoteJobID: record.RemoteJobID,
		LocalJobID: record.LocalJobID, StartAttemptID: "0123456789abcdef0123456789abcdef",
		UnitName:     mustExecutionUnit(t, record.LocalJobID, record.RequestID),
		ArtifactName: record.Name, ArtifactDigest: record.Digest,
		ArtifactDevice: record.Device, ArtifactInode: record.Inode,
		PolicyRevision: record.PolicyRevision, PayloadHash: record.PayloadHash,
	}
	wireRequest, err := workerBrokerRequestFromStart(request)
	if err != nil {
		t.Fatal(err)
	}
	peer := BrokerPeerIdentity{PID: 123, UID: 1001, StartTimeTicks: 55, PIDFD: 9, ExecutableSHA256: strings.Repeat("a", 64)}
	policy := BrokerPeerPolicy{AllowedUID: 1001, AllowedExecutableSHA256: strings.Repeat("a", 64)}
	admitted, err := AdmitWorkerReceivedExecution(peer, policy, request, wireRequest, record, exe, cwd)
	if err != nil {
		_ = exe.Close()
		_ = cwd.Close()
		t.Fatal(err)
	}
	got, err := admitted.Artifact()
	if err != nil || got.Inode != record.Inode {
		t.Fatalf("admitted artifact mismatch: %+v, %v", got, err)
	}
	if err := admitted.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := exe.Stat(); err == nil {
		t.Fatal("admission close did not release executable descriptor")
	}
	if _, err := cwd.Stat(); err == nil {
		t.Fatal("admission close did not release working-directory descriptor")
	}
	if _, err := admitted.Artifact(); err == nil {
		t.Fatal("closed handoff remained inspectable")
	}
}

func TestAdmitWorkerReceivedExecutionRejectsArtifactSubstitutionWithoutTakingOwnership(t *testing.T) {
	data := []byte("registered executable")
	sum := sha256.Sum256(data)
	stageDir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer stageDir.Close()
	uid := uint32(os.Getuid())
	stage, err := StageExecutableSnapshot(&ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}, stageDir, uid, uid+1)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	info, _ := stage.File.Stat()
	st := info.Sys().(*syscall.Stat_t)
	record := ExecutableArtifactRecord{
		Version: executableArtifactRegistryVersion, CallerNodeID: "node-a", RequestID: "request-a",
		RemoteJobID: "remote-a", LocalJobID: "local-a", PayloadHash: hex.EncodeToString(sum[:]),
		PolicyRevision: 2, Name: stage.Name, Digest: snapshotDigest(data), Size: info.Size(),
		BrokerUID: uid, WorkerUID: uid + 1, Device: uint64(st.Dev), Inode: st.Ino, CreatedAt: time.Now().UTC(),
	}
	exe, err := os.OpenFile(filepath.Join(t.TempDir(), "not-executable"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer exe.Close()
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cwd.Close()
	request := systemdStartRequest{Version: systemdStartProtocolVersion, CallerNodeID: "node-a", RequestID: "request-a", RemoteJobID: "remote-a", LocalJobID: "local-a", StartAttemptID: "0123456789abcdef0123456789abcdef", UnitName: mustExecutionUnit(t, "local-a", "request-a"), ArtifactName: stage.Name, ArtifactDigest: record.Digest, ArtifactDevice: record.Device, ArtifactInode: record.Inode, PolicyRevision: 2, PayloadHash: record.PayloadHash}
	wireRequest, err := workerBrokerRequestFromStart(request)
	if err != nil {
		t.Fatal(err)
	}
	peer := BrokerPeerIdentity{PID: 123, UID: 1001, StartTimeTicks: 55, PIDFD: 9, ExecutableSHA256: strings.Repeat("a", 64)}
	policy := BrokerPeerPolicy{AllowedUID: 1001, AllowedExecutableSHA256: strings.Repeat("a", 64)}
	if _, err := AdmitWorkerReceivedExecution(peer, policy, request, wireRequest, record, exe, cwd); err == nil {
		t.Fatal("mismatched received descriptor accepted")
	}
	if _, err := exe.Stat(); err != nil {
		t.Fatalf("failed admission unexpectedly took executable ownership: %v", err)
	}
	if _, err := cwd.Stat(); err != nil {
		t.Fatalf("failed admission unexpectedly took cwd ownership: %v", err)
	}
}

func mustExecutionUnit(t *testing.T, local, request string) string {
	t.Helper()
	unit, err := ExecutionUnitName(local, request)
	if err != nil {
		t.Fatal(err)
	}
	return unit
}

func snapshotDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestWorkerBackendPreparationIsFixedAndNonExecutable(t *testing.T) {
	admitted, record, request, cleanup := newAdmittedWorkerExecutionFixture(t)
	defer cleanup()
	defer admitted.Close()
	profile, err := BuildWorkerServiceProfile(record.WorkerUID, 2001, 2*time.Minute, "/run/cycomagent/root.sock")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := admitted.PrepareBackend(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.ExecutionDisabled() || prepared.Request() != request ||
		prepared.Artifact().Inode != record.Inode || prepared.RuntimeLimit() != 2*time.Minute {
		t.Fatalf("unexpected preparation: %+v", prepared)
	}
	if err := admitted.Revalidate(prepared); err != nil {
		t.Fatal(err)
	}
	profileCopy := prepared.WorkerProfile()
	profileCopy.Properties[0] = "ExecStart=/bin/sh"
	profileCopy.InaccessiblePaths[0] = "/"
	if err := admitted.Revalidate(prepared); err != nil {
		t.Fatalf("mutating an accessor copy changed the immutable preparation: %v", err)
	}
	if prepared.WorkerProfile().Properties[0] == "ExecStart=/bin/sh" {
		t.Fatal("profile accessor exposed mutable internal storage")
	}
}

func TestWorkerBackendPreparationRejectsProfileOverrides(t *testing.T) {
	admitted, record, _, cleanup := newAdmittedWorkerExecutionFixture(t)
	defer cleanup()
	defer admitted.Close()
	profile, err := BuildWorkerServiceProfile(record.WorkerUID, 2001, time.Minute, "/run/cycomagent/root.sock")
	if err != nil {
		t.Fatal(err)
	}
	profile.Properties = append(profile.Properties, "ExecStart=/bin/sh")
	if _, err := admitted.PrepareBackend(profile); err == nil {
		t.Fatal("caller-supplied execution property accepted")
	}
	profile, err = BuildWorkerServiceProfile(record.WorkerUID, 2001, time.Minute, "/run/cycomagent/root.sock")
	if err != nil {
		t.Fatal(err)
	}
	profile.User = "0"
	if _, err := admitted.PrepareBackend(profile); err == nil {
		t.Fatal("root worker profile accepted")
	}
}

func newAdmittedWorkerExecutionFixture(t *testing.T) (*WorkerReceivedExecution, ExecutableArtifactRecord, workerBrokerStartRequest, func()) {
	t.Helper()
	data := []byte("#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	stageDir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	stage, err := StageExecutableSnapshot(snapshot, stageDir, uid, uid+1)
	if err != nil {
		_ = stageDir.Close()
		t.Fatal(err)
	}
	info, err := stage.File.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	record := ExecutableArtifactRecord{
		Version: executableArtifactRegistryVersion, CallerNodeID: "node-a",
		RequestID: "request-a", RemoteJobID: "remote-a", LocalJobID: "local-a",
		PayloadHash: hex.EncodeToString(sum[:]), PolicyRevision: 7,
		Name: stage.Name, Digest: snapshot.Digest(), Size: info.Size(),
		BrokerUID: uid, WorkerUID: uid + 1, Device: uint64(st.Dev),
		Inode: st.Ino, CreatedAt: time.Now().UTC(),
	}
	capability, err := OpenVerifiedExecutableArtifact(stageDir, record)
	if err != nil {
		_ = stage.Close()
		_ = stageDir.Close()
		t.Fatal(err)
	}
	exe, err := capability.DuplicateReadOnlyDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := systemdStartRequest{
		Version: systemdStartProtocolVersion, CallerNodeID: record.CallerNodeID,
		RequestID: record.RequestID, RemoteJobID: record.RemoteJobID,
		LocalJobID: record.LocalJobID, StartAttemptID: "0123456789abcdef0123456789abcdef",
		UnitName:     mustExecutionUnit(t, record.LocalJobID, record.RequestID),
		ArtifactName: record.Name, ArtifactDigest: record.Digest,
		ArtifactDevice: record.Device, ArtifactInode: record.Inode,
		PolicyRevision: record.PolicyRevision, PayloadHash: record.PayloadHash,
	}
	wireRequest, err := workerBrokerRequestFromStart(request)
	if err != nil {
		t.Fatal(err)
	}
	peer := BrokerPeerIdentity{PID: 123, UID: 1001, StartTimeTicks: 55, PIDFD: 9, ExecutableSHA256: strings.Repeat("a", 64)}
	policy := BrokerPeerPolicy{AllowedUID: 1001, AllowedExecutableSHA256: strings.Repeat("a", 64)}
	admitted, err := AdmitWorkerReceivedExecution(peer, policy, request, wireRequest, record, exe, cwd)
	if err != nil {
		_ = exe.Close()
		_ = cwd.Close()
		_ = capability.Close()
		_ = stage.Close()
		_ = stageDir.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_ = capability.Close()
		_ = stage.Close()
		_ = stageDir.Close()
	}
	return admitted, record, wireRequest, cleanup
}
