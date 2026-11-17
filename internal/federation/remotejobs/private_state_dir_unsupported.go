//go:build !linux

package remotejobs

import "fmt"

func ensurePrivateStateDir(string) error {
	return fmt.Errorf("private state directory validation is unsupported on this platform")
}
