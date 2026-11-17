package remotejobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ExecutionPolicy is receiver-local authorization. It is deliberately separate
// from Federation trust and the generic MCP policy. Empty/zero values deny all.
type ExecutionPolicy struct {
	Enabled            bool          `json:"enabled"`
	AllowedCallers     []string      `json:"allowed_callers,omitempty"`
	AllowedExecutables []string      `json:"allowed_executables,omitempty"`
	WorkingDirRoots    []string      `json:"working_dir_roots,omitempty"`
	AllowedEnvKeys     []string      `json:"allowed_env_keys,omitempty"`
	MaxTimeout         time.Duration `json:"max_timeout,omitempty"`
	MaxOutputBytes     int64         `json:"max_output_bytes,omitempty"`
	MaxArgumentCount   int           `json:"max_argument_count,omitempty"`
	MaxArgumentBytes   int           `json:"max_argument_bytes,omitempty"`
}

// ProcessRequest is a structured process invocation. It has no shell field and
// is always executed as executable + argv by a future dedicated launcher.
type ProcessRequest struct {
	Executable  string            `json:"executable"`
	Args        []string          `json:"args,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Timeout     time.Duration     `json:"timeout"`
}

// Validate checks receiver-local authorization and canonical filesystem paths.
// callerTrusted must come from the live local NodeIdentity trust store; a valid
// signature alone is not sufficient. This function never starts a process.
func (p ExecutionPolicy) Validate(callerNodeID string, callerTrusted bool, req ProcessRequest) error {
	_, _, err := p.validateAndResolve(callerNodeID, callerTrusted, req)
	return err
}

func (p ExecutionPolicy) validateAndResolve(callerNodeID string, callerTrusted bool, req ProcessRequest) (string, string, error) {
	if !p.Enabled {
		return "", "", fmt.Errorf("remote execution is disabled by receiver policy")
	}
	if !callerTrusted {
		return "", "", fmt.Errorf("caller is not an active trusted Federation peer")
	}
	if !contains(p.AllowedCallers, callerNodeID) {
		return "", "", fmt.Errorf("caller is not authorized for remote execution")
	}
	if req.Executable == "" || !filepath.IsAbs(req.Executable) {
		return "", "", fmt.Errorf("executable must be an absolute path")
	}
	exe, err := filepath.EvalSymlinks(req.Executable)
	if err != nil {
		return "", "", fmt.Errorf("resolve executable: %w", err)
	}
	st, err := os.Stat(exe)
	if err != nil || !st.Mode().IsRegular() || st.Mode()&0111 == 0 {
		return "", "", fmt.Errorf("executable is not a regular executable file")
	}
	if !containsCanonical(p.AllowedExecutables, exe) {
		return "", "", fmt.Errorf("executable is not allowlisted")
	}
	if req.WorkingDir == "" {
		return "", "", fmt.Errorf("working directory is required")
	}
	cwd, err := filepath.EvalSymlinks(req.WorkingDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve working directory: %w", err)
	}
	cwdInfo, err := os.Stat(cwd)
	if err != nil || !cwdInfo.IsDir() {
		return "", "", fmt.Errorf("working directory is not a directory")
	}
	if !withinAnyRoot(cwd, p.WorkingDirRoots) {
		return "", "", fmt.Errorf("working directory escapes receiver-approved roots")
	}
	if req.Timeout <= 0 || p.MaxTimeout <= 0 || req.Timeout > p.MaxTimeout {
		return "", "", fmt.Errorf("timeout is outside receiver policy limits")
	}
	if len(req.Args) > p.MaxArgumentCount || p.MaxArgumentCount <= 0 {
		return "", "", fmt.Errorf("argument count exceeds receiver policy limits")
	}
	argBytes := 0
	for _, arg := range req.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return "", "", fmt.Errorf("argument contains NUL")
		}
		argBytes += len(arg)
	}
	if p.MaxArgumentBytes <= 0 || argBytes > p.MaxArgumentBytes {
		return "", "", fmt.Errorf("argument bytes exceed receiver policy limits")
	}
	if p.MaxOutputBytes <= 0 {
		return "", "", fmt.Errorf("output limit is not configured")
	}
	for key, value := range req.Environment {
		if !contains(p.AllowedEnvKeys, key) {
			return "", "", fmt.Errorf("environment key %q is not allowlisted", key)
		}
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 {
			return "", "", fmt.Errorf("invalid environment entry")
		}
		if len(key)+len(value) > 4096 {
			return "", "", fmt.Errorf("environment entry is too large")
		}
	}
	return exe, cwd, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsCanonical(paths []string, wanted string) bool {
	for _, path := range paths {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil && resolved == wanted {
			return true
		}
	}
	return false
}

func withinAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(resolved, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// PolicyStore persists receiver-owned execution policy with owner-only
// permissions. It exposes no network or caller-facing mutation operation.
type PolicyStore struct {
	path  string
	store *privateStateStore
}

type versionedPolicy struct {
	Version  int             `json:"version"`
	Revision uint64          `json:"revision"`
	Policy   ExecutionPolicy `json:"policy"`
}

func OpenPolicyStore(stateDir string) (*PolicyStore, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	dir := filepath.Join(stateDir, "federation")
	store, err := openPrivateStateStore(dir)
	if err != nil {
		return nil, err
	}
	return &PolicyStore{path: filepath.Join(dir, "remote-execution-policy.json"), store: store}, nil
}

func (s *PolicyStore) loadUnlocked() (versionedPolicy, error) {
	data, err := s.store.read("remote-execution-policy.json")
	if errors.Is(err, syscall.ENOENT) {
		return versionedPolicy{Version: 1}, nil
	}
	if err != nil {
		return versionedPolicy{}, err
	}
	var disk versionedPolicy
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&disk); err != nil {
		return versionedPolicy{}, fmt.Errorf("decode remote execution policy: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return versionedPolicy{}, fmt.Errorf("remote execution policy has trailing data")
	}
	if disk.Version != 1 || disk.Revision == 0 {
		return versionedPolicy{}, fmt.Errorf("unsupported policy schema or missing revision")
	}
	return disk, nil
}

func (s *PolicyStore) Load() (ExecutionPolicy, error) {
	var policy ExecutionPolicy
	err := s.store.withLock("remote-execution-policy.json.lock", func() error { disk, err := s.loadUnlocked(); policy = disk.Policy; return err })
	return policy, err
}

func (s *PolicyStore) LoadVersioned() (ExecutionPolicy, uint64, error) {
	var policy ExecutionPolicy
	var revision uint64
	err := s.store.withLock("remote-execution-policy.json.lock", func() error { disk, err := s.loadUnlocked(); policy, revision = disk.Policy, disk.Revision; return err })
	return policy, revision, err
}

// Save is intended only for a receiver-local administrative configuration
// path. It is not exposed as an MCP tool and accepts no Caller identity.
func (s *PolicyStore) Save(policy ExecutionPolicy) error {
	_, err := s.SaveVersioned(policy, 0)
	return err
}

// SaveVersioned performs compare-and-swap on the receiver-local policy revision.
// expectedRevision=0 means create-only; updates must provide the exact revision read.
func (s *PolicyStore) SaveVersioned(policy ExecutionPolicy, expectedRevision uint64) (uint64, error) {
	if policy.Enabled {
		if len(policy.AllowedCallers) == 0 || len(policy.AllowedExecutables) == 0 || len(policy.WorkingDirRoots) == 0 || policy.MaxTimeout <= 0 || policy.MaxOutputBytes <= 0 || policy.MaxArgumentCount <= 0 || policy.MaxArgumentBytes <= 0 {
			return 0, fmt.Errorf("enabled policy requires callers, executables, working roots and positive resource limits")
		}
		for _, path := range append(append([]string{}, policy.AllowedExecutables...), policy.WorkingDirRoots...) {
			if !filepath.IsAbs(path) {
				return 0, fmt.Errorf("policy paths must be absolute")
			}
		}
	}
	var revision uint64
	err := s.store.withLock("remote-execution-policy.json.lock", func() error {
		current, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return fmt.Errorf("policy revision conflict: expected %d, current %d", expectedRevision, current.Revision)
		}
		revision = current.Revision + 1
		data, err := json.MarshalIndent(versionedPolicy{Version: 1, Revision: revision, Policy: policy}, "", "  ")
		if err != nil {
			return err
		}
		return s.store.write("remote-execution-policy.json", append(data, '\n'), true)
	})
	return revision, err
}

// Close releases the pinned policy directory descriptor.
func (s *PolicyStore) Close() error {
	if s == nil {
		return nil
	}
	return s.store.close()
}
