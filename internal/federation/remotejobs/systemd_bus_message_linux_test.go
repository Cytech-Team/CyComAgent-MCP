//go:build linux

package remotejobs

import (
	"testing"
	"time"
)

func TestDecodeSystemdNameOwnerChangedRequiresExactSignalProvenance(t *testing.T) {
	now := time.Now().UTC()
	base := testOwnerChangedMessage(now, systemdDBusWellKnownName, ":1.42", ":1.43")
	if _, err := decodeSystemdNameOwnerChanged(base); err != nil {
		t.Fatalf("valid signal rejected: %v", err)
	}
	mutations := map[string]func(*systemdBusDecodedMessage){
		"wrong-type":      func(m *systemdBusDecodedMessage) { m.messageType = "method_return" },
		"wrong-sender":    func(m *systemdBusDecodedMessage) { m.sender = ":1.99" },
		"destination":     func(m *systemdBusDecodedMessage) { m.destination = ":1.42" },
		"wrong-path":      func(m *systemdBusDecodedMessage) { m.path = "/org/example/DBus" },
		"wrong-interface": func(m *systemdBusDecodedMessage) { m.interfaceName = "org.example.DBus" },
		"wrong-member":    func(m *systemdBusDecodedMessage) { m.member = "NameAcquired" },
		"wrong-signature": func(m *systemdBusDecodedMessage) { m.signature = "sss" + "s" },
		"missing-body":    func(m *systemdBusDecodedMessage) { m.bodyStrings = m.bodyStrings[:2] },
		"missing-serial":  func(m *systemdBusDecodedMessage) { m.serial = 0 },
		"duplicate-owner": func(m *systemdBusDecodedMessage) { m.bodyStrings[2] = m.bodyStrings[1] },
		"invalid-owner":   func(m *systemdBusDecodedMessage) { m.bodyStrings[1] = "not-a-unique-name" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := base
			m.bodyStrings = append([]string(nil), base.bodyStrings...)
			mutate(&m)
			if _, err := decodeSystemdNameOwnerChanged(m); err == nil {
				t.Fatal("invalid signal accepted")
			}
		})
	}
}

func TestDecodeSystemdStartMethodReplyRequiresHeaderAndBodyCorrelation(t *testing.T) {
	now := time.Now().UTC()
	base := testSystemdReplyMessage(now, 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	if _, err := decodeSystemdStartMethodReply(base); err != nil {
		t.Fatalf("valid reply rejected: %v", err)
	}
	mutations := map[string]func(*systemdBusDecodedMessage){
		"wrong-type":             func(m *systemdBusDecodedMessage) { m.messageType = "signal" },
		"missing-reply-serial":   func(m *systemdBusDecodedMessage) { m.replySerial = 0 },
		"sender-not-unique":      func(m *systemdBusDecodedMessage) { m.sender = "org.freedesktop.systemd1" },
		"wrong-signature":        func(m *systemdBusDecodedMessage) { m.signature = "s" },
		"bad-object-path":        func(m *systemdBusDecodedMessage) { m.bodyObjectPath = "/org/freedesktop/systemd1/unit/foo" },
		"missing-serial":         func(m *systemdBusDecodedMessage) { m.serial = 0 },
		"unexpected-destination": func(m *systemdBusDecodedMessage) { m.destination = ":1.1" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := base
			mutate(&m)
			if _, err := decodeSystemdStartMethodReply(m); err == nil {
				t.Fatal("invalid method reply accepted")
			}
		})
	}
}

func TestDecodeSystemdMethodErrorIsCorrelatedUnknownEvidence(t *testing.T) {
	m := systemdBusDecodedMessage{messageType: "error", sender: ":1.42", serial: 42, replySerial: 41,
		errorName: "org.freedesktop.systemd1.UnitExists", receivedAt: time.Now().UTC()}
	reply, err := decodeSystemdStartMethodReply(m)
	if err != nil {
		t.Fatal(err)
	}
	if reply.ErrorName == "" || reply.ReplySerial != 41 {
		t.Fatalf("error reply lost correlation: %#v", reply)
	}
}

func TestDecodeSystemdMethodErrorAllowsOptionalStringBody(t *testing.T) {
	m := systemdBusDecodedMessage{messageType: "error", sender: ":1.42", serial: 42, replySerial: 41,
		errorName: "org.freedesktop.systemd1.UnitExists", signature: "s",
		bodyStrings: []string{"unit already exists"}, receivedAt: time.Now().UTC()}
	if _, err := decodeSystemdStartMethodReply(m); err != nil {
		t.Fatalf("valid D-Bus error reply rejected: %v", err)
	}
}

func TestDBusBusNameValidation(t *testing.T) {
	for _, name := range []string{"org.freedesktop.systemd1", "org.example.name-with-hyphen", ":1.42"} {
		if !validDBusBusName(name) {
			t.Errorf("valid bus name rejected: %q", name)
		}
	}
	for _, name := range []string{"", "org", "org..bad", "org.1bad", "org.bad!", ".org.bad"} {
		if validDBusBusName(name) {
			t.Errorf("invalid bus name accepted: %q", name)
		}
	}
}
