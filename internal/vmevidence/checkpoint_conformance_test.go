package vmevidence

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type checkpointAuthorityFactory func(*testing.T, HostJournalCheckpoint) CheckpointAuthority

// runCheckpointAuthorityConformance is the reusable behavioral suite future
// adapters must pass. It proves only API-level behavior; it cannot prove disk
// durability, crash recovery, cross-process exclusion, or anti-rollback.
func runCheckpointAuthorityConformance(t *testing.T, factory checkpointAuthorityFactory) {
	t.Helper()
	genesis := HostJournalCheckpoint{RunnerID: "conformance-runner", RunID: "conformance-run", RunNonce: strings.Repeat("a", 64), Sequence: 0, JournalSHA256: strings.Repeat("0", 64)}
	next := HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: genesis.RunID, RunNonce: genesis.RunNonce, Sequence: 1, JournalSHA256: strings.Repeat("1", 64)}
	authority := factory(t, genesis)
	t.Run("load-exact-genesis", func(t *testing.T) {
		got, err := authority.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
		if err != nil || got != genesis {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
	t.Run("missing-identity-is-not-found", func(t *testing.T) {
		_, err := authority.LoadCheckpoint(genesis.RunnerID, "missing-run", genesis.RunNonce)
		if !errors.Is(err, ErrCheckpointNotFound) {
			t.Fatalf("want not-found, got %v", err)
		}
	})
	t.Run("valid-advance-and-readback", func(t *testing.T) {
		if err := authority.CompareAndAdvance(genesis, next); err != nil {
			t.Fatalf("valid CAS: %v", err)
		}
		got, err := authority.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
		if err != nil || got != next {
			t.Fatalf("read-back=%+v err=%v", got, err)
		}
	})
	t.Run("stale-expected-conflicts-without-mutation", func(t *testing.T) {
		err := authority.CompareAndAdvance(genesis, next)
		if !errors.Is(err, ErrCheckpointConflict) {
			t.Fatalf("want conflict, got %v", err)
		}
		got, loadErr := authority.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
		if loadErr != nil || got != next {
			t.Fatalf("stale CAS mutated head: %+v err=%v", got, loadErr)
		}
	})
}

func TestMemoryCheckpointAuthorityConformance(t *testing.T) {
	runCheckpointAuthorityConformance(t, func(t *testing.T, initial HostJournalCheckpoint) CheckpointAuthority {
		t.Helper()
		a, err := NewMemoryCheckpointAuthority([]HostJournalCheckpoint{initial})
		if err != nil {
			t.Fatal(err)
		}
		return a
	})
}

func TestMemoryCheckpointAuthorityConformanceRejectsInvalidTransitionsWithoutMutation(t *testing.T) {
	genesis := HostJournalCheckpoint{RunnerID: "runner", RunID: "run", RunNonce: strings.Repeat("b", 64), Sequence: 4, JournalSHA256: strings.Repeat("4", 64)}
	cases := []struct {
		name string
		next HostJournalCheckpoint
	}{
		{"sequence-skip", HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: genesis.RunID, RunNonce: genesis.RunNonce, Sequence: 6, JournalSHA256: strings.Repeat("6", 64)}},
		{"sequence-replay", HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: genesis.RunID, RunNonce: genesis.RunNonce, Sequence: 4, JournalSHA256: strings.Repeat("5", 64)}},
		{"cross-run", HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: "other-run", RunNonce: genesis.RunNonce, Sequence: 5, JournalSHA256: strings.Repeat("5", 64)}},
		{"same-digest", HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: genesis.RunID, RunNonce: genesis.RunNonce, Sequence: 5, JournalSHA256: genesis.JournalSHA256}},
		{"invalid-digest", HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: genesis.RunID, RunNonce: genesis.RunNonce, Sequence: 5, JournalSHA256: "not-a-digest"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := NewMemoryCheckpointAuthority([]HostJournalCheckpoint{genesis})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.CompareAndAdvance(genesis, tc.next); err == nil {
				t.Fatal("invalid transition accepted")
			}
			got, err := a.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
			if err != nil || got != genesis {
				t.Fatalf("invalid transition mutated authority: %+v err=%v", got, err)
			}
		})
	}
}

func TestMemoryCheckpointAuthorityConcurrentCASConformance(t *testing.T) {
	genesis := HostJournalCheckpoint{RunnerID: "runner", RunID: "run", RunNonce: strings.Repeat("c", 64), Sequence: 9, JournalSHA256: strings.Repeat("9", 64)}
	next := HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: genesis.RunID, RunNonce: genesis.RunNonce, Sequence: 10, JournalSHA256: strings.Repeat("a", 64)}
	a, err := NewMemoryCheckpointAuthority([]HostJournalCheckpoint{genesis})
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int32
	var conflicts atomic.Int32
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := a.CompareAndAdvance(genesis, next)
			if err == nil {
				winners.Add(1)
			} else if errors.Is(err, ErrCheckpointConflict) {
				conflicts.Add(1)
			} else {
				t.Errorf("unexpected CAS error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 || conflicts.Load() != contenders-1 {
		t.Fatalf("winners=%d conflicts=%d", winners.Load(), conflicts.Load())
	}
	got, err := a.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
	if err != nil || got != next {
		t.Fatalf("final head=%+v err=%v", got, err)
	}
}

type conformanceCommitThenUnknown struct{ inner CheckpointAuthority }

func (a conformanceCommitThenUnknown) LoadCheckpoint(runnerID, runID, runNonce string) (HostJournalCheckpoint, error) {
	return a.inner.LoadCheckpoint(runnerID, runID, runNonce)
}
func (a conformanceCommitThenUnknown) CompareAndAdvance(expected, next HostJournalCheckpoint) error {
	if err := a.inner.CompareAndAdvance(expected, next); err != nil {
		return err
	}
	return fmt.Errorf("%w: injected lost acknowledgement", ErrCheckpointOutcomeUnknown)
}

func TestCheckpointAuthorityUnknownCommitRequiresReadback(t *testing.T) {
	genesis := HostJournalCheckpoint{RunnerID: "runner", RunID: "run", RunNonce: strings.Repeat("d", 64), Sequence: 0, JournalSHA256: strings.Repeat("0", 64)}
	next := HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: genesis.RunID, RunNonce: genesis.RunNonce, Sequence: 1, JournalSHA256: strings.Repeat("1", 64)}
	inner, err := NewMemoryCheckpointAuthority([]HostJournalCheckpoint{genesis})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := conformanceCommitThenUnknown{inner: inner}
	if err := wrapped.CompareAndAdvance(genesis, next); !errors.Is(err, ErrCheckpointOutcomeUnknown) {
		t.Fatalf("lost acknowledgement misclassified: %v", err)
	}
	got, err := wrapped.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
	if err != nil || got != next {
		t.Fatalf("read-back did not reveal committed head: %+v err=%v", got, err)
	}
	if err := wrapped.CompareAndAdvance(genesis, next); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("retry should conflict, got %v", err)
	}
}
