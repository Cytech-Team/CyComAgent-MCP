//go:build linux

package remotejobs

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const systemdDBusWellKnownName = "org.freedesktop.systemd1"

// systemdBusOwnerPin is immutable evidence scoped to one D-Bus connection
// generation. It is held only by systemdBusOwnerSession, never copied into a
// method-reply object supplied by a caller.
type systemdBusOwnerPin struct {
	connectionID  string
	wellKnown     string
	uniqueOwner   string
	acquiredAt    time.Time
	invalidated   bool
	invalidatedAt time.Time
}

// systemdBusOwnerSession serializes owner-change observations and method reply
// classification for one live connection. A reconnect requires a new session.
type systemdBusOwnerSession struct {
	mu  sync.Mutex
	pin systemdBusOwnerPin
}

// newSystemdBusOwnerSession consumes the unique owner and timestamp returned
// by a future transport's GetNameOwner call. The connection generation nonce
// is generated internally. This function performs no D-Bus I/O.
func newSystemdBusOwnerSession(wellKnown, uniqueOwner string, acquiredAt time.Time) (*systemdBusOwnerSession, error) {
	if wellKnown != systemdDBusWellKnownName || !validDBusUniqueName(uniqueOwner) || acquiredAt.IsZero() || acquiredAt.After(time.Now().Add(time.Minute)) {
		return nil, fmt.Errorf("systemd bus owner observation is invalid")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("generate systemd bus connection identity: %w", err)
	}
	return &systemdBusOwnerSession{pin: systemdBusOwnerPin{
		connectionID: hex.EncodeToString(nonce[:]), wellKnown: wellKnown,
		uniqueOwner: uniqueOwner, acquiredAt: acquiredAt,
	}}, nil
}

// observeNameOwnerChanged permanently invalidates this connection generation
// on any matching owner change or service disappearance.
func (s *systemdBusOwnerSession) observeNameOwnerChanged(name, oldOwner, newOwner string, observedAt time.Time) error {
	if s == nil {
		return fmt.Errorf("systemd bus owner session is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pin := s.pin
	if pin.connectionID == "" || pin.invalidated || name != pin.wellKnown || oldOwner != pin.uniqueOwner || observedAt.IsZero() || observedAt.Before(pin.acquiredAt) {
		return fmt.Errorf("systemd bus owner-change observation does not match active pin")
	}
	if newOwner == oldOwner {
		return fmt.Errorf("systemd bus owner-change observation did not change owner")
	}
	s.pin.invalidated = true
	s.pin.invalidatedAt = observedAt
	return nil
}

// close invalidates the connection generation on transport shutdown. The
// session cannot be reopened; a reconnect must construct a fresh session.
func (s *systemdBusOwnerSession) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pin.invalidated {
		s.pin.invalidated = true
		s.pin.invalidatedAt = time.Now().UTC()
	}
}

func (s *systemdBusOwnerSession) validateReplyLocked(sender string, receivedAt time.Time) error {
	pin := s.pin
	if pin.connectionID == "" || pin.wellKnown != systemdDBusWellKnownName || !validDBusUniqueName(pin.uniqueOwner) || pin.invalidated {
		return fmt.Errorf("systemd bus owner pin is absent or invalidated")
	}
	if sender != pin.uniqueOwner {
		return fmt.Errorf("systemd D-Bus reply sender differs from pinned owner")
	}
	if receivedAt.IsZero() || receivedAt.Before(pin.acquiredAt) || receivedAt.After(time.Now().Add(time.Minute)) {
		return fmt.Errorf("systemd bus reply timestamp is outside pinned owner window")
	}
	return nil
}
