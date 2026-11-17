package vmevidence

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type CheckpointHead struct {
	Sequence      uint64 `json:"sequence"`
	JournalSHA256 string `json:"journal_sha256"`
}
type CheckpointAdvanceRequest struct {
	SchemaVersion      string         `json:"schema_version"`
	RequestID          string         `json:"request_id"`
	AuthorityEpoch     uint64         `json:"authority_epoch"`
	RunnerID           string         `json:"runner_id"`
	RunID              string         `json:"run_id"`
	RunNonce           string         `json:"run_nonce"`
	Expected           CheckpointHead `json:"expected"`
	Next               CheckpointHead `json:"next"`
	ObserverReceiptIDs []string       `json:"observer_receipt_ids"`
	PolicyDigestSHA256 string         `json:"policy_digest_sha256"`
}
type CheckpointAcceptanceReceiptPayload struct {
	SchemaVersion         string   `json:"schema_version"`
	ReceiptID             string   `json:"receipt_id"`
	IssuerID              string   `json:"issuer_id"`
	RequestID             string   `json:"request_id"`
	RequestSHA256         string   `json:"request_sha256"`
	RunnerID              string   `json:"runner_id"`
	RunID                 string   `json:"run_id"`
	RunNonce              string   `json:"run_nonce"`
	AuthorityEpoch        uint64   `json:"authority_epoch"`
	CommitSequence        uint64   `json:"commit_sequence"`
	AcceptedJournalSHA256 string   `json:"accepted_journal_sha256"`
	PreviousJournalSHA256 string   `json:"previous_journal_sha256"`
	ObserverReceiptIDs    []string `json:"observer_receipt_ids"`
	Decision              string   `json:"decision"`
	CommittedAtUTC        string   `json:"committed_at_utc"`
	DurabilityClass       string   `json:"durability_class"`
	FencingToken          uint64   `json:"fencing_token"`
}
type VerifiedCheckpointAcceptance struct {
	ReceiptID          string
	IssuerID           string
	RequestSHA256      string
	AcceptedHead       HostJournalCheckpoint
	AuthorityEpoch     uint64
	FencingToken       uint64
	DurabilityClass    string
	ObserverReceiptIDs []string
}

func CanonicalCheckpointAdvanceRequest(r CheckpointAdvanceRequest) ([]byte, error) {
	if r.SchemaVersion != "1.0" || r.RequestID == "" || len(r.RequestID) > 128 || r.AuthorityEpoch == 0 ||
		r.RunnerID == "" || r.RunID == "" || !noncePattern.MatchString(r.RunNonce) || len(r.RunNonce)%2 != 0 ||
		!sha256Pattern.MatchString(r.Expected.JournalSHA256) || !sha256Pattern.MatchString(r.Next.JournalSHA256) ||
		r.Expected.Sequence == ^uint64(0) || r.Next.Sequence != r.Expected.Sequence+1 ||
		len(r.ObserverReceiptIDs) == 0 || len(r.ObserverReceiptIDs) > 512 || !sha256Pattern.MatchString(r.PolicyDigestSHA256) {
		return nil, fmt.Errorf("%w: invalid checkpoint advance request", ErrInvalidEvidence)
	}
	seen := map[string]struct{}{}
	for _, id := range r.ObserverReceiptIDs {
		if id == "" || len(id) > 128 {
			return nil, fmt.Errorf("%w: invalid observer receipt ID", ErrInvalidEvidence)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("%w: duplicate observer receipt ID", ErrInvalidEvidence)
		}
		seen[id] = struct{}{}
	}
	return json.Marshal(r)
}
func DecodeCheckpointAcceptanceReceipt(raw []byte) (CheckpointAcceptanceReceiptPayload, error) {
	var p CheckpointAcceptanceReceiptPayload
	if err := ValidateStrictJSON(raw); err != nil {
		return p, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("%w: checkpoint acceptance schema: %v", ErrInvalidEvidence, err)
	}
	canonical, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	if !bytes.Equal(raw, canonical) {
		return p, fmt.Errorf("%w: checkpoint acceptance is not canonical JSON", ErrInvalidEvidence)
	}
	if p.SchemaVersion != "1.0" || p.ReceiptID == "" || p.IssuerID == "" || p.RequestID == "" || len(p.RequestID) > 128 ||
		!sha256Pattern.MatchString(p.RequestSHA256) || p.RunnerID == "" || p.RunID == "" ||
		!noncePattern.MatchString(p.RunNonce) || len(p.RunNonce)%2 != 0 || p.AuthorityEpoch == 0 || p.CommitSequence == 0 ||
		!sha256Pattern.MatchString(p.AcceptedJournalSHA256) || !sha256Pattern.MatchString(p.PreviousJournalSHA256) ||
		len(p.ObserverReceiptIDs) == 0 || len(p.ObserverReceiptIDs) > 512 || p.Decision != "ACCEPTED" ||
		p.DurabilityClass == "" || len(p.DurabilityClass) > 64 || p.FencingToken == 0 {
		return CheckpointAcceptanceReceiptPayload{}, fmt.Errorf("%w: invalid checkpoint acceptance fields", ErrInvalidEvidence)
	}
	seen := map[string]struct{}{}
	for _, id := range p.ObserverReceiptIDs {
		if id == "" || len(id) > 128 {
			return CheckpointAcceptanceReceiptPayload{}, fmt.Errorf("%w: invalid observer receipt ID", ErrInvalidEvidence)
		}
		if _, ok := seen[id]; ok {
			return CheckpointAcceptanceReceiptPayload{}, fmt.Errorf("%w: duplicate observer receipt ID", ErrInvalidEvidence)
		}
		seen[id] = struct{}{}
	}
	committed, err := time.Parse(time.RFC3339Nano, p.CommittedAtUTC)
	if err != nil || committed.Location() != time.UTC || committed.Format(time.RFC3339Nano) != p.CommittedAtUTC {
		return CheckpointAcceptanceReceiptPayload{}, fmt.Errorf("%w: committed_at_utc must be canonical RFC3339 UTC", ErrInvalidEvidence)
	}
	return p, nil
}
func VerifyCheckpointAcceptanceReceipt(raw []byte, envelope SignatureEnvelope, request CheckpointAdvanceRequest, claims []ObserverReceiptClaim, store TrustStore, at time.Time) (VerifiedCheckpointAcceptance, error) {
	var result VerifiedCheckpointAcceptance
	p, err := DecodeCheckpointAcceptanceReceipt(raw)
	if err != nil {
		return result, err
	}
	requestBytes, err := CanonicalCheckpointAdvanceRequest(request)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(requestBytes)
	requestSHA := hex.EncodeToString(digest[:])
	if p.RequestSHA256 != requestSHA || p.RequestID != request.RequestID || p.RunnerID != request.RunnerID || p.RunID != request.RunID ||
		p.RunNonce != request.RunNonce || p.AuthorityEpoch != request.AuthorityEpoch || p.CommitSequence != request.Next.Sequence ||
		p.AcceptedJournalSHA256 != request.Next.JournalSHA256 || p.PreviousJournalSHA256 != request.Expected.JournalSHA256 ||
		!sameStrings(p.ObserverReceiptIDs, request.ObserverReceiptIDs) {
		return result, fmt.Errorf("%w: acceptance does not bind exact advance request", ErrInvalidEvidence)
	}
	if len(claims) != len(request.ObserverReceiptIDs) {
		return result, fmt.Errorf("%w: observer claim count mismatch", ErrInvalidEvidence)
	}
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ReceiptID
		if c.RunnerID != request.RunnerID || c.RunID != request.RunID || c.RunNonce != request.RunNonce || c.PolicyDigestSHA256 != request.PolicyDigestSHA256 {
			return result, fmt.Errorf("%w: observer claim identity or policy mismatch", ErrInvalidEvidence)
		}
	}
	if !sameStrings(ids, request.ObserverReceiptIDs) {
		return result, fmt.Errorf("%w: observer claim IDs differ from request", ErrInvalidEvidence)
	}
	issuerStore, ok := store.(IssuerTrustStore)
	if !ok || store == nil || at.IsZero() {
		return result, fmt.Errorf("%w: issuer-bound authority trust and verification time required", ErrInvalidEvidence)
	}
	key, err := issuerStore.LookupTrustedIssuerKey(envelope.KeyID, p.IssuerID, KeyPurposeCheckpointAcceptance, at)
	if err != nil {
		return result, fmt.Errorf("%w: authority key lookup: %v", ErrInvalidEvidence, err)
	}
	if err := VerifyDetachedSignature(raw, envelope, map[string]ed25519.PublicKey{envelope.KeyID: key}); err != nil {
		return result, err
	}
	return VerifiedCheckpointAcceptance{ReceiptID: p.ReceiptID, IssuerID: p.IssuerID, RequestSHA256: requestSHA, AcceptedHead: HostJournalCheckpoint{RunnerID: request.RunnerID, RunID: request.RunID, RunNonce: request.RunNonce, Sequence: request.Next.Sequence, JournalSHA256: request.Next.JournalSHA256}, AuthorityEpoch: p.AuthorityEpoch, FencingToken: p.FencingToken, DurabilityClass: p.DurabilityClass, ObserverReceiptIDs: append([]string(nil), p.ObserverReceiptIDs...)}, nil
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
