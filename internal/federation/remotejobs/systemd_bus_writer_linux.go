//go:build linux

package remotejobs

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"
)

// systemdBusContextWriteCloser is an already-connected output half. WriteContext
// must honor cancellation; Close must unblock an outstanding write. No dial,
// socket creation, deadline mutation, or reconnect is performed here.
type systemdBusContextWriteCloser interface {
	WriteContext(context.Context, []byte) (int, error)
	Close() error
}

// systemdBusSingleWriter serializes all bytes for one connection generation.
// Any partial/ambiguous write permanently closes the generation: frames are
// never retried because the peer may already have received the complete call.
type systemdBusSingleWriter struct {
	writeMu   sync.Mutex
	stateMu   sync.Mutex
	closeOnce sync.Once
	sink      systemdBusContextWriteCloser
	session   *systemdBusBootstrapSession
	closed    bool
}

func newSystemdBusSingleWriter(sink systemdBusContextWriteCloser, session *systemdBusBootstrapSession) (*systemdBusSingleWriter, error) {
	if sink == nil || session == nil {
		return nil, fmt.Errorf("D-Bus writer sink and session are required")
	}
	return &systemdBusSingleWriter{sink: sink, session: session}, nil
}

// writeBegin is the only raw authentication command accepted by this writer.
// EXTERNAL authentication is emitted by the connection adapter before this
// writer is created; BEGIN is written on the same underlying stream.
func (w *systemdBusSingleWriter) writeBegin(ctx context.Context, command []byte) error {
	if !bytes.Equal(command, []byte("BEGIN\r\n")) {
		return w.fail(fmt.Errorf("only the exact D-Bus BEGIN command is accepted"))
	}
	if err := w.session.claimBeginWrite(); err != nil {
		return w.fail(err)
	}
	if err := w.writeFrame(ctx, command); err != nil {
		return err
	}
	if err := w.session.confirmBeginWritten(); err != nil {
		return w.fail(err)
	}
	return nil
}

// writePreparedCall verifies the typed call and exact encoded bytes again
// before writing. The caller must have obtained it from the same session;
// no generic caller-supplied D-Bus frame is accepted.
func (w *systemdBusSingleWriter) writePreparedCall(ctx context.Context, prepared systemdBusPreparedCall) error {
	if w == nil || w.session == nil {
		return fmt.Errorf("D-Bus writer is required")
	}
	expected, err := encodeSystemdBusBootstrapCall(prepared.Serial, prepared.Call)
	if err != nil || !bytes.Equal(expected, prepared.Frame) {
		return w.fail(fmt.Errorf("prepared D-Bus call frame does not match its typed description"))
	}
	if err := w.session.claimPreparedBootstrapWrite(prepared); err != nil {
		return w.fail(err)
	}
	message, err := decodeSystemdDBusFrame(prepared.Frame, time.Now().UTC())
	if err != nil || message.messageType != "method_call" || message.serial != prepared.Serial ||
		message.destination != prepared.Call.Destination || message.path != prepared.Call.Path ||
		message.interfaceName != prepared.Call.Interface || message.member != prepared.Call.Member {
		return w.fail(fmt.Errorf("prepared D-Bus method-call frame failed validation"))
	}
	return w.writeFrame(ctx, prepared.Frame)
}

func (w *systemdBusSingleWriter) writeFrame(ctx context.Context, frame []byte) error {
	if w == nil {
		return fmt.Errorf("D-Bus writer is required")
	}
	if ctx == nil {
		return w.fail(fmt.Errorf("D-Bus writer context is required"))
	}
	if len(frame) == 0 || len(frame) > systemdDBusMaxFrameSize {
		return w.fail(fmt.Errorf("D-Bus outbound frame length is outside policy"))
	}
	if !w.writeMu.TryLock() {
		return fmt.Errorf("concurrent D-Bus writes rejected")
	}
	defer w.writeMu.Unlock()
	w.stateMu.Lock()
	closed := w.closed
	w.stateMu.Unlock()
	if closed {
		return fmt.Errorf("D-Bus writer is closed")
	}
	if err := ctx.Err(); err != nil {
		return w.fail(err)
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = w.Close() })
	defer stopCancellation()
	for len(frame) > 0 {
		if err := ctx.Err(); err != nil {
			return w.fail(err)
		}
		n, err := w.sink.WriteContext(ctx, frame)
		if n < 0 || n > len(frame) {
			return w.fail(fmt.Errorf("D-Bus writer returned invalid write count"))
		}
		if n > 0 {
			frame = frame[n:]
		}
		if err != nil {
			return w.fail(fmt.Errorf("D-Bus outbound write is ambiguous: %w", err))
		}
		if n == 0 {
			return w.fail(fmt.Errorf("D-Bus outbound writer made no progress"))
		}
	}
	if err := ctx.Err(); err != nil {
		return w.fail(err)
	}
	return nil
}

func (w *systemdBusSingleWriter) fail(err error) error {
	if w == nil {
		return err
	}
	_ = w.Close()
	return err
}

func (w *systemdBusSingleWriter) Close() error {
	if w == nil {
		return nil
	}
	var closeErr error
	w.closeOnce.Do(func() {
		w.stateMu.Lock()
		w.closed = true
		w.session.close()
		w.stateMu.Unlock()
		if w.sink != nil {
			closeErr = w.sink.Close()
		}
	})
	return closeErr
}

func (w *systemdBusSingleWriter) isClosed() bool {
	if w == nil {
		return true
	}
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	return w.closed
}
