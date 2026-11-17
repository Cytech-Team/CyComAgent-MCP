//go:build !linux || android

package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type pathJobStore struct{ dir string }

func openJobStore(path string) (jobStore, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return nil, fmt.Errorf("job state path must be a real directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, err
	}
	lock := filepath.Join(path, ".locks")
	if err := os.MkdirAll(lock, 0o700); err != nil {
		return nil, err
	}
	return &pathJobStore{dir: path}, nil
}
func (s *pathJobStore) lock(id string, fn func() error) error {
	return withJobFileLock(filepath.Join(s.dir, ".locks", id+".lock"), fn)
}
func (s *pathJobStore) existsRecord(id string) (bool, error) {
	_, e := os.Lstat(filepath.Join(s.dir, id+".json"))
	if e == nil {
		return true, nil
	}
	if os.IsNotExist(e) {
		return false, nil
	}
	return false, e
}
func (s *pathJobStore) createLog(id string, appendMode bool) (*os.File, error) {
	flags := os.O_CREATE | os.O_EXCL | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	}
	return os.OpenFile(filepath.Join(s.dir, id+".log"), flags, 0o600)
}
func (s *pathJobStore) openLog(id string, appendMode bool) (*os.File, error) {
	if appendMode {
		return openJobLogAppend(filepath.Join(s.dir, id+".log"))
	}
	return openJobLogRead(filepath.Join(s.dir, id+".log"))
}
func (s *pathJobStore) readRecord(id string) ([]byte, error) {
	f, e := openJobRecordRead(filepath.Join(s.dir, id+".json"))
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return os.ReadFile(filepath.Join(s.dir, id+".json"))
}
func (s *pathJobStore) writeRecord(id string, data []byte, crash func(string)) error {
	f, e := os.CreateTemp(s.dir, id+".tmp-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if crash != nil {
		crash("temp-created")
	}
	if e = f.Chmod(0o600); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if crash != nil {
		crash("temp-written")
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if crash != nil {
		crash("temp-synced")
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(tmp, filepath.Join(s.dir, id+".json")); e != nil {
		return e
	}
	if crash != nil {
		crash("renamed")
	}
	d, e := os.Open(s.dir)
	if e != nil {
		return e
	}
	defer d.Close()
	if e = d.Sync(); e != nil {
		return e
	}
	if crash != nil {
		crash("directory-synced")
	}
	return nil
}
func (s *pathJobStore) entries() ([]os.DirEntry, error) { return os.ReadDir(s.dir) }
func (s *pathJobStore) remove(name string) error {
	if strings.Contains(name, "/") {
		return fmt.Errorf("invalid job store entry")
	}
	return os.Remove(filepath.Join(s.dir, name))
}
func (s *pathJobStore) removeTemps(id string) error {
	entries, e := s.entries()
	if e != nil {
		return e
	}
	for _, v := range entries {
		if strings.HasPrefix(v.Name(), id+".tmp-") && !v.IsDir() {
			if e := s.remove(v.Name()); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
	}
	return nil
}
