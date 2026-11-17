//go:build linux

package remotejobs

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	systemdBusHandshakeAddMatchRule = "type='signal',sender='org.freedesktop.DBus',interface='org.freedesktop.DBus',member='NameOwnerChanged',arg0='org.freedesktop.systemd1'"
	systemdBusAuthLineMax           = 512
)

type systemdBusHandshakeState uint8

const (
	systemdBusHandshakeInitial systemdBusHandshakeState = iota
	systemdBusHandshakeAuthSent
	systemdBusHandshakeAuthAccepted
	systemdBusHandshakeBeginPending
	systemdBusHandshakeAwaitHello
	systemdBusHandshakeHelloPending
	systemdBusHandshakeHelloAccepted
	systemdBusHandshakeMatchPending
	systemdBusHandshakeMatchInstalled
	systemdBusHandshakeOwnerPending
	systemdBusHandshakeReady
	systemdBusHandshakeFailed
	systemdBusHandshakeClosed
)

// systemdBusConnectionHandshake is a pure protocol state machine. It does not
// write bytes, open a connection, authenticate a peer, or send D-Bus calls.
// The future transport must send each returned command exactly once and report
// write completion before advancing to the next state.
type systemdBusConnectionHandshake struct {
	state       systemdBusHandshakeState
	uid         uint32
	serverGUID  string
	helloSerial uint32
	matchSerial uint32
	ownerSerial uint32
	uniqueName  string
	owner       *systemdBusOwnerSession
}

func newSystemdBusConnectionHandshake(effectiveUID uint32) *systemdBusConnectionHandshake {
	return &systemdBusConnectionHandshake{state: systemdBusHandshakeInitial, uid: effectiveUID}
}

// externalAuthCommand emits the D-Bus SASL EXTERNAL initial NUL and the
// hexadecimal encoding of the decimal effective UID. The UID must come from
// the actual local transport identity, not a remote request.
func (h *systemdBusConnectionHandshake) externalAuthCommand() ([]byte, error) {
	if h == nil || h.state != systemdBusHandshakeInitial {
		return nil, fmt.Errorf("D-Bus EXTERNAL authentication cannot start in this state")
	}
	uidText := strconv.FormatUint(uint64(h.uid), 10)
	encodedUID := make([]byte, hex.EncodedLen(len(uidText)))
	hex.Encode(encodedUID, []byte(uidText))
	command := make([]byte, 0, 1+len("AUTH EXTERNAL ")+len(encodedUID)+2)
	command = append(command, 0)
	command = append(command, "AUTH EXTERNAL "...)
	command = append(command, encodedUID...)
	command = append(command, '\r', '\n')
	h.state = systemdBusHandshakeAuthSent
	return command, nil
}

// decodeSystemdBusAuthLine validates one complete CRLF-terminated SASL line.
// It is a bounded parser helper; it does not read from a stream.
func decodeSystemdBusAuthLine(frame []byte) ([]byte, error) {
	if len(frame) < 3 || len(frame) > systemdBusAuthLineMax+2 ||
		frame[len(frame)-2] != '\r' || frame[len(frame)-1] != '\n' {
		return nil, fmt.Errorf("D-Bus authentication frame is not a bounded CRLF line")
	}
	line := frame[:len(frame)-2]
	if strings.ContainsAny(string(line), "\r\n\x00") {
		return nil, fmt.Errorf("D-Bus authentication line contains a forbidden delimiter")
	}
	return append([]byte(nil), line...), nil
}

// receiveExternalAuthReply consumes one complete SASL line without CRLF.
// Only OK plus a canonical 32-hex D-Bus GUID is supported. DATA challenges,
// fallback mechanisms, REJECTED, ERROR, and malformed lines fail closed.
func (h *systemdBusConnectionHandshake) receiveExternalAuthReply(line []byte) error {
	if h == nil || h.state != systemdBusHandshakeAuthSent {
		return fmt.Errorf("D-Bus EXTERNAL reply arrived in the wrong state")
	}
	if len(line) == 0 || len(line) > systemdBusAuthLineMax || strings.ContainsAny(string(line), "\r\n\x00") {
		return h.fail(fmt.Errorf("D-Bus authentication reply line is malformed or oversized"))
	}
	parts := strings.Split(string(line), " ")
	if len(parts) != 2 || parts[0] != "OK" || !validDBusGUID(parts[1]) {
		return h.fail(fmt.Errorf("D-Bus EXTERNAL authentication was not accepted with a valid GUID"))
	}
	h.serverGUID = strings.ToLower(parts[1])
	h.state = systemdBusHandshakeAuthAccepted
	return nil
}

func (h *systemdBusConnectionHandshake) beginCommand() ([]byte, error) {
	if h == nil || h.state != systemdBusHandshakeAuthAccepted {
		return nil, fmt.Errorf("D-Bus BEGIN is not permitted before EXTERNAL acceptance")
	}
	h.state = systemdBusHandshakeBeginPending
	return []byte("BEGIN\r\n"), nil
}

// confirmBeginWritten must be called only after the transport confirms that
// the complete BEGIN line was written to the same authenticated connection.
func (h *systemdBusConnectionHandshake) confirmBeginWritten() error {
	if h == nil || h.state != systemdBusHandshakeBeginPending {
		return fmt.Errorf("D-Bus BEGIN write confirmation is unexpected")
	}
	h.state = systemdBusHandshakeAwaitHello
	return nil
}

func (h *systemdBusConnectionHandshake) registerHelloCall(serial uint32) error {
	if h == nil || h.state != systemdBusHandshakeAwaitHello || serial == 0 {
		return fmt.Errorf("D-Bus Hello call registration is invalid")
	}
	h.helloSerial = serial
	h.state = systemdBusHandshakeHelloPending
	return nil
}

func (h *systemdBusConnectionHandshake) acceptHelloReply(message systemdBusDecodedMessage) error {
	if h == nil || h.state != systemdBusHandshakeHelloPending {
		return fmt.Errorf("D-Bus Hello reply arrived in the wrong state")
	}
	if message.messageType != "method_return" || message.sender != systemdDBusDaemonName ||
		message.replySerial != h.helloSerial || message.serial == 0 ||
		message.path != "" || message.interfaceName != "" || message.member != "" ||
		message.errorName != "" || message.signature != "s" || len(message.bodyStrings) != 1 ||
		message.bodyObjectPath != "" || !validDBusUniqueName(message.bodyStrings[0]) ||
		message.destination != message.bodyStrings[0] || message.receivedAt.IsZero() {
		return h.fail(fmt.Errorf("D-Bus Hello reply does not match the authenticated connection"))
	}
	h.uniqueName = message.bodyStrings[0]
	h.state = systemdBusHandshakeHelloAccepted
	return nil
}

func (h *systemdBusConnectionHandshake) registerAddMatchCall(serial uint32) (string, error) {
	if h == nil || h.state != systemdBusHandshakeHelloAccepted || serial == 0 {
		return "", fmt.Errorf("D-Bus AddMatch registration is invalid")
	}
	h.matchSerial = serial
	h.state = systemdBusHandshakeMatchPending
	return systemdBusHandshakeAddMatchRule, nil
}

func (h *systemdBusConnectionHandshake) acceptAddMatchReply(message systemdBusDecodedMessage) error {
	if h == nil || h.state != systemdBusHandshakeMatchPending {
		return fmt.Errorf("D-Bus AddMatch reply arrived in the wrong state")
	}
	if !h.validDaemonMethodReturn(message, h.matchSerial, "") {
		return h.fail(fmt.Errorf("D-Bus AddMatch reply is not an exact successful empty return"))
	}
	h.state = systemdBusHandshakeMatchInstalled
	return nil
}

// observeHandshakeMessage must receive every decoded frame in wire order
// while AddMatch is installed but GetNameOwner has not yet completed. A
// systemd owner change in this interval makes the query result ambiguous, so
// the handshake fails closed instead of pinning a possibly stale owner.
func (h *systemdBusConnectionHandshake) observeHandshakeMessage(message systemdBusDecodedMessage) (bool, error) {
	if h == nil || (h.state != systemdBusHandshakeMatchInstalled && h.state != systemdBusHandshakeOwnerPending) {
		return false, fmt.Errorf("handshake message observation is not allowed in this state")
	}
	if !isSystemdNameOwnerChangedEnvelope(message) {
		return false, nil
	}
	event, err := decodeSystemdNameOwnerChanged(message)
	if err != nil {
		return true, h.fail(err)
	}
	if event.name != systemdDBusWellKnownName {
		return false, nil
	}
	return true, h.fail(fmt.Errorf("systemd owner changed before GetNameOwner completed"))
}

func (h *systemdBusConnectionHandshake) registerGetNameOwnerCall(serial uint32) (string, error) {
	if h == nil || h.state != systemdBusHandshakeMatchInstalled || serial == 0 {
		return "", fmt.Errorf("D-Bus GetNameOwner registration is invalid")
	}
	h.ownerSerial = serial
	h.state = systemdBusHandshakeOwnerPending
	return systemdDBusWellKnownName, nil
}

func (h *systemdBusConnectionHandshake) acceptGetNameOwnerReply(message systemdBusDecodedMessage) (*systemdBusOwnerSession, error) {
	if h == nil || h.state != systemdBusHandshakeOwnerPending {
		return nil, fmt.Errorf("D-Bus GetNameOwner reply arrived in the wrong state")
	}
	if !h.validDaemonMethodReturn(message, h.ownerSerial, "s") ||
		len(message.bodyStrings) != 1 || !validDBusUniqueName(message.bodyStrings[0]) {
		return nil, h.fail(fmt.Errorf("D-Bus GetNameOwner reply is invalid"))
	}
	owner, err := newSystemdBusOwnerSession(systemdDBusWellKnownName, message.bodyStrings[0], message.receivedAt)
	if err != nil {
		return nil, h.fail(err)
	}
	h.owner = owner
	h.state = systemdBusHandshakeReady
	return owner, nil
}

func (h *systemdBusConnectionHandshake) validDaemonMethodReturn(message systemdBusDecodedMessage, serial uint32, signature string) bool {
	return message.messageType == "method_return" &&
		message.sender == systemdDBusDaemonName &&
		message.destination == h.uniqueName &&
		message.replySerial == serial && serial != 0 && message.serial != 0 &&
		message.path == "" && message.interfaceName == "" && message.member == "" &&
		message.errorName == "" && message.signature == signature &&
		message.bodyObjectPath == "" && message.receivedAt.IsZero() == false &&
		((signature == "" && len(message.bodyStrings) == 0) || signature == "s")
}

func (h *systemdBusConnectionHandshake) fail(err error) error {
	if h == nil {
		return err
	}
	h.state = systemdBusHandshakeFailed
	if h.owner != nil {
		h.owner.close()
	}
	return err
}

// close permanently invalidates this connection generation. Reconnect requires
// a new handshake, transport, Hello unique name, and owner session.
func (h *systemdBusConnectionHandshake) close() {
	if h == nil || h.state == systemdBusHandshakeClosed {
		return
	}
	h.state = systemdBusHandshakeClosed
	if h.owner != nil {
		h.owner.close()
	}
}

func (h *systemdBusConnectionHandshake) isReady() bool {
	return h != nil && h.state == systemdBusHandshakeReady && h.owner != nil
}

func validDBusGUID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// Handshake replies are deliberately timestamped by the decoded-message
// transport. This helper exists for test fixture readability only.
func handshakeReply(messageType, sender, destination, signature string, serial, replySerial uint32, stringsBody []string, receivedAt time.Time) systemdBusDecodedMessage {
	return systemdBusDecodedMessage{
		messageType: messageType, sender: sender, destination: destination,
		signature: signature, serial: serial, replySerial: replySerial,
		bodyStrings: stringsBody, receivedAt: receivedAt,
	}
}
