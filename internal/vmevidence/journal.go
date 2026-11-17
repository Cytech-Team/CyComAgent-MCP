package vmevidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// HostEventJournal is an independently signed host-side event record.
// Its bytes and signing envelope are separate from the manifest.
type HostEventJournal struct {
	SchemaVersion         string            `json:"schema_version"`
	RunnerID              string            `json:"runner_id"`
	RunID                 string            `json:"run_id"`
	RunNonce              string            `json:"run_nonce"`
	JournalSequence       uint64            `json:"journal_sequence"`
	PreviousJournalSHA256 string            `json:"previous_journal_sha256"`
	Events                []HostEventRecord `json:"events"`
}

type HostJournalVerification struct {
	EventCount int
	RunnerID   string
	RunID      string
}

// HostJournalCheckpoint must come from storage protected independently from
// the journal stream (for example, an externally administered append-only log).
// An anchor stored beside the journal cannot prevent coordinated rollback.
type HostJournalCheckpoint struct {
	RunnerID      string
	RunID         string
	RunNonce      string
	Sequence      uint64
	JournalSHA256 string
}

// HostJournalChainVerification returns the checkpoint to persist atomically
// in the independent checkpoint authority after successful verification.
type HostJournalChainVerification struct {
	HostJournalVerification
	NextCheckpoint HostJournalCheckpoint
}

// VerifyHostEventJournalChain verifies a signed journal against an externally
// trusted predecessor checkpoint. The caller must atomically persist the
// returned checkpoint; this function performs no storage or I/O.
func VerifyHostEventJournalChain(raw []byte, envelope SignatureEnvelope, at time.Time, store TrustStore, manifest Manifest, checkpoint HostJournalCheckpoint) (HostJournalChainVerification, error) {
	var result HostJournalChainVerification
	if checkpoint.RunnerID != manifest.Host.RunnerID || checkpoint.RunID != manifest.RunID || checkpoint.RunNonce != manifest.RunNonce {
		return result, fmt.Errorf("%w: journal checkpoint identity mismatch", ErrInvalidEvidence)
	}
	if checkpoint.Sequence == ^uint64(0) || !sha256Pattern.MatchString(checkpoint.JournalSHA256) {
		return result, fmt.Errorf("%w: invalid journal checkpoint", ErrInvalidEvidence)
	}
	base, err := VerifyHostEventJournal(raw, envelope, at, store, manifest)
	if err != nil {
		return result, err
	}
	var journal HostEventJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return result, fmt.Errorf("%w: decode verified journal: %v", ErrInvalidEvidence, err)
	}
	if journal.JournalSequence != checkpoint.Sequence+1 || journal.PreviousJournalSHA256 != checkpoint.JournalSHA256 {
		return result, fmt.Errorf("%w: journal chain gap, replay, or predecessor mismatch", ErrInvalidEvidence)
	}
	digest := ManifestDigest(raw)
	next := HostJournalCheckpoint{RunnerID: journal.RunnerID, RunID: journal.RunID, RunNonce: journal.RunNonce, Sequence: journal.JournalSequence, JournalSHA256: digest}
	return HostJournalChainVerification{HostJournalVerification: base, NextCheckpoint: next}, nil
}

// VerifyHostEventJournal verifies a separately signed, canonical journal and
// requires an exact ordered match with the manifest's host_events. It proves
// signature and record consistency, not that the signing host was uncompromised.
func VerifyHostEventJournal(raw []byte, envelope SignatureEnvelope, at time.Time, store TrustStore, manifest Manifest) (HostJournalVerification, error) {
	var report HostJournalVerification
	if store == nil || at.IsZero() {
		return report, fmt.Errorf("%w: journal trust store and verification time required", ErrInvalidEvidence)
	}
	if err := VerifyWithTrustStore(raw, envelope, KeyPurposeHostJournal, at, store); err != nil {
		return report, err
	}
	if err := validateManifestStructure(manifest); err != nil {
		return report, err
	}
	if err := ValidateStrictJSON(raw); err != nil {
		return report, err
	}
	var journal HostEventJournal
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return report, fmt.Errorf("%w: host journal schema: %v", ErrInvalidEvidence, err)
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(journal); err != nil {
		return report, fmt.Errorf("%w: host journal canonical encoding: %v", ErrInvalidEvidence, err)
	}
	if !bytes.Equal(raw, bytes.TrimSuffix(canonical.Bytes(), []byte("\n"))) {
		return report, fmt.Errorf("%w: host journal is not canonical JSON", ErrInvalidEvidence)
	}
	if journal.SchemaVersion != "1.0" || journal.RunnerID == "" || journal.RunID == "" ||
		!noncePattern.MatchString(journal.RunNonce) || len(journal.RunNonce)%2 != 0 ||
		len(journal.Events) == 0 || len(journal.Events) > 1024 {
		return report, fmt.Errorf("%w: invalid host journal identity or event count", ErrInvalidEvidence)
	}
	if journal.RunnerID != manifest.Host.RunnerID || journal.RunID != manifest.RunID || journal.RunNonce != manifest.RunNonce {
		return report, fmt.Errorf("%w: host journal run identity mismatch", ErrInvalidEvidence)
	}
	if len(journal.Events) != len(manifest.HostEvents) {
		return report, fmt.Errorf("%w: host journal event count mismatch", ErrInvalidEvidence)
	}
	var last uint64
	seen := make(map[string]struct{}, len(journal.Events))
	for i, event := range journal.Events {
		if event.EventID == "" || !inSet(hostEventTypes, event.EventType) || event.CaseID == "" ||
			event.Actor != "HOST_RUNNER" || event.Result == "" {
			return report, fmt.Errorf("%w: invalid journal event at index %d", ErrInvalidEvidence, i)
		}
		if _, exists := seen[event.EventID]; exists {
			return report, fmt.Errorf("%w: duplicate journal event ID", ErrInvalidEvidence)
		}
		seen[event.EventID] = struct{}{}
		if i > 0 && event.HostMonotonicNS < last {
			return report, fmt.Errorf("%w: host journal monotonic time decreased", ErrInvalidEvidence)
		}
		last = event.HostMonotonicNS
		if event != manifest.HostEvents[i] {
			return report, fmt.Errorf("%w: host journal does not exactly match manifest event %d", ErrInvalidEvidence, i)
		}
	}
	if !validLifecycleOrder(journal.Events) {
		return report, fmt.Errorf("%w: host journal lifecycle order invalid", ErrInvalidEvidence)
	}
	return HostJournalVerification{EventCount: len(journal.Events), RunnerID: journal.RunnerID, RunID: journal.RunID}, nil
}

func validLifecycleOrder(events []HostEventRecord) bool {
	start, vmDestroy, overlayDestroy := -1, -1, -1
	for i, event := range events {
		switch event.EventType {
		case "VM_START":
			if start != -1 {
				return false
			}
			start = i
		case "VM_DESTROY":
			if vmDestroy != -1 {
				return false
			}
			vmDestroy = i
		case "OVERLAY_DESTROY":
			if overlayDestroy != -1 {
				return false
			}
			overlayDestroy = i
		}
	}
	return start >= 0 && vmDestroy > start && overlayDestroy > vmDestroy
}
