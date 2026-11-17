//go:build linux

package remotejobs

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryDBusSink struct {
	mu sync.Mutex
	bytes.Buffer
	closed bool
	max    int
}

func (s *memoryDBusSink) WriteContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("sink closed")
	}
	if s.max > 0 && len(p) > s.max {
		p = p[:s.max]
	}
	return s.Buffer.Write(p)
}

func (s *memoryDBusSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *memoryDBusSink) snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.Buffer.Bytes()...)
}

func TestSystemdBusSingleWriterWritesPreparedCallsInOrder(t *testing.T) {
	session := newSystemdBusBootstrapSession(1001)
	_, _ = session.externalAuthCommand()
	begin, err := session.receiveAuthFrame([]byte("OK 0123456789abcdef0123456789abcdef\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	sink := &memoryDBusSink{max: 3}
	writer, err := newSystemdBusSingleWriter(sink, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.writeBegin(context.Background(), begin); err != nil {
		t.Fatal(err)
	}
	hello, err := session.prepareHelloCall()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.writePreparedCall(context.Background(), hello); err != nil {
		t.Fatal(err)
	}
	got := sink.snapshot()
	if !bytes.HasPrefix(got, []byte("BEGIN\r\n")) || !bytes.Equal(got[len("BEGIN\r\n"):], hello.Frame) {
		t.Fatalf("writer output does not contain exact ordered commands")
	}
	if session.handshake.state != systemdBusHandshakeHelloPending {
		t.Fatalf("BEGIN did not advance handshake: %v", session.handshake.state)
	}
}

func TestSystemdBusSingleWriterRejectsFrameSubstitution(t *testing.T) {
	session := newSystemdBusBootstrapSession(1001)
	sink := &memoryDBusSink{}
	writer, _ := newSystemdBusSingleWriter(sink, session)
	prepared := systemdBusPreparedCall{
		Serial: 1,
		Call: systemdBusBootstrapCall{
			Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath,
			Interface: systemdDBusInterface, Member: "Hello",
		},
		Frame: []byte("not-a-dbus-frame"),
	}
	if err := writer.writePreparedCall(context.Background(), prepared); err == nil {
		t.Fatal("substituted outbound frame accepted")
	}
	if len(sink.snapshot()) != 0 || !writer.isClosed() {
		t.Fatal("invalid outbound frame was written or generation remained open")
	}
}

func TestSystemdBusSingleWriterPartialFailureClosesWithoutRetry(t *testing.T) {
	session := newSystemdBusBootstrapSession(1001)
	_, _ = session.externalAuthCommand()
	_, _ = session.receiveAuthFrame([]byte("OK 0123456789abcdef0123456789abcdef\r\n"))
	sink := &failingDBusSink{firstWrite: true}
	writer, _ := newSystemdBusSingleWriter(sink, session)
	if err := writer.writeBegin(context.Background(), []byte("BEGIN\r\n")); err == nil {
		t.Fatal("ambiguous partial write unexpectedly succeeded")
	}
	if !writer.isClosed() || sink.writeCalls != 1 {
		t.Fatalf("writer closed=%v write calls=%d, want closed and exactly one write", writer.isClosed(), sink.writeCalls)
	}
	if session.handshake.state != systemdBusHandshakeClosed {
		t.Fatal("partial write did not invalidate connection generation")
	}
}

type failingDBusSink struct {
	firstWrite bool
	writeCalls int
	closed     bool
}

func (s *failingDBusSink) WriteContext(context.Context, []byte) (int, error) {
	s.writeCalls++
	if s.firstWrite {
		s.firstWrite = false
		return 2, errors.New("synthetic connection reset")
	}
	return 0, nil
}
func (s *failingDBusSink) Close() error { s.closed = true; return nil }

type blockingDBusSink struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (s *blockingDBusSink) WriteContext(ctx context.Context, p []byte) (int, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.closed:
		return 0, errors.New("sink closed")
	}
}
func (s *blockingDBusSink) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func TestSystemdBusSingleWriterCancellationClosesGeneration(t *testing.T) {
	session := newSystemdBusBootstrapSession(1001)
	_, _ = session.externalAuthCommand()
	begin, err := session.receiveAuthFrame([]byte("OK 0123456789abcdef0123456789abcdef\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	sink := &blockingDBusSink{entered: make(chan struct{}), closed: make(chan struct{})}
	writer, _ := newSystemdBusSingleWriter(sink, session)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- writer.writeBegin(ctx, begin) }()
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not enter blocking sink")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not stop after cancellation")
	}
	if !writer.isClosed() || session.handshake.state != systemdBusHandshakeClosed {
		t.Fatal("cancellation did not close connection generation")
	}
	select {
	case <-sink.closed:
	default:
		t.Fatal("cancellation did not close sink")
	}
}

func TestSystemdBusSingleWriterRejectsRepeatedBeginAndPreparedFrame(t *testing.T) {
	session := newSystemdBusBootstrapSession(1001)
	_, _ = session.externalAuthCommand()
	begin, err := session.receiveAuthFrame([]byte("OK 0123456789abcdef0123456789abcdef\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	sink := &memoryDBusSink{}
	writer, _ := newSystemdBusSingleWriter(sink, session)
	if err := writer.writeBegin(context.Background(), begin); err != nil {
		t.Fatal(err)
	}
	if err := writer.writeBegin(context.Background(), begin); err == nil {
		t.Fatal("repeated BEGIN was written")
	}
	if bytes.Count(sink.snapshot(), []byte("BEGIN\r\n")) != 1 {
		t.Fatal("BEGIN was written more than once")
	}

	session2 := newSystemdBusBootstrapSession(1001)
	_, _ = session2.externalAuthCommand()
	begin2, _ := session2.receiveAuthFrame([]byte("OK 0123456789abcdef0123456789abcdef\r\n"))
	writer2, _ := newSystemdBusSingleWriter(&memoryDBusSink{}, session2)
	if err := writer2.writeBegin(context.Background(), begin2); err != nil {
		t.Fatal(err)
	}
	prepared, err := session2.prepareHelloCall()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer2.writePreparedCall(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if err := writer2.writePreparedCall(context.Background(), prepared); err == nil {
		t.Fatal("prepared Hello frame replay accepted")
	}
}
