//go:build linux

package remotejobs

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	linuxSYSOpenat2     = 437 // Linux generic syscall table (5.6+).
	resolveNoMagicLinks = 0x02
	resolveNoSymlinks   = 0x04
	resolveBeneath      = 0x08
	openPath            = 0x200000
)

type linuxOpenHow struct {
	Flags   uint64
	Mode    uint64
	Resolve uint64
}

// OpenBeneath opens a path relative to an already-open receiver-owned root
// directory. Absolute paths, traversal, symlinks and procfs magic links are
// rejected by both lexical validation and the kernel's openat2 resolver.
// Kernels without openat2 fail closed; there is deliberately no openat fallback.
func OpenBeneath(root *os.File, relative string, flags uint64, mode uint64) (*os.File, error) {
	if root == nil || relative == "" || strings.HasPrefix(relative, "/") || strings.IndexByte(relative, 0) >= 0 {
		return nil, fmt.Errorf("root descriptor and safe relative path are required")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("path contains an unsafe component")
		}
	}
	info, err := root.Stat()
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("root descriptor is not an accessible directory")
	}
	path, err := syscall.BytePtrFromString(relative)
	if err != nil {
		return nil, err
	}
	how := linuxOpenHow{Flags: flags | uint64(syscall.O_CLOEXEC), Mode: mode, Resolve: resolveBeneath | resolveNoMagicLinks | resolveNoSymlinks}
	fd, _, errno := syscall.Syscall6(linuxSYSOpenat2, root.Fd(), uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	if errno != 0 {
		return nil, fmt.Errorf("openat2 receiver reference (fail closed): %w", errno)
	}
	return os.NewFile(fd, relative), nil
}

// OpenExecutableBeneath opens a regular executable file without following any
// symlink and returns the pinned file description for subsequent digest checks.
func OpenExecutableBeneath(root *os.File, relative string) (*os.File, error) {
	f, err := OpenBeneath(root, relative, uint64(syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK), 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		_ = f.Close()
		return nil, fmt.Errorf("receiver reference is not a regular executable")
	}
	return f, nil
}

// OpenDirectoryBeneath returns a pinned directory descriptor suitable for a
// future descriptor-aware worker launcher.
func OpenDirectoryBeneath(root *os.File, relative string) (*os.File, error) {
	f, err := OpenBeneath(root, relative, uint64(openPath|syscall.O_DIRECTORY), 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("receiver reference is not a directory")
	}
	return f, nil
}
