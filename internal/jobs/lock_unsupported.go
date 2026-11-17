//go:build !linux && !android

package jobs

import (
	"fmt"
	"os"
)

func withJobFileLock(path string, fn func() error) error {
	return fmt.Errorf("cross-process Job Manager locking is unsupported on this platform")
}

func openJobRecordRead(path string) (*os.File, error) {
	return openRegularFile(path)
}

func openJobLogRead(path string) (*os.File, error) {
	return openRegularFile(path)
}

func openRegularFile(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("job state file is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("job state file changed during open")
	}
	return f, nil
}

func openJobLogAppend(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("job log is not a regular file")
	}
	return f, nil
}
