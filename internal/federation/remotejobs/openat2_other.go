//go:build !linux

package remotejobs

import (
	"fmt"
	"os"
)

func OpenBeneath(*os.File, string, uint64, uint64) (*os.File, error) {
	return nil, fmt.Errorf("openat2 descriptor resolution is unsupported on this platform")
}
func OpenExecutableBeneath(*os.File, string) (*os.File, error) {
	return nil, fmt.Errorf("openat2 descriptor resolution is unsupported on this platform")
}
func OpenDirectoryBeneath(*os.File, string) (*os.File, error) {
	return nil, fmt.Errorf("openat2 descriptor resolution is unsupported on this platform")
}
