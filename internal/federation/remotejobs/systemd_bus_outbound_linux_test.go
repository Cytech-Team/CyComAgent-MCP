//go:build linux

package remotejobs

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func TestEncodeSystemdBusBootstrapCallsRoundTrip(t *testing.T) {
	cases := []struct {
		name          string
		call          systemdBusBootstrapCall
		wantSignature string
		wantBody      []string
	}{
		{
			name:          "hello",
			call:          systemdBusBootstrapCall{Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath, Interface: systemdDBusInterface, Member: "Hello"},
			wantSignature: "",
		},
		{
			name:          "add-match",
			call:          systemdBusBootstrapCall{Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath, Interface: systemdDBusInterface, Member: "AddMatch", Signature: "s", StringArgs: []string{systemdBusHandshakeAddMatchRule}},
			wantSignature: "s", wantBody: []string{systemdBusHandshakeAddMatchRule},
		},
		{
			name:          "get-name-owner",
			call:          systemdBusBootstrapCall{Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath, Interface: systemdDBusInterface, Member: "GetNameOwner", Signature: "s", StringArgs: []string{systemdDBusWellKnownName}},
			wantSignature: "s", wantBody: []string{systemdDBusWellKnownName},
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := encodeSystemdBusBootstrapCall(uint32(i+1), tc.call)
			if err != nil {
				t.Fatal(err)
			}
			message, err := decodeSystemdDBusFrame(frame, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if message.messageType != "method_call" || message.serial != uint32(i+1) ||
				message.destination != systemdDBusDaemonName || message.path != systemdDBusObjectPath ||
				message.interfaceName != systemdDBusInterface || message.member != tc.call.Member ||
				message.signature != tc.wantSignature || len(message.bodyStrings) != len(tc.wantBody) {
				t.Fatalf("decoded bootstrap call mismatch: %#v", message)
			}
			for j := range tc.wantBody {
				if message.bodyStrings[j] != tc.wantBody[j] {
					t.Fatalf("body[%d] = %q, want %q", j, message.bodyStrings[j], tc.wantBody[j])
				}
			}
		})
	}
}

func TestEncodeSystemdBusBootstrapCallRejectsSubstitution(t *testing.T) {
	valid := systemdBusBootstrapCall{Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath, Interface: systemdDBusInterface, Member: "AddMatch", Signature: "s", StringArgs: []string{systemdBusHandshakeAddMatchRule}}
	cases := map[string]systemdBusBootstrapCall{
		"zero-serial":       valid,
		"wrong-destination": func() systemdBusBootstrapCall { c := valid; c.Destination = "org.example.attacker"; return c }(),
		"wrong-path":        func() systemdBusBootstrapCall { c := valid; c.Path = "/org/example"; return c }(),
		"wrong-interface":   func() systemdBusBootstrapCall { c := valid; c.Interface = "org.example.Interface"; return c }(),
		"wrong-member":      func() systemdBusBootstrapCall { c := valid; c.Member = "StartTransientUnit"; return c }(),
		"wrong-rule":        func() systemdBusBootstrapCall { c := valid; c.StringArgs = []string{"type='signal'"}; return c }(),
		"extra-argument":    func() systemdBusBootstrapCall { c := valid; c.StringArgs = append(c.StringArgs, "extra"); return c }(),
		"embedded-nul":      func() systemdBusBootstrapCall { c := valid; c.StringArgs = []string{"bad\x00rule"}; return c }(),
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			serial := uint32(1)
			if name == "zero-serial" {
				serial = 0
			}
			if _, err := encodeSystemdBusBootstrapCall(serial, call); err == nil {
				t.Fatal("substituted bootstrap call accepted")
			}
		})
	}
}

func TestSystemdBusBootstrapPreparedCallsReserveSerialBeforeReturningFrame(t *testing.T) {
	b := newSystemdBusBootstrapSession(1001)
	_, _ = b.externalAuthCommand()
	_, err := b.receiveAuthFrame([]byte("OK 0123456789abcdef0123456789abcdef\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.confirmBeginWritten(); err != nil {
		t.Fatal(err)
	}
	hello, err := b.prepareHelloCall()
	if err != nil {
		t.Fatal(err)
	}
	if hello.Serial != 1 || b.handshake.helloSerial != hello.Serial {
		t.Fatalf("Hello serial was not reserved before frame return: %#v", hello)
	}
	decodedHello, err := decodeSystemdDBusFrame(hello.Frame, time.Now().UTC())
	if err != nil || decodedHello.serial != hello.Serial || decodedHello.member != "Hello" {
		t.Fatalf("encoded Hello frame invalid: %#v err=%v", decodedHello, err)
	}
	now := time.Now().UTC()
	if _, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, hello.Serial, []string{":1.42"}, now)); err != nil {
		t.Fatal(err)
	}
	addMatch, err := b.prepareAddMatchCall()
	if err != nil {
		t.Fatal(err)
	}
	if addMatch.Serial != 2 || b.handshake.matchSerial != addMatch.Serial {
		t.Fatalf("AddMatch serial = %d, want 2", addMatch.Serial)
	}
	decodedAddMatch, err := decodeSystemdDBusFrame(addMatch.Frame, now.Add(time.Millisecond))
	if err != nil || decodedAddMatch.serial != addMatch.Serial || decodedAddMatch.member != "AddMatch" ||
		len(decodedAddMatch.bodyStrings) != 1 || decodedAddMatch.bodyStrings[0] != systemdBusHandshakeAddMatchRule {
		t.Fatalf("encoded AddMatch frame invalid: %#v err=%v", decodedAddMatch, err)
	}
	if _, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "", 21, addMatch.Serial, nil, now.Add(2*time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	getOwner, err := b.prepareGetNameOwnerCall()
	if err != nil {
		t.Fatal(err)
	}
	if getOwner.Serial != 3 || b.handshake.ownerSerial != getOwner.Serial {
		t.Fatalf("GetNameOwner serial = %d, want 3", getOwner.Serial)
	}
	decodedOwner, err := decodeSystemdDBusFrame(getOwner.Frame, now.Add(3*time.Millisecond))
	if err != nil || decodedOwner.serial != getOwner.Serial || decodedOwner.member != "GetNameOwner" ||
		len(decodedOwner.bodyStrings) != 1 || decodedOwner.bodyStrings[0] != systemdDBusWellKnownName {
		t.Fatalf("encoded GetNameOwner frame invalid: %#v err=%v", decodedOwner, err)
	}
}

func TestSystemdBusBootstrapRejectsReusedOutgoingSerial(t *testing.T) {
	b := newSystemdBusBootstrapSession(1001)
	_, _ = b.externalAuthCommand()
	_, err := b.receiveAuthFrame([]byte("OK 0123456789abcdef0123456789abcdef\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	_ = b.confirmBeginWritten()
	if _, err := b.registerHelloCall(10); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := b.dispatchDecodedMessage(handshakeReply("method_return", systemdDBusDaemonName, ":1.42", "s", 20, 10, []string{":1.42"}, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.registerAddMatchCall(10); err == nil {
		t.Fatal("reused D-Bus outgoing serial accepted")
	}
	if !b.closed {
		t.Fatal("serial reuse did not close connection generation")
	}
}

func TestSystemdBusBootstrapSerialAllocatorNeverWraps(t *testing.T) {
	b := newSystemdBusBootstrapSession(1001)
	b.mu.Lock()
	b.nextSerial = math.MaxUint32
	last, err := b.allocateSerialLocked()
	if err != nil || last != math.MaxUint32 {
		b.mu.Unlock()
		t.Fatalf("last serial = %d err=%v", last, err)
	}
	if _, err := b.allocateSerialLocked(); err == nil {
		b.mu.Unlock()
		t.Fatal("serial allocator wrapped to zero")
	}
	b.mu.Unlock()
}

func TestSystemdBusOutboundFramesUseLittleEndianAndBoundedLengths(t *testing.T) {
	call := systemdBusBootstrapCall{Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath, Interface: systemdDBusInterface, Member: "GetNameOwner", Signature: "s", StringArgs: []string{systemdDBusWellKnownName}}
	frame, err := encodeSystemdBusBootstrapCall(77, call)
	if err != nil {
		t.Fatal(err)
	}
	if frame[0] != 'l' || frame[1] != 1 || frame[2] != 0 || frame[3] != 1 ||
		binary.LittleEndian.Uint32(frame[8:12]) != 77 {
		t.Fatalf("fixed header is not canonical: %x", frame[:16])
	}
	if len(frame) > systemdDBusMaxFrameSize {
		t.Fatalf("outbound frame length %d exceeds limit", len(frame))
	}
}
