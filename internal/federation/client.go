package federation

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation/remotejobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/targets"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	Annotations map[string]any `json:"annotations,omitempty"`
}
type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
type Client struct {
	targets  *targets.Manager
	binary   string
	identity *nodeidentity.Manager
}

func New(tm *targets.Manager, identity *nodeidentity.Manager) *Client {
	return &Client{targets: tm, binary: "/usr/local/bin/cycomagent", identity: identity}
}
func (c *Client) Tools(ctx context.Context, target string) ([]Tool, error) {
	lines := []any{map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cycom-federation", "version": "1"}}}, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}}
	out, err := c.exchange(ctx, target, lines)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(out[2], &payload); err != nil {
		return nil, fmt.Errorf("decode remote tool catalog: %w", err)
	}
	return payload.Tools, nil
}
func (c *Client) CallReadOnly(ctx context.Context, target, name string, args map[string]any) (any, error) {
	if err := c.authenticate(ctx, target); err != nil {
		return nil, err
	}
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return nil, fmt.Errorf("invalid remote tool name")
	}
	catalog, err := c.Tools(ctx, target)
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, t := range catalog {
		if t.Name == name {
			allowed = t.Annotations["readOnlyHint"] == true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("remote tool %q is not explicitly marked read-only", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	if _, ok := args["reason"]; !ok {
		args["reason"] = "Approved read-only cross-device inspection"
	}
	lines := []any{map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cycom-federation", "version": "1"}}}, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}}
	out, err := c.exchange(ctx, target, lines)
	if err != nil {
		return nil, err
	}
	var result any
	if err := json.Unmarshal(out[2], &result); err != nil {
		return nil, fmt.Errorf("decode remote tool result: %w", err)
	}
	return result, nil
}

// SubmitRemoteJobReservation discovers the receiver's claimed identity, signs
// the exact request transcript for that receiver, then invokes only the
// dedicated reserve endpoint. The receiver independently checks local trust.
func (c *Client) SubmitRemoteJobReservation(ctx context.Context, target, requestID, payload string) (any, error) {
	receiver, err := c.discoverIdentity(ctx, target)
	if err != nil {
		return nil, err
	}
	signature, err := remotejobs.SignRequestForPurpose(c.identity, "reserve", receiver.NodeID, requestID, []byte(payload))
	if err != nil {
		return nil, err
	}
	return c.CallRemoteJobProtocol(ctx, target, "federation_remote_job_reserve", c.identity.Public().NodeID, requestID, payload, signature)
}

// RemoteJobStatus signs the same immutable request transcript and asks the
// receiver for the caller-scoped record. It cannot query another caller's key.
func (c *Client) RemoteJobStatus(ctx context.Context, target, requestID, payload string) (any, error) {
	receiver, err := c.discoverIdentity(ctx, target)
	if err != nil {
		return nil, err
	}
	signature, err := remotejobs.SignRequestForPurpose(c.identity, "status", receiver.NodeID, requestID, []byte(payload))
	if err != nil {
		return nil, err
	}
	return c.CallRemoteJobProtocol(ctx, target, "federation_remote_job_status", c.identity.Public().NodeID, requestID, payload, signature)
}

func (c *Client) discoverIdentity(ctx context.Context, target string) (nodeidentity.Public, error) {
	out, err := c.exchange(ctx, target, []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cycom-federation-remote-job", "version": "1"}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "federation_identity", "arguments": map[string]any{"reason": "Discover the receiver identity to bind the signed Remote Job request to its exact destination node."}}},
	})
	if err != nil {
		return nodeidentity.Public{}, fmt.Errorf("remote receiver identity discovery failed: %w", err)
	}
	content, err := structuredToolContent(out[2])
	if err != nil {
		return nodeidentity.Public{}, err
	}
	var identity nodeidentity.Public
	if err := json.Unmarshal(content["node_id"], &identity.NodeID); err != nil {
		return identity, fmt.Errorf("receiver node ID missing")
	}
	if err := json.Unmarshal(content["algorithm"], &identity.Algorithm); err != nil {
		return identity, fmt.Errorf("receiver identity algorithm missing")
	}
	if err := json.Unmarshal(content["public_key"], &identity.PublicKey); err != nil {
		return identity, fmt.Errorf("receiver public key missing")
	}
	if err := json.Unmarshal(content["fingerprint"], &identity.Fingerprint); err != nil {
		return identity, fmt.Errorf("receiver fingerprint missing")
	}
	if err := nodeidentity.ValidatePublic(identity); err != nil {
		return identity, fmt.Errorf("receiver identity invalid: %w", err)
	}
	if c.identity == nil || !c.identity.IsTrusted(identity.NodeID) {
		return identity, fmt.Errorf("receiver %q is not trusted locally", identity.NodeID)
	}
	return identity, nil
}

// RemoteJobReconcile requests read-only evidence from the receiver. It signs
// the same immutable request transcript and cannot mutate receiver state.
func (c *Client) RemoteJobReconcile(ctx context.Context, target, requestID, payload string) (any, error) {
	receiver, err := c.discoverIdentity(ctx, target)
	if err != nil {
		return nil, err
	}
	signature, err := remotejobs.SignRequestForPurpose(c.identity, "reconcile", receiver.NodeID, requestID, []byte(payload))
	if err != nil {
		return nil, err
	}
	return c.CallRemoteJobProtocol(ctx, target, "federation_remote_job_reconcile", c.identity.Public().NodeID, requestID, payload, signature)
}

// CallRemoteJobProtocol is a narrow exception to CallReadOnly for the signed
// Remote Job protocol endpoints. It never accepts arbitrary tool names and
// still performs mutual Federation authentication and catalog checks.
func (c *Client) CallRemoteJobProtocol(ctx context.Context, target, name, callerNodeID, requestID, payload string, signature []byte) (any, error) {
	if _, err := remoteJobCatalogReadOnly(name); err != nil {
		return nil, err
	}
	if len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid Ed25519 request proof length")
	}
	if err := c.authenticate(ctx, target); err != nil {
		return nil, err
	}
	catalog, err := c.Tools(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := validateRemoteJobCatalog(catalog, name); err != nil {
		return nil, err
	}
	args := map[string]any{
		"caller_node_id": callerNodeID,
		"request_id":     requestID,
		"payload":        payload,
		"signature":      base64.RawStdEncoding.EncodeToString(signature),
		"reason":         "Authenticated Federation Remote Job protocol request; no process execution is requested",
	}
	lines := []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cycom-federation-remote-job", "version": "1"}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}},
	}
	out, err := c.exchange(ctx, target, lines)
	if err != nil {
		return nil, fmt.Errorf("remote job protocol outcome may be unknown; query the same request ID before retrying: %w", err)
	}
	content, err := structuredToolContent(out[2])
	if err != nil {
		return nil, fmt.Errorf("remote job protocol call failed: %w", err)
	}
	result := make(map[string]any, len(content))
	for key, raw := range content {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("decode remote job protocol field %q: %w", key, err)
		}
		result[key] = value
	}
	return result, nil
}

func remoteJobCatalogReadOnly(name string) (bool, error) {
	switch name {
	case "federation_remote_job_reserve":
		return false, nil
	case "federation_remote_job_status", "federation_remote_job_reconcile":
		return true, nil
	default:
		return false, fmt.Errorf("remote job protocol tool %q is not allowed", name)
	}
}

func validateRemoteJobCatalog(catalog []Tool, name string) error {
	expected, err := remoteJobCatalogReadOnly(name)
	if err != nil {
		return err
	}
	for _, tool := range catalog {
		if tool.Name == name {
			if tool.Annotations["readOnlyHint"] != expected {
				return fmt.Errorf("remote tool %q is missing or has unexpected readOnlyHint", name)
			}
			return nil
		}
	}
	return fmt.Errorf("remote tool %q is missing or has unexpected readOnlyHint", name)
}

func (c *Client) authenticate(ctx context.Context, target string) error {
	if c.identity == nil {
		return fmt.Errorf("federation identity manager is unavailable; refusing unauthenticated remote invocation")
	}
	var nonceA [32]byte
	if _, err := rand.Read(nonceA[:]); err != nil {
		return fmt.Errorf("generate federation challenge: %w", err)
	}
	encodedA := base64.RawStdEncoding.EncodeToString(nonceA[:])
	local := c.identity.Public()
	requestTranscript := []byte("mutual-auth-request-v1|" + local.NodeID + "|REMOTE|" + encodedA)
	// The target node ID is not assumed from its SSH alias. The server binds its
	// own ID into the signature; the request transcript is rebound after discovery.
	// To avoid trusting an alias as identity, obtain the target's public identity
	// first, then sign the exact caller->receiver transcript.
	probe, err := c.exchange(ctx, target, []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cycom-federation-auth", "version": "1"}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "federation_identity", "arguments": map[string]any{"reason": "Discover and verify the paired peer public identity before mutual authentication."}}},
	})
	if err != nil {
		return fmt.Errorf("remote identity discovery failed: %w", err)
	}
	identityContent, err := structuredToolContent(probe[2])
	if err != nil {
		return fmt.Errorf("decode remote identity: %w", err)
	}
	var remote nodeidentity.Public
	if err := json.Unmarshal(identityContent["node_id"], &remote.NodeID); err != nil {
		return fmt.Errorf("remote node ID missing")
	}
	if err := json.Unmarshal(identityContent["algorithm"], &remote.Algorithm); err != nil {
		return fmt.Errorf("remote identity algorithm missing")
	}
	if err := json.Unmarshal(identityContent["public_key"], &remote.PublicKey); err != nil {
		return fmt.Errorf("remote public key missing")
	}
	if err := json.Unmarshal(identityContent["fingerprint"], &remote.Fingerprint); err != nil {
		return fmt.Errorf("remote fingerprint missing")
	}
	if err := nodeidentity.ValidatePublic(remote); err != nil {
		return fmt.Errorf("remote identity invalid: %w", err)
	}
	if !c.identity.IsTrusted(remote.NodeID) {
		return fmt.Errorf("remote node %q is not trusted on this node; verify and import its fingerprint first", remote.NodeID)
	}
	requestTranscript = []byte("mutual-auth-request-v1|" + local.NodeID + "|" + remote.NodeID + "|" + encodedA)
	signatureA := base64.RawStdEncoding.EncodeToString(c.identity.SignChallenge(requestTranscript))
	out, err := c.exchange(ctx, target, []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cycom-federation-auth", "version": "1"}}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "federation_authenticate_peer", "arguments": map[string]any{"caller_identity": local, "nonce_a": encodedA, "signature_a": signatureA, "reason": "Authenticate the already trusted federation peer using a fresh Ed25519 challenge."}}},
	})
	if err != nil {
		return fmt.Errorf("mutual federation authentication failed: %w", err)
	}
	authContent, err := structuredToolContent(out[2])
	if err != nil {
		return fmt.Errorf("decode mutual authentication response: %w", err)
	}
	var response struct {
		Identity   nodeidentity.Public `json:"identity"`
		NonceB     string              `json:"nonce_b"`
		SignatureB string              `json:"signature_b"`
		Algorithm  string              `json:"algorithm"`
	}
	for key, dst := range map[string]any{"identity": &response.Identity, "nonce_b": &response.NonceB, "signature_b": &response.SignatureB, "algorithm": &response.Algorithm} {
		if err := json.Unmarshal(authContent[key], dst); err != nil {
			return fmt.Errorf("mutual authentication response missing %s", key)
		}
	}
	if response.Algorithm != "Ed25519" {
		return fmt.Errorf("unsupported mutual authentication algorithm")
	}
	if err := nodeidentity.ValidatePublic(response.Identity); err != nil {
		return fmt.Errorf("response identity invalid: %w", err)
	}
	if response.Identity.NodeID != remote.NodeID || response.Identity.PublicKey != remote.PublicKey {
		return fmt.Errorf("remote identity changed during mutual authentication")
	}
	nonceB, err := base64.RawStdEncoding.DecodeString(response.NonceB)
	if err != nil || len(nonceB) != 32 {
		return fmt.Errorf("remote nonce_b is malformed")
	}
	signatureB, err := base64.RawStdEncoding.DecodeString(response.SignatureB)
	if err != nil || len(signatureB) != ed25519.SignatureSize {
		return fmt.Errorf("remote signature_b is malformed")
	}
	responseTranscript := []byte("mutual-auth-response-v1|" + local.NodeID + "|" + remote.NodeID + "|" + encodedA + "|" + response.NonceB)
	if err := c.identity.VerifyTrustedChallenge(remote.NodeID, responseTranscript, signatureB); err != nil {
		return err
	}
	return nil
}

// structuredToolContent unwraps the MCP server's object wrapper for scalar and
// struct tool results. The server places non-map results under structuredContent.items.
func structuredToolContent(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var envelope struct {
		IsError           bool                       `json:"isError"`
		StructuredContent map[string]json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.IsError {
		if len(envelope.Content) > 0 && envelope.Content[0].Text != "" {
			return nil, fmt.Errorf("remote tool error: %s", envelope.Content[0].Text)
		}
		return nil, fmt.Errorf("remote tool returned an error")
	}
	if nested, ok := envelope.StructuredContent["items"]; ok {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(nested, &object); err == nil {
			return object, nil
		}
	}
	return envelope.StructuredContent, nil
}

func (c *Client) exchange(ctx context.Context, target string, requests []any) (map[int]json.RawMessage, error) {
	var input bytes.Buffer
	enc := json.NewEncoder(&input)
	for _, r := range requests {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	command := "printf '%s\\n'"
	for _, line := range strings.Split(strings.TrimSpace(input.String()), "\n") {
		command += " " + shellQuote(line)
	}
	command += " | " + c.binary + " --mode stdio"
	res, err := c.targets.Exec(ctx, target, targets.ExecOptions{Command: command, TimeoutSeconds: 30, MaxOutputBytes: 2 << 20})
	if err != nil {
		return nil, fmt.Errorf("remote SSH transport: %w", err)
	}
	if res.TimedOut {
		return nil, fmt.Errorf("remote MCP request timed out")
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("remote CyComAgent exited %d: %s", res.ExitCode, trim(res.Stderr))
	}
	found := map[int]json.RawMessage{}
	scan := bufio.NewScanner(strings.NewReader(res.Stdout))
	scan.Buffer(make([]byte, 4096), 4<<20)
	for scan.Scan() {
		var r rpcResponse
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
			continue
		}
		if r.Error != nil {
			return nil, fmt.Errorf("remote MCP error %d: %s", r.Error.Code, r.Error.Message)
		}
		if len(r.Result) > 0 {
			found[r.ID] = append(json.RawMessage(nil), r.Result...)
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	for _, id := range []int{1, 2} {
		if len(found[id]) == 0 {
			return nil, fmt.Errorf("remote MCP response %d missing", id)
		}
	}
	return found, nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func trim(s string) string {
	if len(s) > 512 {
		s = s[:512] + "…"
	}
	return strings.TrimSpace(s)
}
