package vmevidence

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func signedReadbackFixture(t *testing.T, accepted VerifiedCheckpointAcceptance, mutate func(*CheckpointReadbackPayload)) ([]byte, SignatureEnvelope, *MemoryTrustStore, time.Time) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC)
	p := CheckpointReadbackPayload{SchemaVersion: "1.0", ReceiptID: "readback-1", IssuerID: accepted.IssuerID, RunnerID: accepted.AcceptedHead.RunnerID, RunID: accepted.AcceptedHead.RunID, RunNonce: accepted.AcceptedHead.RunNonce, AuthorityEpoch: accepted.AuthorityEpoch, FencingToken: accepted.FencingToken, HeadSequence: accepted.AcceptedHead.Sequence, HeadJournalSHA256: accepted.AcceptedHead.JournalSHA256, ReadAtUTC: now.Format(time.RFC3339Nano)}
	if mutate != nil {
		mutate(&p)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	env := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "readback-key", Digest: hex.EncodeToString(digest[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, digest[:]))}
	store, err := NewMemoryTrustStore([]TrustedKey{{KeyID: env.KeyID, IssuerID: accepted.IssuerID, Purpose: KeyPurposeCheckpointReadback, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	return raw, env, store, now
}

func TestVerifyCheckpointReadbackClassifications(t *testing.T) {
	acceptRaw, acceptEnv, req, claims, acceptStore, acceptAt := signedCheckpointAcceptanceFixture(t)
	accepted, err := VerifyCheckpointAcceptanceReceipt(acceptRaw, acceptEnv, req, claims, acceptStore, acceptAt)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*CheckpointReadbackPayload)
		want   CheckpointReadbackStatus
	}{
		{"current", nil, CheckpointReadbackCurrent},
		{"superseded", func(p *CheckpointReadbackPayload) {
			p.HeadSequence++
			p.HeadJournalSHA256 = strings.Repeat("d", 64)
			p.SupersedesAcceptanceReceiptIDs = []string{accepted.ReceiptID}
		}, CheckpointReadbackSuperseded},
		{"unlinked newer head", func(p *CheckpointReadbackPayload) { p.HeadSequence++; p.HeadJournalSHA256 = strings.Repeat("d", 64) }, CheckpointReadbackCorrupt},
		{"lower fencing token", func(p *CheckpointReadbackPayload) { p.FencingToken-- }, CheckpointReadbackCorrupt},
		{"epoch rollback", func(p *CheckpointReadbackPayload) { p.AuthorityEpoch-- }, CheckpointReadbackCorrupt},
		{"same sequence wrong digest", func(p *CheckpointReadbackPayload) { p.HeadJournalSHA256 = strings.Repeat("e", 64) }, CheckpointReadbackCorrupt},
		{"lower sequence", func(p *CheckpointReadbackPayload) { p.HeadSequence--; p.HeadJournalSHA256 = strings.Repeat("f", 64) }, CheckpointReadbackCorrupt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, env, store, at := signedReadbackFixture(t, accepted, tc.mutate)
			got, err := VerifyCheckpointReadback(raw, env, accepted, store, at)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Fatalf("status=%s want=%s", got.Status, tc.want)
			}
		})
	}
}

func TestVerifyCheckpointReadbackRejectsMutation(t *testing.T) {
	acceptRaw, acceptEnv, req, claims, acceptStore, acceptAt := signedCheckpointAcceptanceFixture(t)
	accepted, err := VerifyCheckpointAcceptanceReceipt(acceptRaw, acceptEnv, req, claims, acceptStore, acceptAt)
	if err != nil {
		t.Fatal(err)
	}
	raw, env, store, at := signedReadbackFixture(t, accepted, nil)
	mutated := append([]byte(nil), raw...)
	mutated[len(mutated)-2] ^= 1
	if _, err := VerifyCheckpointReadback(mutated, env, accepted, store, at); err == nil {
		t.Fatal("accepted mutated read-back")
	}
}

func TestDecodeCheckpointReadbackRejectsDuplicateSupersedes(t *testing.T) {
	_, _, req, _, _, _ := signedCheckpointAcceptanceFixture(t)
	raw, _, _, _ := signedReadbackFixture(t, VerifiedCheckpointAcceptance{IssuerID: "authority-1", AcceptedHead: HostJournalCheckpoint{RunnerID: req.RunnerID, RunID: req.RunID, RunNonce: req.RunNonce, Sequence: req.Next.Sequence, JournalSHA256: req.Next.JournalSHA256}, AuthorityEpoch: req.AuthorityEpoch, FencingToken: 901, ReceiptID: "accept-1"}, func(p *CheckpointReadbackPayload) {
		p.SupersedesAcceptanceReceiptIDs = []string{"accept-1", "accept-1"}
	})
	if _, err := DecodeCheckpointReadback(raw); err == nil {
		t.Fatal("accepted duplicate supersedes IDs")
	}
}

func TestClassifyCheckpointReadbackError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want CheckpointReadbackStatus
	}{
		{"nil is not proof of current", nil, CheckpointReadbackUnknown},
		{"unknown commit", ErrCheckpointOutcomeUnknown, CheckpointReadbackUnknown},
		{"unavailable", ErrCheckpointUnavailable, CheckpointReadbackUnknown},
		{"not found after uncertain commit", ErrCheckpointNotFound, CheckpointReadbackUnknown},
		{"corrupt", ErrCheckpointCorrupt, CheckpointReadbackCorrupt},
		{"wrapped unavailable", errors.Join(errors.New("transport"), ErrCheckpointUnavailable), CheckpointReadbackUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyCheckpointReadbackError(tc.err); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
