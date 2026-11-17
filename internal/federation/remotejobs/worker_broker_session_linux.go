//go:build linux

package remotejobs

import (
	"fmt"
	"net"
	"time"
)

// admitWorkerBrokerSession is the final in-process admission check after
// kernel peer authentication and strict request decoding. expected must come
// from the receiver's durable one-shot reservation, never from the connection.
func admitWorkerBrokerSession(identity BrokerPeerIdentity, policy BrokerPeerPolicy, expected systemdStartRequest, received workerBrokerStartRequest) error {
	if identity.PID <= 0 || identity.UID != policy.AllowedUID || identity.StartTimeTicks == 0 || identity.PIDFD < 0 {
		return fmt.Errorf("worker broker kernel peer identity is incomplete or unauthorized")
	}
	if policy.AllowedExecutableSHA256 == "" || identity.ExecutableSHA256 != policy.AllowedExecutableSHA256 || !digestPattern.MatchString(identity.ExecutableSHA256) {
		return fmt.Errorf("worker broker executable identity is unauthorized")
	}
	if _, err := workerBrokerRequestFromStart(expected); err != nil {
		return fmt.Errorf("receiver start reservation is invalid: %w", err)
	}
	if err := received.validate(); err != nil {
		return fmt.Errorf("worker broker request is invalid: %w", err)
	}
	if !received.matchesStartRequest(expected) {
		return fmt.Errorf("worker broker request does not match receiver start reservation")
	}
	return nil
}

// ServeAuthenticatedWorkerBrokerConnection owns one already-accepted Unix
// connection. It authenticates SO_PEERCRED/SO_PEERPIDFD and the peer executable
// before reading a request, then compares the request against expected, which
// must be loaded from the receiver's durable one-shot reservation. It does not
// create a listener, authorize a new reservation, transfer descriptors, call
// systemd, or launch work.
func ServeAuthenticatedWorkerBrokerConnection(conn *net.UnixConn, peerPolicy BrokerPeerPolicy, expected systemdStartRequest, ioTimeout time.Duration, handler func(BrokerPeerIdentity, workerBrokerStartRequest) error) error {
	if conn == nil {
		return fmt.Errorf("worker broker connection is required")
	}
	defer conn.Close()
	if handler == nil {
		return fmt.Errorf("worker broker admission handler is required")
	}
	peer, err := AuthenticateBrokerPeer(conn, peerPolicy)
	if err != nil {
		return err
	}
	defer peer.Close()
	request, err := readWorkerBrokerStartRequest(conn, ioTimeout)
	if err != nil {
		return err
	}
	if err := admitWorkerBrokerSession(peer, peerPolicy, expected, request); err != nil {
		return err
	}
	return handler(peer, request)
}
