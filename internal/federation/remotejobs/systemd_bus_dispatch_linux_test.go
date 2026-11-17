//go:build linux

package remotejobs

import (
	"fmt"
	"testing"
	"time"
)

func testSystemdReplyMessage(now time.Time, replySerial uint32, sender, jobPath string) systemdBusDecodedMessage {
	return systemdBusDecodedMessage{messageType: "method_return", sender: sender, serial: 42, replySerial: replySerial,
		signature: "o", bodyObjectPath: jobPath, receivedAt: now}
}

func testOwnerChangedMessage(now time.Time, name, oldOwner, newOwner string) systemdBusDecodedMessage {
	return systemdBusDecodedMessage{messageType: "signal", sender: systemdDBusDaemonName,
		path: systemdDBusObjectPath, interfaceName: systemdDBusInterface, member: "NameOwnerChanged",
		signature: "sss", serial: 3, bodyStrings: []string{name, oldOwner, newOwner}, receivedAt: now}
}

func dispatchTestStartRequest(t *testing.T) systemdStartRequest {
	t.Helper()
	intent := validTestLaunchIntent()
	intent.State = "launching"
	intent.StartAttemptID = "0123456789abcdef0123456789abcdef"
	intent.StartRequestReservedAt = time.Now().UTC()
	request, err := systemdStartRequestFromIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func newTestDispatcher(t *testing.T) (*systemdBusMessageDispatcher, systemdStartRequest, time.Time) {
	t.Helper()
	session, now := dispatchTestSession(t)
	dispatcher, err := newSystemdBusMessageDispatcher(session)
	if err != nil {
		t.Fatal(err)
	}
	request := dispatchTestStartRequest(t)
	if err := dispatcher.registerStartMethodCall(41, request); err != nil {
		t.Fatal(err)
	}
	return dispatcher, request, now
}

func dispatchTestSession(t *testing.T) (*systemdBusOwnerSession, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	session, err := newSystemdBusOwnerSession(systemdDBusWellKnownName, ":1.42", now)
	if err != nil {
		t.Fatal(err)
	}
	return session, now
}

func TestSystemdBusDispatcherProcessesOwnerChangeBeforeReply(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	event, err := decodeSystemdNameOwnerChanged(testOwnerChangedMessage(now.Add(time.Millisecond), systemdDBusWellKnownName, ":1.42", ":1.43"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.dispatchNameOwnerChanged(4, event); err != nil {
		t.Fatal(err)
	}
	reply := testSystemdReplyMessage(now.Add(2*time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	ack, err := dispatcher.dispatchStartMethodReply(5, reply)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Outcome != "unknown" {
		t.Fatalf("reply after owner change classified as %q", ack.Outcome)
	}
}

func TestSystemdBusDispatcherAcceptsMonotonicSequenceWithGaps(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	reply := testSystemdReplyMessage(now.Add(time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	ack, err := dispatcher.dispatchStartMethodReply(9, reply)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Outcome != "accepted" {
		t.Fatalf("valid reply classified as %q", ack.Outcome)
	}
}

func TestSystemdBusDispatcherClosesOnDuplicateOrReorderedSequence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second uint64
	}{
		{name: "duplicate", first: 3, second: 3},
		{name: "reordered", first: 8, second: 7},
		{name: "zero", first: 0, second: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher, _, now := newTestDispatcher(t)
			first := testSystemdReplyMessage(now.Add(time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
			firstAck, err := dispatcher.dispatchStartMethodReply(tc.first, first)
			if err != nil {
				t.Fatal(err)
			}
			if tc.first == 0 && firstAck.Outcome != "unknown" {
				t.Fatalf("zero sequence classified as %q", firstAck.Outcome)
			}
			second := testSystemdReplyMessage(now.Add(2*time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/8")
			ack, err := dispatcher.dispatchStartMethodReply(tc.second, second)
			if err != nil {
				t.Fatal(err)
			}
			if ack.Outcome != "unknown" {
				t.Fatalf("duplicate/reordered reply classified as %q", ack.Outcome)
			}
			if err := dispatcher.session.validateReplyLocked(":1.42", now.Add(3*time.Millisecond)); err == nil {
				t.Fatal("dispatcher did not invalidate the connection generation")
			}
		})
	}
}

func TestSystemdBusDispatcherClosesOnInvalidOwnerSignal(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	event, decodeErr := decodeSystemdNameOwnerChanged(testOwnerChangedMessage(now.Add(time.Millisecond), "org.example.other", ":1.42", ":1.43"))
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if err := dispatcher.dispatchNameOwnerChanged(1, event); err == nil {
		t.Fatal("unrelated owner signal accepted")
	}
	reply := testSystemdReplyMessage(now.Add(2*time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	ack, err := dispatcher.dispatchStartMethodReply(2, reply)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Outcome != "unknown" {
		t.Fatalf("reply after invalid signal classified as %q", ack.Outcome)
	}
}

func TestSystemdBusDispatcherCloseInvalidatesSession(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	dispatcher.close()
	reply := testSystemdReplyMessage(now.Add(time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	ack, err := dispatcher.dispatchStartMethodReply(1, reply)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Outcome != "unknown" {
		t.Fatalf("closed connection reply classified as %q", ack.Outcome)
	}
}

func TestSystemdBusDispatcherRequiresExactMethodReplySerial(t *testing.T) {
	for _, serial := range []uint32{0, 40, 42} {
		t.Run(fmt.Sprint(serial), func(t *testing.T) {
			dispatcher, _, now := newTestDispatcher(t)
			reply := testSystemdReplyMessage(now.Add(time.Millisecond), serial, ":1.42", "/org/freedesktop/systemd1/job/7")
			ack, err := dispatcher.dispatchStartMethodReply(1, reply)
			if err != nil {
				t.Fatal(err)
			}
			if ack.Outcome != "unknown" {
				t.Fatalf("mismatched serial %d classified as %q", serial, ack.Outcome)
			}
		})
	}
}

func TestSystemdBusDispatcherAcceptsOnlyOneReplyForReservedCall(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	reply := testSystemdReplyMessage(now.Add(time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	ack, err := dispatcher.dispatchStartMethodReply(1, reply)
	if err != nil || ack.Outcome != "accepted" {
		t.Fatalf("first reply failed: %#v %v", ack, err)
	}
	reply.receivedAt = now.Add(2 * time.Millisecond)
	ack, err = dispatcher.dispatchStartMethodReply(2, reply)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Outcome != "unknown" {
		t.Fatalf("duplicate reply classified as %q", ack.Outcome)
	}
}

func TestSystemdBusDispatcherRegistersOnlyOneNonzeroCallSerial(t *testing.T) {
	dispatcher, request, _ := newTestDispatcher(t)
	if err := dispatcher.registerStartMethodCall(42, request); err == nil {
		t.Fatal("second method call registration accepted")
	}
}

func TestSystemdBusUnifiedDispatcherSequencesAndIgnoresUnrelatedMessages(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	unrelated := []systemdBusDecodedMessage{
		{messageType: "method_call", sender: ":1.77", path: "/org/example", member: "Ping", serial: 10, receivedAt: now.Add(time.Millisecond)},
		testOwnerChangedMessage(now.Add(2*time.Millisecond), "org.example.other", ":1.77", ":1.78"),
		{messageType: "method_return", sender: ":1.77", serial: 11, replySerial: 900, receivedAt: now.Add(3 * time.Millisecond)},
	}
	for i, message := range unrelated {
		ack, relevant, err := dispatcher.dispatchDecodedMessage(uint64(i+1), message)
		if err != nil || relevant || ack.Outcome != "" {
			t.Fatalf("unrelated message %d was not ignored: ack=%#v relevant=%v err=%v", i, ack, relevant, err)
		}
	}
	validReply := testSystemdReplyMessage(now.Add(4*time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	ack, relevant, err := dispatcher.dispatchDecodedMessage(4, validReply)
	if err != nil || !relevant || ack.Outcome != "accepted" {
		t.Fatalf("correlated reply after unrelated traffic failed: ack=%#v relevant=%v err=%v", ack, relevant, err)
	}
}

func TestSystemdBusUnifiedDispatcherIgnoresUnrelatedOwnerChangeButInvalidatesSystemd(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	other := testOwnerChangedMessage(now.Add(time.Millisecond), "org.example.other", ":1.70", ":1.71")
	if ack, relevant, err := dispatcher.dispatchDecodedMessage(1, other); err != nil || relevant || ack.Outcome != "" {
		t.Fatalf("unrelated owner change was not ignored: ack=%#v relevant=%v err=%v", ack, relevant, err)
	}
	changed := testOwnerChangedMessage(now.Add(2*time.Millisecond), systemdDBusWellKnownName, ":1.42", ":1.43")
	if ack, relevant, err := dispatcher.dispatchDecodedMessage(2, changed); err != nil || !relevant || ack.Outcome != "" {
		t.Fatalf("systemd owner change was not applied: ack=%#v relevant=%v err=%v", ack, relevant, err)
	}
	reply := testSystemdReplyMessage(now.Add(3*time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	ack, relevant, err := dispatcher.dispatchDecodedMessage(3, reply)
	if err != nil || !relevant || ack.Outcome != "unknown" {
		t.Fatalf("reply after systemd owner change was not unknown: ack=%#v relevant=%v err=%v", ack, relevant, err)
	}
}

func TestSystemdBusUnifiedDispatcherClosesOnSequenceRegressionAfterUnrelatedTraffic(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	unrelated := systemdBusDecodedMessage{messageType: "method_call", sender: ":1.77", path: "/org/example", member: "Ping", serial: 10, receivedAt: now.Add(time.Millisecond)}
	if _, relevant, err := dispatcher.dispatchDecodedMessage(8, unrelated); err != nil || relevant {
		t.Fatalf("unrelated message failed: relevant=%v err=%v", relevant, err)
	}
	reply := testSystemdReplyMessage(now.Add(2*time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	ack, relevant, err := dispatcher.dispatchDecodedMessage(7, reply)
	if err != nil || !relevant || ack.Outcome != "unknown" {
		t.Fatalf("regressed sequence was not failed closed: ack=%#v relevant=%v err=%v", ack, relevant, err)
	}
}

func TestSystemdBusUnifiedDispatcherFailsClosedOnMalformedSystemdOwnerSignal(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	malformed := testOwnerChangedMessage(now.Add(time.Millisecond), systemdDBusWellKnownName, ":1.42", ":1.43")
	malformed.signature = "s"
	malformed.bodyStrings = []string{systemdDBusWellKnownName}
	if _, relevant, err := dispatcher.dispatchDecodedMessage(1, malformed); err == nil || !relevant {
		t.Fatalf("malformed systemd owner signal was not rejected: relevant=%v err=%v", relevant, err)
	}
	reply := testSystemdReplyMessage(now.Add(2*time.Millisecond), 41, ":1.42", "/org/freedesktop/systemd1/job/7")
	ack, relevant, err := dispatcher.dispatchDecodedMessage(2, reply)
	if err != nil || !relevant || ack.Outcome != "unknown" {
		t.Fatalf("reply after malformed owner signal was not unknown: ack=%#v relevant=%v err=%v", ack, relevant, err)
	}
}
