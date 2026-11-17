//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeContractSocket(t *testing.T) (string, BrokerSocketPolicy) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "root.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	return path, BrokerSocketPolicy{SocketUID: uid, ParentUID: uid}
}

func TestBuildWorkerExecutionContractRequiresMatchingDurableAuthorization(t *testing.T) {
	socketPath, socketPolicy := makeContractSocket(t)
	socketValidation, err := ValidateBrokerSocketPathPinned(socketPath, socketPolicy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socketValidation.Close() })
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
	req := validBrokerRequest()
	if _, err := exe.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, exe); err != nil {
		t.Fatal(err)
	}
	req.ExecutableDigest = hex.EncodeToString(h.Sum(nil))
	if _, err := exe.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	auth := BrokerAuthorization{
		Record:         Record{CallerNodeID: "node-a", RequestID: req.RequestID, Status: "accepted", LocalJobID: "local-job", PolicyRevision: req.PolicyRevision},
		PolicyRevision: req.PolicyRevision, Executable: exe, WorkingDirectory: cwd,
	}
	contract, err := BuildWorkerExecutionContract(auth, req, 991, 991, socketValidation)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Profile.User != "991" || contract.Profile.Group != "991" || contract.Timeout.Milliseconds() != req.TimeoutMillis {
		t.Fatalf("unexpected normalized contract: %+v", contract)
	}
	if len(contract.Argv) != len(req.Argv) || contract.Argv[0] != req.Argv[0] || !strings.HasSuffix(contract.UnitName, ".service") {
		t.Fatalf("unexpected command/unit contract: %+v", contract)
	}
	contract.Argv[0] = "mutated"
	contract.Environment["LANG"] = "mutated"
	if req.Argv[0] == "mutated" || req.Environment["LANG"] == "mutated" {
		t.Fatal("contract aliases caller-owned request memory")
	}
}

func TestBuildWorkerExecutionContractRejectsMismatchedOrMissingEvidence(t *testing.T) {
	socketPath, socketPolicy := makeContractSocket(t)
	socketValidation, err := ValidateBrokerSocketPathPinned(socketPath, socketPolicy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socketValidation.Close() })
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
	req := validBrokerRequest()
	auth := BrokerAuthorization{Record: Record{CallerNodeID: "node-a", RequestID: req.RequestID, Status: "accepted", LocalJobID: "local-job", PolicyRevision: req.PolicyRevision}, PolicyRevision: req.PolicyRevision, Executable: exe, WorkingDirectory: cwd}
	cases := []struct {
		name   string
		mutate func(*BrokerAuthorization, *BrokerStartRequest)
	}{
		{"missing executable", func(a *BrokerAuthorization, _ *BrokerStartRequest) { a.Executable = nil }},
		{"missing cwd", func(a *BrokerAuthorization, _ *BrokerStartRequest) { a.WorkingDirectory = nil }},
		{"wrong request", func(a *BrokerAuthorization, r *BrokerStartRequest) { r.RequestID = "req-other" }},
		{"wrong revision", func(a *BrokerAuthorization, r *BrokerStartRequest) { r.PolicyRevision++ }},
		{"not accepted", func(a *BrokerAuthorization, _ *BrokerStartRequest) { a.Record.Status = "reserved" }},
		{"root identity", func(_ *BrokerAuthorization, _ *BrokerStartRequest) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, r := auth, req
			tc.mutate(&a, &r)
			uid, gid := uint32(991), uint32(991)
			if tc.name == "root identity" {
				uid = 0
			}
			if _, err := BuildWorkerExecutionContract(a, r, uid, gid, socketValidation); err == nil {
				t.Fatal("invalid authorization accepted")
			}
		})
	}
}

func TestBuildWorkerExecutionContractRejectsInvalidRootBrokerSocket(t *testing.T) {
	exe, err := os.Open("/bin/sh")
	if err != nil {
		t.Skip(err)
	}
	defer exe.Close()
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cwd.Close()
	req := validBrokerRequest()
	auth := BrokerAuthorization{Record: Record{CallerNodeID: "node-a", RequestID: req.RequestID, Status: "accepted", LocalJobID: "job", PolicyRevision: req.PolicyRevision}, PolicyRevision: req.PolicyRevision, Executable: exe, WorkingDirectory: cwd}
	if _, err := BuildWorkerExecutionContract(auth, req, 991, 991, nil); err == nil {
		t.Fatal("contract accepted missing root broker socket")
	}
}
