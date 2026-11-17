//go:build !linux && !android

package remotejobs

import "fmt"

// WithJobLifecycleLock fails closed on platforms without the supported
// cross-process file-lock implementation.
func WithJobLifecycleLock(stateDir, callerNodeID, requestID string, fn func() error) error {
	return fmt.Errorf("cross-process job lifecycle locking is unsupported on this platform")
}
