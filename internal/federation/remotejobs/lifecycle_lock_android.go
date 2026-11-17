//go:build android

package remotejobs

import (
	"fmt"
	"os"
	"path/filepath"
)

func WithJobLifecycleLock(stateDir, callerNodeID, requestID string, fn func() error) error {
	if stateDir == "" || !validID.MatchString(callerNodeID) || !validID.MatchString(requestID) || fn == nil {
		return fmt.Errorf("state directory, valid job identity, and lifecycle callback are required")
	}
	dir := filepath.Join(stateDir, "federation", "remote-job-lifecycle")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	return withFileLock(filepath.Join(dir, fileKey(callerNodeID, requestID)+".lock"), fn)
}
