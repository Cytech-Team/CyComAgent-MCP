package vmevidence

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CheckpointReadbackPayload is an authority-signed snapshot, not a caller
// assertion. SupersedesAcceptanceReceiptIDs is an explicit authority claim
// linking a newer head to receipts it has superseded.
type CheckpointReadbackPayload struct {
	SchemaVersion                  string   `json:"schema_version"`
	ReceiptID                      string   `json:"receipt_id"`
	IssuerID                       string   `json:"issuer_id"`
	RunnerID                       string   `json:"runner_id"`
	RunID                          string   `json:"run_id"`
	RunNonce                       string   `json:"run_nonce"`
	AuthorityEpoch                 uint64   `json:"authority_epoch"`
	FencingToken                   uint64   `json:"fencing_token"`
	HeadSequence                   uint64   `json:"head_sequence"`
	HeadJournalSHA256              string   `json:"head_journal_sha256"`
	ReadAtUTC                      string   `json:"read_at_utc"`
	SupersedesAcceptanceReceiptIDs []string `json:"supersedes_acceptance_receipt_ids"`
}

type CheckpointReadbackStatus string

const (
	CheckpointReadbackCurrent    CheckpointReadbackStatus = "CURRENT"
	CheckpointReadbackSuperseded CheckpointReadbackStatus = "SUPERSEDED"
	CheckpointReadbackUnknown    CheckpointReadbackStatus = "UNKNOWN"
	CheckpointReadbackCorrupt    CheckpointReadbackStatus = "CORRUPT_OR_ROLLBACK_SUSPECTED"
)

type VerifiedCheckpointReadback struct {
	Status         CheckpointReadbackStatus
	ReceiptID      string
	IssuerID       string
	Head           HostJournalCheckpoint
	AuthorityEpoch uint64
	FencingToken   uint64
	ReadAt         time.Time
}

func DecodeCheckpointReadback(raw []byte) (CheckpointReadbackPayload, error) {
	var p CheckpointReadbackPayload
	if err := ValidateStrictJSON(raw); err != nil {
		return p, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("%w: checkpoint read-back schema: %v", ErrInvalidEvidence, err)
	}
	canonical, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	if !bytes.Equal(raw, canonical) {
		return p, fmt.Errorf("%w: checkpoint read-back is not canonical JSON", ErrInvalidEvidence)
	}
	if p.SchemaVersion != "1.0" || p.ReceiptID == "" || p.IssuerID == "" || p.RunnerID == "" || p.RunID == "" ||
		!noncePattern.MatchString(p.RunNonce) || len(p.RunNonce)%2 != 0 || p.AuthorityEpoch == 0 || p.FencingToken == 0 ||
		!sha256Pattern.MatchString(p.HeadJournalSHA256) || p.HeadSequence == 0 || len(p.SupersedesAcceptanceReceiptIDs) > 512 {
		return CheckpointReadbackPayload{}, fmt.Errorf("%w: invalid checkpoint read-back fields", ErrInvalidEvidence)
	}
	seen := map[string]struct{}{}
	for _, id := range p.SupersedesAcceptanceReceiptIDs {
		if id == "" || len(id) > 128 {
			return CheckpointReadbackPayload{}, fmt.Errorf("%w: invalid superseded receipt ID", ErrInvalidEvidence)
		}
		if _, ok := seen[id]; ok {
			return CheckpointReadbackPayload{}, fmt.Errorf("%w: duplicate superseded receipt ID", ErrInvalidEvidence)
		}
		seen[id] = struct{}{}
	}
	readAt, err := time.Parse(time.RFC3339Nano, p.ReadAtUTC)
	if err != nil || readAt.Location() != time.UTC || readAt.Format(time.RFC3339Nano) != p.ReadAtUTC {
		return CheckpointReadbackPayload{}, fmt.Errorf("%w: read_at_utc must be canonical RFC3339 UTC", ErrInvalidEvidence)
	}
	return p, nil
}

// VerifyCheckpointReadback authenticates an authority snapshot and classifies
// it relative to an already-verified acceptance. A higher sequence is only
// SUPERSEDED when the signed snapshot explicitly names that acceptance receipt.
func VerifyCheckpointReadback(raw []byte, envelope SignatureEnvelope, accepted VerifiedCheckpointAcceptance, store TrustStore, at time.Time) (VerifiedCheckpointReadback, error) {
	var result VerifiedCheckpointReadback
	p, err := DecodeCheckpointReadback(raw)
	if err != nil {
		return result, err
	}
	if store == nil || at.IsZero() {
		return result, fmt.Errorf("%w: authority trust and verification time required", ErrInvalidEvidence)
	}
	issuerStore, ok := store.(IssuerTrustStore)
	if !ok {
		return result, fmt.Errorf("%w: issuer-bound authority trust store required", ErrInvalidEvidence)
	}
	key, err := issuerStore.LookupTrustedIssuerKey(envelope.KeyID, p.IssuerID, KeyPurposeCheckpointReadback, at)
	if err != nil {
		return result, fmt.Errorf("%w: read-back authority key lookup: %v", ErrInvalidEvidence, err)
	}
	if err := VerifyDetachedSignature(raw, envelope, map[string]ed25519.PublicKey{envelope.KeyID: key}); err != nil {
		return result, err
	}
	if p.RunnerID != accepted.AcceptedHead.RunnerID || p.RunID != accepted.AcceptedHead.RunID || p.RunNonce != accepted.AcceptedHead.RunNonce || p.IssuerID != accepted.IssuerID {
		return result, fmt.Errorf("%w: read-back identity differs from acceptance", ErrInvalidEvidence)
	}
	result = VerifiedCheckpointReadback{ReceiptID: p.ReceiptID, IssuerID: p.IssuerID, Head: HostJournalCheckpoint{RunnerID: p.RunnerID, RunID: p.RunID, RunNonce: p.RunNonce, Sequence: p.HeadSequence, JournalSHA256: p.HeadJournalSHA256}, AuthorityEpoch: p.AuthorityEpoch, FencingToken: p.FencingToken}
	result.ReadAt, _ = time.Parse(time.RFC3339Nano, p.ReadAtUTC)
	if p.AuthorityEpoch != accepted.AuthorityEpoch || p.FencingToken < accepted.FencingToken || p.HeadSequence < accepted.AcceptedHead.Sequence {
		result.Status = CheckpointReadbackCorrupt
		return result, nil
	}
	if p.HeadSequence == accepted.AcceptedHead.Sequence {
		if p.HeadJournalSHA256 == accepted.AcceptedHead.JournalSHA256 {
			result.Status = CheckpointReadbackCurrent
		} else {
			result.Status = CheckpointReadbackCorrupt
		}
		return result, nil
	}
	for _, id := range p.SupersedesAcceptanceReceiptIDs {
		if id == accepted.ReceiptID {
			result.Status = CheckpointReadbackSuperseded
			return result, nil
		}
	}
	result.Status = CheckpointReadbackCorrupt
	return result, nil
}

// ClassifyCheckpointReadbackError maps authority transport/storage outcomes to
// a conservative status. NOT_FOUND is UNKNOWN after an uncertain commit: it
// is not proof that no commit happened unless a stronger authority contract
// establishes that guarantee.
func ClassifyCheckpointReadbackError(err error) CheckpointReadbackStatus {
	if err == nil {
		return CheckpointReadbackUnknown
	}
	if errors.Is(err, ErrCheckpointCorrupt) {
		return CheckpointReadbackCorrupt
	}
	if errors.Is(err, ErrCheckpointOutcomeUnknown) || errors.Is(err, ErrCheckpointUnavailable) || errors.Is(err, ErrCheckpointNotFound) {
		return CheckpointReadbackUnknown
	}
	return CheckpointReadbackUnknown
}
