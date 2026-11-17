//go:build linux

package remotejobs

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	workerBrokerProtocolVersion = 1
	maxWorkerBrokerRequestBytes = 8 << 10
)

// workerBrokerStartRequest is the narrow handoff from the coordinator side to
// a future privileged worker broker. It carries durable identities and an
// immutable artifact reference only. It contains no command, argv, environment,
// UID/GID, executable path, working-directory path, socket path, cgroup path,
// unit properties, or caller-selected unit name.
type workerBrokerStartRequest struct {
	Version        int    `json:"version"`
	CallerNodeID   string `json:"caller_node_id"`
	RequestID      string `json:"request_id"`
	RemoteJobID    string `json:"remote_job_id"`
	LocalJobID     string `json:"local_job_id"`
	StartAttemptID string `json:"start_attempt_id"`
	UnitName       string `json:"unit_name"`
	ArtifactName   string `json:"artifact_name"`
	ArtifactDigest string `json:"artifact_digest"`
	ArtifactDevice uint64 `json:"artifact_device"`
	ArtifactInode  uint64 `json:"artifact_inode"`
	PolicyRevision uint64 `json:"policy_revision"`
	PayloadHash    string `json:"payload_hash"`
}

func workerBrokerRequestFromStart(request systemdStartRequest) (workerBrokerStartRequest, error) {
	if request.Version != systemdStartProtocolVersion {
		return workerBrokerStartRequest{}, fmt.Errorf("unsupported source systemd start request version")
	}
	out := workerBrokerStartRequest{
		Version: workerBrokerProtocolVersion, CallerNodeID: request.CallerNodeID,
		RequestID: request.RequestID, RemoteJobID: request.RemoteJobID,
		LocalJobID: request.LocalJobID, StartAttemptID: request.StartAttemptID,
		UnitName: request.UnitName, ArtifactName: request.ArtifactName,
		ArtifactDigest: request.ArtifactDigest, ArtifactDevice: request.ArtifactDevice,
		ArtifactInode: request.ArtifactInode, PolicyRevision: request.PolicyRevision,
		PayloadHash: request.PayloadHash,
	}
	return out, out.validate()
}

func (r workerBrokerStartRequest) validate() error {
	if r.Version != workerBrokerProtocolVersion || !validID.MatchString(r.CallerNodeID) ||
		!validID.MatchString(r.RequestID) || !validID.MatchString(r.RemoteJobID) ||
		!validID.MatchString(r.LocalJobID) || len(r.StartAttemptID) != 32 ||
		!digestPattern.MatchString(r.ArtifactDigest) || !digestPattern.MatchString(r.PayloadHash) ||
		!opaqueRefPattern.MatchString(r.ArtifactName) || r.ArtifactDevice == 0 ||
		r.ArtifactInode == 0 || r.PolicyRevision == 0 ||
		r.StartAttemptID != strings.ToLower(r.StartAttemptID) {
		return fmt.Errorf("worker broker start request identity is invalid")
	}
	if _, err := hex.DecodeString(r.StartAttemptID); err != nil {
		return fmt.Errorf("worker broker start attempt ID is invalid")
	}
	unit, err := ExecutionUnitName(r.LocalJobID, r.RequestID)
	if err != nil || unit != r.UnitName {
		return fmt.Errorf("worker broker unit identity is invalid")
	}
	encoded, err := json.Marshal(r)
	if err != nil || len(encoded) > maxWorkerBrokerRequestBytes {
		return fmt.Errorf("worker broker request exceeds size limit")
	}
	return nil
}

func encodeWorkerBrokerStartRequest(request workerBrokerStartRequest) ([]byte, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}

func decodeWorkerBrokerStartRequest(data []byte) (workerBrokerStartRequest, error) {
	var request workerBrokerStartRequest
	if len(data) == 0 || len(data) > maxWorkerBrokerRequestBytes {
		return request, fmt.Errorf("worker broker request size is invalid")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return request, fmt.Errorf("decode worker broker request: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		return request, fmt.Errorf("decode worker broker request: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return request, fmt.Errorf("worker broker request has trailing data")
	}
	if err := request.validate(); err != nil {
		return request, err
	}
	return request, nil
}

func (r workerBrokerStartRequest) matchesStartRequest(request systemdStartRequest) bool {
	return request.Version == systemdStartProtocolVersion && r.Version == workerBrokerProtocolVersion && r.CallerNodeID == request.CallerNodeID &&
		r.RequestID == request.RequestID && r.RemoteJobID == request.RemoteJobID &&
		r.LocalJobID == request.LocalJobID && r.StartAttemptID == request.StartAttemptID &&
		r.UnitName == request.UnitName && r.ArtifactName == request.ArtifactName &&
		r.ArtifactDigest == request.ArtifactDigest && r.ArtifactDevice == request.ArtifactDevice &&
		r.ArtifactInode == request.ArtifactInode && r.PolicyRevision == request.PolicyRevision &&
		r.PayloadHash == request.PayloadHash
}
