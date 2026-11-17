//go:build linux || android

package remotejobs

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithJobLifecycleLockSerializesCooperatingGoroutines(t *testing.T) {
	state := t.TempDir()
	var inside, maximum atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithJobLifecycleLock(state, "caller-a", "request-a", func() error {
				current := inside.Add(1)
				for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
				}
				time.Sleep(5 * time.Millisecond)
				inside.Add(-1)
				return nil
			})
			if err != nil {
				t.Errorf("lifecycle lock: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent critical sections = %d, want 1", got)
	}
}

func TestWithJobLifecycleLockSeparatesJobIdentities(t *testing.T) {
	state := t.TempDir()
	var inside, maximum atomic.Int32
	var wg sync.WaitGroup
	for _, request := range []string{"request-a", "request-b"} {
		wg.Add(1)
		go func(request string) {
			defer wg.Done()
			if err := WithJobLifecycleLock(state, "caller-a", request, func() error {
				current := inside.Add(1)
				for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
				}
				time.Sleep(20 * time.Millisecond)
				inside.Add(-1)
				return nil
			}); err != nil {
				t.Errorf("lifecycle lock: %v", err)
			}
		}(request)
	}
	wg.Wait()
	if got := maximum.Load(); got != 2 {
		t.Fatalf("independent jobs did not progress concurrently: maximum=%d", got)
	}
}

func TestWithJobLifecycleLockRejectsInvalidIdentity(t *testing.T) {
	if err := WithJobLifecycleLock(t.TempDir(), "../caller", "request", func() error { return nil }); err == nil {
		t.Fatal("accepted invalid caller")
	}
	if err := WithJobLifecycleLock(t.TempDir(), "caller", "request", nil); err == nil {
		t.Fatal("accepted nil callback")
	}
}

func TestLaunchIntentJournalUsesSharedLifecycleLock(t *testing.T) {
	state := t.TempDir()
	journal, err := OpenLaunchIntentJournal(state)
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- WithJobLifecycleLock(state, "caller-a", "request-a", func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	readDone := make(chan error, 1)
	go func() { _, err := journal.Get("caller-a", "request-a"); readDone <- err }()
	select {
	case err := <-readDone:
		t.Fatalf("journal operation bypassed shared lifecycle lock: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	if err := <-lockDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("unexpected journal record")
		}
	case <-time.After(time.Second):
		t.Fatal("journal operation remained blocked after lock release")
	}
}

func TestPublicLedgerMutationUsesSharedLifecycleLock(t *testing.T) {
	state := t.TempDir()
	ledger, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Reserve("caller-a", "request-a", "payload"); err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- WithJobLifecycleLock(state, "caller-a", "request-a", func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	mutationDone := make(chan error, 1)
	go func() {
		_, err := ledger.Transition("caller-a", "request-a", "failed", "", "test")
		mutationDone <- err
	}()
	select {
	case err := <-mutationDone:
		t.Fatalf("public Ledger mutation bypassed lifecycle lock: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	if err := <-lockDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-mutationDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ledger mutation remained blocked after lifecycle lock release")
	}
}
