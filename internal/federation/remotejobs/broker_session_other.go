//go:build !linux

package remotejobs

import (
	"fmt"
	"net"
	"time"
)

func ServeAuthenticatedBrokerConnection(*net.UnixConn, BrokerPeerPolicy, time.Duration, func(BrokerPeerIdentity, BrokerStartRequest) error) error {
	return fmt.Errorf("authenticated broker connections are supported only on Linux")
}
