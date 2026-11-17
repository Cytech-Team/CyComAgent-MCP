package remotejobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DispatchRequest is the signed, immutable dispatch envelope. PolicyVersion
// binds authorization to the exact receiver-local policy revision. Process has
// no shell command string and is not executed by this contract type.
type DispatchRequest struct {
	Version       int            `json:"version"`
	PolicyVersion uint64         `json:"policy_version"`
	Process       ProcessRequest `json:"process"`
}

func EncodeDispatchRequest(req DispatchRequest) ([]byte, error) {
	if req.Version != 1 {
		return nil, fmt.Errorf("unsupported dispatch request version %d", req.Version)
	}
	if req.PolicyVersion == 0 {
		return nil, fmt.Errorf("policy version is required")
	}
	return json.Marshal(req)
}

func DecodeDispatchRequest(data []byte) (DispatchRequest, error) {
	var req DispatchRequest
	if len(data) == 0 || len(data) > 64<<10 {
		return req, fmt.Errorf("dispatch payload size is invalid")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("decode dispatch request: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return req, fmt.Errorf("dispatch payload has trailing data")
	}
	if req.Version != 1 || req.PolicyVersion == 0 {
		return req, fmt.Errorf("dispatch version and policy version are required")
	}
	return req, nil
}
