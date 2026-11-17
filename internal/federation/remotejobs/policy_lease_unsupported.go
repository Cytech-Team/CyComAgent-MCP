//go:build !linux && !android

package remotejobs

import "fmt"

// PolicyLease is unavailable where the repository's inter-process lock
// implementation is unavailable. Dispatch must fail closed on these targets.
type PolicyLease struct {
	Policy   ExecutionPolicy
	Revision uint64
}

func (s *PolicyStore) AcquireLease(expectedRevision uint64) (*PolicyLease, error) {
	return nil, fmt.Errorf("policy execution lease is unsupported on this platform")
}

func (l *PolicyLease) Release() error { return nil }
