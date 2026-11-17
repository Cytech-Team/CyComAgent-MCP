package tools

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/fleet"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/targets"
)

func registerFleet(r *registry.Registry, fm *fleet.Manager, tm *targets.Manager, fc *federation.Client, identity *nodeidentity.Manager) {
	must(r.Add(registry.Tool{Name: "federation_identity", Description: "Show this node’s stable Ed25519 federation identity. The private key never leaves this node; share the public key and fingerprint through a separately authenticated channel to establish trust.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) { return identity.Public(), nil }}))
	must(r.Add(registry.Tool{Name: "federation_sign_challenge", Description: "Sign a fresh 32-byte federation authentication challenge with this node's Ed25519 private key. The private key is never exposed.", InputSchema: registry.ObjectSchema(map[string]any{"nonce": registry.String("base64 raw-standard encoding of a fresh 32-byte random nonce")}, []string{"nonce"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Nonce string `json:"nonce"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		nonce, err := base64.RawStdEncoding.DecodeString(in.Nonce)
		if err != nil || len(nonce) != 32 {
			return nil, fmt.Errorf("nonce must be a valid 32-byte base64 raw-standard value")
		}
		return map[string]any{"identity": identity.Public(), "signature": base64.RawStdEncoding.EncodeToString(identity.SignChallenge(nonce)), "algorithm": "Ed25519"}, nil
	}}))

	must(r.Add(registry.Tool{Name: "federation_authenticate_peer", Description: "Mutually authenticate this caller using its trusted Ed25519 identity and a fresh challenge. The caller must already be trusted by this node; returns a signed response bound to both node IDs and both nonces.", InputSchema: registry.ObjectSchema(map[string]any{"caller_identity": registry.ObjectSchema(map[string]any{"node_id": registry.String("caller node ID"), "algorithm": registry.String("Ed25519"), "public_key": registry.String("base64 raw-standard public key"), "fingerprint": registry.String("SHA256 fingerprint")}, []string{"node_id", "algorithm", "public_key", "fingerprint"}), "nonce_a": registry.String("fresh 32-byte nonce, base64 raw-standard"), "signature_a": registry.String("caller signature over the mutual-auth request transcript, base64 raw-standard")}, []string{"caller_identity", "nonce_a", "signature_a"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Caller     nodeidentity.Public `json:"caller_identity"`
			NonceA     string              `json:"nonce_a"`
			SignatureA string              `json:"signature_a"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := nodeidentity.ValidatePublic(in.Caller); err != nil {
			return nil, fmt.Errorf("caller identity invalid: %w", err)
		}
		nonceA, err := base64.RawStdEncoding.DecodeString(in.NonceA)
		if err != nil || len(nonceA) != 32 {
			return nil, fmt.Errorf("nonce_a must be a 32-byte base64 raw-standard value")
		}
		sigA, err := base64.RawStdEncoding.DecodeString(in.SignatureA)
		if err != nil || len(sigA) != ed25519.SignatureSize {
			return nil, fmt.Errorf("signature_a is malformed")
		}
		local := identity.Public()
		requestTranscript := []byte("mutual-auth-request-v1|" + in.Caller.NodeID + "|" + local.NodeID + "|" + in.NonceA)
		if err := identity.VerifyTrustedChallenge(in.Caller.NodeID, requestTranscript, sigA); err != nil {
			return nil, fmt.Errorf("caller authentication rejected: %w", err)
		}
		var nonceB [32]byte
		if _, err := rand.Read(nonceB[:]); err != nil {
			return nil, err
		}
		encodedB := base64.RawStdEncoding.EncodeToString(nonceB[:])
		responseTranscript := []byte("mutual-auth-response-v1|" + in.Caller.NodeID + "|" + local.NodeID + "|" + in.NonceA + "|" + encodedB)
		return map[string]any{"identity": local, "nonce_b": encodedB, "signature_b": base64.RawStdEncoding.EncodeToString(identity.SignChallenge(responseTranscript)), "algorithm": "Ed25519"}, nil
	}}))

	// Pairing bundle is public-only and is intended to be exchanged out of band.
	must(r.Add(registry.Tool{Name: "federation_pairing_bundle", Description: "Export this node's public-only federation pairing bundle. Exchange it through a separately authenticated channel and compare the full fingerprint before importing it on the other node.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) {
		return map[string]any{"protocol": "CyComAgent-Federation-Pairing-v1", "identity": identity.Public(), "private_key_included": false, "instructions": []string{"Exchange this bundle with the other node through a separately authenticated channel.", "Compare the complete SHA256 fingerprint on both sides.", "Import the peer on each node independently using federation_pairing_import.", "Run a mutual-authenticated read-only remote call only after both sides have imported each other."}}, nil
	}}))
	must(r.Add(registry.Tool{Name: "federation_pairing_import", Description: "Import one peer's public-only pairing bundle after independently comparing its full fingerprint. Requires explicit confirm=true and affects only this node's local trust store; repeat on the other node for bilateral trust.", InputSchema: registry.ObjectSchema(map[string]any{"protocol": registry.String("must be CyComAgent-Federation-Pairing-v1"), "identity": registry.ObjectSchema(map[string]any{"node_id": registry.String("peer node ID"), "algorithm": registry.String("Ed25519"), "public_key": registry.String("base64 raw-standard public key"), "fingerprint": registry.String("SHA256 fingerprint")}, []string{"node_id", "algorithm", "public_key", "fingerprint"}), "confirm_fingerprint": registry.String("exact full fingerprint independently verified out of band"), "confirm": registry.Boolean("must be true to approve this local trust change")}, []string{"protocol", "identity", "confirm_fingerprint", "confirm"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Protocol           string              `json:"protocol"`
			Identity           nodeidentity.Public `json:"identity"`
			ConfirmFingerprint string              `json:"confirm_fingerprint"`
			Confirm            bool                `json:"confirm"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if in.Protocol != "CyComAgent-Federation-Pairing-v1" {
			return nil, fmt.Errorf("unsupported pairing protocol")
		}
		if !in.Confirm {
			return nil, fmt.Errorf("pairing import requires confirm=true")
		}
		peer, err := identity.Trust(in.Identity, in.ConfirmFingerprint)
		if err != nil {
			return nil, err
		}
		return map[string]any{"paired_locally": true, "mutual_pairing_complete": false, "trusted_peer": peer, "next_step": "Import this node's federation_pairing_bundle on the peer node, then run a mutual-authenticated read-only call"}, nil
	}}))
	must(r.Add(registry.Tool{Name: "federation_pairing_status", Description: "Show this node's local pairing state for a peer. This does not contact the peer; mutual=true means both identities are present in this node's local trust store and must not be interpreted as remote confirmation.", InputSchema: registry.ObjectSchema(map[string]any{"node_id": registry.String("peer node ID")}, []string{"node_id"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			NodeID string `json:"node_id"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.NodeID) == "" {
			return nil, fmt.Errorf("node_id is required")
		}
		peers, err := identity.ListTrusted(true)
		if err != nil {
			return nil, err
		}
		for _, peer := range peers {
			if peer.NodeID == in.NodeID {
				active := peer.RevokedAt.IsZero()
				return map[string]any{"node_id": in.NodeID, "trusted_locally": active, "revoked_locally": !active, "mutual_pairing_confirmed": false, "note": "Remote trust state is not observable from this local record"}, nil
			}
		}
		return map[string]any{"node_id": in.NodeID, "trusted_locally": false, "revoked_locally": false, "mutual_pairing_confirmed": false}, nil
	}}))

	must(r.Add(registry.Tool{Name: "federation_trust_list", Description: "List locally trusted federation public identities. Revoked peers are excluded unless requested.", InputSchema: registry.ObjectSchema(map[string]any{"include_revoked": registry.Boolean("include revoked peers; defaults false")}, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			IncludeRevoked bool `json:"include_revoked"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return identity.ListTrusted(in.IncludeRevoked)
	}}))
	must(r.Add(registry.Tool{Name: "federation_trust_add", Description: "Trust a remote Ed25519 node identity only after independently verifying its full SHA-256 fingerprint. Import the public identity, never a private key.", InputSchema: registry.ObjectSchema(map[string]any{"node_id": registry.String("node ID from federation_identity"), "algorithm": registry.String("must be Ed25519"), "public_key": registry.String("base64url Ed25519 public key from federation_identity"), "fingerprint": registry.String("SHA256 fingerprint from federation_identity"), "confirm_fingerprint": registry.String("re-enter the exact fingerprint verified through a separate trusted channel")}, []string{"node_id", "algorithm", "public_key", "fingerprint", "confirm_fingerprint"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			NodeID             string `json:"node_id"`
			Algorithm          string `json:"algorithm"`
			PublicKey          string `json:"public_key"`
			Fingerprint        string `json:"fingerprint"`
			ConfirmFingerprint string `json:"confirm_fingerprint"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return identity.Trust(nodeidentity.Public{NodeID: in.NodeID, Algorithm: in.Algorithm, PublicKey: in.PublicKey, Fingerprint: in.Fingerprint}, in.ConfirmFingerprint)
	}}))
	must(r.Add(registry.Tool{Name: "federation_trust_revoke", Description: "Revoke a previously trusted federation node on this node. This affects only this node's local trust store.", InputSchema: registry.ObjectSchema(map[string]any{"node_id": registry.String("exact trusted node ID to revoke")}, []string{"node_id"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			NodeID string `json:"node_id"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return identity.Revoke(in.NodeID)
	}}))

	must(r.Add(registry.Tool{Name: "fleet_discover", Description: "Discover machines in the local Tailscale tailnet, report online/offline status, and safely inspect CyComAgent installation/version on SSH-authorized peers. Read-only; it never pairs devices or runs arbitrary commands.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) { return fm.Snapshot(), nil }}))
	must(r.Add(registry.Tool{Name: "fleet_pair", Description: "Pair a discovered, online CyComAgent peer as a durable SSH target. Requires confirm=true and only succeeds when strict SSH authentication already works and CyComAgent is installed. It does not copy keys, weaken SSH checks, or execute remote commands.", InputSchema: registry.ObjectSchema(map[string]any{"peer_name": registry.String("exact Tailscale hostname from fleet_discover"), "confirm": registry.Boolean("explicitly confirm creating a durable SSH target")}, []string{"peer_name", "confirm"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			PeerName string `json:"peer_name"`
			Confirm  bool   `json:"confirm"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if !in.Confirm {
			return nil, fmt.Errorf("confirm=true is required to pair a device")
		}
		fm.Refresh()
		snap := fm.Snapshot()
		var peer *fleet.Peer
		for i := range snap.Peers {
			if snap.Peers[i].Name == in.PeerName {
				peer = &snap.Peers[i]
				break
			}
		}
		if peer == nil || peer.Self {
			return nil, fmt.Errorf("peer not found or is the local device")
		}
		if !peer.Online || !peer.SSHReachable || !peer.CyComInstalled {
			return nil, fmt.Errorf("peer must be online, SSH-authenticated and running CyComAgent before pairing")
		}
		name := "cycom-" + peer.Name
		if _, err := tm.Get(name); err == nil {
			return nil, fmt.Errorf("target %q already exists; inspect it with target_get", name)
		}
		target, err := tm.Upsert(targets.Target{Name: name, Transport: "ssh", Host: peer.Name, Tags: []string{"tailscale", "cycom-fleet"}, Enabled: true})
		if err != nil {
			return nil, err
		}
		return map[string]any{"paired": true, "target": target.Name, "host": target.Host, "version": peer.Version, "note": "Use target_list/target_probe/target_exec to work with this peer."}, nil
	}}))
	must(r.Add(registry.Tool{Name: "fleet_remote_tools", Description: "List MCP tools exposed by a paired remote CyComAgent over SSH. This is discovery only and does not execute remote tools.", InputSchema: registry.ObjectSchema(map[string]any{"target": registry.String("paired SSH target name")}, []string{"target"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Target string `json:"target"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return fc.Tools(ctx, in.Target)
	}}))

	must(r.Add(registry.Tool{Name: "fleet_unified_tools", Description: "Discover a unified, namespaced catalog of local and remote MCP tools across all enabled paired SSH nodes. Names are exposed as target.tool to avoid collisions. Offline nodes are listed separately; no central hub is required.", InputSchema: registry.ObjectSchema(map[string]any{"include_local": registry.Boolean("include local node tools; defaults true")}, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			IncludeLocal *bool `json:"include_local"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		includeLocal := in.IncludeLocal == nil || *in.IncludeLocal
		type catalogEntry struct {
			Name        string         `json:"name"`
			Target      string         `json:"target"`
			RemoteName  string         `json:"remote_name,omitempty"`
			Description string         `json:"description,omitempty"`
			InputSchema map[string]any `json:"inputSchema,omitempty"`
			ReadOnly    bool           `json:"readOnly"`
		}
		type nodeStatus struct {
			Target    string `json:"target"`
			Status    string `json:"status"`
			ToolCount int    `json:"tool_count,omitempty"`
			Error     string `json:"error,omitempty"`
		}
		entries := make([]catalogEntry, 0)
		nodes := make([]nodeStatus, 0)
		if includeLocal {
			for _, tool := range r.List() {
				entries = append(entries, catalogEntry{Name: "local." + tool.Name, Target: "local", RemoteName: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, ReadOnly: tool.Annotations["readOnlyHint"] == true})
			}
			nodes = append(nodes, nodeStatus{Target: "local", Status: "online", ToolCount: len(r.List())})
		}
		for _, target := range tm.List() {
			if target.Name == "local" || !target.Enabled || target.Transport != "ssh" {
				continue
			}
			catalog, err := fc.Tools(ctx, target.Name)
			if err != nil {
				nodes = append(nodes, nodeStatus{Target: target.Name, Status: "offline_or_unavailable", Error: err.Error()})
				continue
			}
			nodes = append(nodes, nodeStatus{Target: target.Name, Status: "online", ToolCount: len(catalog)})
			for _, tool := range catalog {
				entries = append(entries, catalogEntry{Name: target.Name + "." + tool.Name, Target: target.Name, RemoteName: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, ReadOnly: tool.Annotations["readOnlyHint"] == true})
			}
		}
		return map[string]any{"mode": "peer_to_peer_ssh", "tools": entries, "nodes": nodes, "tool_count": len(entries), "note": "Namespaced catalog only. Invoke remote tools with fleet_remote_call; remote writes remain blocked unless a separately governed execution policy is implemented."}, nil
	}}))

	must(r.Add(registry.Tool{Name: "fleet_audit_history", Description: "Read and merge recent tool-call audit history from paired CyComAgent nodes over direct SSH. Offline nodes are reported separately; this never requires a central hub and never copies remote private keys.", InputSchema: registry.ObjectSchema(map[string]any{"target": registry.String("optional exact paired target; omit to query all paired targets"), "limit": registry.Integer("events per node; default 50, max 200")}, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Target string `json:"target"`
			Limit  int    `json:"limit"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if in.Limit <= 0 {
			in.Limit = 50
		}
		if in.Limit > 200 {
			in.Limit = 200
		}
		targetsToQuery := tm.List()
		if in.Target != "" {
			t, err := tm.Get(in.Target)
			if err != nil {
				return nil, err
			}
			targetsToQuery = []targets.Target{t}
		}
		type nodeResult struct {
			Target string `json:"target"`
			Status string `json:"status"`
			Events any    `json:"events,omitempty"`
			Count  int    `json:"count,omitempty"`
			Error  string `json:"error,omitempty"`
		}
		results := make([]nodeResult, 0, len(targetsToQuery))
		for _, target := range targetsToQuery {
			if !target.Enabled || target.Transport != "ssh" {
				continue
			}
			value, err := fc.CallReadOnly(ctx, target.Name, "audit_tail", map[string]any{"limit": in.Limit})
			if err != nil {
				results = append(results, nodeResult{Target: target.Name, Status: "offline_or_unavailable", Error: err.Error()})
				continue
			}
			results = append(results, nodeResult{Target: target.Name, Status: "online", Events: value})
		}
		return map[string]any{"mode": "peer_to_peer_ssh", "nodes": results, "queried": len(results), "note": "Each node retains its own authoritative audit log; this is an on-demand merged view. Offline nodes are not treated as empty history."}, nil
	}}))

	must(r.Add(registry.Tool{Name: "fleet_invoke", Description: "Invoke a namespaced tool from fleet_unified_tools using target.tool notation. Local tools remain native MCP calls; this gateway invokes remote paired-node tools directly over SSH. Currently permits only tools explicitly annotated readOnlyHint=true and requires confirm=true.", InputSchema: registry.ObjectSchema(map[string]any{"name": registry.String("fully qualified name from fleet_unified_tools, e.g. cycom-home-server.system_info"), "arguments": registry.ObjectSchema(nil, nil), "confirm": registry.Boolean("explicit approval for this remote read-only invocation")}, []string{"name", "confirm"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
			Confirm   bool           `json:"confirm"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if !in.Confirm {
			return nil, fmt.Errorf("confirm=true is required for cross-device invocation")
		}
		parts := strings.SplitN(in.Name, ".", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("name must use target.tool notation from fleet_unified_tools")
		}
		if parts[0] == "local" {
			return nil, fmt.Errorf("local tools are invoked directly through MCP; fleet_invoke is for remote target.tool names")
		}
		return fc.CallReadOnly(ctx, parts[0], parts[1], in.Arguments)
	}}))

	must(r.Add(registry.Tool{Name: "fleet_remote_call", Description: "Invoke one explicitly read-only tool on a paired CyComAgent over SSH. Requires confirm=true. Remote tools without readOnlyHint=true are always rejected.", InputSchema: registry.ObjectSchema(map[string]any{"target": registry.String("paired SSH target name"), "tool": registry.String("exact remote MCP tool name"), "arguments": registry.ObjectSchema(nil, nil), "confirm": registry.Boolean("explicit approval for this remote read-only call")}, []string{"target", "tool", "confirm"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Target    string         `json:"target"`
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
			Confirm   bool           `json:"confirm"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if !in.Confirm {
			return nil, fmt.Errorf("confirm=true is required for cross-device invocation")
		}
		return fc.CallReadOnly(ctx, in.Target, in.Tool, in.Arguments)
	}}))

}
