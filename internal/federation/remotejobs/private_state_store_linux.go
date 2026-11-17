//go:build linux

package remotejobs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// privateStateStore pins a private directory and performs child operations
// relative to its descriptor. Record names are generated single components.
type privateStateStore struct{ root *os.File }

func openPrivateStateStore(path string) (*privateStateStore, error) {
	// Resolve the longest existing prefix once, allowing an explicit state-root
	// alias while ensuring all subsequently created/opened components are beneath
	// the canonical path. Child operations never traverse the supplied alias.
	resolved, err := canonicalizeExistingPrefix(path)
	if err != nil {
		return nil, err
	}
	path = resolved
	if err := ensurePrivateStateDir(path); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), path)
	info, err := root.Stat()
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	pathInfo, pathErr := os.Lstat(path)
	if !ok || !info.IsDir() || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 || pathErr != nil || !os.SameFile(info, pathInfo) {
		_ = root.Close()
		return nil, fmt.Errorf("pinned metadata directory failed identity, ownership, or permission validation")
	}
	return &privateStateStore{root: root}, nil
}

func canonicalizeExistingPrefix(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(abs)
	var suffix []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(resolveErr) {
			return "", resolveErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", resolveErr
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func (s *privateStateStore) close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

func (s *privateStateStore) entries() ([]os.DirEntry, error) {
	if s == nil || s.root == nil {
		return nil, fmt.Errorf("metadata store is closed")
	}
	fd, err := syscall.Openat(int(s.root.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "metadata-directory")
	defer dir.Close()
	return dir.ReadDir(-1)
}

func (s *privateStateStore) read(name string) ([]byte, error) {
	if s == nil || s.root == nil {
		return nil, fmt.Errorf("metadata store is closed")
	}
	f, err := OpenBeneath(s.root, name, uint64(syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK), 0)
	if err != nil {
		return nil, fmt.Errorf("open metadata record %q: %w", name, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return nil, fmt.Errorf("metadata record is not a bounded private regular file owned by the effective user")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("metadata record exceeds size limit")
	}
	return data, nil
}

func (s *privateStateStore) lockFile(name string) (*os.File, error) {
	if s == nil || s.root == nil {
		return nil, fmt.Errorf("metadata store is closed")
	}
	f, err := OpenBeneath(s.root, name, uint64(syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW), 0600)
	if err != nil {
		return nil, fmt.Errorf("open metadata lock %q: %w", name, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, fmt.Errorf("metadata lock is not a private regular file owned by the effective user")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *privateStateStore) withLock(name string, fn func() error) error {
	f, err := s.lockFile(name)
	if err != nil {
		return err
	}
	defer f.Close()
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func (s *privateStateStore) write(name string, data []byte, replace bool) error {
	if s == nil || s.root == nil {
		return fmt.Errorf("metadata store is closed")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmpName := ".record-" + hex.EncodeToString(nonce[:]) + ".tmp"
	f, err := OpenBeneath(s.root, tmpName, uint64(syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW), 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = syscall.Unlinkat(int(s.root.Fd()), tmpName)
		return err
	}
	if replace {
		err = syscall.Renameat(int(s.root.Fd()), tmpName, int(s.root.Fd()), name)
	} else {
		err = linkat(int(s.root.Fd()), tmpName, int(s.root.Fd()), name)
		_ = syscall.Unlinkat(int(s.root.Fd()), tmpName)
	}
	if err != nil {
		_ = syscall.Unlinkat(int(s.root.Fd()), tmpName)
		return err
	}
	return s.root.Sync()
}

func linkat(oldDirFD int, oldName string, newDirFD int, newName string) error {
	oldPtr, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return err
	}
	newPtr, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_LINKAT, uintptr(oldDirFD), uintptr(unsafe.Pointer(oldPtr)), uintptr(newDirFD), uintptr(unsafe.Pointer(newPtr)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
