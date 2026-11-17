//go:build linux

package remotejobs

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

func bootstrapToHelloPending(t *testing.T) *systemdBusBootstrapSession {
	t.Helper()
	b := newSystemdBusBootstrapSession(1001)
	command, err := b.externalAuthCommand()
	if err != nil {
		t.Fatal(err)
	}
	uidHex := make([]byte, hex.EncodedLen(len("1001")))
	hex.Encode(uidHex, []byte("1001"))
	want := append([]byte{0}, []byte("AUTH EXTERNAL ")...)
	want = append(want, uidHex...)
	want = append(want, '\r', '\n')
	if !bytes.Equal(command, want) {
		t.Fatalf("EXTERNAL command = %q, want %q", command, want)
	}
	begin, err := b.receiveAuthFrame([]byte("OK 0123456789abcdef0123456789abcdef\r\n"))
	if err != nil || string(begin) != "BEGIN\r\n" {
		t.Fatalf("BEGIN = %q err=%v", begin, err)
	}
	if err := b.confirmBeginWritten(); err != nil {
		t.Fatal(err)
	}
	call, err := b.registerHelloCall(10)
	if err != nil {
		t.Fatal(err)
	}
	if call.Member != "Hello" || call.Destination != systemdDBusDaemonName || call.Path != systemdDBusObjectPath {
		t.Fatalf("unexpected Hello call: %#v", call)
	}
	return b
}

func TestSystemdBusBootstrapOrdersAuthenticationAndMethodCalls(t *testing.T) {
	b := bootstrapToHelloPending(t)
	now := time.Now().UTC()
	result, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now))
	if err != nil || result.HandshakeState != systemdBusHandshakeHelloAccepted || result.Ready {
		t.Fatalf("Hello transition result=%#v err=%v", result, err)
	}
	addMatch, err := b.registerAddMatchCall(11)
	if err != nil {
		t.Fatal(err)
	}
	if addMatch.Member != "AddMatch" || addMatch.Signature != "s" || len(addMatch.StringArgs) != 1 || addMatch.StringArgs[0] != systemdBusHandshakeAddMatchRule {
		t.Fatalf("unexpected AddMatch call: %#v", addMatch)
	}
	result, err = b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "", 21, 11, nil, now.Add(time.Millisecond)))
	if err != nil || result.HandshakeState != systemdBusHandshakeMatchInstalled {
		t.Fatalf("AddMatch transition result=%#v err=%v", result, err)
	}
	// A signal can arrive between AddMatch completion and GetNameOwner
	// registration; the unified reader still observes it in order.
	unrelated := testOwnerChangedMessage(now.Add(2*time.Millisecond), "org.example.other", ":1.10", ":1.11")
	result, err = b.dispatchDecodedMessage(unrelated)
	if err != nil || result.Ready {
		t.Fatalf("unrelated owner signal failed bootstrap: result=%#v err=%v", result, err)
	}
	getOwner, err := b.registerGetNameOwnerCall(12)
	if err != nil {
		t.Fatal(err)
	}
	if getOwner.Member != "GetNameOwner" || getOwner.Signature != "s" || len(getOwner.StringArgs) != 1 || getOwner.StringArgs[0] != systemdDBusWellKnownName {
		t.Fatalf("unexpected GetNameOwner call: %#v", getOwner)
	}
	result, err = b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{":1.77"}, now.Add(3*time.Millisecond)))
	if err != nil || !result.Ready || !b.isReady() {
		t.Fatalf("bootstrap did not become ready: result=%#v err=%v", result, err)
	}
}

func TestSystemdBusBootstrapRoutesAllFramesThroughOneSequence(t *testing.T) {
	b := bootstrapToHelloPending(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		msg := systemdBusDecodedMessage{messageType: "method_call", sender: ":1.9", serial: uint32(i + 1), receivedAt: now.Add(time.Duration(i) * time.Millisecond)}
		if _, err := b.dispatchDecodedMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	result, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now.Add(4*time.Millisecond)))
	if err != nil || result.HandshakeState != systemdBusHandshakeHelloAccepted || b.sequence != 4 {
		t.Fatalf("sequence was not unified: sequence=%d result=%#v err=%v", b.sequence, result, err)
	}
}

func TestSystemdBusBootstrapFailsOnOwnerChangeBeforeGetNameOwner(t *testing.T) {
	b := bootstrapToHelloPending(t)
	now := time.Now().UTC()
	_, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now))
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.registerAddMatchCall(11)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "", 21, 11, nil, now.Add(time.Millisecond)))
	if err != nil {
		t.Fatal(err)
	}
	changed := testOwnerChangedMessage(now.Add(2*time.Millisecond), systemdDBusWellKnownName, ":1.77", ":1.78")
	if _, err := b.dispatchDecodedMessage(changed); err == nil {
		t.Fatal("systemd owner change before GetNameOwner was accepted")
	}
	if b.isReady() || !b.closed {
		t.Fatal("owner change did not permanently close bootstrap generation")
	}
}

func TestSystemdBusBootstrapDoesNotAllowStartBeforeReady(t *testing.T) {
	b := bootstrapToHelloPending(t)
	if err := b.registerStartMethodCall(90, systemdStartRequest{Version: systemdStartProtocolVersion, StartAttemptID: "0123456789abcdef0123456789abcdef", UnitName: "cycomagent-job-test.service"}); err == nil {
		t.Fatal("Start call registered before bootstrap ready")
	}
	if !b.closed {
		t.Fatal("invalid start registration did not close bootstrap generation")
	}
}

func TestSystemdBusBootstrapCloseInvalidatesOwnerSession(t *testing.T) {
	b := bootstrapToHelloPending(t)
	now := time.Now().UTC()
	_, _ = b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now))
	_, _ = b.registerAddMatchCall(11)
	_, _ = b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "", 21, 11, nil, now.Add(time.Millisecond)))
	_, _ = b.registerGetNameOwnerCall(12)
	result, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{":1.77"}, now.Add(2*time.Millisecond)))
	if err != nil || !result.Ready {
		t.Fatalf("bootstrap setup failed: %#v %v", result, err)
	}
	owner := b.handshake.owner
	b.close()
	if b.isReady() {
		t.Fatal("closed bootstrap reports ready")
	}
	if err := owner.validateReplyLocked(":1.77", now.Add(3*time.Millisecond)); err == nil {
		t.Fatal("bootstrap close did not invalidate owner session")
	}
}

func TestSystemdBusBootstrapHandsOffToStartDispatcherOnlyAfterReady(t *testing.T) {
	b := bootstrapToHelloPending(t)
	now := time.Now().UTC()
	if _, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.registerAddMatchCall(11); err != nil {
		t.Fatal(err)
	}
	if _, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "", 21, 11, nil, now.Add(time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	if _, err := b.registerGetNameOwnerCall(12); err != nil {
		t.Fatal(err)
	}
	ready, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{":1.77"}, now.Add(2*time.Millisecond)))
	if err != nil || !ready.Ready {
		t.Fatalf("bootstrap failed: %#v %v", ready, err)
	}
	request := dispatchTestStartRequest(t)
	if err := b.registerStartMethodCall(41, request); err != nil {
		t.Fatal(err)
	}
	result, err := b.dispatchDecodedMessage(testSystemdReplyMessage(now.Add(3*time.Millisecond), 41, ":1.77", "/org/freedesktop/systemd1/job/19"))
	if err != nil || !result.Relevant || result.Acknowledgement.Outcome != "accepted" {
		t.Fatalf("post-bootstrap Start reply not dispatched: result=%#v err=%v", result, err)
	}
	if b.sequence != 4 {
		t.Fatalf("unified bootstrap/dispatcher sequence = %d, want 4", b.sequence)
	}
}

func TestSystemdBusBootstrapIgnoresUncorrelatedRepliesDuringHandshake(t *testing.T) {
	b := bootstrapToHelloPending(t)
	now := time.Now().UTC()
	unrelated := handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 19, 999, []string{":1.41"}, now)
	result, err := b.dispatchDecodedMessage(unrelated)
	if err != nil || result.HandshakeState != systemdBusHandshakeHelloPending || result.Ready {
		t.Fatalf("uncorrelated reply changed handshake state: %#v err=%v", result, err)
	}
	correlated := handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now.Add(time.Millisecond))
	result, err = b.dispatchDecodedMessage(correlated)
	if err != nil || result.HandshakeState != systemdBusHandshakeHelloAccepted {
		t.Fatalf("correlated Hello reply was not accepted: %#v err=%v", result, err)
	}
}

func TestSystemdBusBootstrapRejectsMalformedAuthFrameAndCloses(t *testing.T) {
	b := newSystemdBusBootstrapSession(1001)
	if _, err := b.externalAuthCommand(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.receiveAuthFrame([]byte("OK 0123456789abcdef0123456789abcdef\n")); err == nil {
		t.Fatal("LF-only authentication response accepted")
	}
	if b.isReady() || !b.closed || b.handshake.state != systemdBusHandshakeClosed {
		t.Fatal("malformed authentication did not close the generation")
	}
}
