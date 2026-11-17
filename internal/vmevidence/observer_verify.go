package vmevidence

import (
	"fmt"
	"regexp"
)

var observerDigestPattern = regexp.MustCompile("^[0-9a-f]{64}$")

// ObserverReceiptClaim is the semantic projection of a receipt whose exact
// payload digest, signature, issuer authorization, and source measurement have
// already been verified by a separate trust-aware layer. This type performs
// no cryptography and does not establish observer independence.
type ObserverReceiptClaim struct {
	ReceiptID             string
	IssuerID              string
	RunnerID              string
	RunID                 string
	RunNonce              string
	ObserverSequence      uint64
	EventID               string
	EventType             string
	SubjectKind           string
	SubjectID             string
	SourceID              string
	MeasurementSHA256     string
	ReceiptSHA256         string
	PreviousReceiptSHA256 string
	PolicyDigestSHA256    string
}

// ObserverVerificationPolicy is explicit per-run policy. RequiredEventTypes
// maps a host-journal event type to the normalized observer event type that
// independently corroborates it. GenesisSequence and GenesisSHA256 are
// provisioned out of band for this observer stream.
type ObserverVerificationPolicy struct {
	IssuerID             string
	RunnerID             string
	RunID                string
	RunNonce             string
	PolicyDigestSHA256   string
	GenesisSequence      uint64
	GenesisSHA256        string
	RequiredEventTypes   map[string]string
	RequiredSubjectKinds map[string]string
}

// ObserverVerificationReport contains only validation results; it is not a
// signed receipt, durable checkpoint, or authorization decision.
type ObserverVerificationReport struct {
	VerifiedReceiptIDs []string
	VerifiedEventIDs   []string
	FinalSequence      uint64
	FinalReceiptSHA256 string
}

// VerifyObserverCorroboration checks an already-authenticated observer stream
// against host events. Receipts must be supplied in observer-sequence order.
// It fails closed on missing, extra/unbound, duplicate, replayed, gapped,
// misidentified, or contradictory evidence. No I/O or infrastructure action
// occurs here.
func VerifyObserverCorroboration(hostEvents []HostEventRecord, receipts []ObserverReceiptClaim, policy ObserverVerificationPolicy) (ObserverVerificationReport, error) {
	var report ObserverVerificationReport
	if policy.IssuerID == "" || policy.RunnerID == "" || policy.RunID == "" ||
		!noncePattern.MatchString(policy.RunNonce) || len(policy.RunNonce)%2 != 0 ||
		!observerDigestPattern.MatchString(policy.PolicyDigestSHA256) ||
		!observerDigestPattern.MatchString(policy.GenesisSHA256) ||
		len(policy.RequiredEventTypes) == 0 {
		return report, fmt.Errorf("%w: invalid observer verification policy", ErrInvalidEvidence)
	}
	if len(hostEvents) == 0 || len(receipts) == 0 || len(receipts) > 512 {
		return report, fmt.Errorf("%w: host events and bounded observer receipts are required", ErrInvalidEvidence)
	}

	hostByID := make(map[string]HostEventRecord, len(hostEvents))
	hostOrder := make(map[string]int, len(hostEvents))
	for i, event := range hostEvents {
		if event.EventID == "" || event.EventType == "" {
			return report, fmt.Errorf("%w: invalid host event at index %d", ErrInvalidEvidence, i)
		}
		if _, exists := hostByID[event.EventID]; exists {
			return report, fmt.Errorf("%w: duplicate host event ID", ErrInvalidEvidence)
		}
		hostByID[event.EventID] = event
		hostOrder[event.EventID] = i
	}
	for hostType, observerType := range policy.RequiredEventTypes {
		if hostType == "" || observerType == "" {
			return report, fmt.Errorf("%w: empty required event mapping", ErrInvalidEvidence)
		}
		if subjectKind := policy.RequiredSubjectKinds[hostType]; subjectKind == "" {
			return report, fmt.Errorf("%w: missing required subject-kind mapping for %q", ErrInvalidEvidence, hostType)
		}
	}

	seenReceiptIDs := make(map[string]struct{}, len(receipts))
	seenEventIDs := make(map[string]struct{}, len(receipts))
	seenSequences := make(map[uint64]struct{}, len(receipts))
	verifiedRequired := make(map[string]struct{}, len(policy.RequiredEventTypes))
	previousDigest := policy.GenesisSHA256
	expectedSequence := policy.GenesisSequence
	lastHostIndex := -1
	vmSubjectID := ""

	for i, receipt := range receipts {
		if receipt.ReceiptID == "" || receipt.IssuerID != policy.IssuerID ||
			receipt.RunnerID != policy.RunnerID || receipt.RunID != policy.RunID ||
			receipt.RunNonce != policy.RunNonce || receipt.EventID == "" ||
			receipt.EventType == "" || receipt.SubjectKind == "" || receipt.SubjectID == "" ||
			receipt.SourceID == "" || !observerDigestPattern.MatchString(receipt.MeasurementSHA256) ||
			!observerDigestPattern.MatchString(receipt.ReceiptSHA256) ||
			!observerDigestPattern.MatchString(receipt.PreviousReceiptSHA256) ||
			receipt.PolicyDigestSHA256 != policy.PolicyDigestSHA256 {
			return report, fmt.Errorf("%w: invalid observer receipt identity or fields at index %d", ErrInvalidEvidence, i)
		}
		if _, exists := seenReceiptIDs[receipt.ReceiptID]; exists {
			return report, fmt.Errorf("%w: duplicate observer receipt ID", ErrInvalidEvidence)
		}
		if _, exists := seenEventIDs[receipt.EventID]; exists {
			return report, fmt.Errorf("%w: duplicate observer event ID", ErrInvalidEvidence)
		}
		if _, exists := seenSequences[receipt.ObserverSequence]; exists {
			return report, fmt.Errorf("%w: duplicate observer sequence", ErrInvalidEvidence)
		}
		if expectedSequence == ^uint64(0) || receipt.ObserverSequence != expectedSequence+1 {
			return report, fmt.Errorf("%w: observer sequence gap, replay, or overflow", ErrInvalidEvidence)
		}
		if receipt.PreviousReceiptSHA256 != previousDigest {
			return report, fmt.Errorf("%w: observer receipt predecessor mismatch", ErrInvalidEvidence)
		}
		hostEvent, ok := hostByID[receipt.EventID]
		if !ok {
			return report, fmt.Errorf("%w: unbound observer event %q", ErrInvalidEvidence, receipt.EventID)
		}
		requiredObserverType, required := policy.RequiredEventTypes[hostEvent.EventType]
		if !required || receipt.EventType != requiredObserverType {
			return report, fmt.Errorf("%w: observer event type does not corroborate host event %q", ErrInvalidEvidence, receipt.EventID)
		}
		if receipt.SubjectKind != policy.RequiredSubjectKinds[hostEvent.EventType] {
			return report, fmt.Errorf("%w: observer subject kind does not match host event %q", ErrInvalidEvidence, receipt.EventID)
		}
		if _, exists := verifiedRequired[receipt.EventID]; exists {
			return report, fmt.Errorf("%w: multiple observer receipts for one host event", ErrInvalidEvidence)
		}
		index := hostOrder[receipt.EventID]
		if index <= lastHostIndex {
			return report, fmt.Errorf("%w: observer order contradicts host event order", ErrInvalidEvidence)
		}
		lastHostIndex = index
		if receipt.SubjectKind == "VM_INSTANCE" {
			if vmSubjectID == "" {
				vmSubjectID = receipt.SubjectID
			} else if receipt.SubjectID != vmSubjectID {
				return report, fmt.Errorf("%w: VM subject identity changed across lifecycle events", ErrInvalidEvidence)
			}
		}

		seenReceiptIDs[receipt.ReceiptID] = struct{}{}
		seenEventIDs[receipt.EventID] = struct{}{}
		seenSequences[receipt.ObserverSequence] = struct{}{}
		verifiedRequired[receipt.EventID] = struct{}{}
		report.VerifiedReceiptIDs = append(report.VerifiedReceiptIDs, receipt.ReceiptID)
		report.VerifiedEventIDs = append(report.VerifiedEventIDs, receipt.EventID)
		expectedSequence = receipt.ObserverSequence
		previousDigest = receipt.ReceiptSHA256
	}

	for _, event := range hostEvents {
		if _, required := policy.RequiredEventTypes[event.EventType]; !required {
			continue
		}
		if _, ok := verifiedRequired[event.EventID]; !ok {
			return ObserverVerificationReport{}, fmt.Errorf("%w: missing observer corroboration for host event %q", ErrInvalidEvidence, event.EventID)
		}
	}
	report.FinalSequence = expectedSequence
	report.FinalReceiptSHA256 = previousDigest
	return report, nil
}
