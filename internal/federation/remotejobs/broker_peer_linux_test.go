//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func brokerPeerConnection(t *testing.T) *net.UnixConn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peer.sock")
	addr := &net.UnixAddr{Name: path, Net: "unix"}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func currentExecutableDigest(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestAuthenticateBrokerPeerAcceptsExpectedKernelIdentity(t *testing.T) {
	conn := brokerPeerConnection(t)
	identity, err := AuthenticateBrokerPeer(conn, BrokerPeerPolicy{AllowedUID: uint32(os.Getuid()), AllowedExecutableSHA256: currentExecutableDigest(t)})
	if err != nil {
		t.Fatal(err)
	}
	if identity.PID != os.Getpid() || identity.UID != uint32(os.Getuid()) || identity.StartTimeTicks == 0 || identity.PIDFD < 0 {
		t.Fatalf("unexpected peer identity: %+v", identity)
	}
	if err := identity.Close(); err != nil {
		t.Fatalf("close pidfd: %v", err)
	}
}

func TestAuthenticateBrokerPeerRejectsUIDAndExecutableMismatch(t *testing.T) {
	tests := []struct {
		name   string
		policy BrokerPeerPolicy
	}{
		{name: "uid", policy: BrokerPeerPolicy{AllowedUID: uint32(os.Getuid()) + 1, AllowedExecutableSHA256: currentExecutableDigest(t)}},
		{name: "digest", policy: BrokerPeerPolicy{AllowedUID: uint32(os.Getuid()), AllowedExecutableSHA256: strings.Repeat("0", 64)}},
		{name: "missing digest", policy: BrokerPeerPolicy{AllowedUID: uint32(os.Getuid())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := brokerPeerConnection(t)
			if _, err := AuthenticateBrokerPeer(conn, tt.policy); err == nil {
				t.Fatal("unauthorized peer was accepted")
			}
		})
	}
}
