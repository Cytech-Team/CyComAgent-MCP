package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

const requestProofDomain = "CyComAgent-RemoteJob-Request-v1"

// Proof purposes are cryptographically separated to prevent a reservation
// proof from being replayed as a status or reconciliation authorization.
func proofDomain(purpose string) (string, error) {
	switch purpose {
	case "reserve":
		return "CyComAgent-RemoteJob-Reserve-v1", nil
	case "status":
		return "CyComAgent-RemoteJob-Status-v1", nil
	case "reconcile":
		return "CyComAgent-RemoteJob-Reconcile-v1", nil
	case "dispatch":
		return "CyComAgent-RemoteJob-Dispatch-v1", nil
	default:
		return "", fmt.Errorf("unsupported remote job proof purpose %q", purpose)
	}
}

type requestProofTranscript struct {
	Domain       string `json:"domain"`
	CallerNodeID string `json:"caller_node_id"`
	ReceiverID   string `json:"receiver_node_id"`
	RequestID    string `json:"request_id"`
	PayloadHash  string `json:"payload_sha256"`
}

// RequestTranscript binds a stable idempotency key and exact payload bytes to
// both Federation identities. The receiver ID prevents cross-node replay; the
// request ID makes a retry refer to the same ledger reservation.
func RequestTranscript(callerNodeID, receiverNodeID, requestID string, payload []byte) ([]byte, error) {
	return RequestTranscriptForPurpose("reserve", callerNodeID, receiverNodeID, requestID, payload)
}

func RequestTranscriptForPurpose(purpose, callerNodeID, receiverNodeID, requestID string, payload []byte) ([]byte, error) {
	if !validID.MatchString(callerNodeID) || !validID.MatchString(receiverNodeID) || !validID.MatchString(requestID) {
		return nil, fmt.Errorf("caller, receiver, and request IDs must be 1-128 safe characters")
	}
	domain, err := proofDomain(purpose)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	return json.Marshal(requestProofTranscript{
		Domain: domain, CallerNodeID: callerNodeID,
		ReceiverID: receiverNodeID, RequestID: requestID,
		PayloadHash: hex.EncodeToString(sum[:]),
	})
}

// SignRequest signs the canonical transcript using the node's existing
// Ed25519 identity; private key material never leaves nodeidentity.Manager.
func SignRequest(identity *nodeidentity.Manager, receiverID, requestID string, payload []byte) ([]byte, error) {
	if identity == nil {
		return nil, fmt.Errorf("federation identity is unavailable")
	}
	transcript, err := RequestTranscript(identity.Public().NodeID, receiverID, requestID, payload)
	if err != nil {
		return nil, err
	}
	return identity.SignChallenge(transcript), nil
}

// VerifyRequest accepts only an active trusted caller and verifies the exact
// receiver, request ID, and payload hash before a ledger reservation is made.
func VerifyRequest(identity *nodeidentity.Manager, callerNodeID, requestID string, payload, signature []byte) error {
	if identity == nil {
		return fmt.Errorf("federation identity is unavailable")
	}
	transcript, err := RequestTranscript(callerNodeID, identity.Public().NodeID, requestID, payload)
	if err != nil {
		return err
	}
	return identity.VerifyTrustedChallenge(callerNodeID, transcript, signature)
}

// SignRequestForPurpose signs a purpose-separated transcript using the local identity.
func SignRequestForPurpose(identity *nodeidentity.Manager, purpose, receiverID, requestID string, payload []byte) ([]byte, error) {
	if identity == nil {
		return nil, fmt.Errorf("federation identity is unavailable")
	}
	transcript, err := RequestTranscriptForPurpose(purpose, identity.Public().NodeID, receiverID, requestID, payload)
	if err != nil {
		return nil, err
	}
	return identity.SignChallenge(transcript), nil
}

// VerifyRequestForPurpose verifies the proof only for its intended protocol action.
func VerifyRequestForPurpose(identity *nodeidentity.Manager, purpose, callerNodeID, requestID string, payload, signature []byte) error {
	if identity == nil {
		return fmt.Errorf("federation identity is unavailable")
	}
	transcript, err := RequestTranscriptForPurpose(purpose, callerNodeID, identity.Public().NodeID, requestID, payload)
	if err != nil {
		return err
	}
	return identity.VerifyTrustedChallenge(callerNodeID, transcript, signature)
}
