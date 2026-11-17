package remotejobs

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

const maxBrokerIOTimeout = 30 * time.Second

// ReadBrokerStartRequest reads and validates one request using a bounded
// connection read deadline. The caller still owns peer authentication,
// connection admission, replay/policy authorization, and connection closure.
func ReadBrokerStartRequest(conn net.Conn, timeout time.Duration) (BrokerStartRequest, error) {
	var zero BrokerStartRequest
	if conn == nil {
		return zero, fmt.Errorf("broker connection is required")
	}
	if timeout <= 0 || timeout > maxBrokerIOTimeout {
		return zero, fmt.Errorf("broker read timeout is outside limits")
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return zero, fmt.Errorf("set broker read deadline: %w", err)
	}
	payload, readErr := ReadBrokerFrame(conn)
	clearErr := conn.SetReadDeadline(time.Time{})
	if readErr != nil {
		return zero, readErr
	}
	if clearErr != nil {
		return zero, fmt.Errorf("clear broker read deadline: %w", clearErr)
	}
	return DecodeBrokerStartRequest(payload)
}

// WriteBrokerStartRequest validates, serializes, and writes one request using
// a bounded write deadline. A single writer must own the connection at a time.
func WriteBrokerStartRequest(conn net.Conn, req BrokerStartRequest, timeout time.Duration) error {
	if conn == nil {
		return fmt.Errorf("broker connection is required")
	}
	if timeout <= 0 || timeout > maxBrokerIOTimeout {
		return fmt.Errorf("broker write timeout is outside limits")
	}
	if err := ValidateBrokerStartRequest(req); err != nil {
		return err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode broker start request: %w", err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set broker write deadline: %w", err)
	}
	writeErr := WriteBrokerFrame(conn, payload)
	clearErr := conn.SetWriteDeadline(time.Time{})
	if writeErr != nil {
		return writeErr
	}
	if clearErr != nil {
		return fmt.Errorf("clear broker write deadline: %w", clearErr)
	}
	return nil
}

// ReadSignedBrokerStartEnvelope reads one bounded signed request and verifies
// its Federation signature before returning a trusted caller context.
func ReadSignedBrokerStartEnvelope(conn net.Conn, receiver *nodeidentity.Manager, timeout time.Duration) (FederationCallerContext, BrokerStartRequest, error) {
	var zero BrokerStartRequest
	if conn == nil {
		return FederationCallerContext{}, zero, fmt.Errorf("broker connection is required")
	}
	if receiver == nil {
		return FederationCallerContext{}, zero, fmt.Errorf("receiver Federation identity is required")
	}
	if timeout <= 0 || timeout > maxBrokerIOTimeout {
		return FederationCallerContext{}, zero, fmt.Errorf("broker read timeout is outside limits")
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return FederationCallerContext{}, zero, fmt.Errorf("set broker read deadline: %w", err)
	}
	payload, readErr := ReadBrokerFrame(conn)
	clearErr := conn.SetReadDeadline(time.Time{})
	if readErr != nil {
		return FederationCallerContext{}, zero, readErr
	}
	if clearErr != nil {
		return FederationCallerContext{}, zero, fmt.Errorf("clear broker read deadline: %w", clearErr)
	}
	return DecodeAndVerifySignedBrokerStartEnvelope(receiver, payload)
}

// WriteSignedBrokerStartEnvelope writes a pre-signed envelope with a bounded deadline.
func WriteSignedBrokerStartEnvelope(conn net.Conn, envelope []byte, timeout time.Duration) error {
	if conn == nil {
		return fmt.Errorf("broker connection is required")
	}
	if len(envelope) == 0 || len(envelope) > maxBrokerRequestBytes {
		return fmt.Errorf("signed broker envelope size is invalid")
	}
	if timeout <= 0 || timeout > maxBrokerIOTimeout {
		return fmt.Errorf("broker write timeout is outside limits")
	}
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set broker write deadline: %w", err)
	}
	writeErr := WriteBrokerFrame(conn, envelope)
	clearErr := conn.SetWriteDeadline(time.Time{})
	if writeErr != nil {
		return writeErr
	}
	if clearErr != nil {
		return fmt.Errorf("clear broker write deadline: %w", clearErr)
	}
	return nil
}
