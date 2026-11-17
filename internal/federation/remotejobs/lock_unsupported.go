//go:build !linux && !android

package remotejobs

import "fmt"

func processAlive(pid int) bool { return false }

func withFileLock(path string, fn func() error) error {
	return fmt.Errorf("cross-process remote job locking is unsupported on this platform")
}

func currentProcessIdentity() (uint64, string, error) {
	return 0, "", fmt.Errorf("process identity is unsupported on this platform")
}
func ownerAlive(pid int, startTicks uint64, bootID string) bool { return false }
