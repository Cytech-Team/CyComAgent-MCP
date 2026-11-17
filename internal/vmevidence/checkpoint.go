package vmevidence

import (
	"fmt"
	"sync"
	"time"
)

// CheckpointAuthority stores the latest accepted journal head independently
// from the journal stream. CompareAndAdvance must be one atomic durable CAS:
// it must compare the complete expected value and replace it with next in one
// transaction, surviving restart before reporting success. Implementations should wrap
// ErrCheckpointConflict, ErrCheckpointOutcomeUnknown, ErrCheckpointUnavailable,
// ErrCheckpointCorrupt, or ErrCheckpointNotFound as appropriate. Uncertain commits
// must fail closed and require a read-back/reconciliation, never blind retry.
type CheckpointAuthority interface {
	LoadCheckpoint(runnerID, runID, runNonce string) (HostJournalCheckpoint, error)
	CompareAndAdvance(expected, next HostJournalCheckpoint) error
}

// VerifyAndAdvanceHostEventJournalChain loads the current independently held
// checkpoint, verifies the journal against it, then atomically advances it.
// Concurrent verifiers may both verify, but only one can commit the same head.
// If the authority reports an uncertain commit, callers must reload and
// reconcile; they must not blindly retry the same journal.
func VerifyAndAdvanceHostEventJournalChain(raw []byte, envelope SignatureEnvelope, at time.Time, store TrustStore, manifest Manifest, authority CheckpointAuthority) (HostJournalChainVerification, error) {
	var zero HostJournalChainVerification
	if authority == nil {
		return zero, fmt.Errorf("%w: checkpoint authority is required", ErrInvalidEvidence)
	}
	checkpoint, err := authority.LoadCheckpoint(manifest.Host.RunnerID, manifest.RunID, manifest.RunNonce)
	if err != nil {
		return zero, err
	}
	verified, err := VerifyHostEventJournalChain(raw, envelope, at, store, manifest, checkpoint)
	if err != nil {
		return zero, err
	}
	if err := authority.CompareAndAdvance(checkpoint, verified.NextCheckpoint); err != nil {
		return zero, err
	}
	return verified, nil
}

// MemoryCheckpointAuthority is a concurrency-safe reference implementation
// for tests only. It is neither durable nor an independent production anchor.
type MemoryCheckpointAuthority struct {
	mu          sync.RWMutex
	checkpoints map[string]HostJournalCheckpoint
}

func NewMemoryCheckpointAuthority(initial []HostJournalCheckpoint) (*MemoryCheckpointAuthority, error) {
	a := &MemoryCheckpointAuthority{checkpoints: make(map[string]HostJournalCheckpoint, len(initial))}
	for _, checkpoint := range initial {
		if err := validateHostJournalCheckpoint(checkpoint); err != nil {
			return nil, err
		}
		key := checkpointIdentity(checkpoint.RunnerID, checkpoint.RunID, checkpoint.RunNonce)
		if _, exists := a.checkpoints[key]; exists {
			return nil, fmt.Errorf("%w: duplicate initial journal checkpoint", ErrInvalidEvidence)
		}
		a.checkpoints[key] = checkpoint
	}
	return a, nil
}

func (a *MemoryCheckpointAuthority) LoadCheckpoint(runnerID, runID, runNonce string) (HostJournalCheckpoint, error) {
	if a == nil || runnerID == "" || runID == "" || !noncePattern.MatchString(runNonce) || len(runNonce)%2 != 0 {
		return HostJournalCheckpoint{}, fmt.Errorf("%w: invalid checkpoint lookup identity", ErrInvalidEvidence)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	checkpoint, ok := a.checkpoints[checkpointIdentity(runnerID, runID, runNonce)]
	if !ok {
		return HostJournalCheckpoint{}, fmt.Errorf("%w: %w", ErrInvalidEvidence, ErrCheckpointNotFound)
	}
	return checkpoint, nil
}

func (a *MemoryCheckpointAuthority) CompareAndAdvance(expected, next HostJournalCheckpoint) error {
	if a == nil {
		return fmt.Errorf("%w: nil checkpoint authority", ErrInvalidEvidence)
	}
	if err := validateHostJournalCheckpoint(expected); err != nil {
		return err
	}
	if err := validateHostJournalCheckpoint(next); err != nil {
		return err
	}
	if checkpointIdentity(expected.RunnerID, expected.RunID, expected.RunNonce) != checkpointIdentity(next.RunnerID, next.RunID, next.RunNonce) ||
		expected.Sequence == ^uint64(0) || next.Sequence != expected.Sequence+1 || next.JournalSHA256 == expected.JournalSHA256 {
		return fmt.Errorf("%w: invalid checkpoint advance", ErrInvalidEvidence)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := checkpointIdentity(expected.RunnerID, expected.RunID, expected.RunNonce)
	current, ok := a.checkpoints[key]
	if !ok || current != expected {
		return fmt.Errorf("%w: %w", ErrInvalidEvidence, ErrCheckpointConflict)
	}
	a.checkpoints[key] = next
	return nil
}

func validateHostJournalCheckpoint(checkpoint HostJournalCheckpoint) error {
	if checkpoint.RunnerID == "" || checkpoint.RunID == "" || !noncePattern.MatchString(checkpoint.RunNonce) ||
		len(checkpoint.RunNonce)%2 != 0 || !sha256Pattern.MatchString(checkpoint.JournalSHA256) {
		return fmt.Errorf("%w: invalid host journal checkpoint", ErrInvalidEvidence)
	}
	return nil
}

func checkpointIdentity(runnerID, runID, runNonce string) string {
	return runnerID + "\x00" + runID + "\x00" + runNonce
}
