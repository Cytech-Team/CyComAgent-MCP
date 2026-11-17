//go:build linux

package remotejobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ArtifactFileObservation is read-only evidence obtained by reopening the
// opaque artifact name relative to a trusted, already-open staging directory.
type ArtifactFileObservation struct {
	Present         bool
	IdentityMatches bool
	DigestMatches   bool
	SizeMatches     bool
}

// VerifyRegisteredArtifactFile checks the registered device/inode, owner,
// mode, size and digest without following symlinks or resolving a pathname
// outside the pinned staging directory. It never changes or deletes a file.
func VerifyRegisteredArtifactFile(directory *os.File, record ExecutableArtifactRecord) (ArtifactFileObservation, error) {
	var observation ArtifactFileObservation
	if directory == nil || !validExecutableStageName(record.Name) || record.Size <= 0 || record.Inode == 0 || record.BrokerUID == record.WorkerUID || len(record.Digest) != 64 {
		return observation, fmt.Errorf("valid pinned directory and artifact record are required")
	}
	if _, err := hex.DecodeString(record.Digest); err != nil {
		return observation, fmt.Errorf("invalid registered digest")
	}
	dirInfo, err := directory.Stat()
	if err != nil || !dirInfo.IsDir() {
		return observation, fmt.Errorf("staging descriptor is not a directory")
	}
	dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok || dirStat.Uid != record.BrokerUID || dirInfo.Mode().Perm()&0022 != 0 || dirInfo.Mode().Perm()&0001 == 0 {
		return observation, fmt.Errorf("staging directory ownership or permissions are unsafe")
	}
	file, err := OpenBeneath(directory, record.Name, uint64(syscall.O_RDONLY|syscall.O_NOFOLLOW), 0)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOENT) {
			return observation, nil
		}
		return observation, fmt.Errorf("open registered artifact: %w", err)
	}
	defer file.Close()
	observation.Present = true
	info, err := file.Stat()
	if err != nil {
		return observation, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != executableStageMode || st.Uid != record.BrokerUID {
		return observation, fmt.Errorf("registered artifact type, owner, or mode changed")
	}
	observation.IdentityMatches = uint64(st.Dev) == record.Device && st.Ino == record.Inode
	observation.SizeMatches = info.Size() == record.Size
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return observation, err
	}
	observation.DigestMatches = hex.EncodeToString(h.Sum(nil)) == record.Digest
	return observation, nil
}

// InspectArtifactRecovery combines durable cross-store binding with a fresh
// descriptor-relative artifact check. It deliberately stops short of cleanup:
// the current launcher uses a PID/time-derived transient scope name that is not
// durably bound to this artifact, so unit absence cannot yet be proven per job.
func InspectArtifactRecovery(registry *ExecutableArtifactRegistry, ledger *Ledger, records *ExecutionRecordStore, directory *os.File, caller, request string) (string, ArtifactFileObservation, error) {
	status, err := InspectArtifactConsistency(registry, ledger, records, caller, request)
	if err != nil || status != "consistent_prepared_artifact" && status != "terminal_ledger_recorded_unit_absence_still_required" && status != "worker_state_requires_reconciliation" {
		return status, ArtifactFileObservation{}, err
	}
	record, err := registry.Get(caller, request)
	if err != nil {
		return "", ArtifactFileObservation{}, err
	}
	observation, err := VerifyRegisteredArtifactFile(directory, record)
	if err != nil {
		return "artifact_file_invalid", observation, err
	}
	if !observation.Present {
		return "artifact_file_missing", observation, nil
	}
	if !observation.IdentityMatches || !observation.SizeMatches || !observation.DigestMatches {
		return "artifact_file_binding_mismatch", observation, nil
	}
	return "artifact_verified_unit_identity_unavailable", observation, nil
}

// RecoveryGateObservation combines the durable job bindings with exact-unit
// and cgroup-v2 observations while holding the same per-job journal flock used
// by Prepare and Transition. It deliberately never authorizes deletion.
type RecoveryGateObservation struct {
	Intent   LaunchIntentRecord
	Artifact ArtifactFileObservation
	Unit     SystemdUnitObservation
	Cgroup   CgroupObservation
	Status   string
}

// InspectSerializedRecoveryGate performs a conservative terminal-state
// inspection under the per-job launch-intent lock. Every future launcher must
// take this same lock across its durable launch transition and systemd start;
// current legacy launch paths do not, so this API is evidence-only and is not
// a cleanup gate.
func (j *LaunchIntentJournal) InspectSerializedRecoveryGate(ctx context.Context, registry *ExecutableArtifactRegistry, ledger *Ledger, records *ExecutionRecordStore, stagingDir, cgroupRoot *os.File, inspector *SystemdUnitInspector, caller, request string) (RecoveryGateObservation, error) {
	var out RecoveryGateObservation
	if j == nil || ctx == nil || registry == nil || ledger == nil || records == nil || stagingDir == nil || cgroupRoot == nil || inspector == nil || !validID.MatchString(caller) || !validID.MatchString(request) {
		return out, fmt.Errorf("journal, stores, pinned directories, inspector, and job identity are required")
	}
	err := j.withLifecycleLock(caller, request, func() error {
		data, err := j.store.read(fileKey(caller, request) + ".json")
		if err != nil {
			return err
		}
		if err := strictLaunchIntentRecord(data, &out.Intent); err != nil {
			return err
		}
		if out.Intent.State != "completed" && out.Intent.State != "failed" {
			out.Status = "journal_not_terminal"
			return nil
		}
		consistency, err := InspectArtifactConsistency(registry, ledger, records, caller, request)
		if err != nil {
			return err
		}
		if consistency != "terminal_ledger_recorded_unit_absence_still_required" {
			out.Status = "durable_state_not_terminal_or_consistent:" + consistency
			return nil
		}
		ledgerRecord, err := ledger.Get(caller, request)
		if err != nil {
			return err
		}
		if ledgerRecord.Status != out.Intent.State || ledgerRecord.LocalJobID != out.Intent.LocalJobID || ledgerRecord.JobID != out.Intent.RemoteJobID || ledgerRecord.PayloadHash != out.Intent.PayloadHash || ledgerRecord.PolicyRevision != out.Intent.PolicyRevision {
			out.Status = "journal_ledger_binding_mismatch"
			return nil
		}
		artifactRecord, err := registry.Get(caller, request)
		if err != nil {
			return err
		}
		if artifactRecord.Name != out.Intent.ArtifactName || artifactRecord.Digest != out.Intent.ArtifactDigest || artifactRecord.Device != out.Intent.ArtifactDevice || artifactRecord.Inode != out.Intent.ArtifactInode {
			out.Status = "journal_artifact_binding_mismatch"
			return nil
		}
		out.Artifact, err = VerifyRegisteredArtifactFile(stagingDir, artifactRecord)
		if err != nil {
			out.Status = "artifact_file_invalid"
			return err
		}
		if !out.Artifact.Present || !out.Artifact.IdentityMatches || !out.Artifact.SizeMatches || !out.Artifact.DigestMatches {
			out.Status = "artifact_file_not_verified"
			return nil
		}
		out.Unit, err = inspector.Inspect(ctx, out.Intent)
		if err != nil {
			out.Status = "systemd_unit_unknown"
			return err
		}
		if err := validateRecoveryUnitIdentity(out.Intent, out.Unit); err != nil {
			out.Status = "systemd_unit_not_proven_inactive_or_identity_mismatch"
			return nil
		}
		out.Cgroup, err = VerifyCgroupAbsence(cgroupRoot, out.Unit)
		if err != nil {
			out.Status = "cgroup_unknown"
			return err
		}
		if out.Cgroup.Status != "empty_observed" {
			out.Status = "cgroup_not_proven_empty:" + out.Cgroup.Status
			return nil
		}
		out.Status = "terminal_artifact_unit_cgroup_absence_observed_not_cleanup_authorized"
		return nil
	})
	return out, err
}

// validateRecoveryUnitIdentity binds the observation to the exact deterministic
// unit and requires evidence of a real prior invocation. A terminal journal
// state without an InvocationID cannot establish that the queried cgroup is the
// workload that produced that terminal state.
func validateRecoveryUnitIdentity(intent LaunchIntentRecord, unit SystemdUnitObservation) error {
	if unit.UnitName != intent.UnitName || unit.Status != "inactive" || unit.LoadState != "loaded" || unit.ActiveState != "inactive" {
		return fmt.Errorf("systemd unit is not the exact loaded inactive unit")
	}
	if len(intent.SystemdInvocationID) != 32 || unit.InvocationID != intent.SystemdInvocationID {
		return fmt.Errorf("systemd InvocationID is not durably bound to this launch intent")
	}
	if _, err := hex.DecodeString(intent.SystemdInvocationID); err != nil {
		return fmt.Errorf("durable systemd InvocationID is malformed")
	}
	path := unit.ControlGroup
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || filepath.Base(path) != intent.UnitName {
		return fmt.Errorf("systemd ControlGroup does not bind to the expected unit")
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("systemd ControlGroup contains an unsafe path component")
		}
	}
	return nil
}

const MinimumArtifactRetentionAge = 26 * time.Hour

// ArtifactRetentionAssessment is a read-only retention candidate report. Even
// an eligible result is not deletion authorization: the unit/cgroup evidence
// is a point-in-time observation and can become stale immediately.
type ArtifactRetentionAssessment struct {
	Gate       RecoveryGateObservation
	Age        time.Duration
	MinimumAge time.Duration
	Status     string
}

// AssessArtifactRetention serializes the recovery inspection with the shared
// job lifecycle lock and reports whether the durable terminal artifact is old
// enough for operator review. It never unlinks, renames, prunes, or changes any
// durable record or systemd state.
func (j *LaunchIntentJournal) AssessArtifactRetention(ctx context.Context, registry *ExecutableArtifactRegistry, ledger *Ledger, records *ExecutionRecordStore, stagingDir, cgroupRoot *os.File, inspector *SystemdUnitInspector, caller, request string, now time.Time) (ArtifactRetentionAssessment, error) {
	var assessment ArtifactRetentionAssessment
	assessment.MinimumAge = MinimumArtifactRetentionAge
	if now.IsZero() {
		return assessment, fmt.Errorf("assessment time is required")
	}
	gate, err := j.InspectSerializedRecoveryGate(ctx, registry, ledger, records, stagingDir, cgroupRoot, inspector, caller, request)
	assessment.Gate = gate
	if err != nil {
		assessment.Status = "assessment_unknown"
		return assessment, err
	}
	if gate.Status != "terminal_artifact_unit_cgroup_absence_observed_not_cleanup_authorized" {
		assessment.Status = "not_eligible:" + gate.Status
		return assessment, nil
	}
	if gate.Intent.CreatedAt.IsZero() || gate.Intent.CreatedAt.After(now) {
		assessment.Status = "invalid_or_future_launch_intent_time"
		return assessment, nil
	}
	assessment.Age = now.Sub(gate.Intent.CreatedAt)
	if assessment.Age < assessment.MinimumAge {
		assessment.Status = "retention_period_not_elapsed"
		return assessment, nil
	}
	assessment.Status = "eligible_for_operator_review_not_cleanup_authorized"
	return assessment, nil
}
