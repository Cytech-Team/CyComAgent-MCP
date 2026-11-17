//go:build linux || android

package remotejobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, _, err := readProcessIdentity(pid)
	return err == nil
}

func withFileLock(path string, fn func() error) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func currentProcessIdentity() (uint64, string, error) {
	return readProcessIdentity(os.Getpid())
}

func ownerAlive(pid int, startTicks uint64, bootID string) bool {
	if pid <= 0 || startTicks == 0 || bootID == "" {
		return false
	}
	ticks, boot, err := readProcessIdentity(pid)
	return err == nil && ticks == startTicks && boot == bootID
}

func readProcessIdentity(pid int) (uint64, string, error) {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, "", err
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return 0, "", fmt.Errorf("malformed process stat")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) <= 19 {
		return 0, "", fmt.Errorf("incomplete process stat")
	}
	// A zombie still has a /proc entry and a stable start time, but it can no
	// longer own a live dispatch critical section. Treat Z/X as dead so startup
	// recovery cannot leave a dispatching record stuck behind an unreaped PID.
	if fields[0] == "Z" || fields[0] == "X" {
		return 0, "", fmt.Errorf("process %d is not live (state %s)", pid, fields[0])
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, "", err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return 0, "", err
	}
	return ticks, strings.TrimSpace(string(boot)), nil
}
