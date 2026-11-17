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

func signedObserverReceiptFixture(t *testing.T) ([]byte, SignatureEnvelope, *MemoryTrustStore, []byte, time.Time) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	source := []byte("independent observer source record")
	md := sha256.Sum256(source)
	payload := ObserverReceiptPayload{SchemaVersion: "1.0", ReceiptID: "receipt-1", IssuerID: "observer-issuer", RunnerID: "runner-1", RunID: "run-1", RunNonce: strings.Repeat("12", 32), ObserverSequence: 1, EventID: "host-event-1", EventType: "VM_START_OBSERVED", Subject: ObserverReceiptSubject{Kind: "VM_INSTANCE", ID: "vm-1"}, Measurement: ObserverReceiptMeasurement{Kind: "HYPERVISOR_CONTROL_PLANE_EVENT", ValueSHA256: hex.EncodeToString(md[:]), SourceID: "source-1"}, ObservedAtUTC: now.Format(time.RFC3339Nano), ClockUncertaintyMS: 250, PreviousReceiptSHA256: strings.Repeat("a", 64), PolicyDigestSHA256: strings.Repeat("b", 64)}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	d := sha256.Sum256(raw)
	env := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "observer-key", Digest: hex.EncodeToString(d[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, d[:]))}
	store, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "observer-key", IssuerID: "observer-issuer", Purpose: KeyPurposeObserverReceipt, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	return raw, env, store, source, now
}
func TestVerifyObserverReceiptValid(t *testing.T) {
	raw, env, store, source, now := signedObserverReceiptFixture(t)
	claim, err := VerifyObserverReceipt(raw, env, source, store, now)
	if err != nil {
		t.Fatal(err)
	}
	if claim.ReceiptID != "receipt-1" || claim.IssuerID != "observer-issuer" || claim.ReceiptSHA256 != ManifestDigest(raw) {
		t.Fatalf("unexpected claim: %+v", claim)
	}
}
func TestVerifyObserverReceiptRejectsWrongPurposeAndTime(t *testing.T) {
	raw, env, _, source, now := signedObserverReceiptFixture(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	d := sha256.Sum256(raw)
	env.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, d[:]))
	wrong, err := NewMemoryTrustStore([]TrustedKey{{KeyID: env.KeyID, Purpose: KeyPurposeManifest, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyObserverReceipt(raw, env, source, wrong, now); err == nil {
		t.Fatal("accepted wrong key purpose")
	}
	_, validEnv, validStore, _, _ := signedObserverReceiptFixture(t)
	if _, err := VerifyObserverReceipt(raw, validEnv, source, validStore, now.Add(2*time.Hour)); err == nil {
		t.Fatal("accepted expired key")
	}
}
func TestVerifyObserverReceiptRejectsWrongIssuerBinding(t *testing.T) {
	raw, envelope, _, source, now := signedObserverReceiptFixture(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	envelope.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, digest[:]))
	store, err := NewMemoryTrustStore([]TrustedKey{{KeyID: envelope.KeyID, IssuerID: "different-issuer", Purpose: KeyPurposeObserverReceipt, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyObserverReceipt(raw, envelope, source, store, now); err == nil {
		t.Fatal("accepted key bound to a different issuer")
	}
}

func TestVerifyObserverReceiptRejectsMutationAndMeasurementMismatch(t *testing.T) {
	raw, env, store, source, now := signedObserverReceiptFixture(t)
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-2] ^= 1
	if _, err := VerifyObserverReceipt(tampered, env, source, store, now); err == nil {
		t.Fatal("accepted mutation")
	}
	if _, err := VerifyObserverReceipt(raw, env, append(source, '!'), store, now); err == nil {
		t.Fatal("accepted measurement mismatch")
	}
}
func TestDecodeObserverReceiptRejectsNonCanonicalUnknownAndClockBounds(t *testing.T) {
	raw, _, _, _, _ := signedObserverReceiptFixture(t)
	if _, err := DecodeObserverReceipt(append([]byte(" "), raw...)); err == nil {
		t.Fatal("accepted whitespace")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	obj["unknown"] = true
	unknown, _ := json.Marshal(obj)
	if _, err := DecodeObserverReceipt(unknown); err == nil {
		t.Fatal("accepted unknown field")
	}
	delete(obj, "unknown")
	obj["clock_uncertainty_ms"] = float64(300001)
	over, _ := json.Marshal(obj)
	if _, err := DecodeObserverReceipt(over); err == nil {
		t.Fatal("accepted clock uncertainty")
	}
}
func TestDecodeObserverReceiptRejectsDuplicateKeys(t *testing.T) {
	raw, _, _, _, _ := signedObserverReceiptFixture(t)
	q := string(rune(34))
	needle := q + "receipt_id" + q + ":" + q + "receipt-1" + q
	replacement := needle + "," + needle
	if _, err := DecodeObserverReceipt([]byte(strings.Replace(string(raw), needle, replacement, 1))); err == nil {
		t.Fatal("accepted duplicate key")
	}
}
