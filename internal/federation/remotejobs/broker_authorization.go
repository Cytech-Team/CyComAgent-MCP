package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

var ErrBrokerReplay = errors.New("execution broker request has already been reserved")

// BrokerAuthorization is the durable result of receiver-side authorization.
// It is evidence only: creating it never prepares dispatch or launches work.
type BrokerAuthorization struct {
	Record           Record
	PolicyRevision   uint64
	ExecutablePath   string
	WorkingDir       string
	Executable       *os.File
	WorkingDirectory *os.File
}

// AuthorizeAndReserve applies the receiver's current policy and atomically
// consumes the caller/request ID in the durable ledger. callerTrusted must be
// derived by the broker from receiver-owned trust state, never from request JSON.
// resolved must come from receiver-owned opaque-reference resolution.
func AuthorizeAndReserve(store *PolicyStore, ledger *Ledger, caller FederationCallerContext, req BrokerStartRequest, resolved ProcessRequest) (BrokerAuthorization, error) {
	var result BrokerAuthorization
	if store == nil || ledger == nil {
		return result, fmt.Errorf("receiver policy store and replay ledger are required")
	}
	if err := ValidateBrokerStartRequest(req); err != nil {
		return result, err
	}
	if !caller.valid() {
		return result, fmt.Errorf("authenticated Federation caller context is invalid")
	}
	callerNodeID := caller.nodeID
	policy, revision, err := store.LoadVersioned()
	if err != nil {
		return result, fmt.Errorf("load receiver execution policy: %w", err)
	}
	if revision == 0 || req.PolicyRevision != revision {
		return result, fmt.Errorf("broker request policy revision is stale")
	}
	if resolved.Executable == "" || resolved.WorkingDir == "" {
		return result, fmt.Errorf("receiver did not resolve executable and working directory references")
	}
	if req.TimeoutMillis != resolved.Timeout.Milliseconds() {
		return result, fmt.Errorf("resolved timeout differs from authenticated request")
	}
	if len(req.Argv) != len(resolved.Args)+1 || req.Argv[0] != resolved.Executable {
		return result, fmt.Errorf("resolved argv differs from authenticated request")
	}
	for i := range resolved.Args {
		if req.Argv[i+1] != resolved.Args[i] {
			return result, fmt.Errorf("resolved argv differs from authenticated request")
		}
	}
	if len(req.Environment) != len(resolved.Environment) {
		return result, fmt.Errorf("resolved environment differs from authenticated request")
	}
	for k, v := range req.Environment {
		if resolved.Environment[k] != v {
			return result, fmt.Errorf("resolved environment differs from authenticated request")
		}
	}
	exe, cwd, err := policy.validateAndResolve(callerNodeID, true, resolved)
	if err != nil {
		return result, err
	}
	f, err := os.Open(exe)
	if err != nil {
		return result, fmt.Errorf("open allowlisted executable: %w", err)
	}
	h := sha256.New()
	openedInfo, statErr := f.Stat()
	pathInfo, pathErr := os.Stat(exe)
	if statErr == nil && pathErr == nil && !os.SameFile(openedInfo, pathInfo) {
		statErr = fmt.Errorf("executable path changed while opening")
	}
	if statErr != nil {
		_ = f.Close()
		return result, statErr
	}
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if copyErr != nil {
		return result, copyErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	if hex.EncodeToString(h.Sum(nil)) != req.ExecutableDigest {
		return result, fmt.Errorf("executable digest does not match receiver-resolved executable")
	}
	if req.MaxOutputBytes > policy.MaxOutputBytes {
		return result, fmt.Errorf("requested output cap exceeds receiver policy")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return result, err
	}
	rec, created, err := ledger.Reserve(callerNodeID, req.RequestID, string(payload))
	if err != nil {
		return result, err
	}
	if !created {
		return result, ErrBrokerReplay
	}
	// Recheck after durable reservation. A concurrent policy update consumes
	// this request ID but cannot authorize execution under a stale revision.
	_, currentRevision, err := store.LoadVersioned()
	if err != nil {
		return result, fmt.Errorf("recheck receiver policy revision: %w", err)
	}
	if currentRevision != revision {
		return result, fmt.Errorf("receiver policy changed during authorization")
	}
	bound, err := ledger.BindExecutionMetadata(callerNodeID, req.RequestID, rec.JobID, revision)
	if err != nil {
		return result, fmt.Errorf("persist broker authorization binding: %w", err)
	}
	if bound.Status != "accepted" || bound.PolicyRevision != revision || bound.LocalJobID != rec.JobID {
		return result, fmt.Errorf("durable broker authorization binding is inconsistent")
	}
	return BrokerAuthorization{Record: bound, PolicyRevision: revision, ExecutablePath: exe, WorkingDir: cwd}, nil
}

// BrokerAuthorizationTimeout converts a validated request timeout without
// permitting overflow; kept separate for callers that need a duration value.
func BrokerAuthorizationTimeout(req BrokerStartRequest) (time.Duration, error) {
	if err := ValidateBrokerStartRequest(req); err != nil {
		return 0, err
	}
	return time.Duration(req.TimeoutMillis) * time.Millisecond, nil
}

// BrokerResolvedDescriptors owns the exact executable and working-directory
// objects selected by a receiver-owned opaque-reference resolver. The caller
// transfers ownership to AuthorizeAndReserveDescriptors on invocation.
type BrokerResolvedDescriptors struct {
	Request    ProcessRequest
	Executable *os.File
	WorkingDir *os.File
}

// AuthorizeAndReserveDescriptors validates policy against the resolved names,
// then proves those names still identify the already-open descriptors. The
// executable digest is computed from the pinned descriptor, never by reopening
// its pathname. On success the returned authorization owns both descriptors;
// on every error this function closes them.
func AuthorizeAndReserveDescriptors(store *PolicyStore, ledger *Ledger, caller FederationCallerContext, req BrokerStartRequest, resolved BrokerResolvedDescriptors) (result BrokerAuthorization, err error) {
	if resolved.Executable != nil {
		defer func() {
			if err != nil {
				_ = resolved.Executable.Close()
			}
		}()
	}
	if resolved.WorkingDir != nil {
		defer func() {
			if err != nil {
				_ = resolved.WorkingDir.Close()
			}
		}()
	}
	if resolved.Executable == nil || resolved.WorkingDir == nil {
		return result, fmt.Errorf("pinned executable and working-directory descriptors are required")
	}
	if store == nil || ledger == nil || !caller.valid() {
		return result, fmt.Errorf("receiver policy, ledger and authenticated caller are required")
	}
	if err = ValidateBrokerStartRequest(req); err != nil {
		return result, err
	}
	if req.TimeoutMillis != resolved.Request.Timeout.Milliseconds() {
		return result, fmt.Errorf("resolved timeout differs from authenticated request")
	}
	if len(req.Argv) != len(resolved.Request.Args)+1 || req.Argv[0] != resolved.Request.Executable {
		return result, fmt.Errorf("resolved argv differs from authenticated request")
	}
	for i := range resolved.Request.Args {
		if req.Argv[i+1] != resolved.Request.Args[i] {
			return result, fmt.Errorf("resolved argv differs from authenticated request")
		}
	}
	if len(req.Environment) != len(resolved.Request.Environment) {
		return result, fmt.Errorf("resolved environment differs from authenticated request")
	}
	for k, v := range req.Environment {
		if resolved.Request.Environment[k] != v {
			return result, fmt.Errorf("resolved environment differs from authenticated request")
		}
	}
	policy, revision, err := store.LoadVersioned()
	if err != nil {
		return result, fmt.Errorf("load receiver execution policy: %w", err)
	}
	if revision == 0 || req.PolicyRevision != revision {
		return result, fmt.Errorf("broker request policy revision is stale")
	}
	exePath, cwdPath, err := policy.validateAndResolve(caller.NodeID(), true, resolved.Request)
	if err != nil {
		return result, err
	}
	exeInfo, err := resolved.Executable.Stat()
	if err != nil || !exeInfo.Mode().IsRegular() || exeInfo.Mode()&0111 == 0 {
		return result, fmt.Errorf("pinned executable descriptor is not a regular executable")
	}
	pathInfo, err := os.Stat(exePath)
	if err != nil || !os.SameFile(exeInfo, pathInfo) {
		return result, fmt.Errorf("allowlisted executable does not identify the pinned descriptor")
	}
	cwdInfo, err := resolved.WorkingDir.Stat()
	if err != nil || !cwdInfo.IsDir() {
		return result, fmt.Errorf("pinned working-directory descriptor is not a directory")
	}
	cwdPathInfo, err := os.Stat(cwdPath)
	if err != nil || !os.SameFile(cwdInfo, cwdPathInfo) {
		return result, fmt.Errorf("approved working directory does not identify the pinned descriptor")
	}
	h := sha256.New()
	if _, err = io.Copy(h, resolved.Executable); err != nil {
		return result, fmt.Errorf("hash pinned executable: %w", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != req.ExecutableDigest {
		return result, fmt.Errorf("executable digest does not match pinned descriptor")
	}
	// Digest verification consumes the shared file offset. Restore it before
	// transferring ownership so a future descriptor-based exec/copy consumer
	// receives the complete executable from byte zero.
	if _, err = resolved.Executable.Seek(0, io.SeekStart); err != nil {
		return result, fmt.Errorf("rewind pinned executable after digest verification: %w", err)
	}
	if req.MaxOutputBytes > policy.MaxOutputBytes {
		return result, fmt.Errorf("requested output cap exceeds receiver policy")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return result, err
	}
	rec, created, err := ledger.Reserve(caller.NodeID(), req.RequestID, string(payload))
	if err != nil {
		return result, err
	}
	if !created {
		return result, ErrBrokerReplay
	}
	_, currentRevision, err := store.LoadVersioned()
	if err != nil {
		return result, fmt.Errorf("recheck receiver policy revision: %w", err)
	}
	if currentRevision != revision {
		return result, fmt.Errorf("receiver policy changed during descriptor authorization")
	}
	bound, err := ledger.BindExecutionMetadata(caller.NodeID(), req.RequestID, rec.JobID, revision)
	if err != nil {
		return result, fmt.Errorf("persist broker authorization binding: %w", err)
	}
	if bound.Status != "accepted" || bound.PolicyRevision != revision || bound.LocalJobID != rec.JobID {
		return result, fmt.Errorf("durable broker authorization binding is inconsistent")
	}
	result = BrokerAuthorization{Record: bound, PolicyRevision: revision, ExecutablePath: exePath, WorkingDir: cwdPath, Executable: resolved.Executable, WorkingDirectory: resolved.WorkingDir}
	return result, nil
}

// Close releases pinned descriptors retained by descriptor-bound authorization.
func (a *BrokerAuthorization) Close() error {
	if a == nil {
		return nil
	}
	var first error
	if a.Executable != nil {
		if err := a.Executable.Close(); err != nil {
			first = err
		}
		a.Executable = nil
	}
	if a.WorkingDirectory != nil {
		if err := a.WorkingDirectory.Close(); err != nil && first == nil {
			first = err
		}
		a.WorkingDirectory = nil
	}
	return first
}
