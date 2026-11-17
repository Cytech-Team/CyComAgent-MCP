//go:build !linux

package remotejobs

import (
	"fmt"
	"net"
)

type BrokerPeerPolicy struct {
	AllowedUID              uint32
	AllowedExecutableSHA256 string
	PIDFD                   int
}

type BrokerPeerIdentity struct {
	PID              int
	UID              uint32
	GID              uint32
	StartTimeTicks   uint64
	ExecutableSHA256 string
	PIDFD            int
}

func AuthenticateBrokerPeer(_ *net.UnixConn, _ BrokerPeerPolicy) (BrokerPeerIdentity, error) {
	return BrokerPeerIdentity{}, fmt.Errorf("execution broker peer authentication is supported only on Linux")
}
