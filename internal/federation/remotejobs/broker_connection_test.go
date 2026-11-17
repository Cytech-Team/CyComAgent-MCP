package remotejobs

import (
	"net"
	"testing"
	"time"
)

func TestBrokerConnectionRequestRoundTrip(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	want := validBrokerRequest()
	writeDone := make(chan error, 1)
	go func() { writeDone <- WriteBrokerStartRequest(client, want, time.Second) }()
	got, err := ReadBrokerStartRequest(server, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if got.JobID != want.JobID || got.RequestID != want.RequestID || got.PolicyRevision != want.PolicyRevision {
		t.Fatalf("request mismatch: got %+v", got)
	}
}

func TestBrokerConnectionReadDeadlineBoundsSlowPeer(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	started := time.Now()
	_, err := ReadBrokerStartRequest(server, 40*time.Millisecond)
	if err == nil {
		t.Fatal("expected read deadline")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline did not bound read: %s", elapsed)
	}
}

func TestBrokerConnectionRejectsInvalidTimeoutAndNilConnection(t *testing.T) {
	if _, err := ReadBrokerStartRequest(nil, time.Second); err == nil {
		t.Fatal("accepted nil connection")
	}
	if _, err := ReadBrokerStartRequest(nil, 0); err == nil {
		t.Fatal("accepted invalid timeout")
	}
	if err := WriteBrokerStartRequest(nil, validBrokerRequest(), time.Second); err == nil {
		t.Fatal("accepted nil connection for write")
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	if err := WriteBrokerStartRequest(client, validBrokerRequest(), maxBrokerIOTimeout+time.Second); err == nil {
		t.Fatal("accepted excessive timeout")
	}
}
