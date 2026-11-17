package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

func brokerAuthorizationFixture(t *testing.T) (*PolicyStore, *Ledger, FederationCallerContext, BrokerStartRequest, ProcessRequest) {
	t.Helper()
	root := t.TempDir()
	exe := filepath.Join(root, "worker")
	if err := os.WriteFile(exe, []byte("worker binary fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	policy := ExecutionPolicy{Enabled: true, AllowedCallers: []string{"node-a"}, AllowedExecutables: []string{exe}, WorkingDirRoots: []string{root}, AllowedEnvKeys: []string{"LANG"}, MaxTimeout: 5 * time.Second, MaxOutputBytes: 4096, MaxArgumentCount: 8, MaxArgumentBytes: 1024}
	store, err := OpenPolicyStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveVersioned(policy, 0); err != nil {
		t.Fatal(err)
	}
	ledger, err := Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	resolved := ProcessRequest{Executable: exe, Args: []string{"--version"}, WorkingDir: work, Environment: map[string]string{"LANG": "C"}, Timeout: time.Second}
	req := BrokerStartRequest{Version: ExecutionBrokerProtocolVersion, JobID: "remote-1", RequestID: "request-1", PolicyRevision: 1, ExecutableDigest: hex.EncodeToString(sum[:]), Argv: []string{exe, "--version"}, WorkingDirRef: "work-1", Environment: map[string]string{"LANG": "C"}, TimeoutMillis: 1000, MaxOutputBytes: 2048, InputRef: "input-1"}
	receiver, err := nodeidentity.New(filepath.Join(root, "identity"))
	if err != nil {
		t.Fatal(err)
	}
	callerIdentity, err := nodeidentity.New(filepath.Join(root, "caller"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Trust(callerIdentity.Public(), callerIdentity.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	signed, err := SignBrokerStartEnvelope(callerIdentity, receiver.Public().NodeID, req)
	if err != nil {
		t.Fatal(err)
	}
	caller, _, err := DecodeAndVerifySignedBrokerStartEnvelope(receiver, signed)
	if err != nil {
		t.Fatal(err)
	}
	policy.AllowedCallers = []string{caller.NodeID()}
	if _, err := store.SaveVersioned(policy, 1); err != nil {
		t.Fatal(err)
	}
	req.PolicyRevision = 2
	return store, ledger, caller, req, resolved
}

func TestAuthorizeAndReservePersistsOneShotAuthorizationWithoutDispatch(t *testing.T) {
	store, ledger, caller, req, resolved := brokerAuthorizationFixture(t)
	got, err := AuthorizeAndReserve(store, ledger, caller, req, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if got.Record.Status != "accepted" || got.Record.PolicyRevision != 2 || got.Record.LocalJobID == "" {
		t.Fatalf("unexpected authorization record: %+v", got.Record)
	}
	if _, err := ledger.Get(caller.NodeID(), req.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeAndReserve(store, ledger, caller, req, resolved); !errors.Is(err, ErrBrokerReplay) {
		t.Fatalf("duplicate request error=%v", err)
	}
	reloaded, err := Open(filepath.Dir(filepath.Dir(ledger.dir)))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := reloaded.Get(caller.NodeID(), req.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "accepted" {
		t.Fatalf("replay changed state to %q", rec.Status)
	}
}

func TestAuthorizeAndReserveRejectsTrustRevisionDigestAndResolvedMismatch(t *testing.T) {
	tests := []struct {
		name    string
		trusted bool
		mutate  func(*BrokerStartRequest, *ProcessRequest)
	}{
		{name: "untrusted", trusted: false},
		{name: "stale revision", trusted: true, mutate: func(r *BrokerStartRequest, _ *ProcessRequest) { r.PolicyRevision++ }},
		{name: "digest mismatch", trusted: true, mutate: func(r *BrokerStartRequest, _ *ProcessRequest) { r.ExecutableDigest = string(make([]byte, 64)) }},
		{name: "argv mismatch", trusted: true, mutate: func(_ *BrokerStartRequest, p *ProcessRequest) { p.Args = []string{"different"} }},
		{name: "timeout mismatch", trusted: true, mutate: func(_ *BrokerStartRequest, p *ProcessRequest) { p.Timeout = 2 * time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, ledger, caller, req, resolved := brokerAuthorizationFixture(t)
			if tt.mutate != nil {
				tt.mutate(&req, &resolved)
			}
			if !tt.trusted {
				caller = FederationCallerContext{}
			}
			if _, err := AuthorizeAndReserve(store, ledger, caller, req, resolved); err == nil {
				t.Fatal("unauthorized request accepted")
			}
		})
	}
}

func TestAuthorizeAndReserveConcurrentReplayHasSingleWinner(t *testing.T) {
	store, ledger, caller, req, resolved := brokerAuthorizationFixture(t)
	const n = 12
	start := make(chan struct{})
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			<-start
			_, err := AuthorizeAndReserve(store, ledger, caller, req, resolved)
			results <- err
		}()
	}
	close(start)
	winners, replays := 0, 0
	for i := 0; i < n; i++ {
		err := <-results
		if err == nil {
			winners++
		} else if errors.Is(err, ErrBrokerReplay) {
			replays++
		} else {
			t.Fatalf("unexpected authorization error: %v", err)
		}
	}
	if winners != 1 || replays != n-1 {
		t.Fatalf("winners=%d replays=%d", winners, replays)
	}
}

func TestFederationCallerContextRejectsRevokedPeerAndUnavailableIdentity(t *testing.T) {
	root := t.TempDir()
	receiver, err := nodeidentity.New(filepath.Join(root, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	peer, err := nodeidentity.New(filepath.Join(root, "peer"))
	if err != nil {
		t.Fatal(err)
	}
	req := validBrokerRequest()
	if _, _, err := DecodeAndVerifySignedBrokerStartEnvelope(nil, []byte(`{}`)); err == nil {
		t.Fatal("accepted nil receiver identity")
	}
	raw, err := SignBrokerStartEnvelope(peer, receiver.Public().NodeID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeAndVerifySignedBrokerStartEnvelope(receiver, raw); err == nil {
		t.Fatal("accepted unknown peer")
	}
	if _, err := receiver.Trust(peer.Public(), peer.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeAndVerifySignedBrokerStartEnvelope(receiver, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Revoke(peer.Public().NodeID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeAndVerifySignedBrokerStartEnvelope(receiver, raw); err == nil {
		t.Fatal("accepted revoked peer")
	}
}

// Descriptor-bound authorization is Linux-only because it uses openat2 and
// verifies that policy-approved names still identify pinned descriptors.
func TestAuthorizeAndReserveDescriptorsRetainsPinnedObjects(t *testing.T) {
	store, ledger, caller, req, resolved := brokerAuthorizationFixture(t)
	root, err := os.Open(filepath.Dir(resolved.Executable))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	exe, err := OpenExecutableBeneath(root, filepath.Base(resolved.Executable))
	if err != nil {
		t.Fatal(err)
	}
	workRoot, err := os.Open(filepath.Dir(resolved.WorkingDir))
	if err != nil {
		_ = exe.Close()
		t.Fatal(err)
	}
	defer workRoot.Close()
	cwd, err := OpenDirectoryBeneath(workRoot, filepath.Base(resolved.WorkingDir))
	if err != nil {
		_ = exe.Close()
		t.Fatal(err)
	}
	beforeExe, err := exe.Stat()
	if err != nil {
		t.Fatal(err)
	}
	beforeCWD, err := cwd.Stat()
	if err != nil {
		t.Fatal(err)
	}
	got, err := AuthorizeAndReserveDescriptors(store, ledger, caller, req, BrokerResolvedDescriptors{Request: resolved, Executable: exe, WorkingDir: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if got.Executable == nil || got.WorkingDirectory == nil || got.Record.Status != "accepted" {
		t.Fatal("authorization did not retain pinned descriptors")
	}
	if offset, err := got.Executable.Seek(0, 1); err != nil || offset != 0 {
		t.Fatalf("pinned executable offset after digest verification = %d, err=%v; want 0", offset, err)
	}
	defer got.Close()
	if err := os.Rename(resolved.Executable, resolved.Executable+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolved.Executable, []byte("replacement executable"), 0700); err != nil {
		t.Fatal(err)
	}
	afterExe, err := got.Executable.Stat()
	if err != nil {
		t.Fatal(err)
	}
	afterCWD, err := got.WorkingDirectory.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(beforeExe, afterExe) || !os.SameFile(beforeCWD, afterCWD) {
		t.Fatal("authorization descriptors changed inode after pathname replacement")
	}
	if _, err := AuthorizeAndReserveDescriptors(store, ledger, caller, req, BrokerResolvedDescriptors{}); err == nil {
		t.Fatal("missing descriptors were accepted")
	}
}

func TestAuthorizeAndReserveDescriptorsRejectsWrongPinnedExecutable(t *testing.T) {
	store, ledger, caller, req, resolved := brokerAuthorizationFixture(t)
	root := t.TempDir()
	otherPath := filepath.Join(root, "other")
	if err := os.WriteFile(otherPath, []byte("not the approved executable"), 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Open(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Open(resolved.WorkingDir)
	if err != nil {
		_ = exe.Close()
		t.Fatal(err)
	}
	if _, err := AuthorizeAndReserveDescriptors(store, ledger, caller, req, BrokerResolvedDescriptors{Request: resolved, Executable: exe, WorkingDir: cwd}); err == nil {
		t.Fatal("wrong executable descriptor accepted")
	}
	if _, err := ledger.Get(caller.NodeID(), req.RequestID); err == nil {
		t.Fatal("rejected descriptor request was reserved")
	}
}
