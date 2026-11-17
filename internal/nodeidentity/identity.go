package nodeidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Public struct {
	NodeID      string `json:"node_id"`
	Algorithm   string `json:"algorithm"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}

type Manager struct {
	path      string
	trustPath string
	pub       ed25519.PublicKey
	key       ed25519.PrivateKey
}

type diskIdentity struct {
	Version int    `json:"version"`
	Private string `json:"private_key"`
}

// New loads or creates this node's independent Ed25519 identity. Private material
// is stored only in this node's state directory with owner-only permissions.
func New(stateDir string) (*Manager, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, "federation-identity.json")
	m := &Manager{path: path, trustPath: filepath.Join(stateDir, "federation-trust.json")}
	data, err := os.ReadFile(path)
	if err == nil {
		var disk diskIdentity
		if err := json.Unmarshal(data, &disk); err != nil {
			return nil, fmt.Errorf("parse node identity: %w", err)
		}
		if disk.Version != 1 {
			return nil, fmt.Errorf("unsupported node identity version %d", disk.Version)
		}
		private, err := base64.RawStdEncoding.DecodeString(disk.Private)
		if err != nil || len(private) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("invalid node private key")
		}
		m.key = ed25519.PrivateKey(private)
		m.pub = append(ed25519.PublicKey(nil), m.key.Public().(ed25519.PublicKey)...)
		if err := os.Chmod(path, 0600); err != nil {
			return nil, err
		}
		return m, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(diskIdentity{Version: 1, Private: base64.RawStdEncoding.EncodeToString(private)}, "", "  ")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(append(encoded, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	m.pub, m.key = pub, private
	return m, nil
}

func (m *Manager) Public() Public {
	sum := sha256.Sum256(m.pub)
	fp := sum
	return Public{NodeID: hex.EncodeToString(sum[:16]), Algorithm: "Ed25519", PublicKey: base64.RawStdEncoding.EncodeToString(m.pub), Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(fp[:])}
}

func (m *Manager) Sign(message []byte) []byte { return ed25519.Sign(m.key, message) }
func Verify(pub ed25519.PublicKey, message, signature []byte) bool {
	return ed25519.Verify(pub, message, signature)
}

// TrustedPeer is a locally approved public identity. Trust is never inferred
// from Tailscale discovery or SSH reachability alone.
type TrustedPeer struct {
	Public
	TrustedAt time.Time `json:"trusted_at"`
	RevokedAt time.Time `json:"revoked_at,omitempty"`
}

type trustStore struct {
	Version int           `json:"version"`
	Peers   []TrustedPeer `json:"peers"`
}

func (m *Manager) loadTrust() (trustStore, error) {
	data, err := os.ReadFile(m.trustPath)
	if os.IsNotExist(err) {
		return trustStore{Version: 1, Peers: []TrustedPeer{}}, nil
	}
	if err != nil {
		return trustStore{}, err
	}
	var store trustStore
	if err := json.Unmarshal(data, &store); err != nil {
		return trustStore{}, fmt.Errorf("parse federation trust store: %w", err)
	}
	if store.Version != 1 {
		return trustStore{}, fmt.Errorf("unsupported federation trust version %d", store.Version)
	}
	if store.Peers == nil {
		store.Peers = []TrustedPeer{}
	}
	return store, nil
}

func (m *Manager) saveTrust(store trustStore) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.trustPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Rename(tmp, m.trustPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func ValidatePublic(p Public) error {
	if p.Algorithm != "Ed25519" {
		return fmt.Errorf("unsupported identity algorithm")
	}
	key, err := base64.RawStdEncoding.DecodeString(p.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid Ed25519 public key")
	}
	sum := sha256.Sum256(key)
	nodeID := hex.EncodeToString(sum[:16])
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	if p.NodeID != nodeID || p.Fingerprint != fingerprint {
		return fmt.Errorf("public identity fields do not match the supplied key")
	}
	return nil
}

// Trust adds or re-activates a peer only when the caller supplies the exact
// fingerprint obtained through an independent authenticated channel.
func (m *Manager) Trust(p Public, confirmedFingerprint string) (TrustedPeer, error) {
	if err := ValidatePublic(p); err != nil {
		return TrustedPeer{}, err
	}
	if strings.TrimSpace(confirmedFingerprint) != p.Fingerprint {
		return TrustedPeer{}, fmt.Errorf("fingerprint confirmation does not match; verify it out of band")
	}
	if p.NodeID == m.Public().NodeID {
		return TrustedPeer{}, fmt.Errorf("cannot add this node as a remote trusted peer")
	}
	store, err := m.loadTrust()
	if err != nil {
		return TrustedPeer{}, err
	}
	now := time.Now().UTC()
	for i := range store.Peers {
		if store.Peers[i].NodeID == p.NodeID {
			if store.Peers[i].PublicKey != p.PublicKey {
				return TrustedPeer{}, fmt.Errorf("node ID collision with a different public key")
			}
			store.Peers[i].RevokedAt = time.Time{}
			store.Peers[i].TrustedAt = now
			if err := m.saveTrust(store); err != nil {
				return TrustedPeer{}, err
			}
			return store.Peers[i], nil
		}
	}
	peer := TrustedPeer{Public: p, TrustedAt: now}
	store.Peers = append(store.Peers, peer)
	sort.Slice(store.Peers, func(i, j int) bool { return store.Peers[i].NodeID < store.Peers[j].NodeID })
	if err := m.saveTrust(store); err != nil {
		return TrustedPeer{}, err
	}
	return peer, nil
}

func (m *Manager) ListTrusted(includeRevoked bool) ([]TrustedPeer, error) {
	store, err := m.loadTrust()
	if err != nil {
		return nil, err
	}
	out := make([]TrustedPeer, 0, len(store.Peers))
	for _, peer := range store.Peers {
		if includeRevoked || peer.RevokedAt.IsZero() {
			out = append(out, peer)
		}
	}
	return out, nil
}

func (m *Manager) Revoke(nodeID string) (TrustedPeer, error) {
	store, err := m.loadTrust()
	if err != nil {
		return TrustedPeer{}, err
	}
	for i := range store.Peers {
		if store.Peers[i].NodeID == nodeID {
			if store.Peers[i].RevokedAt.IsZero() {
				store.Peers[i].RevokedAt = time.Now().UTC()
			}
			if err := m.saveTrust(store); err != nil {
				return TrustedPeer{}, err
			}
			return store.Peers[i], nil
		}
	}
	return TrustedPeer{}, fmt.Errorf("trusted node %q not found", nodeID)
}

func (m *Manager) IsTrusted(nodeID string) bool {
	peers, err := m.ListTrusted(false)
	if err != nil {
		return false
	}
	for _, peer := range peers {
		if peer.NodeID == nodeID {
			return true
		}
	}
	return false
}

// SignChallenge signs a domain-separated, caller-provided random challenge.
func (m *Manager) SignChallenge(nonce []byte) []byte {
	return m.Sign(append([]byte("CyComAgent-Federation-Challenge-v1\x00"), nonce...))
}

// VerifyTrustedChallenge verifies a response only against an active local trust record.
func (m *Manager) VerifyTrustedChallenge(nodeID string, nonce, signature []byte) error {
	peers, err := m.ListTrusted(false)
	if err != nil {
		return err
	}
	for _, peer := range peers {
		if peer.NodeID != nodeID {
			continue
		}
		key, err := base64.RawStdEncoding.DecodeString(peer.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("trusted public key for %q is invalid", nodeID)
		}
		message := append([]byte("CyComAgent-Federation-Challenge-v1\x00"), nonce...)
		if !ed25519.Verify(ed25519.PublicKey(key), message, signature) {
			return fmt.Errorf("challenge signature verification failed for %q", nodeID)
		}
		return nil
	}
	return fmt.Errorf("remote node %q is not trusted on this node; import and verify its fingerprint first", nodeID)
}
