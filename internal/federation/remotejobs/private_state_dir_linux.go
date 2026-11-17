//go:build linux

package remotejobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ensurePrivateStateDir creates a state directory without accepting symlink
// components. The final directory must be owned by the current effective UID
// and is forced to mode 0700. This is a startup-time path check, not a
// substitute for descriptor-relative access after the directory is opened.
func ensurePrivateStateDir(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("private state directory must be a clean absolute path")
	}
	current := string(filepath.Separator)
	parts := splitPath(path)
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("private state path contains a symlink or non-directory component")
		}
		if i == len(parts)-1 {
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || st.Uid != uint32(os.Geteuid()) {
				return fmt.Errorf("private state directory is not owned by the effective user")
			}
			if err := os.Chmod(current, 0700); err != nil {
				return err
			}
		}
	}
	return nil
}

func splitPath(path string) []string {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rel := strings.TrimPrefix(clean[len(volume):], string(filepath.Separator))
	if rel == "" {
		return nil
	}
	return strings.Split(rel, string(filepath.Separator))
}
