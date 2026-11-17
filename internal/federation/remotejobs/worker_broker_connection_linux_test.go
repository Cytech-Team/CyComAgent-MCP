//go:build linux

package remotejobs

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestWorkerBrokerConnectionRoundTripOnCallerOwnedTransport(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	want := validWorkerBrokerRequest(t)
	writeDone := make(chan error, 1)
	go func() { writeDone <- writeWorkerBrokerStartRequest(left, want, time.Second) }()
	got, err := readWorkerBrokerStartRequest(right, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: got=%#v want=%#v", got, want)
	}
}

func TestWorkerBrokerConnectionRejectsOversizedFrameBeforeAllocation(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	writeDone := make(chan error, 1)
	go func() {
		var header [brokerFrameHeaderBytes]byte
		binary.BigEndian.PutUint32(header[:], maxWorkerBrokerRequestBytes+1)
		_, err := left.Write(header[:])
		writeDone <- err
	}()
	if _, err := readWorkerBrokerStartRequest(right, time.Second); err == nil {
		t.Fatal("oversized worker frame accepted")
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestWorkerBrokerConnectionRejectsInvalidTimeoutAndNilConnection(t *testing.T) {
	request := validWorkerBrokerRequest(t)
	if _, err := readWorkerBrokerStartRequest(nil, time.Second); err == nil {
		t.Fatal("nil reader accepted")
	}
	if err := writeWorkerBrokerStartRequest(nil, request, time.Second); err == nil {
		t.Fatal("nil writer accepted")
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if _, err := readWorkerBrokerStartRequest(right, 0); err == nil {
		t.Fatal("zero read timeout accepted")
	}
	if err := writeWorkerBrokerStartRequest(left, request, maxBrokerIOTimeout+time.Second); err == nil {
		t.Fatal("excessive write timeout accepted")
	}
}

func TestWorkerBrokerConnectionReadTimeout(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	start := time.Now()
	if _, err := readWorkerBrokerStartRequest(right, 20*time.Millisecond); err == nil {
		t.Fatal("silent peer did not time out")
	}
	if time.Since(start) > time.Second {
		t.Fatal("read timeout exceeded expected bound")
	}
}
