package remotejobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	ExecutionBrokerProtocolVersion = 1
	maxBrokerRequestBytes          = 64 << 10
	maxBrokerArgCount              = 128
	maxBrokerArgBytes              = 32 << 10
	maxBrokerEnvCount              = 32
	maxBrokerEnvBytes              = 8 << 10
	maxBrokerInputRefBytes         = 256
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var opaqueRefPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,256}$`)

// BrokerStartRequest is the deliberately narrow, versioned contract between
// CyComAgent and a future privileged execution broker. It contains no shell,
// UID/GID, unit name, cgroup path, systemd property, or executable pathname.
// The broker must resolve every opaque reference against receiver-owned policy.
type BrokerStartRequest struct {
	Version          int               `json:"version"`
	JobID            string            `json:"job_id"`
	RequestID        string            `json:"request_id"`
	PolicyRevision   uint64            `json:"policy_revision"`
	ExecutableDigest string            `json:"executable_digest"`
	Argv             []string          `json:"argv"`
	WorkingDirRef    string            `json:"working_dir_ref"`
	Environment      map[string]string `json:"environment,omitempty"`
	TimeoutMillis    int64             `json:"timeout_ms"`
	MaxOutputBytes   int64             `json:"max_output_bytes"`
	InputRef         string            `json:"input_ref,omitempty"`
}

// ValidateBrokerStartRequest rejects ambiguous or caller-controlled service
// configuration before a request is sent to the privileged broker.
func ValidateBrokerStartRequest(req BrokerStartRequest) error {
	if req.Version != ExecutionBrokerProtocolVersion {
		return fmt.Errorf("unsupported execution broker protocol version")
	}
	if !validID.MatchString(req.JobID) || !validID.MatchString(req.RequestID) {
		return fmt.Errorf("valid immutable job and request IDs are required")
	}
	if req.PolicyRevision == 0 {
		return fmt.Errorf("policy revision is required")
	}
	if !digestPattern.MatchString(req.ExecutableDigest) {
		return fmt.Errorf("executable digest must be lowercase SHA-256")
	}
	if len(req.Argv) == 0 || len(req.Argv) > maxBrokerArgCount {
		return fmt.Errorf("argv count is outside broker limits")
	}
	argBytes := 0
	for _, arg := range req.Argv {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("argv contains NUL")
		}
		argBytes += len(arg)
	}
	if argBytes > maxBrokerArgBytes {
		return fmt.Errorf("argv exceeds broker byte limit")
	}
	if !opaqueRefPattern.MatchString(req.WorkingDirRef) {
		return fmt.Errorf("working directory reference is invalid")
	}
	if len(req.Environment) > maxBrokerEnvCount {
		return fmt.Errorf("environment count exceeds broker limit")
	}
	envBytes := 0
	keys := make([]string, 0, len(req.Environment))
	for key := range req.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := req.Environment[key]
		if !environmentKeyPattern.MatchString(key) || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("environment entry is invalid")
		}
		envBytes += len(key) + len(value)
	}
	if envBytes > maxBrokerEnvBytes {
		return fmt.Errorf("environment exceeds broker byte limit")
	}
	if req.TimeoutMillis <= 0 || req.TimeoutMillis > int64((24*time.Hour)/time.Millisecond) {
		return fmt.Errorf("timeout is outside broker limits")
	}
	if req.MaxOutputBytes <= 0 || req.MaxOutputBytes > 64<<20 {
		return fmt.Errorf("output limit is outside broker limits")
	}
	if req.InputRef != "" && !opaqueRefPattern.MatchString(req.InputRef) {
		return fmt.Errorf("input reference is invalid")
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode broker request: %w", err)
	}
	if len(encoded) > maxBrokerRequestBytes {
		return fmt.Errorf("broker request exceeds maximum size")
	}
	return nil
}

// DecodeBrokerStartRequest enforces a bounded, strict JSON envelope. Unknown
// fields are rejected so future privilege-bearing options cannot be smuggled
// through an older broker implementation.
func DecodeBrokerStartRequest(data []byte) (BrokerStartRequest, error) {
	var req BrokerStartRequest
	if len(data) == 0 || len(data) > maxBrokerRequestBytes {
		return req, fmt.Errorf("broker request size is invalid")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return req, fmt.Errorf("decode execution broker request: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("decode execution broker request: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return req, fmt.Errorf("broker request has trailing data")
	}
	if err := ValidateBrokerStartRequest(req); err != nil {
		return req, err
	}
	return req, nil
}

// ExecutionUnitName is broker-derived and stable across retries. Callers never
// supply systemd unit names; the digest avoids leaking raw request identifiers.
func ExecutionUnitName(jobID, requestID string) (string, error) {
	if !validID.MatchString(jobID) || !validID.MatchString(requestID) {
		return "", fmt.Errorf("valid immutable job and request IDs are required")
	}
	sum := sha256.Sum256([]byte(jobID + "\x00" + requestID))
	return "cycomagent-job-" + hex.EncodeToString(sum[:16]) + ".service", nil
}

// rejectDuplicateJSONKeys prevents parser differentials between components
// that might otherwise select different values for repeated security fields.
func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := consumeJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON token")
		}
		return err
	}
	return nil
}

func consumeJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("malformed JSON object")
		}
	case '[':
		for dec.More() {
			if err := consumeJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("malformed JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}
