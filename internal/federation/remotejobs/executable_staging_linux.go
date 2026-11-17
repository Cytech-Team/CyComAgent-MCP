//go:build linux

package remotejobs

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	executableStageMode  = 0555
	MinAbandonedStageAge = 26 * time.Hour
)

// StagedExecutable owns a broker-created file and a duplicate of the pinned
// staging directory. Close unlinks by directory FD + opaque basename, never
// by resolving a mutable absolute pathname.
type StagedExecutable struct {
	Name string
	File *os.File
	dir  *os.File
}

// Retain closes the staging descriptors without unlinking the artifact. Callers
// may use it only after the durable artifact registry has accepted the binding.
// It transfers cleanup responsibility to the registry/recovery workflow.
func (s *StagedExecutable) Retain() error {
	if s == nil {
		return nil
	}
	var first error
	if s.File != nil {
		if err := s.File.Close(); err != nil {
			first = err
		}
		s.File = nil
	}
	if s.dir != nil {
		if err := s.dir.Close(); err != nil && first == nil {
			first = err
		}
		s.dir = nil
	}
	return first
}

func (s *StagedExecutable) Close() error {
	if s == nil {
		return nil
	}
	var first error
	if s.File != nil {
		if err := s.File.Close(); err != nil {
			first = err
		}
		s.File = nil
	}
	if s.dir != nil && s.Name != "" {
		name, err := syscall.BytePtrFromString(s.Name)
		if err == nil {
			_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, s.dir.Fd(), uintptr(unsafe.Pointer(name)), 0)
			if errno != 0 && errno != syscall.ENOENT && first == nil {
				first = errno
			}
		} else if first == nil {
			first = err
		}
	}
	if s.dir != nil {
		if err := s.dir.Close(); err != nil && first == nil {
			first = err
		}
		s.dir = nil
	}
	return first
}

// StageExecutableSnapshot writes a verified memory snapshot into a trusted,
// already-open directory. The directory must be owned by brokerUID, not
// writable by group/other, and distinct from workerUID. No pathname is used
// for creation or cleanup. This does not make the artifact systemd-launchable.
func StageExecutableSnapshot(snapshot *ExecutableSnapshot, directory *os.File, brokerUID, workerUID uint32) (_ *StagedExecutable, err error) {
	if snapshot == nil || snapshot.Size() <= 0 || snapshot.Size() > MaxExecutableSnapshotBytes || len(snapshot.digest) != 64 || directory == nil {
		return nil, fmt.Errorf("valid executable snapshot and pinned staging directory are required")
	}
	if brokerUID == workerUID {
		return nil, fmt.Errorf("broker and worker identities must be distinct")
	}
	dirInfo, err := directory.Stat()
	if err != nil || !dirInfo.IsDir() {
		return nil, fmt.Errorf("staging descriptor is not a directory")
	}
	st, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != brokerUID || dirInfo.Mode().Perm()&0022 != 0 || dirInfo.Mode().Perm()&0001 == 0 {
		return nil, fmt.Errorf("staging directory must be broker-owned, non-writable by group/other, and traversable by the worker")
	}
	dupFD, err := syscall.Dup(int(directory.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate staging directory descriptor: %w", err)
	}
	syscall.CloseOnExec(dupFD)
	pinnedDir := os.NewFile(uintptr(dupFD), "broker-executable-staging")
	defer func() {
		if err != nil {
			_ = pinnedDir.Close()
		}
	}()
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("generate staging name: %w", err)
	}
	name := "exec-" + hex.EncodeToString(random[:])
	file, err := OpenBeneath(pinnedDir, name, uint64(syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW), executableStageMode)
	if err != nil {
		return nil, fmt.Errorf("create exclusive executable stage: %w", err)
	}
	stage := &StagedExecutable{Name: name, File: file, dir: pinnedDir}
	defer func() {
		if err != nil {
			_ = stage.Close()
		}
	}()
	writerHash := sha256.New()
	reader := snapshot.Open()
	written, copyErr := io.Copy(io.MultiWriter(file, writerHash), reader)
	if copyErr != nil {
		return nil, fmt.Errorf("write executable stage: %w", copyErr)
	}
	if written != snapshot.Size() || hex.EncodeToString(writerHash.Sum(nil)) != snapshot.digest {
		return nil, fmt.Errorf("staged executable content differs from snapshot")
	}
	if err := file.Chmod(executableStageMode); err != nil {
		return nil, fmt.Errorf("seal executable stage permissions: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync executable stage: %w", err)
	}
	verify, err := OpenBeneath(pinnedDir, name, uint64(syscall.O_RDONLY|syscall.O_NOFOLLOW), 0)
	if err != nil {
		return nil, fmt.Errorf("reopen staged executable: %w", err)
	}
	defer verify.Close()
	verifiedInfo, err := verify.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat staged executable: %w", err)
	}
	verifiedOwner, ok := verifiedInfo.Sys().(*syscall.Stat_t)
	if !ok || verifiedOwner.Uid != brokerUID || !verifiedInfo.Mode().IsRegular() || verifiedInfo.Mode().Perm() != executableStageMode || verifiedInfo.Size() != snapshot.Size() {
		return nil, fmt.Errorf("staged executable ownership, mode, type, or size is unsafe")
	}
	h := sha256.New()
	if _, err := io.Copy(h, verify); err != nil {
		return nil, fmt.Errorf("verify staged executable digest: %w", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != snapshot.digest {
		return nil, fmt.Errorf("staged executable digest verification failed")
	}
	if err := pinnedDir.Sync(); err != nil {
		return nil, fmt.Errorf("sync staging directory: %w", err)
	}
	return stage, nil
}

// FindAbandonedExecutableStages reports old, broker-owned artifacts matching
// this package's opaque naming scheme. It is intentionally read-only: age and
// filename alone never prove that a worker or durable job no longer references
// an artifact. Deletion requires a separate ledger- and unit-aware decision.
func FindAbandonedExecutableStages(directory *os.File, brokerUID uint32, olderThan time.Duration) ([]string, error) {
	return findAbandonedExecutableStagesAt(directory, brokerUID, olderThan, time.Now())
}

func findAbandonedExecutableStagesAt(directory *os.File, brokerUID uint32, olderThan time.Duration, now time.Time) ([]string, error) {
	if directory == nil || olderThan < MinAbandonedStageAge {
		return nil, fmt.Errorf("pinned staging directory and inspection age of at least %s are required", MinAbandonedStageAge)
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("staging descriptor is not a directory")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != brokerUID || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0001 == 0 {
		return nil, fmt.Errorf("staging directory ownership or permissions are unsafe")
	}
	dup, err := syscall.Dup(int(directory.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate staging directory: %w", err)
	}
	syscall.CloseOnExec(dup)
	enumerator := os.NewFile(uintptr(dup), "staging-recovery-enumerator")
	defer enumerator.Close()
	entries, err := enumerator.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("enumerate staging directory: %w", err)
	}
	cutoff := now.Add(-olderThan)
	var candidates []string
	for _, entry := range entries {
		name := entry.Name()
		if !validExecutableStageName(name) {
			continue
		}
		f, openErr := OpenBeneath(directory, name, uint64(openPath|syscall.O_NOFOLLOW), 0)
		if openErr != nil {
			continue
		}
		fi, statErr := f.Stat()
		_ = f.Close()
		if statErr != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != executableStageMode {
			continue
		}
		fst, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || fst.Uid != brokerUID || time.Unix(fst.Ctim.Sec, fst.Ctim.Nsec).After(cutoff) {
			continue
		}
		candidates = append(candidates, name)
	}
	return candidates, nil
}

func validExecutableStageName(name string) bool {
	if len(name) != len("exec-")+32 || name[:len("exec-")] != "exec-" {
		return false
	}
	for _, c := range name[len("exec-"):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
