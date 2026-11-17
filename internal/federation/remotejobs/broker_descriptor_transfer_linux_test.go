//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func unixSocketPairForDescriptorTest(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "broker.sock")
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.AcceptUnix()
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client, server
}

func TestBrokerDescriptorTransferPreservesObjectsAndSetsCLOEXEC(t *testing.T) {
	client, server := unixSocketPairForDescriptorTest(t)
	exe, err := os.Open("/bin/sh")
	if err != nil {
		t.Skipf("/bin/sh unavailable: %v", err)
	}
	defer exe.Close()
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cwd.Close()
	if err := SendBrokerDescriptors(client, exe, cwd); err != nil {
		t.Fatal(err)
	}
	receivedExe, receivedCWD, err := ReceiveBrokerDescriptors(server)
	if err != nil {
		t.Fatal(err)
	}
	defer receivedExe.Close()
	defer receivedCWD.Close()
	wantExe, _ := exe.Stat()
	gotExe, _ := receivedExe.Stat()
	if !os.SameFile(wantExe, gotExe) {
		t.Fatal("received executable does not identify sent object")
	}
	wantCWD, _ := cwd.Stat()
	gotCWD, _ := receivedCWD.Stat()
	if !os.SameFile(wantCWD, gotCWD) {
		t.Fatal("received working directory does not identify sent object")
	}
	for _, f := range []*os.File{receivedExe, receivedCWD} {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), uintptr(syscall.F_GETFD), 0)
		if errno != 0 || int(flags)&syscall.FD_CLOEXEC == 0 {
			t.Fatalf("descriptor lacks CLOEXEC: flags=%x errno=%v", flags, errno)
		}
	}
	// SCM_RIGHTS duplicates descriptors; sender retains ownership.
	if _, err := exe.Stat(); err != nil {
		t.Fatalf("sender descriptor was consumed: %v", err)
	}
	if _, err := cwd.Stat(); err != nil {
		t.Fatalf("sender descriptor was consumed: %v", err)
	}
}

func TestBrokerDescriptorTransferRejectsInvalidFrames(t *testing.T) {
	client, server := unixSocketPairForDescriptorTest(t)
	if _, err := client.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReceiveBrokerDescriptors(server); err == nil {
		t.Fatal("invalid marker frame accepted")
	}
	_ = client.Close()
}

func TestSendBrokerDescriptorsRejectsWrongDescriptorTypes(t *testing.T) {
	client, _ := unixSocketPairForDescriptorTest(t)
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cwd.Close()
	if err := SendBrokerDescriptors(client, f, cwd); err == nil {
		t.Fatal("non-executable descriptor accepted")
	}
}

func TestVerifiedWorkerDescriptorHandoffBindsRegistryIdentity(t *testing.T) {
	client, server := unixSocketPairForDescriptorTest(t)
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
		Version: executableArtifactRegistryVersion, CallerNodeID: "node-a",
		RequestID: "request-a", RemoteJobID: "remote-a", LocalJobID: "local-a",
		PayloadHash: hex.EncodeToString(sum[:]), PolicyRevision: 1,
		Name: stage.Name, Digest: snapshot.Digest(), Size: info.Size(),
		BrokerUID: uid, WorkerUID: uid + 1, Device: uint64(st.Dev),
		Inode: st.Ino, CreatedAt: time.Now().UTC(),
	}
	capability, err := OpenVerifiedExecutableArtifact(dir, record)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cwd.Close()
	if err := SendVerifiedWorkerDescriptors(client, capability, cwd); err != nil {
		t.Fatal(err)
	}
	receivedExe, receivedCWD, err := ReceiveVerifiedWorkerDescriptors(server, record)
	if err != nil {
		t.Fatal(err)
	}
	defer receivedExe.Close()
	defer receivedCWD.Close()
	if err := VerifyTransferredExecutableDescriptor(receivedExe, record); err != nil {
		t.Fatal(err)
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, receivedExe.Fd(), uintptr(syscall.F_GETFL), 0)
	if errno != 0 || int(flags)&syscall.O_ACCMODE != syscall.O_RDONLY {
		t.Fatal("received executable descriptor is not read-only")
	}
	if !os.SameFile(mustStat(t, receivedExe), mustStat(t, stage.File)) {
		t.Fatal("received descriptor does not identify registered inode")
	}
}

func TestVerifiedWorkerDescriptorHandoffRejectsRecordSubstitution(t *testing.T) {
	client, server := unixSocketPairForDescriptorTest(t)
	data := []byte("descriptor-bound executable")
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
	capability, err := OpenVerifiedExecutableArtifact(dir, record)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cwd.Close()
	if err := SendVerifiedWorkerDescriptors(client, capability, cwd); err != nil {
		t.Fatal(err)
	}
	bad := record
	bad.Digest = hex.EncodeToString(make([]byte, sha256.Size))
	if _, _, err := ReceiveVerifiedWorkerDescriptors(server, bad); err == nil {
		t.Fatal("descriptor accepted against substituted artifact digest")
	}
}

func mustStat(t *testing.T, file *os.File) os.FileInfo {
	t.Helper()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return info
}
