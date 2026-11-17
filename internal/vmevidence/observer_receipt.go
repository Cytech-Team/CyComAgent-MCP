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

type ObserverReceiptPayload struct {
	SchemaVersion         string                     `json:"schema_version"`
	ReceiptID             string                     `json:"receipt_id"`
	IssuerID              string                     `json:"issuer_id"`
	RunnerID              string                     `json:"runner_id"`
	RunID                 string                     `json:"run_id"`
	RunNonce              string                     `json:"run_nonce"`
	ObserverSequence      uint64                     `json:"observer_sequence"`
	EventID               string                     `json:"event_id"`
	EventType             string                     `json:"event_type"`
	Subject               ObserverReceiptSubject     `json:"subject"`
	Measurement           ObserverReceiptMeasurement `json:"measurement"`
	ObservedAtUTC         string                     `json:"observed_at_utc"`
	ClockUncertaintyMS    uint64                     `json:"clock_uncertainty_ms"`
	PreviousReceiptSHA256 string                     `json:"previous_receipt_sha256"`
	PolicyDigestSHA256    string                     `json:"policy_digest_sha256"`
}
type ObserverReceiptSubject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type ObserverReceiptMeasurement struct {
	Kind        string `json:"kind"`
	ValueSHA256 string `json:"value_sha256"`
	SourceID    string `json:"source_id"`
}

func DecodeObserverReceipt(raw []byte) (ObserverReceiptPayload, error) {
	var payload ObserverReceiptPayload
	if err := ValidateStrictJSON(raw); err != nil {
		return payload, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		return ObserverReceiptPayload{}, fmt.Errorf("%w: observer receipt schema: %v", ErrInvalidEvidence, err)
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return ObserverReceiptPayload{}, fmt.Errorf("%w: observer receipt encoding: %v", ErrInvalidEvidence, err)
	}
	if !bytes.Equal(raw, canonical) {
		return ObserverReceiptPayload{}, fmt.Errorf("%w: observer receipt is not canonical V1 JSON", ErrInvalidEvidence)
	}
	if payload.SchemaVersion != "1.0" || payload.ReceiptID == "" || payload.IssuerID == "" || payload.RunnerID == "" || payload.RunID == "" ||
		!noncePattern.MatchString(payload.RunNonce) || len(payload.RunNonce)%2 != 0 || payload.ObserverSequence == 0 ||
		payload.EventID == "" || payload.EventType == "" || payload.Subject.Kind == "" || payload.Subject.ID == "" ||
		payload.Measurement.Kind == "" || payload.Measurement.SourceID == "" || !sha256Pattern.MatchString(payload.Measurement.ValueSHA256) ||
		!sha256Pattern.MatchString(payload.PreviousReceiptSHA256) || !sha256Pattern.MatchString(payload.PolicyDigestSHA256) || payload.ClockUncertaintyMS > 300000 {
		return ObserverReceiptPayload{}, fmt.Errorf("%w: invalid observer receipt fields or bounds", ErrInvalidEvidence)
	}
	observed, err := time.Parse(time.RFC3339Nano, payload.ObservedAtUTC)
	if err != nil || observed.Location() != time.UTC || observed.Format(time.RFC3339Nano) != payload.ObservedAtUTC {
		return ObserverReceiptPayload{}, fmt.Errorf("%w: observed_at_utc must be canonical RFC3339 UTC", ErrInvalidEvidence)
	}
	return payload, nil
}

func VerifyObserverReceipt(raw []byte, envelope SignatureEnvelope, sourceMeasurement []byte, store TrustStore, at time.Time) (ObserverReceiptClaim, error) {
	var claim ObserverReceiptClaim
	payload, err := DecodeObserverReceipt(raw)
	if err != nil {
		return claim, err
	}
	if len(sourceMeasurement) == 0 || len(sourceMeasurement) > MaxSingleArtifactBytes {
		return claim, fmt.Errorf("%w: source measurement bytes are missing or exceed the artifact size bound", ErrInvalidEvidence)
	}
	measurementDigest := sha256.Sum256(sourceMeasurement)
	if hex.EncodeToString(measurementDigest[:]) != payload.Measurement.ValueSHA256 {
		return claim, fmt.Errorf("%w: observer source measurement digest mismatch", ErrInvalidEvidence)
	}
	issuerStore, ok := store.(IssuerTrustStore)
	if !ok || store == nil || at.IsZero() {
		return claim, fmt.Errorf("%w: issuer-bound observer trust store and verification time are required", ErrInvalidEvidence)
	}
	key, err := issuerStore.LookupTrustedIssuerKey(envelope.KeyID, payload.IssuerID, KeyPurposeObserverReceipt, at)
	if err != nil {
		return claim, fmt.Errorf("%w: observer issuer key lookup: %v", ErrInvalidEvidence, err)
	}
	if err := VerifyDetachedSignature(raw, envelope, map[string]ed25519.PublicKey{envelope.KeyID: key}); err != nil {
		return claim, err
	}
	receiptDigest := sha256.Sum256(raw)
	return ObserverReceiptClaim{
		ReceiptID: payload.ReceiptID, IssuerID: payload.IssuerID, RunnerID: payload.RunnerID, RunID: payload.RunID,
		RunNonce: payload.RunNonce, ObserverSequence: payload.ObserverSequence, EventID: payload.EventID, EventType: payload.EventType,
		SubjectKind: payload.Subject.Kind, SubjectID: payload.Subject.ID, SourceID: payload.Measurement.SourceID,
		MeasurementSHA256: payload.Measurement.ValueSHA256, ReceiptSHA256: hex.EncodeToString(receiptDigest[:]),
		PreviousReceiptSHA256: payload.PreviousReceiptSHA256, PolicyDigestSHA256: payload.PolicyDigestSHA256,
	}, nil
}
