//go:build linux

package remotejobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// BrokerExecutableReference is receiver-local configuration. Digest and path
// are never supplied by a Federation caller; RootPath is opened and pinned by
// the store, and RelativePath is resolved beneath that descriptor with openat2.
type BrokerExecutableReference struct {
	Digest       string `json:"digest"`
	RootPath     string `json:"root_path"`
	RelativePath string `json:"relative_path"`
}

// BrokerWorkingDirectoryReference binds an opaque reference to one trusted
// Federation node. A reference cannot be reused by another caller.
type BrokerWorkingDirectoryReference struct {
	Reference    string `json:"reference"`
	CallerNodeID string `json:"caller_node_id"`
	RootPath     string `json:"root_path"`
	RelativePath string `json:"relative_path"`
}

type pinnedReference struct {
	root         *os.File
	absolutePath string
	relativePath string
}

// StaticBrokerDescriptorResolver is immutable receiver-owned reference config.
// Close it only after all active resolution calls have completed.
type StaticBrokerDescriptorResolver struct {
	mu          sync.RWMutex
	closed      bool
	executables map[string]pinnedReference
	workingDirs map[string]struct {
		caller string
		ref    pinnedReference
	}
}

func openReferenceRoot(path string) (pinnedReference, error) {
	if !filepath.IsAbs(path) {
		return pinnedReference{}, fmt.Errorf("reference root must be an absolute receiver-local path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return pinnedReference{}, fmt.Errorf("resolve reference root: %w", err)
	}
	root, err := os.Open(canonical)
	if err != nil {
		return pinnedReference{}, err
	}
	info, err := root.Stat()
	if err != nil || !info.IsDir() {
		_ = root.Close()
		return pinnedReference{}, fmt.Errorf("reference root is not a directory")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || info.Mode().Perm()&0022 != 0 {
		_ = root.Close()
		return pinnedReference{}, fmt.Errorf("reference root must be owned by root or the service UID and not group/world writable")
	}
	return pinnedReference{root: root, absolutePath: canonical}, nil
}

func NewStaticBrokerDescriptorResolver(executables []BrokerExecutableReference, workingDirs []BrokerWorkingDirectoryReference) (*StaticBrokerDescriptorResolver, error) {
	r := &StaticBrokerDescriptorResolver{executables: make(map[string]pinnedReference), workingDirs: make(map[string]struct {
		caller string
		ref    pinnedReference
	})}
	cleanup := func() { _ = r.Close() }
	for _, entry := range executables {
		if !digestPattern.MatchString(entry.Digest) {
			cleanup()
			return nil, fmt.Errorf("invalid configured executable digest")
		}
		if _, exists := r.executables[entry.Digest]; exists {
			cleanup()
			return nil, fmt.Errorf("duplicate configured executable digest")
		}
		ref, err := openReferenceRoot(entry.RootPath)
		if err != nil {
			cleanup()
			return nil, err
		}
		if !safeRelativeReference(entry.RelativePath) {
			_ = ref.root.Close()
			cleanup()
			return nil, fmt.Errorf("invalid configured executable relative path")
		}
		ref.relativePath = entry.RelativePath
		ref.absolutePath = filepath.Join(ref.absolutePath, filepath.FromSlash(entry.RelativePath))
		r.executables[entry.Digest] = ref
	}
	for _, entry := range workingDirs {
		if !opaqueRefPattern.MatchString(entry.Reference) || !validID.MatchString(entry.CallerNodeID) {
			cleanup()
			return nil, fmt.Errorf("invalid configured working-directory reference or caller")
		}
		if _, exists := r.workingDirs[entry.Reference]; exists {
			cleanup()
			return nil, fmt.Errorf("duplicate configured working-directory reference")
		}
		ref, err := openReferenceRoot(entry.RootPath)
		if err != nil {
			cleanup()
			return nil, err
		}
		if !safeRelativeReference(entry.RelativePath) {
			_ = ref.root.Close()
			cleanup()
			return nil, fmt.Errorf("invalid configured working-directory relative path")
		}
		ref.relativePath = entry.RelativePath
		ref.absolutePath = filepath.Join(ref.absolutePath, filepath.FromSlash(entry.RelativePath))
		r.workingDirs[entry.Reference] = struct {
			caller string
			ref    pinnedReference
		}{entry.CallerNodeID, ref}
	}
	return r, nil
}

func safeRelativeReference(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (r *StaticBrokerDescriptorResolver) ResolveBrokerDescriptors(caller FederationCallerContext, req BrokerStartRequest) (BrokerResolvedDescriptors, error) {
	if r == nil || !caller.valid() {
		return BrokerResolvedDescriptors{}, fmt.Errorf("valid caller and configured reference store are required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return BrokerResolvedDescriptors{}, fmt.Errorf("descriptor reference store is closed")
	}
	exeRef, ok := r.executables[req.ExecutableDigest]
	if !ok {
		return BrokerResolvedDescriptors{}, fmt.Errorf("executable digest is not configured on receiver")
	}
	cwdEntry, ok := r.workingDirs[req.WorkingDirRef]
	if !ok || cwdEntry.caller != caller.NodeID() {
		return BrokerResolvedDescriptors{}, fmt.Errorf("working-directory reference is not assigned to this caller")
	}
	exe, err := OpenExecutableBeneath(exeRef.root, exeRef.relativePath)
	if err != nil {
		return BrokerResolvedDescriptors{}, err
	}
	cwd, err := OpenDirectoryBeneath(cwdEntry.ref.root, cwdEntry.ref.relativePath)
	if err != nil {
		_ = exe.Close()
		return BrokerResolvedDescriptors{}, err
	}
	if len(req.Argv) == 0 {
		_ = exe.Close()
		_ = cwd.Close()
		return BrokerResolvedDescriptors{}, fmt.Errorf("request argv is empty")
	}
	return BrokerResolvedDescriptors{Request: ProcessRequest{Executable: exeRef.absolutePath, Args: append([]string(nil), req.Argv[1:]...), WorkingDir: cwdEntry.ref.absolutePath, Environment: cloneEnvironment(req.Environment), Timeout: time.Duration(req.TimeoutMillis) * time.Millisecond}, Executable: exe, WorkingDir: cwd}, nil
}

func cloneEnvironment(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (r *StaticBrokerDescriptorResolver) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var first error
	for _, ref := range r.executables {
		if err := ref.root.Close(); err != nil && first == nil {
			first = err
		}
	}
	for _, entry := range r.workingDirs {
		if err := entry.ref.root.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
