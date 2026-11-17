package vmevidence

import (
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"
)

// KeyPurpose scopes a signing key to one evidence role.
type KeyPurpose string

const (
	KeyPurposeTestPlan             KeyPurpose = "TEST_PLAN"
	KeyPurposeManifest             KeyPurpose = "MANIFEST"
	KeyPurposeHostJournal          KeyPurpose = "HOST_JOURNAL"
	KeyPurposeObserverReceipt      KeyPurpose = "OBSERVER_RECEIPT"
	KeyPurposeCheckpointAcceptance KeyPurpose = "CHECKPOINT_ACCEPTANCE"
	KeyPurposeCheckpointReadback   KeyPurpose = "CHECKPOINT_READBACK"
)

// TrustedKey is a trust-store record. PublicKey bytes are copied on read.
type TrustedKey struct {
	KeyID     string
	IssuerID  string
	Purpose   KeyPurpose
	PublicKey ed25519.PublicKey
	NotBefore time.Time
	NotAfter  time.Time
	Revoked   bool
}

// TrustStore must be backed by an independently administered trust source.
// Implementations must not accept keys embedded in the document being checked.
type TrustStore interface {
	LookupTrustedKey(keyID string, purpose KeyPurpose, at time.Time) (ed25519.PublicKey, error)
}

// IssuerTrustStore additionally binds an evidence signing key to one issuer.
// Observer and checkpoint-acceptance verification require this stronger
// contract; key ID and purpose alone do not authorize a claimed issuer identity.
type IssuerTrustStore interface {
	TrustStore
	LookupTrustedIssuerKey(keyID, issuerID string, purpose KeyPurpose, at time.Time) (ed25519.PublicKey, error)
}

// VerifyWithTrustStore verifies an envelope against a purpose-scoped key.
func VerifyWithTrustStore(raw []byte, envelope SignatureEnvelope, purpose KeyPurpose, at time.Time, store TrustStore) error {
	if store == nil || at.IsZero() {
		return fmt.Errorf("%w: trust store and verification time are required", ErrInvalidEvidence)
	}
	if envelope.Algorithm != "Ed25519-SHA256" || envelope.KeyID == "" {
		return fmt.Errorf("%w: unsupported signature envelope", ErrInvalidEvidence)
	}
	key, err := store.LookupTrustedKey(envelope.KeyID, purpose, at)
	if err != nil {
		return fmt.Errorf("%w: trusted key lookup: %v", ErrInvalidEvidence, err)
	}
	return VerifyDetachedSignature(raw, envelope, map[string]ed25519.PublicKey{envelope.KeyID: key})
}

// MemoryTrustStore is a test/reference implementation only. It is not durable
// and must never be used as the production trust authority.
type MemoryTrustStore struct {
	mu   sync.RWMutex
	keys map[string]TrustedKey
}

func NewMemoryTrustStore(records []TrustedKey) (*MemoryTrustStore, error) {
	s := &MemoryTrustStore{keys: make(map[string]TrustedKey, len(records))}
	for _, record := range records {
		if record.KeyID == "" || (record.Purpose == KeyPurposeObserverReceipt && record.IssuerID == "") || len(record.PublicKey) != ed25519.PublicKeySize || (record.Purpose != KeyPurposeTestPlan && record.Purpose != KeyPurposeManifest && record.Purpose != KeyPurposeHostJournal && record.Purpose != KeyPurposeObserverReceipt && record.Purpose != KeyPurposeCheckpointAcceptance && record.Purpose != KeyPurposeCheckpointReadback) ||
			record.NotBefore.IsZero() || record.NotAfter.IsZero() || !record.NotAfter.After(record.NotBefore) {
			return nil, fmt.Errorf("%w: invalid trust record", ErrInvalidEvidence)
		}
		if _, exists := s.keys[record.KeyID]; exists {
			return nil, fmt.Errorf("%w: duplicate trusted key ID", ErrInvalidEvidence)
		}
		record.PublicKey = append(ed25519.PublicKey(nil), record.PublicKey...)
		s.keys[record.KeyID] = record
	}
	return s, nil
}

func (s *MemoryTrustStore) LookupTrustedKey(keyID string, purpose KeyPurpose, at time.Time) (ed25519.PublicKey, error) {
	if s == nil {
		return nil, fmt.Errorf("nil trust store")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.keys[keyID]
	if !ok || record.Revoked || record.Purpose != purpose || at.Before(record.NotBefore) || !at.Before(record.NotAfter) {
		return nil, fmt.Errorf("key unavailable for requested purpose or time")
	}
	return append(ed25519.PublicKey(nil), record.PublicKey...), nil
}

// LookupTrustedIssuerKey enforces issuer binding in addition to key purpose,
// validity interval, and revocation. MemoryTrustStore remains test-only.
func (s *MemoryTrustStore) LookupTrustedIssuerKey(keyID, issuerID string, purpose KeyPurpose, at time.Time) (ed25519.PublicKey, error) {
	if s == nil || issuerID == "" {
		return nil, fmt.Errorf("issuer-bound trust lookup requires store and issuer")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.keys[keyID]
	if !ok || record.IssuerID != issuerID || record.Revoked || record.Purpose != purpose || at.Before(record.NotBefore) || !at.Before(record.NotAfter) {
		return nil, fmt.Errorf("key unavailable for requested issuer, purpose, or time")
	}
	return append(ed25519.PublicKey(nil), record.PublicKey...), nil
}

// NonceReservationStore must atomically and durably reserve a nonce within a
// runner scope. A successful call must survive process restart and concurrent
// callers; implementations must fail closed when durability is uncertain.
type NonceReservationStore interface {
	ReserveRunNonce(runnerID, nonce, runID string, reservedAt time.Time) error
}

// MemoryNonceStore is a concurrency-safe test/reference implementation only.
// It is not durable and is not suitable for production anti-replay protection.
type MemoryNonceStore struct {
	mu         sync.Mutex
	usedNonces map[string]string
	usedRuns   map[string]string
}

func NewMemoryNonceStore() *MemoryNonceStore {
	return &MemoryNonceStore{
		usedNonces: make(map[string]string),
		usedRuns:   make(map[string]string),
	}
}

func (s *MemoryNonceStore) ReserveRunNonce(runnerID, nonce, runID string, reservedAt time.Time) error {
	if s == nil || runnerID == "" || runID == "" || !noncePattern.MatchString(nonce) || len(nonce)%2 != 0 || reservedAt.IsZero() {
		return fmt.Errorf("%w: invalid nonce reservation", ErrInvalidEvidence)
	}
	nonceKey := runnerID + "\x00" + nonce
	runKey := runnerID + "\x00" + runID
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.usedNonces[nonceKey]; exists {
		return fmt.Errorf("%w: replayed run nonce", ErrInvalidEvidence)
	}
	if _, exists := s.usedRuns[runKey]; exists {
		return fmt.Errorf("%w: replayed run ID", ErrInvalidEvidence)
	}
	// Both indexes are committed while holding the same lock. This in-memory
	// implementation is atomic within one process, but is not crash durable.
	s.usedNonces[nonceKey] = runID
	s.usedRuns[runKey] = nonce
	return nil
}

// VerifyAndReserveManifest performs signature verification, strict manifest
// decoding, runner identity binding, then one atomic nonce reservation.
// Call only after the signature key has been independently authorized.
func VerifyAndReserveManifest(raw []byte, envelope SignatureEnvelope, store TrustStore, nonces NonceReservationStore, at time.Time) (Manifest, error) {
	var zero Manifest
	if nonces == nil {
		return zero, fmt.Errorf("%w: nonce reservation store is required", ErrInvalidEvidence)
	}
	if err := VerifyWithTrustStore(raw, envelope, KeyPurposeManifest, at, store); err != nil {
		return zero, err
	}
	manifest, err := DecodeManifest(raw)
	if err != nil {
		return zero, err
	}
	if err := nonces.ReserveRunNonce(manifest.Host.RunnerID, manifest.RunNonce, manifest.RunID, at); err != nil {
		return zero, err
	}
	return manifest, nil
}
