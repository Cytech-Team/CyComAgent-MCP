//go:build linux

package remotejobs

import (
	"fmt"
	"sync"
)

// systemdBusMessageDispatcher is a protocol-only ordering boundary for a
// future single-reader D-Bus transport. The transport assigns monotonically
// increasing receive sequence numbers to every decoded message before
// forwarding relevant messages here. Gaps are allowed for unrelated messages;
// duplicates and regressions permanently close this generation.
//
// This type does not read a socket, decode D-Bus wire data, subscribe to
// signals, or invoke a method. A future transport must call it from one ordered
// reader/dispatcher, not from independent signal and reply goroutines.
type systemdBusMessageDispatcher struct {
	mu              sync.Mutex
	session         *systemdBusOwnerSession
	lastSeq         uint64
	closed          bool
	startCallSerial uint32
	startRequest    systemdStartRequest
	startCallSet    bool
	startReplySeen  bool
}

func newSystemdBusMessageDispatcher(session *systemdBusOwnerSession) (*systemdBusMessageDispatcher, error) {
	if session == nil {
		return nil, fmt.Errorf("systemd bus owner session is required")
	}
	return &systemdBusMessageDispatcher{session: session}, nil
}

// acceptSequenceLocked rejects replayed or reordered receive events. A
// sequence gap is valid because the reader may omit unrelated D-Bus messages.
func (d *systemdBusMessageDispatcher) acceptSequenceLocked(sequence uint64) error {
	if d.closed || d.session == nil {
		return fmt.Errorf("systemd bus message dispatcher is closed")
	}
	if sequence == 0 || sequence <= d.lastSeq {
		d.closed = true
		d.session.close()
		return fmt.Errorf("systemd bus receive sequence is zero, duplicated, or reordered")
	}
	d.lastSeq = sequence
	return nil
}

// registerStartMethodCall binds the D-Bus serial allocated by the future
// connection writer to the one durable start request. It must be called once,
// before the writer sends the method call. The serial is connection-local and
// is never accepted from a job caller or remote peer.
func (d *systemdBusMessageDispatcher) registerStartMethodCall(serial uint32, request systemdStartRequest) error {
	if d == nil {
		return fmt.Errorf("systemd bus message dispatcher is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.session == nil || d.startCallSet || serial == 0 {
		return fmt.Errorf("systemd start method call cannot be registered")
	}
	if request.Version != systemdStartProtocolVersion || request.StartAttemptID == "" || request.UnitName == "" {
		return fmt.Errorf("systemd start request identity is incomplete")
	}
	d.startCallSerial = serial
	d.startRequest = request
	d.startCallSet = true
	return nil
}

// dispatchNameOwnerChanged applies a decoded NameOwnerChanged event in the
// exact order supplied by the transport reader. Any mismatch fails closed.
func (d *systemdBusMessageDispatcher) dispatchNameOwnerChanged(sequence uint64, event systemdBusOwnerChangedEvent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.acceptSequenceLocked(sequence); err != nil {
		return err
	}
	return d.applyNameOwnerChangedLocked(event)
}

func (d *systemdBusMessageDispatcher) applyNameOwnerChangedLocked(event systemdBusOwnerChangedEvent) error {
	if event.name == "" || event.receivedAt.IsZero() {
		d.closed = true
		d.session.close()
		return fmt.Errorf("invalid decoded owner-change event")
	}
	if err := d.session.observeNameOwnerChanged(event.name, event.oldOwner, event.newOwner, event.receivedAt); err != nil {
		d.closed = true
		d.session.close()
		return err
	}
	return nil
}

// dispatchDecodedMessage is the single ordered classification entrypoint for
// a future reader. Every decoded frame, including unrelated bus traffic,
// advances the same receive sequence. Only a NameOwnerChanged signal for the
// pinned systemd name or a reply correlated to the one registered start call
// is acted upon. Unrelated messages are ignored after sequence acceptance.
// The bool reports whether the message was relevant to this start protocol.
func (d *systemdBusMessageDispatcher) dispatchDecodedMessage(sequence uint64, message systemdBusDecodedMessage) (systemdStartAcknowledgement, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.acceptSequenceLocked(sequence); err != nil {
		if d.startCallSet {
			ack, ackErr := newSystemdStartAcknowledgement(d.startRequest, "unknown", message.receivedAt)
			if ackErr != nil {
				return systemdStartAcknowledgement{}, false, ackErr
			}
			return ack, true, nil
		}
		return systemdStartAcknowledgement{}, false, err
	}
	if isSystemdNameOwnerChangedEnvelope(message) {
		event, err := decodeSystemdNameOwnerChanged(message)
		if err != nil {
			d.closed = true
			d.session.close()
			return systemdStartAcknowledgement{}, true, err
		}
		if event.name != systemdDBusWellKnownName {
			return systemdStartAcknowledgement{}, false, nil
		}
		return systemdStartAcknowledgement{}, true, d.applyNameOwnerChangedLocked(event)
	}
	if d.startCallSet && message.replySerial != 0 && message.replySerial == d.startCallSerial {
		ack, err := d.dispatchStartMethodReplyLocked(message)
		return ack, true, err
	}
	return systemdStartAcknowledgement{}, false, nil
}

func isSystemdNameOwnerChangedEnvelope(message systemdBusDecodedMessage) bool {
	return message.messageType == "signal" && message.sender == systemdDBusDaemonName &&
		message.destination == "" && message.path == systemdDBusObjectPath &&
		message.interfaceName == systemdDBusInterface && message.member == "NameOwnerChanged"
}

// dispatchStartMethodReply remains a focused test/helper entrypoint. A real
// reader must use dispatchDecodedMessage so unrelated frames also advance the
// receive sequence.
func (d *systemdBusMessageDispatcher) dispatchStartMethodReply(sequence uint64, message systemdBusDecodedMessage) (systemdStartAcknowledgement, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.acceptSequenceLocked(sequence); err != nil {
		if !d.startCallSet {
			return systemdStartAcknowledgement{}, err
		}
		return newSystemdStartAcknowledgement(d.startRequest, "unknown", message.receivedAt)
	}
	return d.dispatchStartMethodReplyLocked(message)
}

func (d *systemdBusMessageDispatcher) dispatchStartMethodReplyLocked(message systemdBusDecodedMessage) (systemdStartAcknowledgement, error) {
	reply, decodeErr := decodeSystemdStartMethodReply(message)
	if decodeErr != nil {
		d.closed = true
		d.session.close()
		if !d.startCallSet {
			return systemdStartAcknowledgement{}, decodeErr
		}
		return newSystemdStartAcknowledgement(d.startRequest, "unknown", message.receivedAt)
	}
	if !d.startCallSet || d.startReplySeen || reply.ReplySerial == 0 || reply.ReplySerial != d.startCallSerial {
		d.closed = true
		d.session.close()
		if !d.startCallSet {
			return systemdStartAcknowledgement{}, fmt.Errorf("systemd method reply has no registered start call")
		}
		return newSystemdStartAcknowledgement(d.startRequest, "unknown", reply.ReceivedAt)
	}
	d.startReplySeen = true
	ack, err := acknowledgementFromSystemdMethodReply(d.session, d.startRequest, reply)
	if err != nil {
		d.closed = true
		d.session.close()
		return systemdStartAcknowledgement{}, err
	}
	if ack.Outcome != "accepted" {
		d.closed = true
		d.session.close()
	}
	return ack, nil
}

// close permanently ends the connection generation. A reconnect requires a
// fresh owner session and dispatcher.
func (d *systemdBusMessageDispatcher) close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.closed = true
		d.session.close()
	}
}
