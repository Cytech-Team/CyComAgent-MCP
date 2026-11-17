package remotejobs

import (
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

func trustedPair(t *testing.T) (*nodeidentity.Manager, *nodeidentity.Manager) {
	t.Helper()
	a, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Trust(b.Public(), b.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Trust(a.Public(), a.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestRequestProofBindsBothNodesRequestAndPayload(t *testing.T) {
	caller, receiver := trustedPair(t)
	payload := []byte(`{"command":"printf safe"}`)
	sig, err := SignRequest(caller, receiver.Public().NodeID, "req-001", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequest(receiver, caller.Public().NodeID, "req-001", payload, sig); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	if err := VerifyRequest(receiver, caller.Public().NodeID, "req-002", payload, sig); err == nil {
		t.Fatal("request ID substitution accepted")
	}
	if err := VerifyRequest(receiver, caller.Public().NodeID, "req-001", []byte(`{"command":"printf altered"}`), sig); err == nil {
		t.Fatal("payload substitution accepted")
	}
	other, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequest(other, caller.Public().NodeID, "req-001", payload, sig); err == nil {
		t.Fatal("receiver substitution or untrusted caller accepted")
	}
}

func TestRequestProofRejectsInvalidIdentifiersAndMissingIdentity(t *testing.T) {
	if _, err := RequestTranscript("bad id", "receiver", "req", nil); err == nil {
		t.Fatal("invalid caller ID accepted")
	}
	if _, err := SignRequest(nil, "receiver", "req", nil); err == nil {
		t.Fatal("nil identity accepted")
	}
	if err := VerifyRequest(nil, "caller", "req", nil, nil); err == nil {
		t.Fatal("nil verifier accepted")
	}
}

func TestRequestProofPurposesAreCryptographicallySeparated(t *testing.T) {
	caller, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Trust(caller.Public(), caller.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"kind":"probe"}`)
	reserveSig, err := SignRequestForPurpose(caller, "reserve", receiver.Public().NodeID, "purpose-1", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequestForPurpose(receiver, "reserve", caller.Public().NodeID, "purpose-1", payload, reserveSig); err != nil {
		t.Fatal(err)
	}
	for _, purpose := range []string{"status", "reconcile"} {
		if err := VerifyRequestForPurpose(receiver, purpose, caller.Public().NodeID, "purpose-1", payload, reserveSig); err == nil {
			t.Fatalf("reserve proof was accepted for %s", purpose)
		}
	}
	if _, err := SignRequestForPurpose(caller, "unknown", receiver.Public().NodeID, "purpose-1", payload); err == nil {
		t.Fatal("unknown purpose accepted")
	}
}

func TestDispatchProofIsPurposeSeparatedAndBindsPolicyPayload(t *testing.T) {
	caller, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Trust(caller.Public(), caller.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeDispatchRequest(DispatchRequest{Version: 1, PolicyVersion: 7, Process: ProcessRequest{Executable: "/usr/bin/id", Args: []string{"-u"}, WorkingDir: "/tmp", Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := SignRequestForPurpose(caller, "dispatch", receiver.Public().NodeID, "dispatch-1", payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequestForPurpose(receiver, "dispatch", caller.Public().NodeID, "dispatch-1", payload, sig); err != nil {
		t.Fatal(err)
	}
	for _, purpose := range []string{"reserve", "status", "reconcile"} {
		if err := VerifyRequestForPurpose(receiver, purpose, caller.Public().NodeID, "dispatch-1", payload, sig); err == nil {
			t.Fatalf("dispatch proof accepted for %s", purpose)
		}
	}
	changed, err := EncodeDispatchRequest(DispatchRequest{Version: 1, PolicyVersion: 8, Process: ProcessRequest{Executable: "/usr/bin/id", Args: []string{"-u"}, WorkingDir: "/tmp", Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequestForPurpose(receiver, "dispatch", caller.Public().NodeID, "dispatch-1", changed, sig); err == nil {
		t.Fatal("proof accepted after policy version changed")
	}
}
