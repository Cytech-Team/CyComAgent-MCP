//go:build linux || android

package remotejobs

import (
	"fmt"
	"os"
	"syscall"
)

// PolicyLease holds the same inter-process lock used by SaveVersioned. While
// held, administrative policy updates cannot change the revision being used by
// a future dispatch gate. The caller must Release on every path.
type PolicyLease struct {
	file     *os.File
	Policy   ExecutionPolicy
	Revision uint64
}

func (s *PolicyStore) AcquireLease(expectedRevision uint64) (*PolicyLease, error) {
	if s == nil || s.path == "" || expectedRevision == 0 {
		return nil, fmt.Errorf("policy store and non-zero expected revision are required")
	}
	f, err := s.store.lockFile("remote-execution-policy.json.lock")
	if err != nil {
		return nil, err
	}
	disk, err := s.loadUnlocked()
	if err != nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, err
	}
	if disk.Revision != expectedRevision {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, fmt.Errorf("policy revision conflict: expected %d, current %d", expectedRevision, disk.Revision)
	}
	return &PolicyLease{file: f, Policy: disk.Policy, Revision: disk.Revision}, nil
}

func (l *PolicyLease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
