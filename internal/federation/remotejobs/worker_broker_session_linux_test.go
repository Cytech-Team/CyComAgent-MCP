//go:build linux

package remotejobs

import "testing"

func validWorkerBrokerPeer(policy BrokerPeerPolicy) BrokerPeerIdentity {
	return BrokerPeerIdentity{PID: 123, UID: policy.AllowedUID, GID: 456, StartTimeTicks: 789, ExecutableSHA256: policy.AllowedExecutableSHA256, PIDFD: 9}
}

func validExpectedWorkerStart(t *testing.T) systemdStartRequest {
	t.Helper()
	intent := validTestLaunchIntent()
	intent.State = "launching"
	intent.StartAttemptID = "0123456789abcdef0123456789abcdef"
	intent.StartRequestReservedAt = nowForSystemdTest()
	request, err := systemdStartRequestFromIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestAdmitWorkerBrokerSessionRequiresAuthenticatedPeerAndExactReservation(t *testing.T) {
	policy := BrokerPeerPolicy{AllowedUID: 2001, AllowedExecutableSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	peer := validWorkerBrokerPeer(policy)
	expected := validExpectedWorkerStart(t)
	received, err := workerBrokerRequestFromStart(expected)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitWorkerBrokerSession(peer, policy, expected, received); err != nil {
		t.Fatalf("valid worker session rejected: %v", err)
	}
}

func TestAdmitWorkerBrokerSessionRejectsPeerAndRequestSubstitution(t *testing.T) {
	policy := BrokerPeerPolicy{AllowedUID: 2001, AllowedExecutableSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	basePeer := validWorkerBrokerPeer(policy)
	expected := validExpectedWorkerStart(t)
	baseRequest, err := workerBrokerRequestFromStart(expected)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		peer     BrokerPeerIdentity
		policy   BrokerPeerPolicy
		expected systemdStartRequest
		received workerBrokerStartRequest
	}{
		"wrong-uid":          {peer: func() BrokerPeerIdentity { p := basePeer; p.UID++; return p }(), policy: policy, expected: expected, received: baseRequest},
		"missing-pidfd":      {peer: func() BrokerPeerIdentity { p := basePeer; p.PIDFD = -1; return p }(), policy: policy, expected: expected, received: baseRequest},
		"missing-start-time": {peer: func() BrokerPeerIdentity { p := basePeer; p.StartTimeTicks = 0; return p }(), policy: policy, expected: expected, received: baseRequest},
		"wrong-executable": {peer: func() BrokerPeerIdentity {
			p := basePeer
			p.ExecutableSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			return p
		}(), policy: policy, expected: expected, received: baseRequest},
		"wrong-policy-digest": {peer: basePeer, policy: BrokerPeerPolicy{AllowedUID: policy.AllowedUID, AllowedExecutableSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}, expected: expected, received: baseRequest},
		"different-attempt": {peer: basePeer, policy: policy, expected: expected, received: func() workerBrokerStartRequest {
			r := baseRequest
			r.StartAttemptID = "ffffffffffffffffffffffffffffffff"
			return r
		}()},
		"different-artifact":  {peer: basePeer, policy: policy, expected: expected, received: func() workerBrokerStartRequest { r := baseRequest; r.ArtifactInode++; return r }()},
		"different-unit":      {peer: basePeer, policy: policy, expected: expected, received: func() workerBrokerStartRequest { r := baseRequest; r.UnitName = "attacker.service"; return r }()},
		"invalid-reservation": {peer: basePeer, policy: policy, expected: func() systemdStartRequest { r := expected; r.StartAttemptID = ""; return r }(), received: baseRequest},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if err := admitWorkerBrokerSession(tc.peer, tc.policy, tc.expected, tc.received); err == nil {
				t.Fatal("unauthorized worker broker session accepted")
			}
		})
	}
}
