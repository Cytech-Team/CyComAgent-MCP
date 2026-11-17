//go:build linux

package remotejobs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSystemdBusBootstrapReaderReadsAuthThenBinaryFrames(t *testing.T) {
	session := newSystemdBusBootstrapSession(1001)
	authCommand, err := session.externalAuthCommand()
	if err != nil || len(authCommand) == 0 {
		t.Fatalf("EXTERNAL command failed: %q %v", authCommand, err)
	}
	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	hello := builder.frame(2, 20, "s", func() {
		builder.field(5, "u", "", 10)
		builder.field(7, "s", systemdDBusDaemonName, 0)
		builder.field(6, "s", ":1.42", 0)
		builder.field(8, "g", "s", 0)
		builder.alignBody(4)
		builder.wireString(&builder.body, ":1.42")
	})
	streamBytes := append([]byte("OK 0123456789abcdef0123456789abcdef\r\n"), hello...)
	stream := bytes.NewReader(streamBytes)
	source := newMemoryDBusSource(stream)
	reader, err := newSystemdBusBootstrapReader(source, session)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := reader.readAuthReply(context.Background())
	if err != nil || string(begin) != "BEGIN\r\n" {
		t.Fatalf("BEGIN command = %q err=%v", begin, err)
	}
	if err := session.confirmBeginWritten(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.registerHelloCall(10); err != nil {
		t.Fatal(err)
	}
	result, err := reader.readAndDispatch(context.Background())
	if err != nil || result.HandshakeState != systemdBusHandshakeHelloAccepted {
		t.Fatalf("Hello frame dispatch failed: result=%#v err=%v", result, err)
	}
	if reader.isClosed() || stream.Len() != 0 {
		t.Fatalf("reader closed=%v remaining=%d", reader.isClosed(), stream.Len())
	}
}

func TestSystemdBusBootstrapReaderRejectsUnboundedAuthLine(t *testing.T) {
	session := newSystemdBusBootstrapSession(1001)
	_, _ = session.externalAuthCommand()
	input := bytes.Repeat([]byte{'A'}, systemdBusAuthLineMax+3)
	reader, err := newSystemdBusBootstrapReader(newMemoryDBusSource(bytes.NewReader(input)), session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.readAuthReply(context.Background()); err == nil {
		t.Fatal("oversized auth line accepted")
	}
	if !reader.isClosed() {
		t.Fatal("oversized auth line did not close reader")
	}
}

type bootstrapBlockedSource struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (s *bootstrapBlockedSource) ReadContext(ctx context.Context, p []byte) (int, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.closed:
		return 0, errors.New("source closed")
	}
}

func (s *bootstrapBlockedSource) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func TestSystemdBusBootstrapReaderCancellationClosesGeneration(t *testing.T) {
	session := newSystemdBusBootstrapSession(1001)
	_, _ = session.externalAuthCommand()
	source := &bootstrapBlockedSource{entered: make(chan struct{}), closed: make(chan struct{})}
	reader, err := newSystemdBusBootstrapReader(source, session)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := reader.readAuthReply(ctx)
		done <- err
	}()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("bootstrap reader did not enter blocked source read")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("auth read error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bootstrap reader did not stop after cancellation")
	}
	if !reader.isClosed() || session.handshake.state != systemdBusHandshakeClosed {
		t.Fatal("cancellation did not close bootstrap generation")
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("cancellation did not close the underlying source")
	}
}

func TestSystemdBusBootstrapReaderCloseIsIdempotent(t *testing.T) {
	session := newSystemdBusBootstrapSession(1001)
	_, _ = session.externalAuthCommand()
	source := newMemoryDBusSource(bytes.NewReader(nil))
	reader, err := newSystemdBusBootstrapReader(source, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !reader.isClosed() {
		t.Fatal("reader remains open after Close")
	}
}

func TestSystemdBusBootstrapReaderCancellationDuringBinaryPayloadRead(t *testing.T) {
	session := bootstrapToHelloPending(t)
	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	frame := builder.methodReturn(":1.42", 10, "/org/freedesktop/systemd1/job/15", 205)
	source := &payloadBlockedDBusReader{
		prefix:  append([]byte(nil), frame[:systemdDBusFixedHeaderSize]...),
		release: make(chan struct{}), closed: make(chan struct{}),
	}
	reader, err := newSystemdBusBootstrapReader(source, session)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = reader.readAndDispatch(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("binary frame read error = %v, want deadline exceeded", err)
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("binary payload deadline did not close source")
	}
	if !reader.isClosed() || session.handshake.state != systemdBusHandshakeClosed {
		t.Fatal("binary payload deadline did not close bootstrap generation")
	}
}
