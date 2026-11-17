package remotejobs

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

func TestBrokerReceiverHandlerResolvesAndReservesWithoutDispatch(t *testing.T) {
	store, ledger, caller, req, resolved := brokerAuthorizationFixture(t)
	root := t.TempDir()
	identity, err := nodeidentity.New(filepath.Join(root, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the trusted peer in this receiver identity store.
	peerIdentity, err := nodeidentity.New(filepath.Join(root, "caller"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Trust(peerIdentity.Public(), peerIdentity.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	// The request fixture uses a different receiver trust root. Build a matching
	// signed caller context for this handler's receiver.
	raw, err := SignBrokerStartEnvelope(peerIdentity, identity.Public().NodeID, req)
	if err != nil {
		t.Fatal(err)
	}
	caller, _, err = DecodeAndVerifySignedBrokerStartEnvelope(identity, raw)
	if err != nil {
		t.Fatal(err)
	}
	policy, revision, err := store.LoadVersioned()
	if err != nil {
		t.Fatal(err)
	}
	policy.AllowedCallers = []string{caller.NodeID()}
	if _, err := store.SaveVersioned(policy, revision); err != nil {
		t.Fatal(err)
	}
	_, revision, err = store.LoadVersioned()
	if err != nil {
		t.Fatal(err)
	}
	req.PolicyRevision = revision
	// Re-sign after the policy revision changed.
	raw, err = SignBrokerStartEnvelope(peerIdentity, identity.Public().NodeID, req)
	if err != nil {
		t.Fatal(err)
	}
	caller, req, err = DecodeAndVerifySignedBrokerStartEnvelope(identity, raw)
	if err != nil {
		t.Fatal(err)
	}
	admitted, resolvedCalled := false, false
	h := &BrokerReceiverHandler{Identity: identity, Policy: store, Ledger: ledger,
		Admission: BrokerPeerAdmissionFunc(func(peer BrokerPeerIdentity, got FederationCallerContext) error {
			admitted = true
			if peer.UID != 1000 || got.NodeID() != caller.NodeID() {
				t.Fatal("peer/Federation binding mismatch")
			}
			return nil
		}),
		Resolver: BrokerReferenceResolverFunc(func(got FederationCallerContext, request BrokerStartRequest) (ProcessRequest, error) {
			resolvedCalled = true
			if got.NodeID() != caller.NodeID() || request.WorkingDirRef != "work-1" {
				t.Fatal("resolver received unexpected context")
			}
			return resolved, nil
		}),
	}
	got, err := h.Handle(BrokerPeerIdentity{PID: 12, UID: 1000, ExecutableSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PIDFD: 9}, caller, req)
	if err != nil {
		t.Fatal(err)
	}
	if !admitted || !resolvedCalled || got.Record.Status != "accepted" {
		t.Fatalf("pipeline did not complete: %+v", got.Record)
	}
	if got.Record.PolicyRevision != revision {
		t.Fatalf("policy revision=%d want %d", got.Record.PolicyRevision, revision)
	}
	if _, err := ledger.Get(caller.NodeID(), req.RequestID); err != nil {
		t.Fatal(err)
	}
}

func TestBrokerReceiverHandlerFailsClosedBeforeReservation(t *testing.T) {
	tests := []struct {
		name         string
		admissionErr error
		resolverErr  error
		revoke       bool
	}{
		{name: "peer denied", admissionErr: errors.New("wrong local executable")},
		{name: "reference denied", resolverErr: errors.New("unknown working directory reference")},
		{name: "caller revoked", revoke: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, ledger, caller, req, _ := brokerAuthorizationFixture(t)
			root := t.TempDir()
			identity, err := nodeidentity.New(filepath.Join(root, "receiver"))
			if err != nil {
				t.Fatal(err)
			}
			peerID, err := nodeidentity.New(filepath.Join(root, "caller"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := identity.Trust(peerID.Public(), peerID.Public().Fingerprint); err != nil {
				t.Fatal(err)
			}
			raw, err := SignBrokerStartEnvelope(peerID, identity.Public().NodeID, req)
			if err != nil {
				t.Fatal(err)
			}
			caller, req, err = DecodeAndVerifySignedBrokerStartEnvelope(identity, raw)
			if err != nil {
				t.Fatal(err)
			}
			policy, rev, err := store.LoadVersioned()
			if err != nil {
				t.Fatal(err)
			}
			policy.AllowedCallers = []string{caller.NodeID()}
			rev, err = store.SaveVersioned(policy, rev)
			if err != nil {
				t.Fatal(err)
			}
			req.PolicyRevision = rev
			raw, err = SignBrokerStartEnvelope(peerID, identity.Public().NodeID, req)
			if err != nil {
				t.Fatal(err)
			}
			caller, req, err = DecodeAndVerifySignedBrokerStartEnvelope(identity, raw)
			if err != nil {
				t.Fatal(err)
			}
			if tt.revoke {
				if _, err := identity.Revoke(caller.NodeID()); err != nil {
					t.Fatal(err)
				}
			}
			h := &BrokerReceiverHandler{Identity: identity, Policy: store, Ledger: ledger,
				Admission: BrokerPeerAdmissionFunc(func(BrokerPeerIdentity, FederationCallerContext) error { return tt.admissionErr }),
				Resolver: BrokerReferenceResolverFunc(func(FederationCallerContext, BrokerStartRequest) (ProcessRequest, error) {
					if tt.resolverErr != nil {
						return ProcessRequest{}, tt.resolverErr
					}
					return ProcessRequest{}, nil
				})}
			_, err = h.Handle(BrokerPeerIdentity{PID: 1, UID: 1000, ExecutableSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PIDFD: 1}, caller, req)
			if err == nil {
				t.Fatal("expected handler denial")
			}
			if _, getErr := ledger.Get(caller.NodeID(), req.RequestID); getErr == nil {
				t.Fatal("denied request was reserved")
			}
		})
	}
}
