//go:build linux

package remotejobs

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// systemdBusBootstrapReader consumes authentication lines and binary D-Bus
// frames from one already-open, cancellable stream. It never opens, dials,
// writes, configures, or reconnects a socket. Outbound commands/calls are
// returned by the bootstrap session for a separate trusted writer.
type systemdBusBootstrapReader struct {
	readMu    sync.Mutex
	stateMu   sync.Mutex
	closeOnce sync.Once
	source    systemdBusContextReadCloser
	session   *systemdBusBootstrapSession
	closed    bool
}

func newSystemdBusBootstrapReader(source systemdBusContextReadCloser, session *systemdBusBootstrapSession) (*systemdBusBootstrapReader, error) {
	if source == nil || session == nil {
		return nil, fmt.Errorf("D-Bus bootstrap source and session are required")
	}
	return &systemdBusBootstrapReader{source: source, session: session}, nil
}

// readAuthReply reads exactly one bounded CRLF-terminated SASL response and
// returns the next outbound command (BEGIN). It is valid only before binary
// mode starts.
func (r *systemdBusBootstrapReader) readAuthReply(ctx context.Context) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("D-Bus bootstrap reader is required")
	}
	if ctx == nil {
		return nil, fmt.Errorf("D-Bus bootstrap reader context is required")
	}
	if !r.readMu.TryLock() {
		return nil, fmt.Errorf("concurrent D-Bus bootstrap read rejected")
	}
	defer r.readMu.Unlock()
	if r.isClosed() {
		return nil, fmt.Errorf("D-Bus bootstrap reader is closed")
	}
	if err := ctx.Err(); err != nil {
		_ = r.Close()
		return nil, err
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = r.Close() })
	defer stopCancellation()

	frame, err := readBoundedSystemdBusAuthFrameContext(ctx, r.source)
	if err != nil {
		_ = r.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	r.stateMu.Lock()
	if r.closed || ctx.Err() != nil {
		r.stateMu.Unlock()
		_ = r.Close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("D-Bus bootstrap reader closed before auth dispatch")
	}
	command, err := r.session.receiveAuthFrame(frame)
	r.stateMu.Unlock()
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	return command, nil
}

// readBoundedSystemdBusAuthFrameContext reads one CRLF line without an
// unbounded ReadString/Scanner allocation. The maximum includes CRLF.
func readBoundedSystemdBusAuthFrameContext(ctx context.Context, source systemdBusContextReadCloser) ([]byte, error) {
	if ctx == nil || source == nil {
		return nil, fmt.Errorf("D-Bus auth frame context and source are required")
	}
	var frame [systemdBusAuthLineMax + 2]byte
	for n := 0; n < len(frame); n++ {
		if _, err := readFullSystemdDBusContext(ctx, source, frame[n:n+1]); err != nil {
			return nil, fmt.Errorf("read D-Bus authentication line: %w", err)
		}
		if frame[n] == '\n' {
			out := append([]byte(nil), frame[:n+1]...)
			if _, err := decodeSystemdBusAuthLine(out); err != nil {
				return nil, err
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("D-Bus authentication line exceeds configured bound")
}

// readAndDispatch reads one exact bounded binary frame, decodes it, and sends
// it through the bootstrap session's single ordered classification entrypoint.
func (r *systemdBusBootstrapReader) readAndDispatch(ctx context.Context) (systemdBusBootstrapResult, error) {
	if r == nil {
		return systemdBusBootstrapResult{}, fmt.Errorf("D-Bus bootstrap reader is required")
	}
	if ctx == nil {
		return systemdBusBootstrapResult{}, fmt.Errorf("D-Bus bootstrap reader context is required")
	}
	if !r.readMu.TryLock() {
		return systemdBusBootstrapResult{}, fmt.Errorf("concurrent D-Bus bootstrap read rejected")
	}
	defer r.readMu.Unlock()
	if r.isClosed() {
		return systemdBusBootstrapResult{}, fmt.Errorf("D-Bus bootstrap reader is closed")
	}
	if err := ctx.Err(); err != nil {
		_ = r.Close()
		return systemdBusBootstrapResult{}, err
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = r.Close() })
	defer stopCancellation()

	frame, err := readBoundedSystemdDBusFrameContext(ctx, r.source)
	if err != nil {
		_ = r.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return systemdBusBootstrapResult{}, ctxErr
		}
		return systemdBusBootstrapResult{}, err
	}
	if err := ctx.Err(); err != nil {
		_ = r.Close()
		return systemdBusBootstrapResult{}, err
	}
	message, err := decodeSystemdDBusFrame(frame, time.Now().UTC())
	if err != nil {
		_ = r.Close()
		return systemdBusBootstrapResult{}, fmt.Errorf("decode D-Bus bootstrap frame: %w", err)
	}
	r.stateMu.Lock()
	if r.closed || ctx.Err() != nil {
		r.stateMu.Unlock()
		_ = r.Close()
		if err := ctx.Err(); err != nil {
			return systemdBusBootstrapResult{}, err
		}
		return systemdBusBootstrapResult{}, fmt.Errorf("D-Bus bootstrap reader closed before dispatch")
	}
	result, dispatchErr := r.session.dispatchDecodedMessage(message)
	r.stateMu.Unlock()
	if dispatchErr != nil {
		_ = r.Close()
		return result, dispatchErr
	}
	return result, nil
}

func (r *systemdBusBootstrapReader) isClosed() bool {
	if r == nil {
		return true
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.closed
}

func (r *systemdBusBootstrapReader) Close() error {
	if r == nil {
		return nil
	}
	var closeErr error
	r.closeOnce.Do(func() {
		r.stateMu.Lock()
		r.closed = true
		r.session.close()
		r.stateMu.Unlock()
		if r.source != nil {
			closeErr = r.source.Close()
		}
	})
	return closeErr
}
