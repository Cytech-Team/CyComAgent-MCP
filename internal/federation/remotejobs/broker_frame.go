package remotejobs

import (
	"encoding/binary"
	"fmt"
	"io"
)

const brokerFrameHeaderBytes = 4

// ReadBrokerFrame reads exactly one length-prefixed broker message. The
// uint32 length is network-byte-order and is checked before allocating memory.
// It intentionally does not impose a deadline; the connection owner must set
// read/write deadlines before calling it.
func ReadBrokerFrame(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("broker frame reader is required")
	}
	var header [brokerFrameHeaderBytes]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("read broker frame header: %w", err)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxBrokerRequestBytes {
		return nil, fmt.Errorf("broker frame size is invalid")
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("read broker frame payload: %w", err)
	}
	return payload, nil
}

// WriteBrokerFrame writes one bounded length-prefixed broker message. A single
// writer should own a connection at a time to prevent interleaved frames.
func WriteBrokerFrame(w io.Writer, payload []byte) error {
	if w == nil {
		return fmt.Errorf("broker frame writer is required")
	}
	if len(payload) == 0 || len(payload) > maxBrokerRequestBytes {
		return fmt.Errorf("broker frame size is invalid")
	}
	var header [brokerFrameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeBrokerFramePart(w, header[:]); err != nil {
		return fmt.Errorf("write broker frame header: %w", err)
	}
	if err := writeBrokerFramePart(w, payload); err != nil {
		return fmt.Errorf("write broker frame payload: %w", err)
	}
	return nil
}

func writeBrokerFramePart(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n < 0 || n > len(data) {
			return fmt.Errorf("writer returned invalid byte count")
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
