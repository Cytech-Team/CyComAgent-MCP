//go:build linux && !android

package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type linuxJobStore struct {
	dir   *os.File
	locks *os.File
}

func openJobStore(path string) (jobStore, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("job state path must be a real directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, err
	}
	dfd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(dfd), path)
	pinned, err := dir.Stat()
	if err != nil {
		dir.Close()
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(pinned, current) {
		dir.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("job state directory changed while pinning")
	}
	if !pinned.IsDir() || pinned.Mode().Perm()&0o077 != 0 || pinned.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		dir.Close()
		return nil, fmt.Errorf("job state directory must be private")
	}
	if err := syscall.Mkdirat(int(dir.Fd()), ".locks", 0o700); err != nil && err != syscall.EEXIST {
		dir.Close()
		return nil, err
	}
	lfd, err := syscall.Openat(int(dir.Fd()), ".locks", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		dir.Close()
		return nil, err
	}
	locks := os.NewFile(uintptr(lfd), filepath.Join(path, ".locks"))
	if err := locks.Chmod(0o700); err != nil {
		locks.Close()
		dir.Close()
		return nil, err
	}
	li, err := locks.Stat()
	if err != nil {
		locks.Close()
		dir.Close()
		return nil, err
	}
	if !li.IsDir() || li.Mode().Perm()&0o077 != 0 || li.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		locks.Close()
		dir.Close()
		return nil, fmt.Errorf("job lock directory must be private")
	}
	return &linuxJobStore{dir: dir, locks: locks}, nil
}

func (s *linuxJobStore) lock(id string, fn func() error) error {
	fd, err := syscall.Openat(int(s.locks.Fd()), id+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), id+".lock")
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("job lock must be an owned regular file")
	}
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	return fn()
}
func (s *linuxJobStore) existsRecord(id string) (bool, error) {
	fd, err := syscall.Openat(int(s.dir.Fd()), id+".json", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOENT {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f := os.NewFile(uintptr(fd), id+".json")
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return false, fmt.Errorf("job record is not an owned private regular file")
	}
	return true, nil
}
func (s *linuxJobStore) createLog(id string, appendMode bool) (*os.File, error) {
	flags := syscall.O_CREAT | syscall.O_EXCL | syscall.O_WRONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if appendMode {
		flags |= syscall.O_APPEND
	}
	fd, err := syscall.Openat(int(s.dir.Fd()), id+".log", flags, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), id+".log")
	if st, e := f.Stat(); e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		f.Close()
		if e != nil {
			return nil, e
		}
		return nil, fmt.Errorf("job log is not regular")
	}
	return f, nil
}
func (s *linuxJobStore) openLog(id string, appendMode bool) (*os.File, error) {
	flags := syscall.O_RDONLY
	if appendMode {
		flags = syscall.O_WRONLY | syscall.O_APPEND
	}
	fd, err := syscall.Openat(int(s.dir.Fd()), id+".log", flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), id+".log")
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		f.Close()
		if e != nil {
			return nil, e
		}
		return nil, fmt.Errorf("job log is not an owned private regular file")
	}
	return f, nil
}
func (s *linuxJobStore) readRecord(id string) ([]byte, error) {
	fd, err := syscall.Openat(int(s.dir.Fd()), id+".json", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), id+".json")
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 || st.Mode().Perm()&0o077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("job record is not an owned bounded private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("job record exceeds size limit")
	}
	return data, nil
}
func (s *linuxJobStore) writeRecord(id string, data []byte, crash func(string)) error {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	tmp := id + ".tmp-" + hex.EncodeToString(raw[:])
	fd, err := syscall.Openat(int(s.dir.Fd()), tmp, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	if crash != nil {
		crash("temp-created")
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			_ = syscall.Unlinkat(int(s.dir.Fd()), tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if crash != nil {
		crash("temp-written")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if crash != nil {
		crash("temp-synced")
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = syscall.Renameat(int(s.dir.Fd()), tmp, int(s.dir.Fd()), id+".json"); err != nil {
		return err
	}
	if crash != nil {
		crash("renamed")
	}
	if err = s.dir.Sync(); err != nil {
		return err
	}
	if crash != nil {
		crash("directory-synced")
	}
	ok = true
	return nil
}
func (s *linuxJobStore) entries() ([]os.DirEntry, error) {
	fd, err := syscall.Openat(int(s.dir.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "jobs")
	defer f.Close()
	return f.ReadDir(-1)
}
func (s *linuxJobStore) remove(name string) error {
	if strings.Contains(name, "/") || name == "." || name == ".." {
		return fmt.Errorf("invalid job store entry")
	}
	err := syscall.Unlinkat(int(s.dir.Fd()), name)
	if err == nil {
		return s.dir.Sync()
	}
	return err
}
func (s *linuxJobStore) removeTemps(id string) error {
	entries, err := s.entries()
	if err != nil {
		return err
	}
	prefix := id + ".tmp-"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && !e.IsDir() {
			if err := s.remove(e.Name()); err != nil && err != syscall.ENOENT {
				return err
			}
		}
	}
	return nil
}
