package nodeidentity

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityPersistsAndSigns(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	pub := first.Public()
	if pub.NodeID == "" || pub.Fingerprint == "" || pub.Algorithm != "Ed25519" {
		t.Fatalf("invalid public identity: %+v", pub)
	}
	message := []byte("cycom-federation-test")
	if !Verify(first.pub, message, first.Sign(message)) {
		t.Fatal("signature did not verify")
	}
	second, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Public() != pub {
		t.Fatal("identity changed after reload")
	}
	info, err := os.Stat(filepath.Join(dir, "federation-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("private identity permissions = %o", info.Mode().Perm())
	}
	if _, err := base64.RawStdEncoding.DecodeString(pub.PublicKey); err != nil {
		t.Fatal(err)
	}
}

func TestIdentitiesAreIndependent(t *testing.T) {
	a, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a.Public().NodeID == b.Public().NodeID {
		t.Fatal("two nodes received the same identity")
	}
}

func TestTrustRequiresExactFingerprintAndSupportsRevocation(t *testing.T) {
	local, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub := remote.Public()
	if _, err := local.Trust(pub, "wrong"); err == nil {
		t.Fatal("accepted mismatched fingerprint")
	}
	if _, err := local.Trust(pub, pub.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if !local.IsTrusted(pub.NodeID) {
		t.Fatal("peer should be trusted")
	}
	peers, err := local.ListTrusted(false)
	if err != nil || len(peers) != 1 {
		t.Fatalf("list trusted: %v %+v", err, peers)
	}
	if _, err := local.Revoke(pub.NodeID); err != nil {
		t.Fatal(err)
	}
	if local.IsTrusted(pub.NodeID) {
		t.Fatal("revoked peer still trusted")
	}
	all, err := local.ListTrusted(true)
	if err != nil || len(all) != 1 || all[0].RevokedAt.IsZero() {
		t.Fatalf("revoked record missing: %v %+v", err, all)
	}
}

func TestTrustRejectsTamperedIdentityAndSelf(t *testing.T) {
	local, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub := remote.Public()
	pub.NodeID = "tampered"
	if _, err := local.Trust(pub, remote.Public().Fingerprint); err == nil {
		t.Fatal("accepted tampered node ID")
	}
	self := local.Public()
	if _, err := local.Trust(self, self.Fingerprint); err == nil {
		t.Fatal("trusted self")
	}
}

func TestTrustedChallengeVerification(t *testing.T) {
	local, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub := remote.Public()
	if _, err := local.Trust(pub, pub.Fingerprint); err != nil {
		t.Fatal(err)
	}
	nonce := []byte("0123456789abcdef0123456789abcdef")
	if err := local.VerifyTrustedChallenge(pub.NodeID, nonce, remote.SignChallenge(nonce)); err != nil {
		t.Fatal(err)
	}
	if err := local.VerifyTrustedChallenge(pub.NodeID, nonce, remote.SignChallenge([]byte("wrong nonce"))); err == nil {
		t.Fatal("accepted signature for a different nonce")
	}
	if err := local.VerifyTrustedChallenge("unknown-node", nonce, remote.SignChallenge(nonce)); err == nil {
		t.Fatal("accepted an untrusted node")
	}
	if _, err := local.Revoke(pub.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := local.VerifyTrustedChallenge(pub.NodeID, nonce, remote.SignChallenge(nonce)); err == nil {
		t.Fatal("accepted revoked node")
	}
}

func TestMutualAuthenticationTranscript(t *testing.T) {
	a, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ap, bp := a.Public(), b.Public()
	if _, err := a.Trust(bp, bp.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Trust(ap, ap.Fingerprint); err != nil {
		t.Fatal(err)
	}
	nonceA := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXowMTIzNDU"
	nonceB := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"
	request := []byte("mutual-auth-request-v1|" + ap.NodeID + "|" + bp.NodeID + "|" + nonceA)
	if err := b.VerifyTrustedChallenge(ap.NodeID, request, a.SignChallenge(request)); err != nil {
		t.Fatal(err)
	}
	response := []byte("mutual-auth-response-v1|" + ap.NodeID + "|" + bp.NodeID + "|" + nonceA + "|" + nonceB)
	if err := a.VerifyTrustedChallenge(bp.NodeID, response, b.SignChallenge(response)); err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyTrustedChallenge(bp.NodeID, []byte("mutated"), b.SignChallenge(response)); err == nil {
		t.Fatal("accepted response for altered transcript")
	}
	if _, err := b.Revoke(ap.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := b.VerifyTrustedChallenge(ap.NodeID, request, a.SignChallenge(request)); err == nil {
		t.Fatal("accepted revoked caller")
	}
}
