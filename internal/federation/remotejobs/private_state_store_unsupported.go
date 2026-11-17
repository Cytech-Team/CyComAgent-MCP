//go:build !linux

package remotejobs

import (
	"fmt"
	"os"
)

type privateStateStore struct{}

func openPrivateStateStore(string) (*privateStateStore, error) {
	return nil, fmt.Errorf("descriptor-relative metadata store is unsupported on this platform")
}
func (*privateStateStore) close() error { return nil }
func (*privateStateStore) entries() ([]os.DirEntry, error) {
	return nil, fmt.Errorf("descriptor-relative metadata store is unsupported on this platform")
}
func (*privateStateStore) read(string) ([]byte, error) {
	return nil, fmt.Errorf("descriptor-relative metadata store is unsupported on this platform")
}
func (*privateStateStore) lockFile(string) (*os.File, error) {
	return nil, fmt.Errorf("descriptor-relative metadata store is unsupported on this platform")
}
func (*privateStateStore) withLock(string, func() error) error {
	return fmt.Errorf("descriptor-relative metadata store is unsupported on this platform")
}
func (*privateStateStore) write(string, []byte, bool) error {
	return fmt.Errorf("descriptor-relative metadata store is unsupported on this platform")
}
