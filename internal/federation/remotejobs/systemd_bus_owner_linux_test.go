//go:build linux

package remotejobs

import (
	"testing"
	"time"
)

func TestSystemdBusOwnerSessionCreatesConnectionScopedPin(t *testing.T) {
	now := time.Now().UTC()
	session, err := newSystemdBusOwnerSession(systemdDBusWellKnownName, ":1.42", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.pin.connectionID) != 32 {
		t.Fatalf("unexpected connection identity length: %d", len(session.pin.connectionID))
	}
	if session.pin.uniqueOwner != ":1.42" || session.pin.wellKnown != systemdDBusWellKnownName {
		t.Fatal("owner pin did not retain exact bus identity")
	}
}

func TestSystemdBusOwnerSessionInvalidatesPermanently(t *testing.T) {
	now := time.Now().UTC()
	session, err := newSystemdBusOwnerSession(systemdDBusWellKnownName, ":1.42", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.observeNameOwnerChanged(systemdDBusWellKnownName, ":1.42", ":1.43", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := session.validateReplyLocked(":1.42", now.Add(2*time.Second)); err == nil {
		t.Fatal("invalidated owner pin accepted a method reply")
	}
	if err := session.observeNameOwnerChanged(systemdDBusWellKnownName, ":1.42", ":1.44", now.Add(3*time.Second)); err == nil {
		t.Fatal("invalidated session was reusable")
	}
}

func TestSystemdBusOwnerSessionRejectsUnrelatedOrStaleOwnerChange(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name, busName, oldOwner, newOwner string
		at                                time.Time
	}{
		{"wrong-name", "org.example.other", ":1.42", ":1.43", now.Add(time.Second)},
		{"wrong-old-owner", systemdDBusWellKnownName, ":1.99", ":1.43", now.Add(time.Second)},
		{"same-owner", systemdDBusWellKnownName, ":1.42", ":1.42", now.Add(time.Second)},
		{"stale-event", systemdDBusWellKnownName, ":1.42", ":1.43", now.Add(-time.Second)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session, err := newSystemdBusOwnerSession(systemdDBusWellKnownName, ":1.42", now)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.observeNameOwnerChanged(tc.busName, tc.oldOwner, tc.newOwner, tc.at); err == nil {
				t.Fatal("unrelated or stale owner event accepted")
			}
		})
	}
}

func TestSystemdBusOwnerSessionRejectsInvalidOwnerObservation(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name, wellKnown, owner string
		at                     time.Time
	}{
		{"wrong-service", "org.example.other", ":1.42", now},
		{"well-known-as-owner", systemdDBusWellKnownName, systemdDBusWellKnownName, now},
		{"missing-time", systemdDBusWellKnownName, ":1.42", time.Time{}},
		{"future-time", systemdDBusWellKnownName, ":1.42", now.Add(2 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newSystemdBusOwnerSession(tc.wellKnown, tc.owner, tc.at); err == nil {
				t.Fatal("invalid owner observation accepted")
			}
		})
	}
}

func TestSystemdBusOwnerSessionCloseInvalidatesGeneration(t *testing.T) {
	now := time.Now().UTC()
	session, err := newSystemdBusOwnerSession(systemdDBusWellKnownName, ":1.42", now)
	if err != nil {
		t.Fatal(err)
	}
	session.close()
	if err := session.validateReplyLocked(":1.42", now.Add(time.Second)); err == nil {
		t.Fatal("closed connection generation accepted a reply")
	}
}
