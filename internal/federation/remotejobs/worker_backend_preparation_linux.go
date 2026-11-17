//go:build linux

package remotejobs

import (
	"fmt"
	"reflect"
	"strconv"
	"syscall"
	"time"
)

// WorkerBackendPreparation is an immutable, descriptive-only snapshot for a
// future execution backend. Its fields are private and slice-bearing values
// are exposed only through defensive copies. It contains no paths, argv,
// environment, descriptor numbers, or launch operation.
type WorkerBackendPreparation struct {
	request           workerBrokerStartRequest
	artifact          ExecutableArtifactRecord
	workerProfile     WorkerServiceProfile
	workingDirDevice  uint64
	workingDirInode   uint64
	runtimeLimit      time.Duration
	executionDisabled bool
}

// Request returns the admitted request binding.
func (p WorkerBackendPreparation) Request() workerBrokerStartRequest { return p.request }

// Artifact returns the durable artifact binding.
func (p WorkerBackendPreparation) Artifact() ExecutableArtifactRecord { return p.artifact }

// WorkerProfile returns a defensive copy of the fixed worker profile.
func (p WorkerBackendPreparation) WorkerProfile() WorkerServiceProfile {
	return cloneWorkerServiceProfile(p.workerProfile)
}

// WorkingDirectoryIdentity returns the descriptor's device and inode.
func (p WorkerBackendPreparation) WorkingDirectoryIdentity() (uint64, uint64) {
	return p.workingDirDevice, p.workingDirInode
}

// RuntimeLimit returns the validated runtime bound.
func (p WorkerBackendPreparation) RuntimeLimit() time.Duration { return p.runtimeLimit }

// ExecutionDisabled reports the hard-coded non-executable state.
func (p WorkerBackendPreparation) ExecutionDisabled() bool { return p.executionDisabled }

func cloneWorkerServiceProfile(profile WorkerServiceProfile) WorkerServiceProfile {
	return WorkerServiceProfile{
		User: profile.User, Group: profile.Group,
		Properties:        append([]string(nil), profile.Properties...),
		InaccessiblePaths: append([]string(nil), profile.InaccessiblePaths...),
	}
}

// PrepareBackend validates the broker-owned service profile and snapshots the
// admitted descriptor identities. It does not duplicate or transfer FDs,
// contact systemd, construct ExecStart, or launch anything. Execution remains
// disabled until a separately reviewed descriptor-capable backend exists.
func (w *WorkerReceivedExecution) PrepareBackend(profile WorkerServiceProfile) (WorkerBackendPreparation, error) {
	var out WorkerBackendPreparation
	if w == nil {
		return out, fmt.Errorf("worker execution handoff is closed")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.executable == nil || w.directory == nil {
		return out, fmt.Errorf("worker execution handoff is closed")
	}
	if err := VerifyTransferredExecutableDescriptor(w.executable, w.artifact); err != nil {
		return out, err
	}
	runtimeLimit, err := validateWorkerServiceProfile(profile, w.artifact.WorkerUID)
	if err != nil {
		return out, err
	}
	cwdInfo, err := w.directory.Stat()
	if err != nil || !cwdInfo.IsDir() {
		return out, fmt.Errorf("admitted working-directory descriptor is no longer valid")
	}
	st, ok := cwdInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return out, fmt.Errorf("working-directory identity is unavailable")
	}
	return WorkerBackendPreparation{
		request: w.request, artifact: w.artifact,
		workerProfile:    cloneWorkerServiceProfile(profile),
		workingDirDevice: uint64(st.Dev), workingDirInode: st.Ino,
		runtimeLimit: runtimeLimit, executionDisabled: true,
	}, nil
}

// Revalidate verifies that the private preparation snapshot still matches the
// retained admitted descriptors. It is observational only.
func (w *WorkerReceivedExecution) Revalidate(prepared WorkerBackendPreparation) error {
	if w == nil {
		return fmt.Errorf("worker execution handoff is closed")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.executable == nil || w.directory == nil {
		return fmt.Errorf("worker execution handoff is closed")
	}
	if !prepared.executionDisabled || prepared.request != w.request ||
		!reflect.DeepEqual(prepared.artifact, w.artifact) {
		return fmt.Errorf("backend preparation binding mismatch")
	}
	runtimeLimit, err := validateWorkerServiceProfile(prepared.workerProfile, w.artifact.WorkerUID)
	if err != nil {
		return err
	}
	if prepared.runtimeLimit != runtimeLimit {
		return fmt.Errorf("backend preparation runtime limit differs from fixed profile")
	}
	if err := VerifyTransferredExecutableDescriptor(w.executable, w.artifact); err != nil {
		return err
	}
	info, err := w.directory.Stat()
	if err != nil || !info.IsDir() {
		return fmt.Errorf("working-directory descriptor is no longer valid")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(st.Dev) != prepared.workingDirDevice || st.Ino != prepared.workingDirInode {
		return fmt.Errorf("working-directory descriptor identity changed")
	}
	return nil
}

func validateWorkerServiceProfile(profile WorkerServiceProfile, workerUID uint32) (time.Duration, error) {
	if profile.User != strconv.FormatUint(uint64(workerUID), 10) ||
		len(profile.InaccessiblePaths) != 3 || profile.InaccessiblePaths[0] != "/run/user" ||
		profile.InaccessiblePaths[1] != "/run/dbus/system_bus_socket" {
		return 0, fmt.Errorf("worker profile identity or inaccessible-path baseline mismatch")
	}
	gid64, err := strconv.ParseUint(profile.Group, 10, 32)
	if err != nil || gid64 == 0 {
		return 0, fmt.Errorf("worker profile group must be a dedicated non-root numeric GID")
	}
	var runtimeLimit time.Duration
	for _, property := range profile.Properties {
		const prefix = "RuntimeMaxSec="
		if len(property) >= len(prefix) && property[:len(prefix)] == prefix {
			if runtimeLimit != 0 {
				return 0, fmt.Errorf("duplicate runtime limit in worker profile")
			}
			runtimeLimit, err = time.ParseDuration(property[len(prefix):])
			if err != nil {
				return 0, fmt.Errorf("invalid worker runtime limit")
			}
		}
	}
	expected, err := BuildWorkerServiceProfile(workerUID, uint32(gid64), runtimeLimit, profile.InaccessiblePaths[2])
	if err != nil || !reflect.DeepEqual(profile, expected) {
		return 0, fmt.Errorf("worker service profile differs from fixed broker baseline")
	}
	return runtimeLimit, nil
}
