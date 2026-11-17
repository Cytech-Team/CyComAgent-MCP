//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

const MaxExecutableSnapshotBytes int64 = 128 << 20

// ExecutableSnapshot is a bounded, broker-owned in-memory copy of the exact
// executable bytes whose digest was authorized. It is not yet a launchable
// artifact; a privileged staging adapter must persist it safely before dispatch.
type ExecutableSnapshot struct {
	content []byte
	digest  string
}

func (s *ExecutableSnapshot) Digest() string {
	if s == nil {
		return ""
	}
	return s.digest
}

func (s *ExecutableSnapshot) Size() int64 {
	if s == nil {
		return 0
	}
	return int64(len(s.content))
}

// Open returns an independent read-only view; callers cannot mutate the
// snapshot retained by the contract.
func (s *ExecutableSnapshot) Open() *io.SectionReader {
	if s == nil {
		return nil
	}
	return io.NewSectionReader(bytesReaderAt(s.content), 0, int64(len(s.content)))
}

type snapshotBytes []byte

func (b snapshotBytes) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
func bytesReaderAt(b []byte) snapshotBytes { return snapshotBytes(b) }

func CreateExecutableSnapshot(source *os.File, expectedDigest string) (*ExecutableSnapshot, error) {
	if source == nil || len(expectedDigest) != 64 {
		return nil, fmt.Errorf("pinned executable and SHA-256 digest are required")
	}
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return nil, fmt.Errorf("snapshot source is not a regular executable")
	}
	if info.Size() <= 0 || info.Size() > MaxExecutableSnapshotBytes {
		return nil, fmt.Errorf("executable size is outside snapshot bounds")
	}
	content := make([]byte, info.Size())
	n, err := source.ReadAt(content, 0)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read executable snapshot: %w", err)
	}
	if int64(n) != info.Size() {
		return nil, fmt.Errorf("executable changed or was truncated during snapshot")
	}
	after, err := source.Stat()
	if err != nil {
		return nil, fmt.Errorf("restat executable after snapshot: %w", err)
	}
	if !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("executable metadata changed during snapshot")
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if digest != expectedDigest {
		return nil, fmt.Errorf("executable snapshot digest does not match authorized digest")
	}
	return &ExecutableSnapshot{content: content, digest: digest}, nil
}
