// Package remotejobs stores durable idempotency records for authenticated
// federation-originated jobs. It deliberately does not launch processes: the
// caller must persist each transition before invoking the local Job Manager.
package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	ErrConflict   = errors.New("idempotency key already exists with a different payload")
	ErrNotFound   = errors.New("remote job request not found")
	ErrTransition = errors.New("invalid remote job state transition")
	validID       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type Record struct {
	CallerNodeID    string    `json:"caller_node_id"`
	RequestID       string    `json:"request_id"`
	PayloadHash     string    `json:"payload_hash"`
	Status          string    `json:"status"`
	JobID           string    `json:"job_id,omitempty"`
	LocalJobID      string    `json:"local_job_id,omitempty"`
	PolicyRevision  uint64    `json:"policy_revision,omitempty"`
	OwnerPID        int       `json:"owner_pid,omitempty"`
	OwnerStartTicks uint64    `json:"owner_start_ticks,omitempty"`
	OwnerBootID     string    `json:"owner_boot_id,omitempty"`
	Error           string    `json:"error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Ledger struct {
	mu      sync.Mutex
	dir     string
	store   *privateStateStore
	records map[string]Record
}

// Open loads the ledger while holding an OS-level lock. Every operation also
// takes that lock and reloads disk state, so independent stdio Runtime
// processes sharing a State Directory observe one serialized transaction log.
func Open(stateDir string) (*Ledger, error) {
	dir := filepath.Join(stateDir, "federation", "remote-jobs")
	store, err := openPrivateStateStore(dir)
	if err != nil {
		return nil, err
	}
	l := &Ledger{dir: dir, store: store, records: make(map[string]Record)}
	if err := l.withLock(func() error { return l.reload(false) }); err != nil {
		return nil, err
	}
	if err := l.recoverAbandonedDispatches(); err != nil {
		return nil, err
	}
	return l, nil
}

// reload must run while the cross-process lock is held.
func (l *Ledger) reload(recoverDispatching bool) error {
	records := make(map[string]Record)
	entries, err := l.store.entries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := l.store.read(entry.Name())
		if err != nil {
			return err
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("decode remote job record %s: %w", entry.Name(), err)
		}
		if !validID.MatchString(rec.CallerNodeID) || !validID.MatchString(rec.RequestID) || len(rec.PayloadHash) != 64 || !validStatus(rec.Status) {
			return fmt.Errorf("invalid remote job record %s", entry.Name())
		}
		key := recordKey(rec.CallerNodeID, rec.RequestID)
		if _, exists := records[key]; exists {
			return fmt.Errorf("duplicate remote job record %s", entry.Name())
		}
		if recoverDispatching && rec.Status == "dispatching" && !ownerAlive(rec.OwnerPID, rec.OwnerStartTicks, rec.OwnerBootID) {
			rec.Status = "unknown"
			rec.Error = "dispatch owner process is no longer alive; outcome requires reconciliation"
			rec.OwnerPID = 0
			rec.UpdatedAt = time.Now().UTC()
			if err := l.persist(rec); err != nil {
				return err
			}
		}
		records[key] = rec
	}
	l.records = records
	return nil
}

// withLock serializes both goroutines and independent processes. State is
// refreshed after acquiring flock to prevent stale in-memory decisions.
func (l *Ledger) withLock(fn func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.store.withLock(".ledger.lock", fn)
}

// recoverAbandonedDispatches snapshots candidates without holding the Ledger
// lock, then acquires the shared lifecycle lock before reloading and mutating
// each record. This preserves lifecycle -> store lock ordering and avoids
// racing a live Coordinator that is still holding the job lifecycle lock.
func (l *Ledger) recoverAbandonedDispatches() error {
	var candidates []Record
	if err := l.withLock(func() error {
		if err := l.reload(false); err != nil {
			return err
		}
		for _, rec := range l.records {
			if rec.Status == "dispatching" {
				candidates = append(candidates, rec)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	stateDir := filepath.Dir(filepath.Dir(l.dir))
	for _, candidate := range candidates {
		err := WithJobLifecycleLock(stateDir, candidate.CallerNodeID, candidate.RequestID, func() error {
			return l.withLock(func() error {
				if err := l.reload(false); err != nil {
					return err
				}
				key := recordKey(candidate.CallerNodeID, candidate.RequestID)
				rec, ok := l.records[key]
				if !ok || rec.Status != "dispatching" || ownerAlive(rec.OwnerPID, rec.OwnerStartTicks, rec.OwnerBootID) {
					return nil
				}
				rec.Status = "unknown"
				rec.Error = "dispatch owner process is no longer alive; outcome requires reconciliation"
				rec.OwnerPID, rec.OwnerStartTicks, rec.OwnerBootID = 0, 0, ""
				rec.UpdatedAt = time.Now().UTC()
				if err := l.persist(rec); err != nil {
					return err
				}
				l.records[key] = rec
				return nil
			})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Reserve atomically records a new request before any process launch. Repeated
// requests with the same key and payload return the original record.
func (l *Ledger) Reserve(callerNodeID, requestID, payload string) (Record, bool, error) {
	var result Record
	var created bool
	stateDir := filepath.Dir(filepath.Dir(l.dir))
	err := WithJobLifecycleLock(stateDir, callerNodeID, requestID, func() error {
		var callErr error
		result, created, callErr = l.ReserveUnderLifecycleLock(callerNodeID, requestID, payload)
		return callErr
	})
	return result, created, err
}

func (l *Ledger) ReserveUnderLifecycleLock(callerNodeID, requestID, payload string) (Record, bool, error) {
	if !validID.MatchString(callerNodeID) || !validID.MatchString(requestID) {
		return Record{}, false, fmt.Errorf("caller node ID and request ID must be 1-128 safe characters")
	}
	h := sha256.Sum256([]byte(payload))
	hash := hex.EncodeToString(h[:])
	key := recordKey(callerNodeID, requestID)
	var result Record
	var created bool
	err := l.withLock(func() error {
		if err := l.reload(false); err != nil {
			return err
		}
		if existing, ok := l.records[key]; ok {
			if existing.PayloadHash != hash {
				return ErrConflict
			}
			result = existing
			return nil
		}
		now := time.Now().UTC()
		jobHash := sha256.Sum256([]byte(recordKey(callerNodeID, requestID)))
		rec := Record{CallerNodeID: callerNodeID, RequestID: requestID, PayloadHash: hash, Status: "accepted", JobID: "rjob-" + hex.EncodeToString(jobHash[:]), CreatedAt: now, UpdatedAt: now}
		if err := l.persist(rec); err != nil {
			return err
		}
		l.records[key] = rec
		result, created = rec, true
		return nil
	})
	if err != nil {
		return Record{}, false, err
	}
	return result, created, nil
}

// Transition persists the state before publishing it to readers. dispatching
// is intentionally a one-way gate: recovery changes it to unknown, and callers
// cannot transition unknown back into dispatching.
func (l *Ledger) Transition(callerNodeID, requestID, status, jobID, message string) (Record, error) {
	var result Record
	stateDir := filepath.Dir(filepath.Dir(l.dir))
	err := WithJobLifecycleLock(stateDir, callerNodeID, requestID, func() error {
		var callErr error
		result, callErr = l.TransitionUnderLifecycleLock(callerNodeID, requestID, status, jobID, message)
		return callErr
	})
	return result, err
}

func (l *Ledger) TransitionUnderLifecycleLock(callerNodeID, requestID, status, jobID, message string) (Record, error) {
	if !validStatus(status) {
		return Record{}, ErrTransition
	}
	if jobID != "" && !validID.MatchString(jobID) {
		return Record{}, fmt.Errorf("invalid job ID")
	}
	key := recordKey(callerNodeID, requestID)
	var result Record
	err := l.withLock(func() error {
		if err := l.reload(false); err != nil {
			return err
		}
		rec, ok := l.records[key]
		if !ok {
			return ErrNotFound
		}
		if !allowed(rec.Status, status) {
			return fmt.Errorf("%w: %s -> %s", ErrTransition, rec.Status, status)
		}
		if rec.JobID != "" && jobID != "" && rec.JobID != jobID {
			return ErrConflict
		}
		if jobID != "" {
			rec.JobID = jobID
		}
		if status == "dispatching" {
			ticks, boot, err := currentProcessIdentity()
			if err != nil {
				return fmt.Errorf("capture dispatch owner identity: %w", err)
			}
			rec.OwnerPID, rec.OwnerStartTicks, rec.OwnerBootID = os.Getpid(), ticks, boot
		}
		if status != "dispatching" {
			rec.OwnerPID, rec.OwnerStartTicks, rec.OwnerBootID = 0, 0, ""
		}
		rec.Status = status
		rec.Error = message
		rec.UpdatedAt = time.Now().UTC()
		if err := l.persist(rec); err != nil {
			return err
		}
		l.records[key] = rec
		result = rec
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return result, nil
}

// PrepareDispatch atomically freezes the local job binding and policy revision
// while moving accepted to dispatching. This is the single durable gate that
// must be committed before a launcher can be called. Re-entry is rejected.
func (l *Ledger) PrepareDispatch(callerNodeID, requestID, localJobID string, policyRevision uint64) (Record, error) {
	var result Record
	stateDir := filepath.Dir(filepath.Dir(l.dir))
	err := WithJobLifecycleLock(stateDir, callerNodeID, requestID, func() error {
		var callErr error
		result, callErr = l.PrepareDispatchUnderLifecycleLock(callerNodeID, requestID, localJobID, policyRevision)
		return callErr
	})
	return result, err
}

func (l *Ledger) PrepareDispatchUnderLifecycleLock(callerNodeID, requestID, localJobID string, policyRevision uint64) (Record, error) {
	if !validID.MatchString(callerNodeID) || !validID.MatchString(requestID) || !validID.MatchString(localJobID) || policyRevision == 0 {
		return Record{}, fmt.Errorf("invalid dispatch binding or policy revision")
	}
	var result Record
	err := l.withLock(func() error {
		if err := l.reload(false); err != nil {
			return err
		}
		key := recordKey(callerNodeID, requestID)
		rec, ok := l.records[key]
		if !ok {
			return ErrNotFound
		}
		if rec.Status != "accepted" {
			return fmt.Errorf("%w: dispatch can only be prepared from accepted", ErrTransition)
		}
		if rec.LocalJobID == "" || rec.PolicyRevision == 0 {
			return fmt.Errorf("%w: immutable execution metadata must be bound before dispatch", ErrTransition)
		}
		if rec.LocalJobID != localJobID {
			return ErrConflict
		}
		if rec.PolicyRevision != 0 && rec.PolicyRevision != policyRevision {
			return ErrConflict
		}
		ticks, boot, err := currentProcessIdentity()
		if err != nil {
			return fmt.Errorf("capture dispatch owner identity: %w", err)
		}
		rec.LocalJobID = localJobID
		rec.PolicyRevision = policyRevision
		rec.Status = "dispatching"
		rec.OwnerPID, rec.OwnerStartTicks, rec.OwnerBootID = os.Getpid(), ticks, boot
		rec.UpdatedAt = time.Now().UTC()
		if err := l.persist(rec); err != nil {
			return err
		}
		l.records[key] = rec
		result = rec
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return result, nil
}

// BindExecutionMetadata freezes the receiver-local Job ID and policy revision
// while leaving the ledger in accepted. It is not a dispatch authorization.
func (l *Ledger) BindExecutionMetadata(callerNodeID, requestID, localJobID string, policyRevision uint64) (Record, error) {
	var result Record
	stateDir := filepath.Dir(filepath.Dir(l.dir))
	err := WithJobLifecycleLock(stateDir, callerNodeID, requestID, func() error {
		var callErr error
		result, callErr = l.BindExecutionMetadataUnderLifecycleLock(callerNodeID, requestID, localJobID, policyRevision)
		return callErr
	})
	return result, err
}

func (l *Ledger) BindExecutionMetadataUnderLifecycleLock(callerNodeID, requestID, localJobID string, policyRevision uint64) (Record, error) {
	if !validID.MatchString(callerNodeID) || !validID.MatchString(requestID) || !validID.MatchString(localJobID) || policyRevision == 0 {
		return Record{}, fmt.Errorf("invalid execution metadata binding")
	}
	var result Record
	err := l.withLock(func() error {
		if err := l.reload(false); err != nil {
			return err
		}
		key := recordKey(callerNodeID, requestID)
		rec, ok := l.records[key]
		if !ok {
			return ErrNotFound
		}
		if rec.Status != "accepted" {
			return fmt.Errorf("execution metadata can only bind while accepted")
		}
		if rec.LocalJobID != "" && rec.LocalJobID != localJobID {
			return ErrConflict
		}
		if rec.PolicyRevision != 0 && rec.PolicyRevision != policyRevision {
			return ErrConflict
		}
		if rec.LocalJobID == "" || rec.PolicyRevision == 0 {
			rec.LocalJobID, rec.PolicyRevision = localJobID, policyRevision
			rec.UpdatedAt = time.Now().UTC()
			if err := l.persist(rec); err != nil {
				return err
			}
			l.records[key] = rec
		}
		result = rec
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return result, nil
}

// BindLocalJob durably binds the deterministic Remote Job to exactly one
// receiver-local Job ID before any launcher is allowed to run. The binding is
// immutable and idempotent; it cannot be changed after dispatch has begun.
func (l *Ledger) BindLocalJob(callerNodeID, requestID, localJobID string) (Record, error) {
	var result Record
	stateDir := filepath.Dir(filepath.Dir(l.dir))
	err := WithJobLifecycleLock(stateDir, callerNodeID, requestID, func() error {
		var callErr error
		result, callErr = l.BindLocalJobUnderLifecycleLock(callerNodeID, requestID, localJobID)
		return callErr
	})
	return result, err
}

func (l *Ledger) BindLocalJobUnderLifecycleLock(callerNodeID, requestID, localJobID string) (Record, error) {
	if !validID.MatchString(callerNodeID) || !validID.MatchString(requestID) || !validID.MatchString(localJobID) {
		return Record{}, fmt.Errorf("invalid caller, request, or local job ID")
	}
	var result Record
	err := l.withLock(func() error {
		if err := l.reload(false); err != nil {
			return err
		}
		key := recordKey(callerNodeID, requestID)
		rec, ok := l.records[key]
		if !ok {
			return ErrNotFound
		}
		if rec.Status != "accepted" {
			return fmt.Errorf("local job binding is only allowed before dispatch")
		}
		if rec.LocalJobID != "" && rec.LocalJobID != localJobID {
			return ErrConflict
		}
		if rec.LocalJobID == "" {
			rec.LocalJobID = localJobID
			rec.UpdatedAt = time.Now().UTC()
			if err := l.persist(rec); err != nil {
				return err
			}
			l.records[key] = rec
		}
		result = rec
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return result, nil
}

func (l *Ledger) Get(callerNodeID, requestID string) (Record, error) {
	var result Record
	err := l.withLock(func() error {
		if err := l.reload(false); err != nil {
			return err
		}
		rec, ok := l.records[recordKey(callerNodeID, requestID)]
		if !ok {
			return ErrNotFound
		}
		result = rec
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return result, nil
}

func (l *Ledger) persist(rec Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return l.store.write(fileKey(rec.CallerNodeID, rec.RequestID)+".json", data, true)
}

// Close releases the pinned ledger directory descriptor.
func (l *Ledger) Close() error {
	if l == nil {
		return nil
	}
	return l.store.close()
}

func recordKey(caller, request string) string { return caller + "\x00" + request }
func fileKey(caller, request string) string {
	h := sha256.Sum256([]byte(recordKey(caller, request)))
	return hex.EncodeToString(h[:])
}
func validStatus(s string) bool {
	switch s {
	case "accepted", "dispatching", "running", "completed", "failed", "unknown":
		return true
	}
	return false
}
func allowed(from, to string) bool {
	switch from {
	case "accepted":
		return to == "dispatching" || to == "failed"
	case "dispatching":
		return to == "running" || to == "completed" || to == "failed" || to == "unknown"
	case "running":
		return to == "completed" || to == "failed" || to == "unknown"
	case "unknown":
		return to == "running" || to == "completed" || to == "failed"
	}
	return false
}
