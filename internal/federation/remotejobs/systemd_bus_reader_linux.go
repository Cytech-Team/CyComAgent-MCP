//go:build linux

package remotejobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"
)

// systemdBusContextReadCloser is the only source contract accepted by the
// reader. ReadContext must honor cancellation, and Close must unblock an
// outstanding ReadContext. A future socket adapter must implement both
// guarantees; this package does not create or configure that socket.
type systemdBusContextReadCloser interface {
	ReadContext(context.Context, []byte) (int, error)
	Close() error
}

// systemdBusSingleReader consumes one already-open, interruptible byte stream.
// It does not dial, authenticate, configure deadlines, or create a socket.
// Exactly one readAndDispatch call may be active at a time. Close or context
// cancellation terminates the reader generation and invalidates its session.
type systemdBusSingleReader struct {
	readMu     sync.Mutex
	stateMu    sync.Mutex
	closeOnce  sync.Once
	source     systemdBusContextReadCloser
	dispatcher *systemdBusMessageDispatcher
	sequence   uint64
	closed     bool
}

func newSystemdBusSingleReader(source systemdBusContextReadCloser, dispatcher *systemdBusMessageDispatcher) (*systemdBusSingleReader, error) {
	if source == nil || dispatcher == nil {
		return nil, fmt.Errorf("D-Bus interruptible source and message dispatcher are required")
	}
	return &systemdBusSingleReader{source: source, dispatcher: dispatcher}, nil
}

func (r *systemdBusSingleReader) readAndDispatch(ctx context.Context) (systemdStartAcknowledgement, bool, error) {
	if r == nil {
		return systemdStartAcknowledgement{}, false, fmt.Errorf("D-Bus single reader is required")
	}
	if ctx == nil {
		return systemdStartAcknowledgement{}, false, fmt.Errorf("D-Bus reader context is required")
	}
	if !r.readMu.TryLock() {
		return systemdStartAcknowledgement{}, false, fmt.Errorf("concurrent D-Bus reader invocation rejected")
	}
	defer r.readMu.Unlock()
	if r.isClosed() {
		return systemdStartAcknowledgement{}, false, fmt.Errorf("D-Bus single reader is closed")
	}
	if err := ctx.Err(); err != nil {
		_ = r.Close()
		return systemdStartAcknowledgement{}, false, err
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = r.Close() })
	defer stopCancellation()

	frame, err := readBoundedSystemdDBusFrameContext(ctx, r.source)
	if err != nil {
		_ = r.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return systemdStartAcknowledgement{}, false, ctxErr
		}
		return systemdStartAcknowledgement{}, false, err
	}
	if err := ctx.Err(); err != nil {
		_ = r.Close()
		return systemdStartAcknowledgement{}, false, err
	}
	message, err := decodeSystemdDBusFrame(frame, time.Now().UTC())
	if err != nil {
		_ = r.Close()
		return systemdStartAcknowledgement{}, false, fmt.Errorf("decode D-Bus frame: %w", err)
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closed || ctx.Err() != nil {
		if err := ctx.Err(); err != nil {
			return systemdStartAcknowledgement{}, false, err
		}
		return systemdStartAcknowledgement{}, false, fmt.Errorf("D-Bus reader closed before dispatch")
	}
	if r.sequence == math.MaxUint64 {
		r.closed = true
		_ = r.source.Close()
		r.dispatcher.close()
		return systemdStartAcknowledgement{}, false, fmt.Errorf("D-Bus receive sequence exhausted")
	}
	r.sequence++
	ack, relevant, err := r.dispatcher.dispatchDecodedMessage(r.sequence, message)
	if err != nil {
		r.closed = true
		_ = r.source.Close()
		return ack, relevant, err
	}
	return ack, relevant, nil
}

func readBoundedSystemdDBusFrameContext(ctx context.Context, source systemdBusContextReadCloser) ([]byte, error) {
	if ctx == nil || source == nil {
		return nil, fmt.Errorf("D-Bus frame context and source are required")
	}
	var fixed [systemdDBusFixedHeaderSize]byte
	if _, err := readFullSystemdDBusContext(ctx, source, fixed[:]); err != nil {
		return nil, fmt.Errorf("read D-Bus fixed header: %w", err)
	}
	header, err := inspectSystemdDBusFrameHeader(fixed[:])
	if err != nil {
		return nil, fmt.Errorf("validate D-Bus fixed header: %w", err)
	}
	total := int(header.totalFrameSize)
	if total < systemdDBusFixedHeaderSize || total > systemdDBusMaxFrameSize {
		return nil, fmt.Errorf("D-Bus frame size is outside configured bounds")
	}
	frame := make([]byte, total)
	copy(frame, fixed[:])
	if _, err := readFullSystemdDBusContext(ctx, source, frame[systemdDBusFixedHeaderSize:]); err != nil {
		return nil, fmt.Errorf("read D-Bus frame payload: %w", err)
	}
	return frame, nil
}

func readFullSystemdDBusContext(ctx context.Context, source systemdBusContextReadCloser, buffer []byte) (int, error) {
	read := 0
	for read < len(buffer) {
		if err := ctx.Err(); err != nil {
			return read, err
		}
		n, err := source.ReadContext(ctx, buffer[read:])
		if n < 0 || n > len(buffer)-read {
			return read, fmt.Errorf("D-Bus source returned an invalid read count")
		}
		read += n
		if err != nil {
			if read == len(buffer) && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
				return read, nil
			}
			if read > 0 && errors.Is(err, io.EOF) {
				return read, io.ErrUnexpectedEOF
			}
			return read, err
		}
		if n == 0 {
			return read, io.ErrNoProgress
		}
	}
	return read, nil
}

// readBoundedSystemdDBusFrame remains a parser/fuzz helper for finite in-memory
// io.Reader inputs. The production-facing reader uses the cancellable contract.
func readBoundedSystemdDBusFrame(source io.Reader) ([]byte, error) {
	if source == nil {
		return nil, fmt.Errorf("D-Bus frame source is required")
	}
	var fixed [systemdDBusFixedHeaderSize]byte
	if _, err := io.ReadFull(source, fixed[:]); err != nil {
		return nil, fmt.Errorf("read D-Bus fixed header: %w", err)
	}
	header, err := inspectSystemdDBusFrameHeader(fixed[:])
	if err != nil {
		return nil, fmt.Errorf("validate D-Bus fixed header: %w", err)
	}
	total := int(header.totalFrameSize)
	if total < systemdDBusFixedHeaderSize || total > systemdDBusMaxFrameSize {
		return nil, fmt.Errorf("D-Bus frame size is outside configured bounds")
	}
	frame := make([]byte, total)
	copy(frame, fixed[:])
	if _, err := io.ReadFull(source, frame[systemdDBusFixedHeaderSize:]); err != nil {
		return nil, fmt.Errorf("read D-Bus frame payload: %w", err)
	}
	return frame, nil
}

func (r *systemdBusSingleReader) isClosed() bool {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.closed
}

func (r *systemdBusSingleReader) Close() error {
	if r == nil {
		return nil
	}
	var closeErr error
	r.closeOnce.Do(func() {
		r.stateMu.Lock()
		r.closed = true
		r.stateMu.Unlock()
		if r.source != nil {
			closeErr = r.source.Close()
		}
		if r.dispatcher != nil {
			r.dispatcher.close()
		}
	})
	return closeErr
}
