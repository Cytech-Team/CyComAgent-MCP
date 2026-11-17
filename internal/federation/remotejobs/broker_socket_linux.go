//go:build linux

package remotejobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// BrokerSocketPolicy is receiver-owned filesystem policy for a pre-created
// Unix socket. It does not create, chmod, unlink, or otherwise mutate paths.
type BrokerSocketPolicy struct {
	SocketUID uint32
	ParentUID uint32
}

// BrokerSocketValidation owns descriptors for the validated parent directory
// and socket inode. Callers must retain it until the broker has completed the
// operation that relies on this validation, then call Close.
type BrokerSocketValidation struct {
	Path   string
	parent *os.File
	socket *os.File
}

func (v *BrokerSocketValidation) Close() error {
	if v == nil {
		return nil
	}
	var first error
	if v.socket != nil {
		if err := v.socket.Close(); err != nil {
			first = err
		}
		v.socket = nil
	}
	if v.parent != nil {
		if err := v.parent.Close(); first == nil && err != nil {
			first = err
		}
		v.parent = nil
	}
	return first
}

// ValidateBrokerSocketPathPinned resolves every component beneath a pinned /
// descriptor with openat2 and retains O_PATH descriptors for the immediate
// parent and socket inode. This prevents symlink traversal and pins the objects
// inspected; it does not make a later pathname-based systemd operation atomic.
func ValidateBrokerSocketPathPinned(path string, policy BrokerSocketPolicy) (*BrokerSocketValidation, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 || path == "/" {
		return nil, fmt.Errorf("broker socket path must be clean and absolute")
	}
	root, err := os.Open("/")
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	defer root.Close()
	relative := strings.TrimPrefix(path, "/")
	parentRelative := filepath.Dir(relative)
	parent, err := OpenBeneath(root, parentRelative, uint64(openPath|syscall.O_DIRECTORY), 0)
	if err != nil {
		return nil, fmt.Errorf("pin broker socket parent: %w", err)
	}
	fail := func(e error) (*BrokerSocketValidation, error) { _ = parent.Close(); return nil, e }
	parentInfo, err := parent.Stat()
	if err != nil {
		return fail(fmt.Errorf("stat pinned broker parent: %w", err))
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != policy.ParentUID {
		return fail(fmt.Errorf("broker socket parent owner is not authorized"))
	}
	if parentInfo.Mode().Perm()&0022 != 0 {
		return fail(fmt.Errorf("broker socket parent is group/world writable"))
	}
	socket, err := OpenBeneath(root, relative, uint64(openPath|syscall.O_NOFOLLOW), 0)
	if err != nil {
		return fail(fmt.Errorf("pin broker socket: %w", err))
	}
	socketInfo, err := socket.Stat()
	if err != nil {
		_ = socket.Close()
		return fail(fmt.Errorf("stat pinned broker socket: %w", err))
	}
	if socketInfo.Mode()&os.ModeSocket == 0 {
		_ = socket.Close()
		return fail(fmt.Errorf("broker path is not a Unix socket"))
	}
	socketStat, ok := socketInfo.Sys().(*syscall.Stat_t)
	if !ok || socketStat.Uid != policy.SocketUID {
		_ = socket.Close()
		return fail(fmt.Errorf("broker socket owner is not authorized"))
	}
	if socketInfo.Mode().Perm()&0077 != 0 {
		_ = socket.Close()
		return fail(fmt.Errorf("broker socket is accessible by group or others"))
	}
	return &BrokerSocketValidation{Path: path, parent: parent, socket: socket}, nil
}

// ValidateBrokerSocketPath is a compatibility check. Security-sensitive
// callers should retain the pinned validation returned by
// ValidateBrokerSocketPathPinned instead of discarding its descriptors.
func ValidateBrokerSocketPath(path string, policy BrokerSocketPolicy) error {
	validation, err := ValidateBrokerSocketPathPinned(path, policy)
	if err != nil {
		return err
	}
	return validation.Close()
}
