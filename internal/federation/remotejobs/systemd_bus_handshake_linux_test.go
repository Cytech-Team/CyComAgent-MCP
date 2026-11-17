//go:build linux

package remotejobs

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

func readyHandshakeForTest(t *testing.T) (*systemdBusConnectionHandshake, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	h := newSystemdBusConnectionHandshake(1001)
	command, err := h.externalAuthCommand()
	if err != nil {
		t.Fatal(err)
	}
	uidHex := make([]byte, hex.EncodedLen(len("1001")))
	hex.Encode(uidHex, []byte("1001"))
	expected := append([]byte{0}, []byte("AUTH EXTERNAL ")...)
	expected = append(expected, uidHex...)
	expected = append(expected, '\r', '\n')
	if !bytes.Equal(command, expected) {
		t.Fatalf("EXTERNAL command = %q, want %q", command, expected)
	}
	if err := h.receiveExternalAuthReply([]byte("OK 0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	if h.serverGUID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("server GUID = %q", h.serverGUID)
	}
	begin, err := h.beginCommand()
	if err != nil || string(begin) != "BEGIN\r\n" {
		t.Fatalf("BEGIN command = %q, err=%v", begin, err)
	}
	if err := h.confirmBeginWritten(); err != nil {
		t.Fatal(err)
	}
	if err := h.registerHelloCall(10); err != nil {
		t.Fatal(err)
	}
	hello := handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now)
	if err := h.acceptHelloReply(hello); err != nil {
		t.Fatal(err)
	}
	rule, err := h.registerAddMatchCall(11)
	if err != nil || rule != systemdBusHandshakeAddMatchRule {
		t.Fatalf("AddMatch rule = %q, err=%v", rule, err)
	}
	addMatch := handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "", 21, 11, nil, now.Add(time.Millisecond))
	if err := h.acceptAddMatchReply(addMatch); err != nil {
		t.Fatal(err)
	}
	name, err := h.registerGetNameOwnerCall(12)
	if err != nil || name != systemdDBusWellKnownName {
		t.Fatalf("GetNameOwner name = %q, err=%v", name, err)
	}
	return h, now
}

func TestSystemdBusHandshakeExternalAuthAndHelloSequence(t *testing.T) {
	h, now := readyHandshakeForTest(t)
	if h.state != systemdBusHandshakeOwnerPending || h.uniqueName != ":1.42" {
		t.Fatalf("unexpected handshake state %d unique=%q", h.state, h.uniqueName)
	}
	reply := handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{":1.77"}, now.Add(2*time.Millisecond))
	owner, err := h.acceptGetNameOwnerReply(reply)
	if err != nil {
		t.Fatal(err)
	}
	if !h.isReady() || owner.pin.uniqueOwner != ":1.77" {
		t.Fatalf("handshake not ready with exact owner: ready=%v owner=%#v", h.isReady(), owner.pin)
	}
}

func TestSystemdBusHandshakeRejectsWrongOrderAndDuplicateCommands(t *testing.T) {
	h := newSystemdBusConnectionHandshake(1001)
	if _, err := h.beginCommand(); err == nil {
		t.Fatal("BEGIN before authentication accepted")
	}
	if _, err := h.externalAuthCommand(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.externalAuthCommand(); err == nil {
		t.Fatal("duplicate EXTERNAL command accepted")
	}
	if err := h.confirmBeginWritten(); err == nil {
		t.Fatal("BEGIN write confirmation before BEGIN accepted")
	}
}

func TestSystemdBusHandshakeRejectsAuthenticationChallengesAndMalformedGUID(t *testing.T) {
	for _, line := range []string{
		"OK", "OK not-a-guid", "OK 0123456789abcdef0123456789abcdeg",
		"OK 0123456789abcdef0123456789abcdef extra", "DATA 1234", "REJECTED EXTERNAL",
		"ERROR", "OK 0123456789abcdef0123456789abcdef\r",
	} {
		t.Run(line, func(t *testing.T) {
			h := newSystemdBusConnectionHandshake(1001)
			if _, err := h.externalAuthCommand(); err != nil {
				t.Fatal(err)
			}
			if err := h.receiveExternalAuthReply([]byte(line)); err == nil {
				t.Fatalf("malformed or unsupported auth response %q accepted", line)
			}
			if h.state != systemdBusHandshakeFailed {
				t.Fatalf("failed auth state = %d", h.state)
			}
		})
	}
}

func TestSystemdBusHandshakeRequiresExactHelloReply(t *testing.T) {
	cases := []struct {
		name string
		edit func(*systemdBusDecodedMessage)
	}{
		{"wrong-sender", func(m *systemdBusDecodedMessage) { m.sender = ":1.99" }},
		{"wrong-destination", func(m *systemdBusDecodedMessage) { m.destination = ":1.99" }},
		{"wrong-reply-serial", func(m *systemdBusDecodedMessage) { m.replySerial++ }},
		{"missing-message-serial", func(m *systemdBusDecodedMessage) { m.serial = 0 }},
		{"wrong-signature", func(m *systemdBusDecodedMessage) { m.signature = "o" }},
		{"invalid-unique-name", func(m *systemdBusDecodedMessage) { m.bodyStrings[0] = "org.example.bus" }},
		{"multiple-body-values", func(m *systemdBusDecodedMessage) { m.bodyStrings = append(m.bodyStrings, ":1.43") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSystemdBusConnectionHandshake(1001)
			_, _ = h.externalAuthCommand()
			_ = h.receiveExternalAuthReply([]byte("OK 0123456789abcdef0123456789abcdef"))
			_, _ = h.beginCommand()
			_ = h.confirmBeginWritten()
			_ = h.registerHelloCall(10)
			now := time.Now().UTC()
			msg := handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now)
			tc.edit(&msg)
			if err := h.acceptHelloReply(msg); err == nil {
				t.Fatal("invalid Hello reply accepted")
			}
			if h.state != systemdBusHandshakeFailed {
				t.Fatalf("invalid Hello reply did not fail generation: %d", h.state)
			}
		})
	}
}

func TestSystemdBusHandshakeRequiresAddMatchBeforeGetNameOwner(t *testing.T) {
	h := newSystemdBusConnectionHandshake(1001)
	if _, err := h.registerGetNameOwnerCall(12); err == nil {
		t.Fatal("GetNameOwner before Hello/AddMatch accepted")
	}
	h, _ = readyHandshakeForTest(t)
	if _, err := h.registerGetNameOwnerCall(13); err == nil {
		t.Fatal("duplicate GetNameOwner registration accepted")
	}
}

func TestSystemdBusHandshakeRejectsInvalidAddMatchAndOwnerReplies(t *testing.T) {
	t.Run("add-match-error", func(t *testing.T) {
		h := newSystemdBusConnectionHandshake(1001)
		_, _ = h.externalAuthCommand()
		_ = h.receiveExternalAuthReply([]byte("OK 0123456789abcdef0123456789abcdef"))
		_, _ = h.beginCommand()
		_ = h.confirmBeginWritten()
		_ = h.registerHelloCall(10)
		now := time.Now().UTC()
		_ = h.acceptHelloReply(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now))
		_, _ = h.registerAddMatchCall(11)
		bad := handshakeReply("error", systemdDBusDaemonName, ":1.42", "", 21, 11, nil, now.Add(time.Millisecond))
		if err := h.acceptAddMatchReply(bad); err == nil {
			t.Fatal("AddMatch error accepted")
		}
	})
	t.Run("owner-reply-invalid", func(t *testing.T) {
		h, now := readyHandshakeForTest(t)
		bad := handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{"org.freedesktop.systemd1"}, now.Add(2*time.Millisecond))
		if _, err := h.acceptGetNameOwnerReply(bad); err == nil {
			t.Fatal("well-known name accepted as unique owner")
		}
		if h.state != systemdBusHandshakeFailed {
			t.Fatalf("invalid owner reply did not fail generation: %d", h.state)
		}
	})
}

func TestSystemdBusHandshakeCloseInvalidatesReadyOwner(t *testing.T) {
	h, now := readyHandshakeForTest(t)
	owner, err := h.acceptGetNameOwnerReply(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{":1.77"}, now.Add(2*time.Millisecond)))
	if err != nil {
		t.Fatal(err)
	}
	h.close()
	if h.state != systemdBusHandshakeClosed {
		t.Fatalf("close state = %d", h.state)
	}
	if err := owner.validateReplyLocked(":1.77", now.Add(3*time.Millisecond)); err == nil {
		t.Fatal("closed handshake retained a valid owner pin")
	}
}

func TestSystemdBusHandshakeRequiresCanonicalAuthLine(t *testing.T) {
	for _, line := range [][]byte{
		{}, bytes.Repeat([]byte{'A'}, systemdBusAuthLineMax+1),
		[]byte("OK 0123456789abcdef0123456789abcdef\x00"),
	} {
		h := newSystemdBusConnectionHandshake(1001)
		_, _ = h.externalAuthCommand()
		if err := h.receiveExternalAuthReply(line); err == nil {
			t.Fatalf("invalid auth line %q accepted", line)
		}
	}
}

func TestSystemdBusAuthLineDecoderRequiresExactCRLF(t *testing.T) {
	for _, frame := range [][]byte{
		[]byte("OK 0123456789abcdef0123456789abcdef\r\n"),
		[]byte("OK 0123456789abcdef0123456789abcdef\n"),
		[]byte("OK 0123456789abcdef0123456789abcdef\r\r\n"),
		[]byte("OK\x00 0123456789abcdef0123456789abcdef\r\n"),
		bytes.Repeat([]byte{'A'}, systemdBusAuthLineMax+3),
	} {
		line, err := decodeSystemdBusAuthLine(frame)
		if string(frame) == "OK 0123456789abcdef0123456789abcdef\r\n" {
			if err != nil || string(line) != "OK 0123456789abcdef0123456789abcdef" {
				t.Fatalf("valid CRLF line rejected: line=%q err=%v", line, err)
			}
		} else if err == nil {
			t.Fatalf("invalid auth frame accepted: %q", frame)
		}
	}
}

func FuzzSystemdBusAuthLineDecoderNeverPanics(f *testing.F) {
	f.Add([]byte("OK 0123456789abcdef0123456789abcdef\r\n"))
	f.Add([]byte("REJECTED EXTERNAL\r\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, frame []byte) {
		if len(frame) > systemdBusAuthLineMax+2 {
			t.Skip()
		}
		line, err := decodeSystemdBusAuthLine(frame)
		if err == nil {
			h := newSystemdBusConnectionHandshake(1000)
			if _, startErr := h.externalAuthCommand(); startErr != nil {
				t.Fatal(startErr)
			}
			_ = h.receiveExternalAuthReply(line)
		}
	})
}

func TestSystemdBusHandshakeFailsClosedOnOwnerChangeDuringGetNameOwner(t *testing.T) {
	h, now := readyHandshakeForTest(t)
	changed := testOwnerChangedMessage(now.Add(2*time.Millisecond), systemdDBusWellKnownName, ":1.77", ":1.78")
	relevant, err := h.observeHandshakeMessage(changed)
	if err == nil || !relevant {
		t.Fatalf("owner change was not rejected: relevant=%v err=%v", relevant, err)
	}
	reply := handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{":1.78"}, now.Add(3*time.Millisecond))
	if _, err := h.acceptGetNameOwnerReply(reply); err == nil {
		t.Fatal("GetNameOwner reply accepted after an intervening owner change")
	}
	if h.state != systemdBusHandshakeFailed {
		t.Fatalf("owner-change race did not fail generation: %d", h.state)
	}
}

func TestSystemdBusHandshakeIgnoresOtherServiceOwnerChanges(t *testing.T) {
	h, now := readyHandshakeForTest(t)
	changed := testOwnerChangedMessage(now.Add(2*time.Millisecond), "org.example.other", ":1.10", ":1.11")
	relevant, err := h.observeHandshakeMessage(changed)
	if err != nil || relevant {
		t.Fatalf("unrelated owner change was not ignored: relevant=%v err=%v", relevant, err)
	}
	reply := handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{":1.77"}, now.Add(3*time.Millisecond))
	if _, err := h.acceptGetNameOwnerReply(reply); err != nil {
		t.Fatalf("valid owner reply failed after unrelated signal: %v", err)
	}
}

func TestSystemdBusHandshakeReconnectRequiresFreshGeneration(t *testing.T) {
	first, now := readyHandshakeForTest(t)
	firstOwner, err := first.acceptGetNameOwnerReply(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{":1.77"}, now.Add(2*time.Millisecond)))
	if err != nil {
		t.Fatal(err)
	}
	first.close()

	second, now2 := readyHandshakeForTest(t)
	secondOwner, err := second.acceptGetNameOwnerReply(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 22, 12, []string{":1.88"}, now2.Add(2*time.Millisecond)))
	if err != nil {
		t.Fatal(err)
	}
	if firstOwner.pin.connectionID == secondOwner.pin.connectionID {
		t.Fatal("reconnect reused the prior connection generation identity")
	}
	if err := firstOwner.validateReplyLocked(":1.77", now.Add(3*time.Millisecond)); err == nil {
		t.Fatal("closed previous generation retained a valid owner pin")
	}
}
