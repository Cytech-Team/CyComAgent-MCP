//go:build linux && !android

package remotejobs

import (
	"fmt"
	"path/filepath"
)

// WithJobLifecycleLock serializes cooperating lifecycle adapters across
// processes for one authenticated caller/request pair. Linux uses a pinned,
// private directory descriptor so lock files cannot be redirected by symlink
// replacement after the directory is opened.
func WithJobLifecycleLock(stateDir, callerNodeID, requestID string, fn func() error) error {
	if stateDir == "" || !validID.MatchString(callerNodeID) || !validID.MatchString(requestID) || fn == nil {
		return fmt.Errorf("state directory, valid job identity, and lifecycle callback are required")
	}
	dir := filepath.Join(stateDir, "federation", "remote-job-lifecycle")
	store, err := openPrivateStateStore(dir)
	if err != nil {
		return err
	}
	defer store.close()
	return store.withLock(fileKey(callerNodeID, requestID)+".lock", fn)
}
