//go:build linux

package remotejobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const launchIntentJournalVersion = 1

type LaunchIntentRecord struct {
	Version                 int       `json:"version"`
	CallerNodeID            string    `json:"caller_node_id"`
	RequestID               string    `json:"request_id"`
	RemoteJobID             string    `json:"remote_job_id"`
	LocalJobID              string    `json:"local_job_id"`
	UnitName                string    `json:"unit_name"`
	SystemdInvocationID     string    `json:"systemd_invocation_id,omitempty"`
	StartAttemptID          string    `json:"start_attempt_id,omitempty"`
	StartRequestReservedAt  time.Time `json:"start_request_reserved_at,omitempty"`
	StartRequestOutcome     string    `json:"start_request_outcome,omitempty"`
	StartRequestAckAt       time.Time `json:"start_request_ack_at,omitempty"`
	StartRequestUncertainAt time.Time `json:"start_request_uncertain_at,omitempty"`
	ArtifactName            string    `json:"artifact_name"`
	ArtifactDigest          string    `json:"artifact_digest"`
	ArtifactDevice          uint64    `json:"artifact_device"`
	ArtifactInode           uint64    `json:"artifact_inode"`
	PolicyRevision          uint64    `json:"policy_revision"`
	PayloadHash             string    `json:"payload_hash"`
	State                   string    `json:"state"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type LaunchIntentJournal struct {
	dir      string
	stateDir string
	store    *privateStateStore
}

func OpenLaunchIntentJournal(stateDir string) (*LaunchIntentJournal, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	dir := filepath.Join(stateDir, "federation", "remote-launch-intents")
	store, err := openPrivateStateStore(dir)
	if err != nil {
		return nil, err
	}
	return &LaunchIntentJournal{dir: dir, stateDir: stateDir, store: store}, nil
}

// Close releases the pinned metadata directory descriptor.
func (j *LaunchIntentJournal) Close() error {
	if j == nil {
		return nil
	}
	return j.store.close()
}

func (j *LaunchIntentJournal) withLifecycleLock(caller, request string, fn func() error) error {
	return WithJobLifecycleLock(j.stateDir, caller, request, fn)
}

// Prepare creates the durable per-job systemd identity before any launch.
// It is create-only, idempotent for an identical binding, and performs no
// systemd, process, IPC, or dispatch operation.
func (j *LaunchIntentJournal) Prepare(registry *ExecutableArtifactRegistry, ledger *Ledger, records *ExecutionRecordStore, caller, request string) (LaunchIntentRecord, bool, error) {
	var out LaunchIntentRecord
	if j == nil || registry == nil || ledger == nil || records == nil || !validID.MatchString(caller) || !validID.MatchString(request) {
		return out, false, fmt.Errorf("journal, stores, and valid job identity are required")
	}
	created := false
	err := j.withLifecycleLock(caller, request, func() error {
		// All cross-store reads happen after acquiring the common lifecycle lock;
		// otherwise a Coordinator could dispatch between preflight and journal write.
		status, err := InspectArtifactConsistency(registry, ledger, records, caller, request)
		if err != nil {
			return err
		}
		if status != "consistent_prepared_artifact" {
			return fmt.Errorf("launch intent requires consistent prepared artifact, got %s", status)
		}
		artifact, err := registry.Get(caller, request)
		if err != nil {
			return err
		}
		lr, err := ledger.Get(caller, request)
		if err != nil {
			return err
		}
		unit, err := ExecutionUnitName(lr.LocalJobID, request)
		if err != nil {
			return err
		}
		rec := LaunchIntentRecord{Version: launchIntentJournalVersion, CallerNodeID: caller, RequestID: request, RemoteJobID: lr.JobID, LocalJobID: lr.LocalJobID, UnitName: unit, ArtifactName: artifact.Name, ArtifactDigest: artifact.Digest, ArtifactDevice: artifact.Device, ArtifactInode: artifact.Inode, PolicyRevision: lr.PolicyRevision, PayloadHash: lr.PayloadHash, State: "prepared"}
		if data, e := j.store.read(fileKey(caller, request) + ".json"); e == nil {
			var old LaunchIntentRecord
			if e := strictLaunchIntentRecord(data, &old); e != nil {
				return e
			}
			if !sameLaunchIntentBinding(old, rec) {
				return ErrConflict
			}
			out = old
			return nil
		} else if !errors.Is(e, syscall.ENOENT) {
			return e
		}
		now := time.Now().UTC()
		rec.CreatedAt, rec.UpdatedAt = now, now
		data, e := json.MarshalIndent(rec, "", "  ")
		if e != nil {
			return e
		}
		if e = j.store.write(fileKey(caller, request)+".json", data, false); e != nil {
			if os.IsExist(e) {
				return ErrConflict
			}
			return e
		}
		out, created = rec, true
		return nil
	})
	return out, created, err
}

func sameLaunchIntentBinding(a, b LaunchIntentRecord) bool {
	return a.Version == b.Version && a.CallerNodeID == b.CallerNodeID && a.RequestID == b.RequestID && a.RemoteJobID == b.RemoteJobID && a.LocalJobID == b.LocalJobID && a.UnitName == b.UnitName && a.ArtifactName == b.ArtifactName && a.ArtifactDigest == b.ArtifactDigest && a.ArtifactDevice == b.ArtifactDevice && a.ArtifactInode == b.ArtifactInode && a.PolicyRevision == b.PolicyRevision && a.PayloadHash == b.PayloadHash
}

func (j *LaunchIntentJournal) Get(caller, request string) (LaunchIntentRecord, error) {
	var rec LaunchIntentRecord
	if j == nil || !validID.MatchString(caller) || !validID.MatchString(request) {
		return rec, fmt.Errorf("journal and valid identity are required")
	}
	err := j.withLifecycleLock(caller, request, func() error {
		data, err := j.store.read(fileKey(caller, request) + ".json")
		if err != nil {
			return err
		}
		return strictLaunchIntentRecord(data, &rec)
	})
	return rec, err
}

// bindObservedSystemdInvocation binds an InvocationID only after querying the
// exact journal-bound unit through the read-only inspector while holding the
// per-job lifecycle lock. It proves that this exact unit is currently active;
// it does not prove which caller requested the start or authorize a launch.
func (j *LaunchIntentJournal) bindObservedSystemdInvocation(ctx context.Context, inspector *SystemdUnitInspector, caller, request string) (LaunchIntentRecord, error) {
	var out LaunchIntentRecord
	if j == nil || inspector == nil || ctx == nil || !validID.MatchString(caller) || !validID.MatchString(request) {
		return out, fmt.Errorf("journal, inspector, context, and valid identity are required")
	}
	err := j.withLifecycleLock(caller, request, func() error {
		data, err := j.store.read(fileKey(caller, request) + ".json")
		if err != nil {
			return err
		}
		if err := strictLaunchIntentRecord(data, &out); err != nil {
			return err
		}
		if out.State != "launching" || out.StartRequestOutcome != "accepted" || out.StartRequestAckAt.IsZero() {
			return fmt.Errorf("InvocationID requires a durable accepted start acknowledgement while launching")
		}
		observation, err := inspector.Inspect(ctx, out)
		if err != nil {
			return err
		}
		if observation.UnitName != out.UnitName || observation.LoadState != "loaded" || observation.ActiveState != "active" || observation.Status != "active" {
			return fmt.Errorf("exact systemd unit is not verified active")
		}
		if len(observation.InvocationID) != 32 {
			return fmt.Errorf("active systemd unit has no valid InvocationID")
		}
		if _, err := hex.DecodeString(observation.InvocationID); err != nil {
			return fmt.Errorf("active systemd unit has malformed InvocationID")
		}
		if observation.InvocationID != strings.ToLower(observation.InvocationID) {
			return fmt.Errorf("active systemd unit InvocationID is not canonical")
		}
		if !path.IsAbs(observation.ControlGroup) || path.Clean(observation.ControlGroup) != observation.ControlGroup || observation.ControlGroup == "/" || !strings.HasSuffix(observation.ControlGroup, "/"+out.UnitName) {
			return fmt.Errorf("active systemd unit has invalid or mismatched ControlGroup")
		}
		if observation.ObservedAt.IsZero() || observation.ObservedAt.After(time.Now().Add(time.Minute)) {
			return fmt.Errorf("systemd observation timestamp is missing or in the future")
		}
		if out.SystemdInvocationID != "" {
			if out.SystemdInvocationID == observation.InvocationID {
				return nil
			}
			return fmt.Errorf("systemd InvocationID binding is immutable")
		}
		out.SystemdInvocationID = observation.InvocationID
		out.UpdatedAt = time.Now().UTC()
		encoded, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		return j.store.write(fileKey(caller, request)+".json", encoded, true)
	})
	return out, err
}

// Transition durably records the start-attempt identity before any future
// adapter may issue a systemd request. This package never starts a worker itself.
func (j *LaunchIntentJournal) Transition(caller, request, next string) (LaunchIntentRecord, error) {
	var out LaunchIntentRecord
	if j == nil || !validID.MatchString(caller) || !validID.MatchString(request) {
		return out, fmt.Errorf("journal and valid identity are required")
	}
	err := j.withLifecycleLock(caller, request, func() error {
		data, err := j.store.read(fileKey(caller, request) + ".json")
		if err != nil {
			return err
		}
		if err := strictLaunchIntentRecord(data, &out); err != nil {
			return err
		}
		if !allowedLaunchIntentTransition(out.State, next) {
			return fmt.Errorf("%w: launch intent %s -> %s", ErrTransition, out.State, next)
		}
		if !out.StartRequestReservedAt.IsZero() {
			if next == "unknown" && out.StartRequestOutcome != "accepted" && out.StartRequestOutcome != "unknown" && out.StartRequestUncertainAt.IsZero() {
				return fmt.Errorf("reserved start request requires an acknowledgement or explicit crash-recovery uncertainty before becoming unknown")
			}
			if next == "failed" && out.StartRequestOutcome == "" {
				return fmt.Errorf("reserved start request requires a definitive rejection acknowledgement before failing")
			}
			if next == "failed" && out.StartRequestOutcome != "rejected" && out.StartRequestOutcome != "accepted" {
				return fmt.Errorf("failed state requires a definitive rejection or an accepted job failure")
			}
			if (next == "running" || next == "completed") && out.StartRequestOutcome != "accepted" {
				return fmt.Errorf("running/completed requires an accepted systemd start acknowledgement")
			}
		}
		if next == "launching" {
			var attempt [16]byte
			if _, err := rand.Read(attempt[:]); err != nil {
				return fmt.Errorf("create durable systemd start attempt identity: %w", err)
			}
			out.StartAttemptID = hex.EncodeToString(attempt[:])
		}
		if (next == "running" || next == "completed") && out.SystemdInvocationID == "" {
			return fmt.Errorf("launch intent cannot enter %s without a verified systemd InvocationID", next)
		}
		out.State, out.UpdatedAt = next, time.Now().UTC()
		encoded, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		return j.store.write(fileKey(caller, request)+".json", encoded, true)
	})
	return out, err
}

// recordSystemdStartAcknowledgement durably records one correlated reply from
// the future trusted adapter. It is intentionally package-private: network or
// federation callers cannot manufacture systemd acknowledgement evidence.
func (j *LaunchIntentJournal) recordSystemdStartAcknowledgement(ack systemdStartAcknowledgement) (LaunchIntentRecord, error) {
	var out LaunchIntentRecord
	if j == nil || !validID.MatchString(ack.CallerNodeID) || !validID.MatchString(ack.RequestID) {
		return out, fmt.Errorf("journal and valid acknowledgement identity are required")
	}
	if err := ack.validate(); err != nil {
		return out, err
	}
	err := j.withLifecycleLock(ack.CallerNodeID, ack.RequestID, func() error {
		data, err := j.store.read(fileKey(ack.CallerNodeID, ack.RequestID) + ".json")
		if err != nil {
			return err
		}
		if err := strictLaunchIntentRecord(data, &out); err != nil {
			return err
		}
		if out.State != "launching" || out.StartRequestReservedAt.IsZero() {
			return fmt.Errorf("systemd acknowledgement requires a reserved launching intent")
		}
		request, err := systemdStartRequestFromIntent(out)
		if err != nil {
			return err
		}
		if !ack.matchesRequest(request) {
			return fmt.Errorf("systemd acknowledgement does not match the reserved launch attempt")
		}
		if !out.StartRequestAckAt.IsZero() || out.StartRequestOutcome != "" {
			return fmt.Errorf("systemd start acknowledgement is immutable; duplicate reply rejected")
		}
		if ack.ReceivedAt.Before(out.StartRequestReservedAt) || ack.ReceivedAt.After(time.Now().Add(time.Minute)) {
			return fmt.Errorf("systemd acknowledgement timestamp is outside the valid reservation window")
		}
		out.StartRequestOutcome = ack.Outcome
		out.StartRequestAckAt = ack.ReceivedAt.UTC()
		if ack.Outcome == "rejected" {
			out.State = "failed"
		}
		if ack.Outcome == "unknown" {
			out.State = "unknown"
		}
		out.UpdatedAt = time.Now().UTC()
		encoded, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		return j.store.write(fileKey(ack.CallerNodeID, ack.RequestID)+".json", encoded, true)
	})
	return out, err
}

// markReservedStartUncertain records the conservative recovery outcome when a
// process restarts with a durable one-shot reservation but no acknowledgement.
// It deliberately does not populate StartRequestOutcome or StartRequestAckAt:
// absence of a reply is not an acknowledgement. The attempt becomes terminal
// unknown and can never be reserved or sent again.
func (j *LaunchIntentJournal) markReservedStartUncertain(caller, request string) (LaunchIntentRecord, error) {
	var out LaunchIntentRecord
	if j == nil || !validID.MatchString(caller) || !validID.MatchString(request) {
		return out, fmt.Errorf("journal and valid identity are required")
	}
	err := j.withLifecycleLock(caller, request, func() error {
		data, err := j.store.read(fileKey(caller, request) + ".json")
		if err != nil {
			return err
		}
		if err := strictLaunchIntentRecord(data, &out); err != nil {
			return err
		}
		if out.State == "unknown" && !out.StartRequestUncertainAt.IsZero() {
			return nil
		}
		if out.State != "launching" || out.StartRequestReservedAt.IsZero() || out.StartRequestOutcome != "" || !out.StartRequestAckAt.IsZero() {
			return fmt.Errorf("crash recovery uncertainty requires a reserved launching request with no acknowledgement")
		}
		out.StartRequestUncertainAt = time.Now().UTC()
		out.State = "unknown"
		out.UpdatedAt = out.StartRequestUncertainAt
		encoded, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		return j.store.write(fileKey(caller, request)+".json", encoded, true)
	})
	return out, err
}

// reserveSystemdStartRequest is the one-shot write-ahead gate for a future
// trusted adapter. The durable reservation is committed before a request can
// leave this process. Any crash after this write is ambiguous and must become
// unknown; the same attempt must never be reserved or sent again.
func (j *LaunchIntentJournal) reserveSystemdStartRequest(caller, request string) (systemdStartRequest, error) {
	var out systemdStartRequest
	if j == nil || !validID.MatchString(caller) || !validID.MatchString(request) {
		return out, fmt.Errorf("journal and valid identity are required")
	}
	err := j.withLifecycleLock(caller, request, func() error {
		data, err := j.store.read(fileKey(caller, request) + ".json")
		if err != nil {
			return err
		}
		var intent LaunchIntentRecord
		if err := strictLaunchIntentRecord(data, &intent); err != nil {
			return err
		}
		if intent.State != "launching" || intent.StartAttemptID == "" {
			return fmt.Errorf("systemd start reservation requires a launching intent")
		}
		if !intent.StartRequestReservedAt.IsZero() {
			return fmt.Errorf("systemd start request was already reserved; replay is forbidden")
		}
		intent.StartRequestReservedAt = time.Now().UTC()
		encoded, err := json.MarshalIndent(intent, "", "  ")
		if err != nil {
			return err
		}
		if err := j.store.write(fileKey(caller, request)+".json", encoded, true); err != nil {
			return err
		}
		out, err = systemdStartRequestFromIntent(intent)
		return err
	})
	return out, err
}

func allowedLaunchIntentTransition(from, to string) bool {
	switch from {
	case "prepared":
		return to == "launching" || to == "failed" || to == "unknown"
	case "launching":
		return to == "running" || to == "completed" || to == "failed" || to == "unknown"
	case "running":
		return to == "completed" || to == "failed" || to == "unknown"
	default:
		return false
	}
}

func strictLaunchIntentRecord(data []byte, rec *LaunchIntentRecord) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(rec); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("launch intent has trailing data")
	}
	unit, err := ExecutionUnitName(rec.LocalJobID, rec.RequestID)
	if err != nil || rec.UnitName != unit || rec.Version != launchIntentJournalVersion || !validID.MatchString(rec.CallerNodeID) || !validID.MatchString(rec.RequestID) || !validID.MatchString(rec.RemoteJobID) || !validID.MatchString(rec.LocalJobID) || !validExecutableStageName(rec.ArtifactName) || len(rec.ArtifactDigest) != 64 || rec.ArtifactDevice == 0 || rec.ArtifactInode == 0 || rec.PolicyRevision == 0 || len(rec.PayloadHash) != 64 || rec.CreatedAt.IsZero() || rec.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid launch intent record")
	}
	if _, err := hex.DecodeString(rec.ArtifactDigest); err != nil {
		return fmt.Errorf("invalid artifact digest")
	}
	if _, err := hex.DecodeString(rec.PayloadHash); err != nil {
		return fmt.Errorf("invalid payload hash")
	}
	if rec.StartAttemptID != "" {
		if len(rec.StartAttemptID) != 32 {
			return fmt.Errorf("invalid systemd start attempt identity")
		}
		if _, err := hex.DecodeString(rec.StartAttemptID); err != nil || rec.StartAttemptID != strings.ToLower(rec.StartAttemptID) {
			return fmt.Errorf("invalid systemd start attempt identity")
		}
	}
	if (rec.State == "launching" || rec.State == "running" || rec.State == "completed") && rec.StartAttemptID == "" {
		return fmt.Errorf("launch intent state requires a durable systemd start attempt identity")
	}
	if !rec.StartRequestReservedAt.IsZero() && (rec.StartAttemptID == "" || (rec.State != "launching" && rec.State != "running" && rec.State != "completed" && rec.State != "failed" && rec.State != "unknown")) {
		return fmt.Errorf("systemd start reservation is inconsistent with launch intent state")
	}
	if !rec.StartRequestUncertainAt.IsZero() {
		if rec.StartRequestReservedAt.IsZero() || rec.State != "unknown" || rec.StartRequestOutcome != "" || !rec.StartRequestAckAt.IsZero() || rec.StartRequestUncertainAt.Before(rec.StartRequestReservedAt) {
			return fmt.Errorf("crash-recovery uncertainty marker is inconsistent with the reserved start request")
		}
	}
	if rec.StartRequestOutcome == "" {
		if !rec.StartRequestAckAt.IsZero() {
			return fmt.Errorf("systemd acknowledgement timestamp has no outcome")
		}
		if !rec.StartRequestReservedAt.IsZero() && rec.State == "unknown" && rec.StartRequestUncertainAt.IsZero() {
			return fmt.Errorf("unknown reserved start request lacks acknowledgement or crash-recovery uncertainty")
		}
	} else {
		if !rec.StartRequestUncertainAt.IsZero() {
			return fmt.Errorf("acknowledged start request cannot also carry crash-recovery uncertainty")
		}
		if rec.StartRequestReservedAt.IsZero() || rec.StartRequestAckAt.IsZero() || rec.StartRequestAckAt.Before(rec.StartRequestReservedAt) {
			return fmt.Errorf("systemd acknowledgement is missing or predates its reservation")
		}
		switch rec.StartRequestOutcome {
		case "accepted":
		case "rejected":
			if rec.State != "failed" {
				return fmt.Errorf("rejected start acknowledgement requires failed state")
			}
		case "unknown":
			if rec.State != "unknown" {
				return fmt.Errorf("unknown start acknowledgement requires unknown state")
			}
		default:
			return fmt.Errorf("invalid systemd start acknowledgement outcome")
		}
	}
	if rec.SystemdInvocationID != "" {
		if len(rec.SystemdInvocationID) != 32 {
			return fmt.Errorf("invalid systemd InvocationID")
		}
		if _, err := hex.DecodeString(rec.SystemdInvocationID); err != nil || rec.SystemdInvocationID != strings.ToLower(rec.SystemdInvocationID) {
			return fmt.Errorf("invalid systemd InvocationID")
		}
	}
	if rec.State != "prepared" && rec.State != "launching" && rec.State != "running" && rec.State != "completed" && rec.State != "failed" && rec.State != "unknown" {
		return fmt.Errorf("invalid launch intent state")
	}
	return nil
}
