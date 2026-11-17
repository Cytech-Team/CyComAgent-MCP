//go:build linux

package remotejobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// BrokerPeerPolicy is receiver-owned authorization state. The caller must not
// be able to populate or override either field from a broker request.
type BrokerPeerPolicy struct {
	AllowedUID              uint32
	AllowedExecutableSHA256 string
}

// BrokerPeerIdentity records kernel-authenticated socket credentials and the
// executable identity observed for that process. StartTimeTicks helps detect
// PID reuse while /proc is inspected; it is not a durable process identifier.
type BrokerPeerIdentity struct {
	PID              int
	UID              uint32
	GID              uint32
	StartTimeTicks   uint64
	ExecutableSHA256 string
	// PIDFD is owned by the caller and must be closed with Close. Keeping it open
	// pins the kernel process identity and prevents PID reuse.
	PIDFD int
}

// Close releases the pidfd returned by AuthenticateBrokerPeer.
func (identity *BrokerPeerIdentity) Close() error {
	if identity == nil || identity.PIDFD < 0 {
		return nil
	}
	err := syscall.Close(identity.PIDFD)
	identity.PIDFD = -1
	return err
}

// AuthenticateBrokerPeer checks SO_PEERCRED and hashes the connected peer's
// running executable through /proc. It intentionally does not create a
// listener or make authorization decisions from request-supplied fields.
func AuthenticateBrokerPeer(conn *net.UnixConn, policy BrokerPeerPolicy) (BrokerPeerIdentity, error) {
	var identity BrokerPeerIdentity
	if conn == nil {
		return identity, fmt.Errorf("broker peer connection is required")
	}
	if policy.AllowedExecutableSHA256 == "" || !digestPattern.MatchString(policy.AllowedExecutableSHA256) {
		return identity, fmt.Errorf("broker peer executable digest policy is invalid")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return identity, fmt.Errorf("access broker peer socket: %w", err)
	}
	var cred *syscall.Ucred
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		cred, controlErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return identity, fmt.Errorf("read broker peer credentials: %w", err)
	}
	if controlErr != nil {
		return identity, fmt.Errorf("read broker peer credentials: %w", controlErr)
	}
	if cred == nil || cred.Pid <= 0 {
		return identity, fmt.Errorf("broker peer PID is invalid")
	}
	if cred.Uid != policy.AllowedUID {
		return identity, fmt.Errorf("broker peer UID is not authorized")
	}

	pid := int(cred.Pid)
	pidfd, err := peerPIDFD(raw)
	if err != nil {
		return identity, fmt.Errorf("pin broker peer process identity: %w", err)
	}
	identity.PIDFD = pidfd
	keepPIDFD := false
	defer func() {
		if !keepPIDFD {
			_ = syscall.Close(pidfd)
		}
	}()
	startBefore, err := processStartTimeTicks(pid)
	if err != nil {
		return BrokerPeerIdentity{}, fmt.Errorf("inspect broker peer process: %w", err)
	}
	f, err := os.Open(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return identity, fmt.Errorf("open broker peer executable: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return identity, fmt.Errorf("stat broker peer executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return identity, fmt.Errorf("broker peer executable is not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return identity, fmt.Errorf("hash broker peer executable: %w", err)
	}
	startAfter, err := processStartTimeTicks(pid)
	if err != nil || startAfter != startBefore {
		return identity, fmt.Errorf("broker peer process changed during authentication")
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if digest != policy.AllowedExecutableSHA256 {
		return identity, fmt.Errorf("broker peer executable is not authorized")
	}
	identity = BrokerPeerIdentity{PID: pid, UID: cred.Uid, GID: cred.Gid, StartTimeTicks: startBefore, ExecutableSHA256: digest, PIDFD: pidfd}
	keepPIDFD = true
	return identity, nil
}

// peerPIDFD obtains a pidfd from the socket itself where supported. This is
// stronger than pidfd_open(SO_PEERCRED.PID), which has a PID-reuse window
// between accept and pidfd_open. Older kernels fail closed rather than using
// that racy fallback.
func peerPIDFD(raw syscall.RawConn) (int, error) {
	const soPeerPIDFD = 77
	var pidfd int = -1
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		pidfd, controlErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, soPeerPIDFD)
	}); err != nil {
		return -1, err
	}
	if controlErr != nil {
		return -1, fmt.Errorf("SO_PEERPIDFD unavailable: %w", controlErr)
	}
	if pidfd < 0 {
		return -1, fmt.Errorf("kernel returned invalid peer pidfd")
	}
	return pidfd, nil
}

func processStartTimeTicks(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// comm (field 2) is parenthesized and may itself contain spaces or ')'.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 || end+1 >= len(data) {
		return 0, fmt.Errorf("malformed proc stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	// The first token is field 3 (state); starttime is field 22.
	if len(fields) <= 19 {
		return 0, fmt.Errorf("proc stat lacks start time")
	}
	value, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("proc stat start time is invalid")
	}
	return value, nil
}
