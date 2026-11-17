//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const executableArtifactRegistryVersion = 1

type ExecutableArtifactRecord struct {
	Version        int       `json:"version"`
	CallerNodeID   string    `json:"caller_node_id"`
	RequestID      string    `json:"request_id"`
	RemoteJobID    string    `json:"remote_job_id"`
	LocalJobID     string    `json:"local_job_id"`
	PayloadHash    string    `json:"payload_hash"`
	PolicyRevision uint64    `json:"policy_revision"`
	Name           string    `json:"name"`
	Digest         string    `json:"digest"`
	Size           int64     `json:"size"`
	BrokerUID      uint32    `json:"broker_uid"`
	WorkerUID      uint32    `json:"worker_uid"`
	Device         uint64    `json:"device"`
	Inode          uint64    `json:"inode"`
	CreatedAt      time.Time `json:"created_at"`
}

type ExecutableArtifactRegistry struct {
	dir   string
	store *privateStateStore
}

func OpenExecutableArtifactRegistry(stateDir string) (*ExecutableArtifactRegistry, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	dir := filepath.Join(stateDir, "federation", "remote-executable-artifacts")
	store, err := openPrivateStateStore(dir)
	if err != nil {
		return nil, err
	}
	return &ExecutableArtifactRegistry{dir: dir, store: store}, nil
}

// Close releases the pinned metadata directory descriptor.
func (r *ExecutableArtifactRegistry) Close() error {
	if r == nil {
		return nil
	}
	return r.store.close()
}

// Register binds a staged inode to the already-prepared receiver execution.
// It is create-only and requires the ledger and execution record to agree.
// No process, systemd unit, or dispatch action is performed.
func (r *ExecutableArtifactRegistry) Register(stage *StagedExecutable, snapshot *ExecutableSnapshot, ledger *Ledger, records *ExecutionRecordStore, caller, request string, brokerUID, workerUID uint32) (ExecutableArtifactRecord, bool, error) {
	if r == nil || stage == nil || stage.File == nil || stage.dir == nil || snapshot == nil || ledger == nil || records == nil || !validID.MatchString(caller) || !validID.MatchString(request) || brokerUID == workerUID || !validExecutableStageName(stage.Name) {
		return ExecutableArtifactRecord{}, false, fmt.Errorf("valid staged artifact, stores, identity, and request are required")
	}
	if err := stage.File.Sync(); err != nil {
		return ExecutableArtifactRecord{}, false, fmt.Errorf("staged executable is not durable: %w", err)
	}
	consistency, err := InspectExecutionConsistency(ledger, records, caller, request)
	if err != nil || consistency != "consistent_prepared" {
		return ExecutableArtifactRecord{}, false, fmt.Errorf("execution must be consistently prepared before artifact registration: %s", consistency)
	}
	lr, err := ledger.Get(caller, request)
	if err != nil {
		return ExecutableArtifactRecord{}, false, err
	}
	er, err := records.Get(caller, request)
	if err != nil {
		return ExecutableArtifactRecord{}, false, err
	}
	if lr.Status != "accepted" || lr.LocalJobID != er.LocalJobID || lr.PolicyRevision != er.PolicyRevision {
		return ExecutableArtifactRecord{}, false, fmt.Errorf("execution is not in accepted prepared state")
	}
	verify, err := OpenBeneath(stage.dir, stage.Name, uint64(syscall.O_RDONLY|syscall.O_NOFOLLOW), 0)
	if err != nil {
		return ExecutableArtifactRecord{}, false, fmt.Errorf("open staged artifact for registry verification: %w", err)
	}
	defer verify.Close()
	info, err := verify.Stat()
	if err != nil {
		return ExecutableArtifactRecord{}, false, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != executableStageMode || st.Uid != brokerUID || info.Size() != snapshot.Size() {
		return ExecutableArtifactRecord{}, false, fmt.Errorf("staged artifact metadata mismatch")
	}
	openedInfo, err := stage.File.Stat()
	if err != nil {
		return ExecutableArtifactRecord{}, false, err
	}
	if !os.SameFile(info, openedInfo) {
		return ExecutableArtifactRecord{}, false, fmt.Errorf("staged artifact inode changed before registry binding")
	}
	h := sha256.New()
	if _, err := io.Copy(h, verify); err != nil {
		return ExecutableArtifactRecord{}, false, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if digest != snapshot.Digest() {
		return ExecutableArtifactRecord{}, false, fmt.Errorf("staged artifact digest mismatch")
	}
	record := ExecutableArtifactRecord{Version: executableArtifactRegistryVersion, CallerNodeID: caller, RequestID: request, RemoteJobID: lr.JobID, LocalJobID: lr.LocalJobID, PayloadHash: lr.PayloadHash, PolicyRevision: lr.PolicyRevision, Name: stage.Name, Digest: digest, Size: info.Size(), BrokerUID: brokerUID, WorkerUID: workerUID, Device: uint64(st.Dev), Inode: st.Ino, CreatedAt: time.Now().UTC()}
	var result ExecutableArtifactRecord
	created := false
	err = r.store.withLock(fileKey(caller, request)+".lock", func() error {
		if data, e := r.store.read(fileKey(caller, request) + ".json"); e == nil {
			var old ExecutableArtifactRecord
			if e := strictArtifactRecord(data, &old); e != nil {
				return e
			}
			if old.CallerNodeID != caller || old.RequestID != request || old.Name != record.Name || old.Digest != record.Digest || old.Device != record.Device || old.Inode != record.Inode || old.LocalJobID != record.LocalJobID {
				return ErrConflict
			}
			result = old
			return nil
		} else if !errors.Is(e, syscall.ENOENT) {
			return e
		}
		data, e := json.MarshalIndent(record, "", "  ")
		if e != nil {
			return e
		}
		if e = r.store.write(fileKey(caller, request)+".json", data, false); e != nil {
			if os.IsExist(e) {
				return ErrConflict
			}
			return e
		}
		result, created = record, true
		return nil
	})
	return result, created, err
}

func (r *ExecutableArtifactRegistry) Get(caller, request string) (ExecutableArtifactRecord, error) {
	var rec ExecutableArtifactRecord
	if r == nil || !validID.MatchString(caller) || !validID.MatchString(request) {
		return rec, fmt.Errorf("valid registry and request are required")
	}
	err := r.store.withLock(fileKey(caller, request)+".lock", func() error {
		data, err := r.store.read(fileKey(caller, request) + ".json")
		if err != nil {
			return err
		}
		return strictArtifactRecord(data, &rec)
	})
	return rec, err
}

func strictArtifactRecord(data []byte, rec *ExecutableArtifactRecord) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(rec); err != nil {
		return err
	}
	if _, err := hex.DecodeString(rec.Digest); err != nil {
		return fmt.Errorf("invalid artifact digest")
	}
	if _, err := hex.DecodeString(rec.PayloadHash); err != nil {
		return fmt.Errorf("invalid payload hash")
	}
	if rec.Version != executableArtifactRegistryVersion || !validID.MatchString(rec.CallerNodeID) || !validID.MatchString(rec.RequestID) || !validID.MatchString(rec.RemoteJobID) || !validID.MatchString(rec.LocalJobID) || !validExecutableStageName(rec.Name) || len(rec.Digest) != 64 || len(rec.PayloadHash) != 64 || rec.PolicyRevision == 0 || rec.Size <= 0 || rec.Inode == 0 || rec.BrokerUID == rec.WorkerUID || rec.CreatedAt.IsZero() {
		return fmt.Errorf("invalid executable artifact registry record")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("artifact record has trailing data")
	}
	return nil
}

// InspectArtifactConsistency is read-only and cross-checks the registry against
// the durable ledger and immutable execution record. It never deletes artifacts.
func InspectArtifactConsistency(registry *ExecutableArtifactRegistry, ledger *Ledger, records *ExecutionRecordStore, caller, request string) (string, error) {
	if registry == nil || ledger == nil || records == nil {
		return "", fmt.Errorf("artifact registry, ledger, and execution records are required")
	}
	artifact, ae := registry.Get(caller, request)
	lr, le := ledger.Get(caller, request)
	er, ee := records.Get(caller, request)
	if ae != nil {
		if errors.Is(ae, syscall.ENOENT) {
			return "artifact_record_missing", nil
		}
		return "", ae
	}
	if le != nil {
		if errors.Is(le, ErrNotFound) {
			return "ledger_record_missing", nil
		}
		return "", le
	}
	if ee != nil {
		if os.IsNotExist(ee) {
			return "execution_record_missing", nil
		}
		return "", ee
	}
	if artifact.CallerNodeID != caller || artifact.RequestID != request || artifact.RemoteJobID != lr.JobID || artifact.LocalJobID != lr.LocalJobID || artifact.LocalJobID != er.LocalJobID || artifact.PayloadHash != lr.PayloadHash || artifact.PayloadHash != er.PayloadHash || artifact.PolicyRevision != lr.PolicyRevision || artifact.PolicyRevision != er.PolicyRevision {
		return "binding_mismatch", nil
	}
	if lr.Status == "dispatching" || lr.Status == "running" || lr.Status == "unknown" {
		return "worker_state_requires_reconciliation", nil
	}
	if lr.Status == "completed" || lr.Status == "failed" {
		return "terminal_ledger_recorded_unit_absence_still_required", nil
	}
	if lr.Status != "accepted" {
		return "ledger_state_not_recoverable", nil
	}
	return "consistent_prepared_artifact", nil
}
