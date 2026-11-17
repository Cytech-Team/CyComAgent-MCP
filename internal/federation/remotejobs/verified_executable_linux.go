//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
)

// VerifiedExecutableArtifact owns a read-only descriptor to the exact inode
// named by a durable artifact record. It is an identity capability, not a
// systemd ExecStart path and not permission to launch the executable.
type VerifiedExecutableArtifact struct {
	mu     sync.Mutex
	file   *os.File
	record ExecutableArtifactRecord
	closed bool
}

// OpenVerifiedExecutableArtifact opens relative to a trusted pinned directory,
// then verifies the opened descriptor's device, inode, owner, mode, size and
// digest against the durable registry record.
func OpenVerifiedExecutableArtifact(directory *os.File, record ExecutableArtifactRecord) (*VerifiedExecutableArtifact, error) {
	if directory == nil || !validExecutableStageName(record.Name) || record.Size <= 0 ||
		record.Inode == 0 || record.BrokerUID == record.WorkerUID || len(record.Digest) != 64 {
		return nil, fmt.Errorf("valid pinned directory and executable artifact record are required")
	}
	if _, err := hex.DecodeString(record.Digest); err != nil {
		return nil, fmt.Errorf("artifact digest is invalid")
	}
	dirInfo, err := directory.Stat()
	if err != nil || !dirInfo.IsDir() {
		return nil, fmt.Errorf("staging directory descriptor is invalid")
	}
	dirStat, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok || dirStat.Uid != record.BrokerUID || dirInfo.Mode().Perm()&0022 != 0 || dirInfo.Mode().Perm()&0001 == 0 {
		return nil, fmt.Errorf("staging directory ownership or permissions are unsafe")
	}
	file, err := OpenBeneath(directory, record.Name, uint64(syscall.O_RDONLY|syscall.O_NOFOLLOW), 0)
	if err != nil {
		return nil, fmt.Errorf("open registered executable by pinned directory: %w", err)
	}
	capability := &VerifiedExecutableArtifact{file: file, record: record}
	if err := capability.Verify(); err != nil {
		_ = capability.Close()
		return nil, err
	}
	return capability, nil
}

// Verify rechecks the retained descriptor itself. Replacing or renaming the
// directory entry cannot retarget this descriptor to a different inode.
func (v *VerifiedExecutableArtifact) Verify() error {
	if v == nil {
		return fmt.Errorf("verified executable capability is closed")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.verifyLocked()
}

func (v *VerifiedExecutableArtifact) verifyLocked() error {
	if v.closed || v.file == nil {
		return fmt.Errorf("verified executable capability is closed")
	}
	info, err := v.file.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != executableStageMode ||
		st.Uid != v.record.BrokerUID || uint64(st.Dev) != v.record.Device ||
		st.Ino != v.record.Inode || info.Size() != v.record.Size {
		return fmt.Errorf("retained executable descriptor identity differs from registry")
	}
	hash := sha256.New()
	reader := io.NewSectionReader(v.file, 0, v.record.Size)
	if _, err := io.Copy(hash, reader); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != v.record.Digest {
		return fmt.Errorf("retained executable descriptor digest differs from registry")
	}
	return nil
}

// DuplicateReadOnlyDescriptor returns a CLOEXEC duplicate after revalidation.
// The caller owns the returned descriptor. It does not expose a pathname.
func (v *VerifiedExecutableArtifact) DuplicateReadOnlyDescriptor() (*os.File, error) {
	if v == nil {
		return nil, fmt.Errorf("verified executable capability is required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.verifyLocked(); err != nil {
		return nil, err
	}
	fd, err := syscall.Dup(int(v.file.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate verified executable descriptor: %w", err)
	}
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "verified-executable-artifact"), nil
}

func (v *VerifiedExecutableArtifact) Record() (ExecutableArtifactRecord, error) {
	if v == nil {
		return ExecutableArtifactRecord{}, fmt.Errorf("verified executable capability is required")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.verifyLocked(); err != nil {
		return ExecutableArtifactRecord{}, err
	}
	return v.record, nil
}

func (v *VerifiedExecutableArtifact) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true
	if v.file == nil {
		return nil
	}
	err := v.file.Close()
	v.file = nil
	return err
}

// VerifyTransferredExecutableDescriptor validates an SCM_RIGHTS-received
// descriptor against the receiver's durable artifact record. It never opens
// a path and requires the transferred open-file description to be read-only.
func VerifyTransferredExecutableDescriptor(file *os.File, record ExecutableArtifactRecord) error {
	if file == nil || !validExecutableStageName(record.Name) || record.Size <= 0 ||
		record.Inode == 0 || record.BrokerUID == record.WorkerUID ||
		len(record.Digest) != 64 || !digestPattern.MatchString(record.Digest) {
		return fmt.Errorf("valid received descriptor and artifact record are required")
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), uintptr(syscall.F_GETFL), 0)
	if errno != 0 || int(flags)&syscall.O_ACCMODE != syscall.O_RDONLY {
		return fmt.Errorf("transferred executable descriptor is not read-only")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != executableStageMode ||
		st.Uid != record.BrokerUID || uint64(st.Dev) != record.Device ||
		st.Ino != record.Inode || info.Size() != record.Size {
		return fmt.Errorf("transferred executable descriptor identity differs from artifact record")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(file, 0, record.Size)); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != record.Digest {
		return fmt.Errorf("transferred executable descriptor digest differs from artifact record")
	}
	return nil
}
