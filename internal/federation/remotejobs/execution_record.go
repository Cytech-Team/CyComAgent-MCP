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

// ExecutionRecordSchemaVersion identifies the immutable intent schema. Version 2
// adds ProcessHash so timeout and environment are bound alongside argv. Version 1
// records are intentionally rejected; they cannot prove the complete process intent.
const ExecutionRecordSchemaVersion = 2

// ExecutionRecord is the receiver-local durable intent/evidence envelope.
// It is deliberately separate from jobs.Job: creating it never launches a
// process and it contains no shell command string.
type ExecutionRecord struct {
	Version        int       `json:"version"`
	CallerNodeID   string    `json:"caller_node_id"`
	RequestID      string    `json:"request_id"`
	RemoteJobID    string    `json:"remote_job_id"`
	PayloadHash    string    `json:"payload_hash"`
	LocalJobID     string    `json:"local_job_id"`
	PolicyRevision uint64    `json:"policy_revision"`
	State          string    `json:"state"`
	Executable     string    `json:"executable"`
	WorkingDir     string    `json:"working_dir"`
	ArgsHash       string    `json:"args_hash"`
	ProcessHash    string    `json:"process_hash"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ExecutionRecordStore struct {
	dir   string
	store *privateStateStore
}

func OpenExecutionRecordStore(stateDir string) (*ExecutionRecordStore, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	dir := filepath.Join(stateDir, "federation", "remote-execution-records")
	store, err := openPrivateStateStore(dir)
	if err != nil {
		return nil, err
	}
	return &ExecutionRecordStore{dir: dir, store: store}, nil
}

func (s *ExecutionRecordStore) recordName(caller, request string) string {
	return fileKey(caller, request) + ".json"
}

// path is retained for package-level diagnostics and tests only. Runtime I/O
// must use the pinned store methods above.
func (s *ExecutionRecordStore) path(caller, request string) string {
	return filepath.Join(s.dir, s.recordName(caller, request))
}

// Close releases the pinned execution-record directory descriptor.
func (s *ExecutionRecordStore) Close() error {
	if s == nil {
		return nil
	}
	return s.store.close()
}

// CreateIntent is create-only and idempotent for byte-equivalent immutable
// bindings. It refuses to replace an existing record with different evidence.
func (s *ExecutionRecordStore) CreateIntent(rec ExecutionRecord) (ExecutionRecord, bool, error) {
	if rec.Version != ExecutionRecordSchemaVersion || !validID.MatchString(rec.CallerNodeID) || !validID.MatchString(rec.RequestID) || !validID.MatchString(rec.RemoteJobID) || !validID.MatchString(rec.LocalJobID) || len(rec.PayloadHash) != 64 || rec.PolicyRevision == 0 || rec.Executable == "" || rec.WorkingDir == "" || len(rec.ArgsHash) != 64 || len(rec.ProcessHash) != 64 {
		return ExecutionRecord{}, false, fmt.Errorf("invalid execution intent")
	}
	if rec.State != "prepared" {
		return ExecutionRecord{}, false, fmt.Errorf("new execution intent must be prepared")
	}
	name := s.recordName(rec.CallerNodeID, rec.RequestID)
	lockName := s.recordName(rec.CallerNodeID, rec.RequestID) + ".lock"
	var result ExecutionRecord
	created := false
	err := s.store.withLock(lockName, func() error {
		if data, err := s.store.read(name); err == nil {
			var old ExecutionRecord
			if err := strictExecutionRecord(data, &old); err != nil {
				return err
			}
			if !sameExecutionIntent(old, rec) {
				return ErrConflict
			}
			result = old
			return nil
		} else if !errors.Is(err, syscall.ENOENT) {
			return err
		}
		now := time.Now().UTC()
		rec.CreatedAt, rec.UpdatedAt = now, now
		data, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return err
		}
		if err := s.store.write(name, data, false); err != nil {
			return err
		}
		result, created = rec, true
		return nil
	})
	return result, created, err
}

// Get returns the durable record. Corrupt or unknown-schema records fail closed.
func (s *ExecutionRecordStore) Get(caller, request string) (ExecutionRecord, error) {
	var rec ExecutionRecord
	name := s.recordName(caller, request)
	err := s.store.withLock(name+".lock", func() error {
		data, err := s.store.read(name)
		if err != nil {
			return err
		}
		return strictExecutionRecord(data, &rec)
	})
	return rec, err
}

func sameExecutionIntent(a, b ExecutionRecord) bool {
	return a.Version == b.Version && a.CallerNodeID == b.CallerNodeID && a.RequestID == b.RequestID && a.RemoteJobID == b.RemoteJobID && a.PayloadHash == b.PayloadHash && a.LocalJobID == b.LocalJobID && a.PolicyRevision == b.PolicyRevision && a.Executable == b.Executable && a.WorkingDir == b.WorkingDir && a.ArgsHash == b.ArgsHash && a.ProcessHash == b.ProcessHash
}

func strictExecutionRecord(data []byte, rec *ExecutionRecord) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(rec); err != nil {
		return fmt.Errorf("decode execution record: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("execution record has trailing data")
	}
	if rec.Version != ExecutionRecordSchemaVersion || rec.State != "prepared" || !validID.MatchString(rec.CallerNodeID) || !validID.MatchString(rec.RequestID) || !validID.MatchString(rec.RemoteJobID) || !validID.MatchString(rec.LocalJobID) || len(rec.PayloadHash) != 64 || len(rec.ArgsHash) != 64 || len(rec.ProcessHash) != 64 || rec.PolicyRevision == 0 {
		return fmt.Errorf("invalid execution record")
	}
	return nil
}

// PrepareExecutionIntent coordinates the two independent durable stores without
// pretending they share an atomic transaction. The execution record is written
// first; the ledger binding follows. A crash between writes leaves an explicit
// detectable orphan and never authorizes dispatch.
func PrepareExecutionIntent(ledger *Ledger, store *ExecutionRecordStore, intent ExecutionRecord) (ExecutionRecord, bool, error) {
	return prepareExecutionIntent(ledger, store, intent, nil)
}

// prepareExecutionIntent exposes a deterministic boundary hook to package tests
// so a crash after the first durable write can be modeled without process launch.
func prepareExecutionIntent(ledger *Ledger, store *ExecutionRecordStore, intent ExecutionRecord, afterRecord func() error) (ExecutionRecord, bool, error) {
	if ledger == nil || store == nil {
		return ExecutionRecord{}, false, fmt.Errorf("ledger and execution record store are required")
	}
	ledgerFederationDir, err := filepath.Abs(filepath.Dir(ledger.dir))
	if err != nil {
		return ExecutionRecord{}, false, fmt.Errorf("resolve ledger federation directory: %w", err)
	}
	federationDir, err := filepath.EvalSymlinks(ledgerFederationDir)
	if err != nil {
		return ExecutionRecord{}, false, fmt.Errorf("resolve ledger federation directory: %w", err)
	}
	storeFederationPath, err := filepath.Abs(filepath.Dir(store.dir))
	if err != nil {
		return ExecutionRecord{}, false, fmt.Errorf("resolve execution-record federation directory: %w", err)
	}
	storeFederationDir, err := filepath.EvalSymlinks(storeFederationPath)
	if err != nil {
		return ExecutionRecord{}, false, fmt.Errorf("resolve execution-record federation directory: %w", err)
	}
	if federationDir != storeFederationDir {
		return ExecutionRecord{}, false, fmt.Errorf("ledger and execution record store must share a federation state directory")
	}
	keyHash := sha256.Sum256([]byte(recordKey(intent.CallerNodeID, intent.RequestID)))
	var result ExecutionRecord
	var wasCreated bool
	err = ledger.store.withLock("prepare-"+hex.EncodeToString(keyHash[:])+".lock", func() error {
		created, fresh, err := prepareExecutionIntentLocked(ledger, store, intent, afterRecord)
		if err != nil {
			return err
		}
		result, wasCreated = created, fresh
		return nil
	})
	return result, wasCreated, err
}

func prepareExecutionIntentLocked(ledger *Ledger, store *ExecutionRecordStore, intent ExecutionRecord, afterRecord func() error) (ExecutionRecord, bool, error) {
	rec, err := ledger.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil {
		return ExecutionRecord{}, false, err
	}
	if rec.Status != "accepted" {
		return ExecutionRecord{}, false, fmt.Errorf("execution intent requires accepted ledger state")
	}
	if rec.PayloadHash != intent.PayloadHash || rec.JobID != intent.RemoteJobID {
		return ExecutionRecord{}, false, ErrConflict
	}
	created, fresh, err := store.CreateIntent(intent)
	if err != nil {
		return ExecutionRecord{}, false, err
	}
	if afterRecord != nil {
		if err := afterRecord(); err != nil {
			return created, fresh, err
		}
	}
	bound, err := ledger.BindExecutionMetadata(intent.CallerNodeID, intent.RequestID, intent.LocalJobID, intent.PolicyRevision)
	if err != nil {
		return created, fresh, err
	}
	if bound.PayloadHash != created.PayloadHash || bound.LocalJobID != created.LocalJobID || bound.PolicyRevision != created.PolicyRevision {
		return created, fresh, fmt.Errorf("ledger and execution record binding mismatch")
	}
	return created, fresh, nil
}

// InspectExecutionConsistency is read-only. It labels interrupted cross-store
// writes for operator reconciliation and never repairs or launches anything.
func InspectExecutionConsistency(ledger *Ledger, store *ExecutionRecordStore, caller, request string) (string, error) {
	if ledger == nil || store == nil {
		return "", fmt.Errorf("ledger and execution record store are required")
	}
	lr, le := ledger.Get(caller, request)
	er, ee := store.Get(caller, request)
	ledgerMissing := errors.Is(le, ErrNotFound)
	executionMissing := errors.Is(ee, os.ErrNotExist)
	if le != nil && !ledgerMissing {
		return "", le
	}
	if ee != nil && !executionMissing {
		return "", ee
	}
	if ledgerMissing && executionMissing {
		return "both_missing", nil
	}
	if ledgerMissing {
		return "ledger_record_missing", nil
	}
	if executionMissing {
		return "execution_record_missing", nil
	}
	if lr.PayloadHash != er.PayloadHash || lr.JobID != er.RemoteJobID {
		return "binding_mismatch", nil
	}
	if lr.LocalJobID == "" || lr.PolicyRevision == 0 {
		return "ledger_binding_incomplete", nil
	}
	if lr.LocalJobID != er.LocalJobID || lr.PolicyRevision != er.PolicyRevision {
		return "binding_mismatch", nil
	}
	if lr.Status != "accepted" {
		return "ledger_not_dispatchable", nil
	}
	return "consistent_prepared", nil
}
