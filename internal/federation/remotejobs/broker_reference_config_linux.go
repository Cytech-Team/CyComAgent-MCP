//go:build linux

package remotejobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
)

const maxBrokerReferenceConfigBytes = 1 << 20

// BrokerReferenceConfig is an administrator-managed, receiver-local mapping.
// It intentionally has no caller-controlled write/update API in the broker.
type BrokerReferenceConfig struct {
	Version            int                               `json:"version"`
	Executables        []BrokerExecutableReference       `json:"executables"`
	WorkingDirectories []BrokerWorkingDirectoryReference `json:"working_directories"`
}

// LoadBrokerReferenceConfig opens a regular, owner-only file without following
// a final symlink, verifies its inode after open, and strictly decodes bounded
// JSON. The caller must protect parent directories from untrusted writes.
func LoadBrokerReferenceConfig(path string) (BrokerReferenceConfig, error) {
	var cfg BrokerReferenceConfig
	if path == "" || !filepathIsAbs(path) {
		return cfg, fmt.Errorf("reference config path must be absolute")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return cfg, fmt.Errorf("open reference config without following symlinks: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return cfg, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0077 != 0 {
		return cfg, fmt.Errorf("reference config must be a regular file owned by the service UID with mode 0600 or stricter")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBrokerReferenceConfigBytes+1))
	if err != nil {
		return cfg, err
	}
	if len(data) == 0 || len(data) > maxBrokerReferenceConfigBytes {
		return cfg, fmt.Errorf("reference config size is invalid")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return cfg, fmt.Errorf("reference config JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode reference config: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return cfg, fmt.Errorf("reference config has trailing data")
	}
	if cfg.Version != 1 {
		return cfg, fmt.Errorf("unsupported reference config version")
	}
	if len(cfg.Executables) == 0 || len(cfg.Executables) > 256 || len(cfg.WorkingDirectories) == 0 || len(cfg.WorkingDirectories) > 4096 {
		return cfg, fmt.Errorf("reference config entry count is outside limits")
	}
	return cfg, nil
}

func NewBrokerDescriptorResolverFromConfig(path string) (*StaticBrokerDescriptorResolver, error) {
	cfg, err := LoadBrokerReferenceConfig(path)
	if err != nil {
		return nil, err
	}
	return NewStaticBrokerDescriptorResolver(cfg.Executables, cfg.WorkingDirectories)
}

func filepathIsAbs(path string) bool { return len(path) > 0 && path[0] == '/' }
