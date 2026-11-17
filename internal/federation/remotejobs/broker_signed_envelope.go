package remotejobs

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

const brokerSignedEnvelopeVersion = 1

type SignedBrokerStartEnvelope struct {
	Version      int                `json:"version"`
	CallerNodeID string             `json:"caller_node_id"`
	Request      BrokerStartRequest `json:"request"`
	Signature    string             `json:"signature"`
}

// SignBrokerStartEnvelope signs the exact canonical request JSON and binds it
// to the receiver, request ID, and the reserve purpose. It never exports keys.
func SignBrokerStartEnvelope(identity *nodeidentity.Manager, receiverNodeID string, req BrokerStartRequest) ([]byte, error) {
	if identity == nil {
		return nil, fmt.Errorf("Federation identity is required")
	}
	if err := ValidateBrokerStartRequest(req); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	signature, err := SignRequestForPurpose(identity, "reserve", receiverNodeID, req.RequestID, payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(SignedBrokerStartEnvelope{Version: brokerSignedEnvelopeVersion, CallerNodeID: identity.Public().NodeID, Request: req, Signature: base64.RawStdEncoding.EncodeToString(signature)})
}

// DecodeAndVerifySignedBrokerStartEnvelope verifies the active trust record and
// Ed25519 signature before creating a receiver-validated caller context.
func DecodeAndVerifySignedBrokerStartEnvelope(receiver *nodeidentity.Manager, raw []byte) (FederationCallerContext, BrokerStartRequest, error) {
	var zero BrokerStartRequest
	if receiver == nil {
		return FederationCallerContext{}, zero, fmt.Errorf("receiver Federation identity is required")
	}
	if len(raw) == 0 || len(raw) > maxBrokerRequestBytes {
		return FederationCallerContext{}, zero, fmt.Errorf("signed broker envelope size is invalid")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return FederationCallerContext{}, zero, fmt.Errorf("decode signed broker envelope: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var envelope SignedBrokerStartEnvelope
	if err := dec.Decode(&envelope); err != nil {
		return FederationCallerContext{}, zero, fmt.Errorf("decode signed broker envelope: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return FederationCallerContext{}, zero, fmt.Errorf("signed broker envelope has trailing data")
	}
	if envelope.Version != brokerSignedEnvelopeVersion {
		return FederationCallerContext{}, zero, fmt.Errorf("unsupported signed broker envelope version")
	}
	if !validID.MatchString(envelope.CallerNodeID) {
		return FederationCallerContext{}, zero, fmt.Errorf("signed broker caller ID is invalid")
	}
	if err := ValidateBrokerStartRequest(envelope.Request); err != nil {
		return FederationCallerContext{}, zero, err
	}
	signature, err := base64.RawStdEncoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != 64 {
		return FederationCallerContext{}, zero, fmt.Errorf("signed broker signature is invalid")
	}
	payload, err := json.Marshal(envelope.Request)
	if err != nil {
		return FederationCallerContext{}, zero, err
	}
	if err := VerifyRequestForPurpose(receiver, "reserve", envelope.CallerNodeID, envelope.Request.RequestID, payload, signature); err != nil {
		return FederationCallerContext{}, zero, fmt.Errorf("verify signed broker request: %w", err)
	}
	caller, err := newFederationCallerContext(receiver, envelope.CallerNodeID)
	if err != nil {
		return FederationCallerContext{}, zero, err
	}
	return caller, envelope.Request, nil
}
