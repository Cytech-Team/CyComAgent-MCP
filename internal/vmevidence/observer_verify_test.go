package vmevidence

import (
	"errors"
	"strings"
	"testing"
)

const (
	testObserverGenesis = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testObserverPolicy  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testObserverDigest1 = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testObserverDigest2 = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	testObserverMeasure = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

func observerTestFixture() ([]HostEventRecord, []ObserverReceiptClaim, ObserverVerificationPolicy) {
	host := []HostEventRecord{
		{EventID: "h-start", EventType: "VM_START", Actor: "HOST_RUNNER", Result: "RECORDED"},
		{EventID: "h-destroy", EventType: "VM_DESTROY", Actor: "HOST_RUNNER", Result: "RECORDED"},
		{EventID: "h-overlay", EventType: "OVERLAY_DESTROY", Actor: "HOST_RUNNER", Result: "RECORDED"},
	}
	receipts := []ObserverReceiptClaim{
		{ReceiptID: "o-start", IssuerID: "observer-a", RunnerID: "runner-a", RunID: "run-a", RunNonce: strings.Repeat("12", 32), ObserverSequence: 11, EventID: "h-start", EventType: "VM_START_OBSERVED", SubjectKind: "VM_INSTANCE", SubjectID: "vm-1", SourceID: "source-a", MeasurementSHA256: testObserverMeasure, ReceiptSHA256: testObserverDigest1, PreviousReceiptSHA256: testObserverGenesis, PolicyDigestSHA256: testObserverPolicy},
		{ReceiptID: "o-destroy", IssuerID: "observer-a", RunnerID: "runner-a", RunID: "run-a", RunNonce: strings.Repeat("12", 32), ObserverSequence: 12, EventID: "h-destroy", EventType: "VM_DESTROYED", SubjectKind: "VM_INSTANCE", SubjectID: "vm-1", SourceID: "source-a", MeasurementSHA256: testObserverMeasure, ReceiptSHA256: testObserverDigest2, PreviousReceiptSHA256: testObserverDigest1, PolicyDigestSHA256: testObserverPolicy},
		{ReceiptID: "o-overlay", IssuerID: "observer-a", RunnerID: "runner-a", RunID: "run-a", RunNonce: strings.Repeat("12", 32), ObserverSequence: 13, EventID: "h-overlay", EventType: "OVERLAY_DESTROYED", SubjectKind: "OVERLAY", SubjectID: "overlay-1", SourceID: "source-a", MeasurementSHA256: testObserverMeasure, ReceiptSHA256: strings.Repeat("f", 64), PreviousReceiptSHA256: testObserverDigest2, PolicyDigestSHA256: testObserverPolicy},
	}
	policy := ObserverVerificationPolicy{
		IssuerID: "observer-a", RunnerID: "runner-a", RunID: "run-a", RunNonce: strings.Repeat("12", 32),
		PolicyDigestSHA256: testObserverPolicy, GenesisSequence: 10, GenesisSHA256: testObserverGenesis,
		RequiredEventTypes:   map[string]string{"VM_START": "VM_START_OBSERVED", "VM_DESTROY": "VM_DESTROYED", "OVERLAY_DESTROY": "OVERLAY_DESTROYED"},
		RequiredSubjectKinds: map[string]string{"VM_START": "VM_INSTANCE", "VM_DESTROY": "VM_INSTANCE", "OVERLAY_DESTROY": "OVERLAY"},
	}
	return host, receipts, policy
}

func TestVerifyObserverCorroborationValidStream(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	got, err := VerifyObserverCorroboration(host, receipts, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.VerifiedReceiptIDs) != 3 || got.FinalSequence != 13 || got.FinalReceiptSHA256 != strings.Repeat("f", 64) {
		t.Fatalf("unexpected report: %+v", got)
	}
}

func TestVerifyObserverCorroborationRejectsMissingReceipt(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	_, err := VerifyObserverCorroboration(host, receipts[:2], policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "missing observer") {
		t.Fatalf("expected missing observer rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsDuplicateReceiptAndEvent(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	duplicateID := append([]ObserverReceiptClaim(nil), receipts...)
	duplicateID[1].ReceiptID = duplicateID[0].ReceiptID
	_, err := VerifyObserverCorroboration(host, duplicateID, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "duplicate observer receipt ID") {
		t.Fatalf("expected duplicate receipt rejection, got %v", err)
	}

	duplicateEvent := append([]ObserverReceiptClaim(nil), receipts...)
	duplicateEvent[1].EventID = duplicateEvent[0].EventID
	_, err = VerifyObserverCorroboration(host, duplicateEvent, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "duplicate observer event ID") {
		t.Fatalf("expected duplicate event rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsSequenceGapReplayAndPredecessor(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	gap := append([]ObserverReceiptClaim(nil), receipts...)
	gap[1].ObserverSequence++
	_, err := VerifyObserverCorroboration(host, gap, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "sequence gap") {
		t.Fatalf("expected sequence gap rejection, got %v", err)
	}

	replay := append([]ObserverReceiptClaim(nil), receipts...)
	replay[1].ObserverSequence = replay[0].ObserverSequence
	_, err = VerifyObserverCorroboration(host, replay, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "duplicate observer sequence") {
		t.Fatalf("expected sequence replay rejection, got %v", err)
	}

	predecessor := append([]ObserverReceiptClaim(nil), receipts...)
	predecessor[1].PreviousReceiptSHA256 = testObserverGenesis
	_, err = VerifyObserverCorroboration(host, predecessor, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "predecessor mismatch") {
		t.Fatalf("expected predecessor rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsIdentityAndUnboundEvents(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	wrongIdentity := append([]ObserverReceiptClaim(nil), receipts...)
	wrongIdentity[0].RunNonce = strings.Repeat("34", 32)
	_, err := VerifyObserverCorroboration(host, wrongIdentity, policy)
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("expected identity rejection, got %v", err)
	}

	unbound := append([]ObserverReceiptClaim(nil), receipts...)
	unbound[0].EventID = "not-in-host-journal"
	_, err = VerifyObserverCorroboration(host, unbound, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "unbound observer event") {
		t.Fatalf("expected unbound event rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsWrongTypeAndOrder(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	wrongType := append([]ObserverReceiptClaim(nil), receipts...)
	wrongType[1].EventType = "VM_STOP_OBSERVED"
	_, err := VerifyObserverCorroboration(host, wrongType, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "does not corroborate") {
		t.Fatalf("expected event type rejection, got %v", err)
	}

	wrongOrder := append([]ObserverReceiptClaim(nil), receipts...)
	wrongOrder[0], wrongOrder[1] = wrongOrder[1], wrongOrder[0]
	_, err = VerifyObserverCorroboration(host, wrongOrder, policy)
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("expected ordering rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsVMSubjectSwitch(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	receipts[1].SubjectID = "different-vm"
	_, err := VerifyObserverCorroboration(host, receipts, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "subject identity changed") {
		t.Fatalf("expected VM subject continuity rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsWrongSubjectKind(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	receipts[0].SubjectKind = "OVERLAY"
	_, err := VerifyObserverCorroboration(host, receipts, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "subject kind") {
		t.Fatalf("expected subject-kind rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsHostObserverDisagreement(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	// D1 says VM_DESTROY while the independently supplied D3 claim says
	// the VM merely stopped. This is a contradiction, not a weaker success.
	receipts[1].EventType = "VM_STOP_OBSERVED"
	_, err := VerifyObserverCorroboration(host, receipts, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "does not corroborate") {
		t.Fatalf("expected D1/D3 contradiction rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsDuplicateHostEventID(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	host[1].EventID = host[0].EventID
	_, err := VerifyObserverCorroboration(host, receipts, policy)
	if !errors.Is(err, ErrInvalidEvidence) || !strings.Contains(err.Error(), "duplicate host event ID") {
		t.Fatalf("expected duplicate host event rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsWrongIssuer(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	receipts[0].IssuerID = "untrusted-observer"
	_, err := VerifyObserverCorroboration(host, receipts, policy)
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("expected issuer mismatch rejection, got %v", err)
	}
}

func TestVerifyObserverCorroborationRejectsPolicyAndDigestMutation(t *testing.T) {
	host, receipts, policy := observerTestFixture()
	wrongPolicy := policy
	wrongPolicy.PolicyDigestSHA256 = strings.Repeat("9", 64)
	_, err := VerifyObserverCorroboration(host, receipts, wrongPolicy)
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("expected policy mismatch rejection, got %v", err)
	}

	badDigest := append([]ObserverReceiptClaim(nil), receipts...)
	badDigest[0].ReceiptSHA256 = "not-a-digest"
	_, err = VerifyObserverCorroboration(host, badDigest, policy)
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("expected malformed digest rejection, got %v", err)
	}
}
