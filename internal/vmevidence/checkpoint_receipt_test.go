package vmevidence

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func signedCheckpointAcceptanceFixture(t *testing.T) ([]byte, SignatureEnvelope, CheckpointAdvanceRequest, []ObserverReceiptClaim, *MemoryTrustStore, time.Time) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	req := CheckpointAdvanceRequest{SchemaVersion: "1.0", RequestID: "advance-req-01", AuthorityEpoch: 7, RunnerID: "runner-1", RunID: "run-1", RunNonce: strings.Repeat("12", 32), Expected: CheckpointHead{Sequence: 17, JournalSHA256: strings.Repeat("a", 64)}, Next: CheckpointHead{Sequence: 18, JournalSHA256: strings.Repeat("b", 64)}, ObserverReceiptIDs: []string{"obs-1", "obs-2"}, PolicyDigestSHA256: strings.Repeat("c", 64)}
	reqBytes, err := CanonicalCheckpointAdvanceRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	rd := sha256.Sum256(reqBytes)
	claims := []ObserverReceiptClaim{{ReceiptID: "obs-1", RunnerID: req.RunnerID, RunID: req.RunID, RunNonce: req.RunNonce, PolicyDigestSHA256: req.PolicyDigestSHA256}, {ReceiptID: "obs-2", RunnerID: req.RunnerID, RunID: req.RunID, RunNonce: req.RunNonce, PolicyDigestSHA256: req.PolicyDigestSHA256}}
	payload := CheckpointAcceptanceReceiptPayload{SchemaVersion: "1.0", ReceiptID: "accept-1", IssuerID: "authority-1", RequestID: req.RequestID, RequestSHA256: hex.EncodeToString(rd[:]), RunnerID: req.RunnerID, RunID: req.RunID, RunNonce: req.RunNonce, AuthorityEpoch: req.AuthorityEpoch, CommitSequence: req.Next.Sequence, AcceptedJournalSHA256: req.Next.JournalSHA256, PreviousJournalSHA256: req.Expected.JournalSHA256, ObserverReceiptIDs: []string{"obs-1", "obs-2"}, Decision: "ACCEPTED", CommittedAtUTC: now.Format(time.RFC3339Nano), DurabilityClass: "QUORUM_COMMITTED", FencingToken: 901}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	d := sha256.Sum256(raw)
	env := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "authority-key", Digest: hex.EncodeToString(d[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, d[:]))}
	store, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "authority-key", IssuerID: "authority-1", Purpose: KeyPurposeCheckpointAcceptance, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	return raw, env, req, claims, store, now
}
func TestVerifyCheckpointAcceptanceReceiptValid(t *testing.T) {
	raw, env, req, claims, store, now := signedCheckpointAcceptanceFixture(t)
	v, err := VerifyCheckpointAcceptanceReceipt(raw, env, req, claims, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if v.ReceiptID != "accept-1" || v.AcceptedHead.Sequence != 18 || v.AcceptedHead.JournalSHA256 != req.Next.JournalSHA256 || v.FencingToken != 901 {
		t.Fatalf("unexpected result: %+v", v)
	}
}
func TestVerifyCheckpointAcceptanceReceiptRejectsRequestAndObserverMismatch(t *testing.T) {
	raw, env, req, claims, store, now := signedCheckpointAcceptanceFixture(t)
	changed := req
	changed.Next.JournalSHA256 = strings.Repeat("d", 64)
	if _, err := VerifyCheckpointAcceptanceReceipt(raw, env, changed, claims, store, now); err == nil {
		t.Fatal("accepted different successor")
	}
	cc := append([]ObserverReceiptClaim(nil), claims...)
	cc[1].ReceiptID = "other"
	if _, err := VerifyCheckpointAcceptanceReceipt(raw, env, req, cc, store, now); err == nil {
		t.Fatal("accepted different receipt IDs")
	}
	cc = append([]ObserverReceiptClaim(nil), claims...)
	cc[0].PolicyDigestSHA256 = strings.Repeat("e", 64)
	if _, err := VerifyCheckpointAcceptanceReceipt(raw, env, req, cc, store, now); err == nil {
		t.Fatal("accepted wrong observer policy")
	}
}
func TestVerifyCheckpointAcceptanceReceiptRejectsWrongIssuerAndPurpose(t *testing.T) {
	raw, env, req, claims, _, now := signedCheckpointAcceptanceFixture(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d := sha256.Sum256(raw)
	env.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, d[:]))
	wrong, err := NewMemoryTrustStore([]TrustedKey{{KeyID: env.KeyID, IssuerID: "another-authority", Purpose: KeyPurposeCheckpointAcceptance, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCheckpointAcceptanceReceipt(raw, env, req, claims, wrong, now); err == nil {
		t.Fatal("accepted wrong issuer")
	}
	wrong, err = NewMemoryTrustStore([]TrustedKey{{KeyID: env.KeyID, IssuerID: "authority-1", Purpose: KeyPurposeObserverReceipt, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCheckpointAcceptanceReceipt(raw, env, req, claims, wrong, now); err == nil {
		t.Fatal("accepted wrong purpose")
	}
}
func TestDecodeCheckpointAcceptanceRejectsMalformedReceipt(t *testing.T) {
	raw, _, _, _, _, _ := signedCheckpointAcceptanceFixture(t)
	if _, err := DecodeCheckpointAcceptanceReceipt(append([]byte(" "), raw...)); err == nil {
		t.Fatal("accepted whitespace")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	obj["decision"] = "PENDING"
	bad, _ := json.Marshal(obj)
	if _, err := DecodeCheckpointAcceptanceReceipt(bad); err == nil {
		t.Fatal("accepted pending")
	}
	obj["decision"] = "ACCEPTED"
	obj["fencing_token"] = float64(0)
	bad, _ = json.Marshal(obj)
	if _, err := DecodeCheckpointAcceptanceReceipt(bad); err == nil {
		t.Fatal("accepted zero fence")
	}
	q := string(rune(34))
	needle := q + "receipt_id" + q + ":" + q + "accept-1" + q
	duplicate := strings.Replace(string(raw), needle, needle+","+needle, 1)
	if _, err := DecodeCheckpointAcceptanceReceipt([]byte(duplicate)); err == nil {
		t.Fatal("accepted duplicate key")
	}
}
func TestCanonicalCheckpointAdvanceRequestRejectsDuplicateIDsAndSequenceGap(t *testing.T) {
	_, _, req, _, _, _ := signedCheckpointAcceptanceFixture(t)
	dup := req
	dup.ObserverReceiptIDs = []string{"obs-1", "obs-1"}
	if _, err := CanonicalCheckpointAdvanceRequest(dup); err == nil {
		t.Fatal("accepted duplicate IDs")
	}
	gap := req
	gap.Next.Sequence++
	if _, err := CanonicalCheckpointAdvanceRequest(gap); err == nil {
		t.Fatal("accepted sequence gap")
	}
}
