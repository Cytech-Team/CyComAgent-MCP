//go:build linux

package remotejobs

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeAuthenticatedBrokerConnectionRetainsPeerAndClosesSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	request := validBrokerRequest()
	writeDone := make(chan error, 1)
	go func() { writeDone <- WriteBrokerStartRequest(client, request, time.Second) }()
	called := false
	err = ServeAuthenticatedBrokerConnection(server, BrokerPeerPolicy{AllowedUID: uint32(os.Getuid()), AllowedExecutableSHA256: currentExecutableDigest(t)}, time.Second, func(identity BrokerPeerIdentity, got BrokerStartRequest) error {
		called = true
		if identity.PID != os.Getpid() || identity.PIDFD < 0 {
			t.Fatalf("peer identity not retained: %+v", identity)
		}
		if got.RequestID != request.RequestID {
			t.Fatalf("request mismatch: %+v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
	if _, err := server.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained open after one request")
	}
}

func TestServeAuthenticatedBrokerConnectionClosesOnHandlerFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- WriteBrokerStartRequest(client, validBrokerRequest(), time.Second) }()
	wantErr := errors.New("authorization denied")
	err = ServeAuthenticatedBrokerConnection(server, BrokerPeerPolicy{AllowedUID: uint32(os.Getuid()), AllowedExecutableSHA256: currentExecutableDigest(t)}, time.Second, func(BrokerPeerIdentity, BrokerStartRequest) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("handler error=%v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := server.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection remained open after handler failure")
	}
}
