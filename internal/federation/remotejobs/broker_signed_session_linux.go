//go:build linux

package remotejobs

import (
	"fmt"
	"net"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

// ServeAuthenticatedSignedBrokerConnection independently authenticates the
// local kernel peer and Federation signer, retaining the kernel pidfd through
// the handler. It handles one request and never dispatches work itself.
func ServeAuthenticatedSignedBrokerConnection(conn *net.UnixConn, peerPolicy BrokerPeerPolicy, receiver *nodeidentity.Manager, ioTimeout time.Duration, handler func(BrokerPeerIdentity, FederationCallerContext, BrokerStartRequest) error) error {
	if conn == nil {
		return fmt.Errorf("broker connection is required")
	}
	defer conn.Close()
	if handler == nil {
		return fmt.Errorf("broker request handler is required")
	}
	peer, err := AuthenticateBrokerPeer(conn, peerPolicy)
	if err != nil {
		return err
	}
	defer peer.Close()
	caller, req, err := ReadSignedBrokerStartEnvelope(conn, receiver, ioTimeout)
	if err != nil {
		return err
	}
	return handler(peer, caller, req)
}

// ServeBrokerReceiverConnection is the complete pre-dispatch receiver path:
// kernel peer authentication -> Federation signature/trust verification ->
// receiver peer admission -> opaque reference resolution -> durable reserve.
// It never changes the record to dispatching and never launches a process.
func ServeBrokerReceiverConnection(conn *net.UnixConn, peerPolicy BrokerPeerPolicy, handler *BrokerReceiverHandler, ioTimeout time.Duration) error {
	if handler == nil {
		return fmt.Errorf("broker receiver handler is required")
	}
	if handler.DescriptorResolver == nil {
		return fmt.Errorf("descriptor-bound broker reference resolver is required")
	}
	return ServeAuthenticatedSignedBrokerConnection(conn, peerPolicy, handler.Identity, ioTimeout, func(peer BrokerPeerIdentity, caller FederationCallerContext, req BrokerStartRequest) error {
		authorization, err := handler.HandleDescriptorBound(peer, caller, req)
		if err != nil {
			return err
		}
		return authorization.Close()
	})
}
