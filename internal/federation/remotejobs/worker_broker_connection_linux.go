//go:build linux

package remotejobs

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// readWorkerBrokerStartRequest reads exactly one bounded worker-broker frame
// from an already-connected transport. The caller remains responsible for
// authenticating the kernel peer and owning/closing the connection. This
// helper performs no authorization, descriptor transfer, or process launch.
func readWorkerBrokerStartRequest(conn net.Conn, timeout time.Duration) (workerBrokerStartRequest, error) {
	var zero workerBrokerStartRequest
	if conn == nil {
		return zero, fmt.Errorf("worker broker connection is required")
	}
	if timeout <= 0 || timeout > maxBrokerIOTimeout {
		return zero, fmt.Errorf("worker broker read timeout is outside limits")
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return zero, fmt.Errorf("set worker broker read deadline: %w", err)
	}
	var header [brokerFrameHeaderBytes]byte
	_, readErr := io.ReadFull(conn, header[:])
	if readErr == nil {
		size := binary.BigEndian.Uint32(header[:])
		if size == 0 || size > maxWorkerBrokerRequestBytes {
			readErr = fmt.Errorf("worker broker frame size is invalid")
		} else {
			payload := make([]byte, int(size))
			_, readErr = io.ReadFull(conn, payload)
			if readErr == nil {
				request, decodeErr := decodeWorkerBrokerStartRequest(payload)
				readErr = decodeErr
				if decodeErr == nil {
					if err := conn.SetReadDeadline(time.Time{}); err != nil {
						return zero, fmt.Errorf("clear worker broker read deadline: %w", err)
					}
					return request, nil
				}
			}
		}
	}
	if clearErr := conn.SetReadDeadline(time.Time{}); clearErr != nil && readErr == nil {
		readErr = fmt.Errorf("clear worker broker read deadline: %w", clearErr)
	}
	return zero, fmt.Errorf("read worker broker request: %w", readErr)
}

// writeWorkerBrokerStartRequest writes one bounded worker-broker frame to an
// already-connected transport. It never dials or creates a socket.
func writeWorkerBrokerStartRequest(conn net.Conn, request workerBrokerStartRequest, timeout time.Duration) error {
	if conn == nil {
		return fmt.Errorf("worker broker connection is required")
	}
	if timeout <= 0 || timeout > maxBrokerIOTimeout {
		return fmt.Errorf("worker broker write timeout is outside limits")
	}
	payload, err := encodeWorkerBrokerStartRequest(request)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set worker broker write deadline: %w", err)
	}
	var header [brokerFrameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	writeErr := writeBrokerFramePart(conn, header[:])
	if writeErr == nil {
		writeErr = writeBrokerFramePart(conn, payload)
	}
	clearErr := conn.SetWriteDeadline(time.Time{})
	if writeErr != nil {
		return fmt.Errorf("write worker broker request: %w", writeErr)
	}
	if clearErr != nil {
		return fmt.Errorf("clear worker broker write deadline: %w", clearErr)
	}
	return nil
}
