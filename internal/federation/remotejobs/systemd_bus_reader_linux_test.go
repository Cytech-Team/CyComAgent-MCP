//go:build linux

package remotejobs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestSystemdBusSingleReaderOrdersEveryFrame(t *testing.T) {
	dispatcher, _, _ := newTestDispatcher(t)
	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	unrelated := builder.frame(1, 100, "", func() {
		builder.field(1, "o", "/org/example", 0)
		builder.field(3, "s", "Ping", 0)
	})
	otherOwner := builder.signal("org.example.other", ":1.70", ":1.71", 101)
	validReply := builder.methodReturn(":1.42", 41, "/org/freedesktop/systemd1/job/8", 102)
	stream := bytes.NewReader(append(append(append([]byte(nil), unrelated...), otherOwner...), validReply...))
	reader, err := newSystemdBusSingleReader(newMemoryDBusSource(stream), dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ack, relevant, err := reader.readAndDispatch(context.Background())
		if err != nil || relevant || ack.Outcome != "" {
			t.Fatalf("frame %d was not ignored in sequence: ack=%#v relevant=%v err=%v", i, ack, relevant, err)
		}
	}
	ack, relevant, err := reader.readAndDispatch(context.Background())
	if err != nil || !relevant || ack.Outcome != "accepted" {
		t.Fatalf("correlated reply was not accepted after unrelated frames: ack=%#v relevant=%v err=%v", ack, relevant, err)
	}
	if reader.sequence != 3 {
		t.Fatalf("reader sequence = %d, want 3", reader.sequence)
	}
	if stream.Len() != 0 {
		t.Fatalf("reader left %d bytes after consuming three frames", stream.Len())
	}
}

func TestSystemdBusSingleReaderInvalidatesOnOwnerChangeBeforeReply(t *testing.T) {
	dispatcher, _, _ := newTestDispatcher(t)
	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	ownerChange := builder.signal(systemdDBusWellKnownName, ":1.42", ":1.43", 200)
	reply := builder.methodReturn(":1.42", 41, "/org/freedesktop/systemd1/job/9", 201)
	reader, err := newSystemdBusSingleReader(newMemoryDBusSource(bytes.NewReader(append(ownerChange, reply...))), dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	if _, relevant, err := reader.readAndDispatch(context.Background()); err != nil || !relevant {
		t.Fatalf("owner change was not applied: relevant=%v err=%v", relevant, err)
	}
	ack, relevant, err := reader.readAndDispatch(context.Background())
	if err != nil || !relevant || ack.Outcome != "unknown" {
		t.Fatalf("reply after owner change was not unknown: ack=%#v relevant=%v err=%v", ack, relevant, err)
	}
}

func TestReadBoundedSystemdDBusFrameRejectsOversizeAndTruncation(t *testing.T) {
	oversize := make([]byte, systemdDBusFixedHeaderSize)
	oversize[0], oversize[1], oversize[3] = 'l', 2, 1
	binary.LittleEndian.PutUint32(oversize[4:8], systemdDBusMaxBodySize+1)
	binary.LittleEndian.PutUint32(oversize[8:12], 1)
	if _, err := readBoundedSystemdDBusFrame(bytes.NewReader(oversize)); err == nil {
		t.Fatal("oversized body frame accepted")
	}

	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	valid := builder.methodReturn(":1.42", 41, "/org/freedesktop/systemd1/job/10", 202)
	if _, err := readBoundedSystemdDBusFrame(bytes.NewReader(valid[:len(valid)-1])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated payload error = %v, want unexpected EOF", err)
	}
}

func TestSystemdBusSingleReaderFailsClosedOnMalformedFrameAndEOF(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{name: "malformed", input: []byte("not a D-Bus frame")},
		{name: "clean-eof", input: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher, _, now := newTestDispatcher(t)
			reader, err := newSystemdBusSingleReader(newMemoryDBusSource(bytes.NewReader(tc.input)), dispatcher)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := reader.readAndDispatch(context.Background()); err == nil {
				t.Fatal("invalid input unexpectedly succeeded")
			}
			if !reader.isClosed() {
				t.Fatal("reader did not close after failure")
			}
			if _, _, err := reader.readAndDispatch(context.Background()); err == nil {
				t.Fatal("closed reader accepted another read")
			}
			if err := dispatcher.session.validateReplyLocked(":1.42", now.Add(2*time.Millisecond)); err == nil {
				t.Fatal("reader failure did not invalidate owner session")
			}
		})
	}
}

func TestSystemdBusSingleReaderRejectsSequenceOverflow(t *testing.T) {
	dispatcher2, _, _ := newTestDispatcher(t)
	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	frame := builder.methodReturn(":1.42", 41, "/org/freedesktop/systemd1/job/11", 203)
	reader2, err := newSystemdBusSingleReader(newMemoryDBusSource(bytes.NewReader(frame)), dispatcher2)
	if err != nil {
		t.Fatal(err)
	}
	reader2.sequence = ^uint64(0)
	if _, _, err := reader2.readAndDispatch(context.Background()); err == nil {
		t.Fatal("sequence overflow accepted")
	}
	if !reader2.isClosed() {
		t.Fatal("sequence overflow did not close reader")
	}
}

type memoryDBusSource struct{ source io.Reader }

func newMemoryDBusSource(source io.Reader) *memoryDBusSource {
	return &memoryDBusSource{source: source}
}
func (r *memoryDBusSource) ReadContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}
func (r *memoryDBusSource) Close() error { return nil }

type gatedDBusReader struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	source  *bytes.Reader
	closed  chan struct{}
}

func (r *gatedDBusReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-r.closed:
		return 0, io.ErrClosedPipe
	}
	return r.source.Read(p)
}
func (r *gatedDBusReader) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}

func TestSystemdBusSingleReaderRejectsConcurrentReadInvocation(t *testing.T) {
	dispatcher, _, _ := newTestDispatcher(t)
	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	frame := builder.methodReturn(":1.42", 41, "/org/freedesktop/systemd1/job/12", 204)
	gated := &gatedDBusReader{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), source: bytes.NewReader(frame)}
	reader, err := newSystemdBusSingleReader(gated, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := reader.readAndDispatch(context.Background())
		firstDone <- err
	}()
	<-gated.started
	if _, _, err := reader.readAndDispatch(context.Background()); err == nil {
		t.Fatal("concurrent reader invocation was accepted")
	}
	close(gated.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first read failed after release: %v", err)
	}
	if reader.sequence != 1 {
		t.Fatalf("reader sequence = %d, want 1", reader.sequence)
	}
}

func TestSystemdBusSingleReaderCancellationClosesBlockedSource(t *testing.T) {
	dispatcher, _, now := newTestDispatcher(t)
	gated := &gatedDBusReader{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), source: bytes.NewReader(nil)}
	reader, err := newSystemdBusSingleReader(gated, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, err := reader.readAndDispatch(ctx)
		result <- err
	}()
	<-gated.started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt blocked source")
	}
	select {
	case <-gated.closed:
	default:
		t.Fatal("cancellation did not close source")
	}
	if err := dispatcher.session.validateReplyLocked(":1.42", now.Add(time.Second)); err == nil {
		t.Fatal("cancellation did not invalidate owner session")
	}
}

func TestSystemdBusSingleReaderCloseIsIdempotent(t *testing.T) {
	dispatcher, _, _ := newTestDispatcher(t)
	gated := &gatedDBusReader{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), source: bytes.NewReader(nil)}
	reader, err := newSystemdBusSingleReader(gated, dispatcher)
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
		t.Fatal("reader did not remain closed")
	}
	select {
	case <-gated.closed:
	default:
		t.Fatal("Close did not close source")
	}
}

type payloadBlockedDBusReader struct {
	prefix  []byte
	release chan struct{}
	closed  chan struct{}
}

func (r *payloadBlockedDBusReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	select {
	case <-r.release:
		return 0, io.EOF
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-r.closed:
		return 0, io.ErrClosedPipe
	}
}
func (r *payloadBlockedDBusReader) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}

func TestSystemdBusSingleReaderCancellationDuringPayloadRead(t *testing.T) {
	dispatcher, _, _ := newTestDispatcher(t)
	builder := newDBusWireBuilder(binary.LittleEndian, 'l')
	frame := builder.methodReturn(":1.42", 41, "/org/freedesktop/systemd1/job/14", 205)
	source := &payloadBlockedDBusReader{prefix: append([]byte(nil), frame[:systemdDBusFixedHeaderSize]...), release: make(chan struct{}), closed: make(chan struct{})}
	reader, err := newSystemdBusSingleReader(source, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err = reader.readAndDispatch(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read error = %v, want deadline exceeded", err)
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("deadline did not close payload source")
	}
	if !reader.isClosed() {
		t.Fatal("deadline did not close reader")
	}
}
