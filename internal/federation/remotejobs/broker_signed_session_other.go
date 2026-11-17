//go:build !linux

package remotejobs

import (
	"fmt"
	"net"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

func ServeAuthenticatedSignedBrokerConnection(*net.UnixConn, BrokerPeerPolicy, *nodeidentity.Manager, time.Duration, func(BrokerPeerIdentity, FederationCallerContext, BrokerStartRequest) error) error {
	return fmt.Errorf("signed execution broker sessions are unsupported on this platform")
}

func ServeBrokerReceiverConnection(*net.UnixConn, BrokerPeerPolicy, *BrokerReceiverHandler, time.Duration) error {
	return fmt.Errorf("execution broker receiver is unsupported on this platform")
}
