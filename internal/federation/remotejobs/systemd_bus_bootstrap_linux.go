//go:build linux

package remotejobs

import (
	"fmt"
	"math"
	"sync"
)

type systemdBusBootstrapCall struct {
	Destination string
	Path        string
	Interface   string
	Member      string
	Signature   string
	StringArgs  []string
}

type systemdBusBootstrapResult struct {
	HandshakeState  systemdBusHandshakeState
	Ready           bool
	Acknowledgement systemdStartAcknowledgement
	Relevant        bool
}

// systemdBusBootstrapSession is the single ordered protocol coordinator for
// one already-established byte stream. It owns SASL state, binary message
// sequence assignment, Hello/AddMatch/GetNameOwner correlation, and the
// post-handshake start dispatcher. It does not open sockets, read/write bytes,
// or perform D-Bus calls.
type systemdBusBootstrapSession struct {
	mu                sync.Mutex
	handshake         *systemdBusConnectionHandshake
	dispatcher        *systemdBusMessageDispatcher
	sequence          uint64
	nextSerial        uint32
	reservedSerial    map[uint32]struct{}
	sentSerial        map[uint32]struct{}
	beginWriteClaimed bool
	closed            bool
}

func newSystemdBusBootstrapSession(effectiveUID uint32) *systemdBusBootstrapSession {
	return &systemdBusBootstrapSession{
		handshake:  newSystemdBusConnectionHandshake(effectiveUID),
		nextSerial: 1, reservedSerial: make(map[uint32]struct{}), sentSerial: make(map[uint32]struct{}),
	}
}

func (b *systemdBusBootstrapSession) externalAuthCommand() ([]byte, error) {
	if b == nil {
		return nil, fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, fmt.Errorf("D-Bus bootstrap session is closed")
	}
	command, err := b.handshake.externalAuthCommand()
	if err != nil {
		return nil, b.failLocked(err)
	}
	return command, nil
}

// receiveAuthFrame accepts exactly one bounded CRLF-terminated SASL response
// and returns the BEGIN command. The transport remains responsible for writing
// that command on the same connection and then confirming completion.
func (b *systemdBusBootstrapSession) receiveAuthFrame(frame []byte) ([]byte, error) {
	if b == nil {
		return nil, fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, fmt.Errorf("D-Bus bootstrap session is closed")
	}
	line, err := decodeSystemdBusAuthLine(frame)
	if err != nil {
		return nil, b.failLocked(err)
	}
	if err := b.handshake.receiveExternalAuthReply(line); err != nil {
		return nil, b.failLocked(err)
	}
	command, err := b.handshake.beginCommand()
	if err != nil {
		return nil, b.failLocked(err)
	}
	return command, nil
}

func (b *systemdBusBootstrapSession) confirmBeginWritten() error {
	if b == nil {
		return fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("D-Bus bootstrap session is closed")
	}
	if err := b.handshake.confirmBeginWritten(); err != nil {
		return b.failLocked(err)
	}
	return nil
}

func (b *systemdBusBootstrapSession) registerHelloCall(serial uint32) (systemdBusBootstrapCall, error) {
	if b == nil {
		return systemdBusBootstrapCall{}, fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return systemdBusBootstrapCall{}, fmt.Errorf("D-Bus bootstrap session is closed")
	}
	if err := b.reserveSerialLocked(serial); err != nil {
		return systemdBusBootstrapCall{}, b.failLocked(err)
	}
	if err := b.handshake.registerHelloCall(serial); err != nil {
		return systemdBusBootstrapCall{}, b.failLocked(err)
	}
	return systemdBusBootstrapCall{
		Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath,
		Interface: systemdDBusInterface, Member: "Hello",
	}, nil
}

func (b *systemdBusBootstrapSession) registerAddMatchCall(serial uint32) (systemdBusBootstrapCall, error) {
	if b == nil {
		return systemdBusBootstrapCall{}, fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return systemdBusBootstrapCall{}, fmt.Errorf("D-Bus bootstrap session is closed")
	}
	if err := b.reserveSerialLocked(serial); err != nil {
		return systemdBusBootstrapCall{}, b.failLocked(err)
	}
	rule, err := b.handshake.registerAddMatchCall(serial)
	if err != nil {
		return systemdBusBootstrapCall{}, b.failLocked(err)
	}
	return systemdBusBootstrapCall{
		Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath,
		Interface: systemdDBusInterface, Member: "AddMatch",
		Signature: "s", StringArgs: []string{rule},
	}, nil
}

func (b *systemdBusBootstrapSession) registerGetNameOwnerCall(serial uint32) (systemdBusBootstrapCall, error) {
	if b == nil {
		return systemdBusBootstrapCall{}, fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return systemdBusBootstrapCall{}, fmt.Errorf("D-Bus bootstrap session is closed")
	}
	if err := b.reserveSerialLocked(serial); err != nil {
		return systemdBusBootstrapCall{}, b.failLocked(err)
	}
	name, err := b.handshake.registerGetNameOwnerCall(serial)
	if err != nil {
		return systemdBusBootstrapCall{}, b.failLocked(err)
	}
	return systemdBusBootstrapCall{
		Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath,
		Interface: systemdDBusInterface, Member: "GetNameOwner",
		Signature: "s", StringArgs: []string{name},
	}, nil
}

// prepareHelloCall allocates and reserves the connection-local serial before
// returning the exact encoded frame to the future writer.
func (b *systemdBusBootstrapSession) prepareHelloCall() (systemdBusPreparedCall, error) {
	return b.prepareBootstrapCall("Hello")
}

func (b *systemdBusBootstrapSession) prepareAddMatchCall() (systemdBusPreparedCall, error) {
	return b.prepareBootstrapCall("AddMatch")
}

func (b *systemdBusBootstrapSession) prepareGetNameOwnerCall() (systemdBusPreparedCall, error) {
	return b.prepareBootstrapCall("GetNameOwner")
}

func (b *systemdBusBootstrapSession) prepareBootstrapCall(member string) (systemdBusPreparedCall, error) {
	if b == nil {
		return systemdBusPreparedCall{}, fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return systemdBusPreparedCall{}, fmt.Errorf("D-Bus bootstrap session is closed")
	}
	serial, err := b.allocateSerialLocked()
	if err != nil {
		return systemdBusPreparedCall{}, b.failLocked(err)
	}
	var call systemdBusBootstrapCall
	switch member {
	case "Hello":
		err = b.handshake.registerHelloCall(serial)
		call = systemdBusBootstrapCall{Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath, Interface: systemdDBusInterface, Member: "Hello"}
	case "AddMatch":
		var rule string
		rule, err = b.handshake.registerAddMatchCall(serial)
		call = systemdBusBootstrapCall{Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath, Interface: systemdDBusInterface, Member: "AddMatch", Signature: "s", StringArgs: []string{rule}}
	case "GetNameOwner":
		var name string
		name, err = b.handshake.registerGetNameOwnerCall(serial)
		call = systemdBusBootstrapCall{Destination: systemdDBusDaemonName, Path: systemdDBusObjectPath, Interface: systemdDBusInterface, Member: "GetNameOwner", Signature: "s", StringArgs: []string{name}}
	default:
		err = fmt.Errorf("unsupported D-Bus bootstrap method %q", member)
	}
	if err != nil {
		return systemdBusPreparedCall{}, b.failLocked(err)
	}
	frame, err := encodeSystemdBusBootstrapCall(serial, call)
	if err != nil {
		return systemdBusPreparedCall{}, b.failLocked(err)
	}
	return systemdBusPreparedCall{Serial: serial, Call: call, Frame: frame}, nil
}

func (b *systemdBusBootstrapSession) claimBeginWrite() error {
	if b == nil {
		return fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.beginWriteClaimed || b.handshake.state != systemdBusHandshakeBeginPending {
		return b.failLocked(fmt.Errorf("D-Bus BEGIN write is unavailable or already claimed"))
	}
	b.beginWriteClaimed = true
	return nil
}

func (b *systemdBusBootstrapSession) claimPreparedBootstrapWrite(prepared systemdBusPreparedCall) error {
	if b == nil {
		return fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("D-Bus bootstrap session is closed")
	}
	if _, ok := b.reservedSerial[prepared.Serial]; !ok {
		return b.failLocked(fmt.Errorf("D-Bus prepared call serial was not reserved by this connection"))
	}
	if _, ok := b.sentSerial[prepared.Serial]; ok {
		return b.failLocked(fmt.Errorf("D-Bus prepared call cannot be sent more than once"))
	}
	valid := false
	switch prepared.Call.Member {
	case "Hello":
		valid = b.handshake.state == systemdBusHandshakeHelloPending && b.handshake.helloSerial == prepared.Serial
	case "AddMatch":
		valid = b.handshake.state == systemdBusHandshakeMatchPending && b.handshake.matchSerial == prepared.Serial
	case "GetNameOwner":
		valid = b.handshake.state == systemdBusHandshakeOwnerPending && b.handshake.ownerSerial == prepared.Serial
	}
	if !valid {
		return b.failLocked(fmt.Errorf("D-Bus prepared call does not match pending handshake transition"))
	}
	b.sentSerial[prepared.Serial] = struct{}{}
	return nil
}

func (b *systemdBusBootstrapSession) reserveSerialLocked(serial uint32) error {
	if serial == 0 {
		return fmt.Errorf("D-Bus outgoing serial zero is forbidden")
	}
	if _, exists := b.reservedSerial[serial]; exists {
		return fmt.Errorf("D-Bus outgoing serial was already reserved")
	}
	b.reservedSerial[serial] = struct{}{}
	return nil
}

func (b *systemdBusBootstrapSession) allocateSerialLocked() (uint32, error) {
	for b.nextSerial != 0 {
		serial := b.nextSerial
		if serial == math.MaxUint32 {
			b.nextSerial = 0
		} else {
			b.nextSerial++
		}
		if _, exists := b.reservedSerial[serial]; exists {
			continue
		}
		b.reservedSerial[serial] = struct{}{}
		return serial, nil
	}
	return 0, fmt.Errorf("D-Bus outgoing serial space exhausted")
}

// dispatchDecodedMessage is the only binary-message entrypoint. Every frame
// advances one sequence counter, including unrelated messages during
// bootstrap. After GetNameOwner succeeds, the same sequence is handed to the
// start dispatcher; no second reader/counter is introduced.
func (b *systemdBusBootstrapSession) dispatchDecodedMessage(message systemdBusDecodedMessage) (systemdBusBootstrapResult, error) {
	if b == nil {
		return systemdBusBootstrapResult{}, fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return systemdBusBootstrapResult{}, fmt.Errorf("D-Bus bootstrap session is closed")
	}
	if b.sequence == math.MaxUint64 {
		return systemdBusBootstrapResult{}, b.failLocked(fmt.Errorf("D-Bus bootstrap receive sequence exhausted"))
	}
	b.sequence++
	result := systemdBusBootstrapResult{HandshakeState: b.handshake.state}
	switch b.handshake.state {
	case systemdBusHandshakeHelloPending:
		if message.replySerial == b.handshake.helloSerial {
			if err := b.handshake.acceptHelloReply(message); err != nil {
				return result, b.failLocked(err)
			}
		}
	case systemdBusHandshakeMatchPending:
		if message.replySerial == b.handshake.matchSerial {
			if err := b.handshake.acceptAddMatchReply(message); err != nil {
				return result, b.failLocked(err)
			}
		}
	case systemdBusHandshakeMatchInstalled, systemdBusHandshakeOwnerPending:
		relevant, err := b.handshake.observeHandshakeMessage(message)
		if err != nil {
			return result, b.failLocked(err)
		}
		result.Relevant = relevant
		if b.handshake.state == systemdBusHandshakeOwnerPending &&
			message.replySerial == b.handshake.ownerSerial {
			owner, err := b.handshake.acceptGetNameOwnerReply(message)
			if err != nil {
				return result, b.failLocked(err)
			}
			dispatcher, err := newSystemdBusMessageDispatcher(owner)
			if err != nil {
				return result, b.failLocked(err)
			}
			b.dispatcher = dispatcher
		}
	case systemdBusHandshakeReady:
		if b.dispatcher == nil {
			return result, b.failLocked(fmt.Errorf("ready D-Bus bootstrap has no start dispatcher"))
		}
		ack, relevant, err := b.dispatcher.dispatchDecodedMessage(b.sequence, message)
		result.Acknowledgement, result.Relevant = ack, relevant
		if err != nil {
			return result, b.failLocked(err)
		}
	default:
		return result, b.failLocked(fmt.Errorf("D-Bus binary message arrived before BEGIN/Hello or after handshake termination"))
	}
	result.HandshakeState = b.handshake.state
	result.Ready = b.handshake.isReady() && b.dispatcher != nil
	return result, nil
}

func (b *systemdBusBootstrapSession) registerStartMethodCall(serial uint32, request systemdStartRequest) error {
	if b == nil {
		return fmt.Errorf("D-Bus bootstrap session is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("D-Bus bootstrap session is closed")
	}
	if !b.handshake.isReady() || b.dispatcher == nil {
		return b.failLocked(fmt.Errorf("systemd start call is forbidden before verified D-Bus bootstrap"))
	}
	if err := b.reserveSerialLocked(serial); err != nil {
		return b.failLocked(err)
	}
	if err := b.dispatcher.registerStartMethodCall(serial, request); err != nil {
		return b.failLocked(err)
	}
	return nil
}

func (b *systemdBusBootstrapSession) failLocked(err error) error {
	b.closed = true
	if b.handshake != nil {
		b.handshake.close()
	}
	if b.dispatcher != nil {
		b.dispatcher.close()
	}
	return err
}

func (b *systemdBusBootstrapSession) close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.failLocked(fmt.Errorf("D-Bus bootstrap connection closed"))
}

func (b *systemdBusBootstrapSession) isReady() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed && b.handshake.isReady() && b.dispatcher != nil
}
