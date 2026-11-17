//go:build linux

package remotejobs

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const systemdStartProtocolVersion = 1

// systemdStartRequest is a broker-owned, in-process transaction description.
// It deliberately contains no caller-selected unit name, UID/GID, command,
// environment, systemd properties, or filesystem paths. It is not transported
// and has no executor in this package yet.
type systemdStartRequest struct {
	Version        int
	CallerNodeID   string
	RequestID      string
	RemoteJobID    string
	LocalJobID     string
	StartAttemptID string
	UnitName       string
	ArtifactName   string
	ArtifactDigest string
	ArtifactDevice uint64
	ArtifactInode  uint64
	PolicyRevision uint64
	PayloadHash    string
}

// systemdStartRequestFromIntent derives the only permitted start identity from
// a validated, durable launching record. It performs no I/O and cannot start a
// process or systemd unit. A future trusted broker adapter must consume this
// immutable description only after the journal has durably reserved the one-shot request; an ambiguous response is unknown/no-replay.
func systemdStartRequestFromIntent(intent LaunchIntentRecord) (systemdStartRequest, error) {
	var out systemdStartRequest
	if err := strictLaunchIntentRecordMustBeValid(intent); err != nil {
		return out, fmt.Errorf("invalid durable launch intent: %w", err)
	}
	if intent.State != "launching" {
		return out, fmt.Errorf("systemd start request requires launching state")
	}
	if intent.StartAttemptID == "" || intent.StartRequestReservedAt.IsZero() {
		return out, fmt.Errorf("durable start attempt and one-shot request reservation are required")
	}
	return systemdStartRequest{
		Version:        systemdStartProtocolVersion,
		CallerNodeID:   intent.CallerNodeID,
		RequestID:      intent.RequestID,
		RemoteJobID:    intent.RemoteJobID,
		LocalJobID:     intent.LocalJobID,
		StartAttemptID: intent.StartAttemptID,
		UnitName:       intent.UnitName,
		ArtifactName:   intent.ArtifactName,
		ArtifactDigest: intent.ArtifactDigest,
		ArtifactDevice: intent.ArtifactDevice,
		ArtifactInode:  intent.ArtifactInode,
		PolicyRevision: intent.PolicyRevision,
		PayloadHash:    intent.PayloadHash,
	}, nil
}

// systemdStartAcknowledgement is private evidence emitted only by the future
// trusted systemd adapter. The journal independently correlates every field
// with its reserved request before accepting the result.
type systemdStartAcknowledgement struct {
	Version        int
	CallerNodeID   string
	RequestID      string
	RemoteJobID    string
	LocalJobID     string
	StartAttemptID string
	UnitName       string
	ArtifactDigest string
	PolicyRevision uint64
	PayloadHash    string
	Outcome        string // accepted, rejected (definitive not-accepted), or unknown
	ReceivedAt     time.Time
}

func newSystemdStartAcknowledgement(request systemdStartRequest, outcome string, receivedAt time.Time) (systemdStartAcknowledgement, error) {
	ack := systemdStartAcknowledgement{
		Version: systemdStartProtocolVersion, CallerNodeID: request.CallerNodeID,
		RequestID: request.RequestID, RemoteJobID: request.RemoteJobID,
		LocalJobID: request.LocalJobID, StartAttemptID: request.StartAttemptID,
		UnitName: request.UnitName, ArtifactDigest: request.ArtifactDigest,
		PolicyRevision: request.PolicyRevision, PayloadHash: request.PayloadHash,
		Outcome: outcome, ReceivedAt: receivedAt,
	}
	return ack, ack.validate()
}

func (a systemdStartAcknowledgement) matchesRequest(request systemdStartRequest) bool {
	return a.Version == request.Version && a.CallerNodeID == request.CallerNodeID &&
		a.RequestID == request.RequestID && a.RemoteJobID == request.RemoteJobID &&
		a.LocalJobID == request.LocalJobID && a.StartAttemptID == request.StartAttemptID &&
		a.UnitName == request.UnitName && a.ArtifactDigest == request.ArtifactDigest &&
		a.PolicyRevision == request.PolicyRevision && a.PayloadHash == request.PayloadHash
}

func (a systemdStartAcknowledgement) validate() error {
	if a.Version != systemdStartProtocolVersion || !validID.MatchString(a.CallerNodeID) ||
		!validID.MatchString(a.RequestID) || !validID.MatchString(a.RemoteJobID) ||
		!validID.MatchString(a.LocalJobID) || len(a.StartAttemptID) != 32 ||
		len(a.ArtifactDigest) != 64 || len(a.PayloadHash) != 64 || a.PolicyRevision == 0 ||
		a.StartAttemptID != strings.ToLower(a.StartAttemptID) || a.ReceivedAt.IsZero() {
		return fmt.Errorf("invalid systemd start acknowledgement identity")
	}
	if _, err := hex.DecodeString(a.StartAttemptID); err != nil {
		return fmt.Errorf("invalid systemd start acknowledgement attempt ID")
	}
	if _, err := hex.DecodeString(a.ArtifactDigest); err != nil {
		return fmt.Errorf("invalid systemd acknowledgement artifact digest")
	}
	if _, err := hex.DecodeString(a.PayloadHash); err != nil {
		return fmt.Errorf("invalid systemd acknowledgement payload hash")
	}
	unit, err := ExecutionUnitName(a.LocalJobID, a.RequestID)
	if err != nil || unit != a.UnitName {
		return fmt.Errorf("systemd start acknowledgement unit mismatch")
	}
	switch a.Outcome {
	case "accepted", "rejected", "unknown":
	default:
		return fmt.Errorf("invalid systemd start acknowledgement outcome")
	}
	return nil
}

// systemdStartMethodReply is the minimal decoded D-Bus method-return envelope
// needed to classify StartTransientUnit. ReplySerial and Sender must come
// from the actual D-Bus reply header; neither may come from the job caller.
type systemdStartMethodReply struct {
	ReplySerial   uint32
	Sender        string
	JobObjectPath string
	ErrorName     string
	ReceivedAt    time.Time
}

// acknowledgementFromSystemdMethodReply accepts only a normal method return
// from the exact pinned systemd unique owner with a canonical job object path.
// Every error, owner change, malformed reply, or ambiguous result is unknown;
// this parser never manufactures a definitive rejection.
func acknowledgementFromSystemdMethodReply(session *systemdBusOwnerSession, request systemdStartRequest, reply systemdStartMethodReply) (systemdStartAcknowledgement, error) {
	outcome := "unknown"
	if session != nil {
		session.mu.Lock()
		ownerErr := session.validateReplyLocked(reply.Sender, reply.ReceivedAt)
		session.mu.Unlock()
		if ownerErr == nil && reply.ErrorName == "" && validSystemdJobObjectPath(reply.JobObjectPath) {
			outcome = "accepted"
		}
	}
	return newSystemdStartAcknowledgement(request, outcome, reply.ReceivedAt)
}

func validDBusUniqueName(value string) bool {
	if !strings.HasPrefix(value, ":") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, ":"), ".")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if len(part) > 1 && part[0] == '0' {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func validSystemdJobObjectPath(value string) bool {
	const prefix = "/org/freedesktop/systemd1/job/"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	id := strings.TrimPrefix(value, prefix)
	if id == "" || strings.Contains(id, "/") || (len(id) > 1 && id[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}
