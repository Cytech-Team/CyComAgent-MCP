//go:build linux

package remotejobs

import (
	"fmt"
	"os"
	"sync"
)

// WorkerReceivedExecution is a descriptor-owning, admitted Worker-side
// execution description. It is not executable by itself and exposes no
// systemd, process, shell, or filesystem-path operation.
type WorkerReceivedExecution struct {
	mu         sync.Mutex
	request    workerBrokerStartRequest
	artifact   ExecutableArtifactRecord
	executable *os.File
	directory  *os.File
	closed     bool
}

// AdmitWorkerReceivedExecution binds the authenticated session, receiver's
// durable one-shot reservation, independently loaded artifact registry record,
// and the two already-received descriptors. On success this object takes
// ownership of both descriptors; on failure the caller retains ownership.
func AdmitWorkerReceivedExecution(
	peer BrokerPeerIdentity,
	peerPolicy BrokerPeerPolicy,
	expected systemdStartRequest,
	received workerBrokerStartRequest,
	artifact ExecutableArtifactRecord,
	executable, workingDirectory *os.File,
) (*WorkerReceivedExecution, error) {
	if executable == nil || workingDirectory == nil || executable == workingDirectory {
		return nil, fmt.Errorf("distinct executable and working-directory descriptors are required")
	}
	if err := admitWorkerBrokerSession(peer, peerPolicy, expected, received); err != nil {
		return nil, err
	}
	if artifact.Version != executableArtifactRegistryVersion ||
		artifact.CallerNodeID != received.CallerNodeID ||
		artifact.RequestID != received.RequestID ||
		artifact.RemoteJobID != received.RemoteJobID ||
		artifact.LocalJobID != received.LocalJobID ||
		artifact.PayloadHash != received.PayloadHash ||
		artifact.PolicyRevision != received.PolicyRevision ||
		artifact.Name != received.ArtifactName ||
		artifact.Digest != received.ArtifactDigest ||
		artifact.Device != received.ArtifactDevice ||
		artifact.Inode != received.ArtifactInode {
		return nil, fmt.Errorf("worker artifact registry binding differs from admitted request")
	}
	if err := VerifyTransferredExecutableDescriptor(executable, artifact); err != nil {
		return nil, err
	}
	cwdInfo, err := workingDirectory.Stat()
	if err != nil || !cwdInfo.IsDir() {
		return nil, fmt.Errorf("received working-directory descriptor is invalid")
	}
	return &WorkerReceivedExecution{
		request: received, artifact: artifact,
		executable: executable, directory: workingDirectory,
	}, nil
}

// Close releases the two descriptors owned by an admitted handoff. It never
// removes an artifact or starts a process.
func (w *WorkerReceivedExecution) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var first error
	if w.executable != nil {
		first = w.executable.Close()
		w.executable = nil
	}
	if w.directory != nil {
		if err := w.directory.Close(); err != nil && first == nil {
			first = err
		}
		w.directory = nil
	}
	return first
}

// Artifact returns the immutable registry binding while the handoff remains
// open. The descriptor itself is intentionally not exposed.
func (w *WorkerReceivedExecution) Artifact() (ExecutableArtifactRecord, error) {
	if w == nil {
		return ExecutableArtifactRecord{}, fmt.Errorf("worker execution handoff is closed")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.executable == nil || w.directory == nil {
		return ExecutableArtifactRecord{}, fmt.Errorf("worker execution handoff is closed")
	}
	if err := VerifyTransferredExecutableDescriptor(w.executable, w.artifact); err != nil {
		return ExecutableArtifactRecord{}, err
	}
	return w.artifact, nil
}
