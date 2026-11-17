//go:build linux

package remotejobs

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

func TestSignedBrokerEnvelopeVerifiesExactRequestAndReceiver(t *testing.T) {
	root := t.TempDir()
	receiver, err := nodeidentity.New(filepath.Join(root, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := nodeidentity.New(filepath.Join(root, "caller"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Trust(caller.Public(), caller.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	req := validBrokerRequest()
	raw, err := SignBrokerStartEnvelope(caller, receiver.Public().NodeID, req)
	if err != nil {
		t.Fatal(err)
	}
	gotCaller, got, err := DecodeAndVerifySignedBrokerStartEnvelope(receiver, raw)
	if err != nil {
		t.Fatal(err)
	}
	if gotCaller.NodeID() != caller.Public().NodeID || got.RequestID != req.RequestID {
		t.Fatalf("unexpected verified identity/request: %q %+v", gotCaller.NodeID(), got)
	}

	var envelope SignedBrokerStartEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Request.Argv = []string{"/attacker/changed"}
	mutated, _ := json.Marshal(envelope)
	if _, _, err := DecodeAndVerifySignedBrokerStartEnvelope(receiver, mutated); err == nil {
		t.Fatal("accepted payload modified after signing")
	}
	wrongReceiver, err := nodeidentity.New(filepath.Join(root, "wrong-receiver"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeAndVerifySignedBrokerStartEnvelope(wrongReceiver, raw); err == nil {
		t.Fatal("accepted signature addressed to another receiver")
	}
}

func TestSignedBrokerEnvelopeRejectsRevokedPeerMalformedAndWrongPurpose(t *testing.T) {
	root := t.TempDir()
	receiver, err := nodeidentity.New(filepath.Join(root, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := nodeidentity.New(filepath.Join(root, "caller"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Trust(caller.Public(), caller.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	req := validBrokerRequest()
	raw, err := SignBrokerStartEnvelope(caller, receiver.Public().NodeID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeAndVerifySignedBrokerStartEnvelope(receiver, append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("accepted trailing data")
	}
	if _, err := receiver.Revoke(caller.Public().NodeID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeAndVerifySignedBrokerStartEnvelope(receiver, raw); err == nil {
		t.Fatal("accepted revoked caller")
	}
	if _, err := SignRequestForPurpose(caller, "status", receiver.Public().NodeID, req.RequestID, mustJSON(t, req)); err != nil {
		t.Fatal(err)
	}
	sig, err := SignRequestForPurpose(caller, "status", receiver.Public().NodeID, req.RequestID, mustJSON(t, req))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequestForPurpose(receiver, "reserve", caller.Public().NodeID, req.RequestID, mustJSON(t, req), sig); err == nil {
		t.Fatal("accepted signature from another purpose")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSignedBrokerEnvelopeReadWriteConnection(t *testing.T) {
	root := t.TempDir()
	receiver, err := nodeidentity.New(filepath.Join(root, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := nodeidentity.New(filepath.Join(root, "caller"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Trust(caller.Public(), caller.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	raw, err := SignBrokerStartEnvelope(caller, receiver.Public().NodeID, validBrokerRequest())
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	writeDone := make(chan error, 1)
	go func() { writeDone <- WriteSignedBrokerStartEnvelope(client, raw, time.Second) }()
	gotCaller, got, err := ReadSignedBrokerStartEnvelope(server, receiver, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if gotCaller.NodeID() != caller.Public().NodeID || got.RequestID != validBrokerRequest().RequestID {
		t.Fatal("signed request context mismatch")
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestServeAuthenticatedSignedBrokerConnection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux peer credentials required")
	}
	root := t.TempDir()
	receiver, err := nodeidentity.New(filepath.Join(root, "receiver"))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := nodeidentity.New(filepath.Join(root, "caller"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Trust(caller.Public(), caller.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	raw, err := SignBrokerStartEnvelope(caller, receiver.Public().NodeID, validBrokerRequest())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "broker.sock")
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- WriteSignedBrokerStartEnvelope(client, raw, time.Second); _ = client.Close() }()
	called := false
	err = ServeAuthenticatedSignedBrokerConnection(server, BrokerPeerPolicy{AllowedUID: uint32(os.Getuid()), AllowedExecutableSHA256: currentExecutableDigest(t)}, receiver, time.Second, func(peer BrokerPeerIdentity, federationCaller FederationCallerContext, req BrokerStartRequest) error {
		called = true
		if peer.PID != os.Getpid() || federationCaller.NodeID() != caller.Public().NodeID || req.RequestID != validBrokerRequest().RequestID {
			t.Fatal("signed broker session identity mismatch")
		}
		if peer.PIDFD < 0 {
			t.Fatal("pidfd not retained during handler")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("signed broker handler was not called")
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}
