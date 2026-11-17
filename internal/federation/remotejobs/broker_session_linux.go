//go:build linux

package remotejobs

import (
	"fmt"
	"net"
	"time"
)

// ServeAuthenticatedBrokerConnection owns exactly one accepted connection:
// authenticate the kernel peer, retain its pidfd through request handling,
// decode one bounded request, invoke the receiver-owned handler, then close
// both pidfd and connection. It does not create a listener or launch work.
func ServeAuthenticatedBrokerConnection(conn *net.UnixConn, peerPolicy BrokerPeerPolicy, ioTimeout time.Duration, handler func(BrokerPeerIdentity, BrokerStartRequest) error) error {
	if conn == nil {
		return fmt.Errorf("broker connection is required")
	}
	defer conn.Close()
	if handler == nil {
		return fmt.Errorf("broker request handler is required")
	}
	identity, err := AuthenticateBrokerPeer(conn, peerPolicy)
	if err != nil {
		return err
	}
	defer identity.Close()
	req, err := ReadBrokerStartRequest(conn, ioTimeout)
	if err != nil {
		return err
	}
	if err := handler(identity, req); err != nil {
		return err
	}
	return nil
}
