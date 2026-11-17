//go:build linux

package remotejobs

import (
	"testing"
	"time"
)

func TestSystemdStartRequestDerivedOnlyFromDurableLaunchingIntent(t *testing.T) {
	intent := validTestLaunchIntent()
	intent.State = "launching"
	intent.StartAttemptID = "0123456789abcdef0123456789abcdef"
	intent.StartRequestReservedAt = time.Now().UTC()
	request, err := systemdStartRequestFromIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	if request.Version != systemdStartProtocolVersion || request.UnitName != intent.UnitName ||
		request.StartAttemptID != intent.StartAttemptID || request.LocalJobID != intent.LocalJobID ||
		request.RequestID != intent.RequestID || request.ArtifactDigest != intent.ArtifactDigest ||
		request.ArtifactDevice != intent.ArtifactDevice || request.ArtifactInode != intent.ArtifactInode ||
		request.PolicyRevision != intent.PolicyRevision || request.PayloadHash != intent.PayloadHash {
		t.Fatalf("start request did not preserve immutable intent binding: %#v", request)
	}
}

func TestSystemdStartRequestRejectsNonLaunchingOrInvalidIntent(t *testing.T) {
	intent := validTestLaunchIntent()
	if _, err := systemdStartRequestFromIntent(intent); err == nil {
		t.Fatal("prepared intent produced a start request")
	}
	intent.State = "launching"
	if _, err := systemdStartRequestFromIntent(intent); err == nil {
		t.Fatal("launching intent without attempt ID produced a start request")
	}
	intent.StartAttemptID = "0123456789abcdef0123456789abcdef"
	if _, err := systemdStartRequestFromIntent(intent); err == nil {
		t.Fatal("intent without durable reservation produced a start request")
	}
	intent.StartRequestReservedAt = time.Now().UTC()
	intent.UnitName = "caller-controlled.service"
	if _, err := systemdStartRequestFromIntent(intent); err == nil {
		t.Fatal("intent with caller-controlled unit name produced a start request")
	}
}

func TestSystemdStartAcknowledgementRequiresExactCorrelatedIdentity(t *testing.T) {
	intent := validTestLaunchIntent()
	intent.State = "launching"
	intent.StartAttemptID = "0123456789abcdef0123456789abcdef"
	intent.StartRequestReservedAt = time.Now().UTC()
	request, err := systemdStartRequestFromIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := newSystemdStartAcknowledgement(request, "accepted", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*systemdStartAcknowledgement){
		"attempt": func(a *systemdStartAcknowledgement) { a.StartAttemptID = "1123456789abcdef0123456789abcdef" },
		"unit":    func(a *systemdStartAcknowledgement) { a.UnitName = "other.service" },
		"artifact": func(a *systemdStartAcknowledgement) {
			a.ArtifactDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		},
		"policy": func(a *systemdStartAcknowledgement) { a.PolicyRevision++ },
		"payload": func(a *systemdStartAcknowledgement) {
			a.PayloadHash = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := ack
			mutate(&bad)
			if err := bad.validate(); err == nil && bad.matchesRequest(request) {
				t.Fatal("mismatched acknowledgement matched reserved request")
			}
		})
	}
	for _, outcome := range []string{"accepted", "rejected", "unknown"} {
		if _, err := newSystemdStartAcknowledgement(request, outcome, time.Now().UTC()); err != nil {
			t.Fatalf("valid outcome %q rejected: %v", outcome, err)
		}
	}
	if _, err := newSystemdStartAcknowledgement(request, "maybe", time.Now().UTC()); err == nil {
		t.Fatal("ambiguous non-enum outcome accepted")
	}
}

func TestSystemdStartMethodReplyRequiresPinnedOwnerAndCanonicalJobPath(t *testing.T) {
	intent := validTestLaunchIntent()
	intent.State = "launching"
	intent.StartAttemptID = "0123456789abcdef0123456789abcdef"
	intent.StartRequestReservedAt = time.Now().UTC()
	request, err := systemdStartRequestFromIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	acquiredAt := time.Now().UTC()
	ownerSession, err := newSystemdBusOwnerSession(systemdDBusWellKnownName, ":1.42", acquiredAt)
	if err != nil {
		t.Fatal(err)
	}
	base := systemdStartMethodReply{Sender: ":1.42", JobObjectPath: "/org/freedesktop/systemd1/job/123", ReceivedAt: acquiredAt.Add(time.Millisecond)}
	ack, err := acknowledgementFromSystemdMethodReply(ownerSession, request, base)
	if err != nil || ack.Outcome != "accepted" {
		t.Fatalf("valid systemd method return not accepted: %#v err=%v", ack, err)
	}
	tests := map[string]func(*systemdStartMethodReply){
		"owner-changed":       func(r *systemdStartMethodReply) { r.Sender = ":1.43" },
		"error-reply":         func(r *systemdStartMethodReply) { r.ErrorName = "org.freedesktop.systemd1.UnitExists" },
		"missing-job-path":    func(r *systemdStartMethodReply) { r.JobObjectPath = "" },
		"wrong-object-path":   func(r *systemdStartMethodReply) { r.JobObjectPath = "/org/freedesktop/systemd1/unit/foo" },
		"noncanonical-job-id": func(r *systemdStartMethodReply) { r.JobObjectPath = "/org/freedesktop/systemd1/job/0123" },
		"invalid-owner":       func(r *systemdStartMethodReply) { r.Sender = "org.freedesktop.other" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			reply := base
			mutate(&reply)
			got, err := acknowledgementFromSystemdMethodReply(ownerSession, request, reply)
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != "unknown" {
				t.Fatalf("ambiguous method reply classified as %q", got.Outcome)
			}
		})
	}
}

func TestSystemdJobObjectPathAndUniqueNameAreStrict(t *testing.T) {
	for _, value := range []string{":1.1", ":0.42", ":12.3456"} {
		if !validDBusUniqueName(value) {
			t.Errorf("valid unique name rejected: %q", value)
		}
	}
	for _, value := range []string{"", "org.freedesktop.systemd1", ":1", ":1.02", ":a.2", ":1.2.3"} {
		if validDBusUniqueName(value) {
			t.Errorf("invalid unique name accepted: %q", value)
		}
	}
	for _, value := range []string{"/org/freedesktop/systemd1/job/1", "/org/freedesktop/systemd1/job/999"} {
		if !validSystemdJobObjectPath(value) {
			t.Errorf("valid job path rejected: %q", value)
		}
	}
	for _, value := range []string{"", "/org/freedesktop/systemd1/job/0", "/org/freedesktop/systemd1/job/01", "/org/freedesktop/systemd1/job/1/2", "/org/freedesktop/systemd1/unit/foo", "/org/freedesktop/systemd1/job/18446744073709551616"} {
		if validSystemdJobObjectPath(value) {
			t.Errorf("invalid job path accepted: %q", value)
		}
	}
}
