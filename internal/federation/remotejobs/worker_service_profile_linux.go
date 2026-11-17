//go:build linux

package remotejobs

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// WorkerServiceProfile is a broker-owned, immutable description of the system
// service boundary for one remote job. It is deliberately only data: building
// it never contacts systemd, creates a unit, or launches a process.
type WorkerServiceProfile struct {
	User              string
	Group             string
	Properties        []string
	InaccessiblePaths []string
}

// BuildWorkerServiceProfile creates the fixed baseline for a dedicated worker
// identity. UID/GID and timeout must come from broker-local configuration and
// validated policy, never from BrokerStartRequest. A caller cannot supply or
// override systemd properties through the broker protocol.
func BuildWorkerServiceProfile(workerUID, workerGID uint32, timeout time.Duration, rootBrokerSocketPath string) (WorkerServiceProfile, error) {
	if workerUID == 0 || workerGID == 0 {
		return WorkerServiceProfile{}, fmt.Errorf("dedicated non-root worker UID and GID are required")
	}
	if timeout <= 0 || timeout > 24*time.Hour {
		return WorkerServiceProfile{}, fmt.Errorf("worker runtime limit is outside supported bounds")
	}
	if !filepath.IsAbs(rootBrokerSocketPath) || filepath.Clean(rootBrokerSocketPath) != rootBrokerSocketPath || filepath.Base(rootBrokerSocketPath) != "root.sock" || strings.ContainsAny(rootBrokerSocketPath, " \t\r\n") {
		return WorkerServiceProfile{}, fmt.Errorf("trusted root-broker socket path must be a clean absolute root.sock path without whitespace")
	}
	properties := []string{
		"KillMode=control-group",
		"SendSIGKILL=yes",
		"TimeoutStopSec=250ms",
		"RuntimeMaxSec=" + timeout.String(),
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ProtectHome=yes",
		"PrivateTmp=yes",
		"PrivateDevices=yes",
		"PrivateNetwork=yes",
		"ProtectProc=invisible",
		"ProcSubset=pid",
		"RestrictNamespaces=yes",
		"RestrictAddressFamilies=AF_UNIX",
		"DevicePolicy=closed",
		"ProtectClock=yes",
		"ProtectHostname=yes",
		"ProtectKernelTunables=yes",
		"ProtectKernelModules=yes",
		"ProtectKernelLogs=yes",
		"ProtectControlGroups=yes",
		"RestrictSUIDSGID=yes",
		"RestrictRealtime=yes",
		"LockPersonality=yes",
		"CapabilityBoundingSet=",
		"AmbientCapabilities=",
		"UMask=0077",
		"StandardInput=null",
		// Defense in depth: the system manager must not pass session-bus
		// routing variables into the worker even if its manager environment
		// was contaminated by an administrator's global environment.
		"UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR",
	}
	return WorkerServiceProfile{
		User: strconv.FormatUint(uint64(workerUID), 10), Group: strconv.FormatUint(uint64(workerGID), 10),
		Properties:        properties,
		InaccessiblePaths: []string{"/run/user", "/run/dbus/system_bus_socket", rootBrokerSocketPath},
	}, nil
}
