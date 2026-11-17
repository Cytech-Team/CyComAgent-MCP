//go:build linux

package remotejobs

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

const brokerDescriptorTransferMarker byte = 0xD7

// SendBrokerDescriptors sends exactly the executable and working-directory
// descriptors over an already-authenticated Unix connection. Sending does not
// relinquish or close the sender's descriptors; the receiver gets new owned
// descriptors. This function does not create or accept connections.
func SendBrokerDescriptors(conn *net.UnixConn, executable, workingDirectory *os.File) error {
	if conn == nil || executable == nil || workingDirectory == nil {
		return fmt.Errorf("connected Unix socket and both descriptors are required")
	}
	if executable == workingDirectory {
		return fmt.Errorf("executable and working-directory descriptors must be distinct")
	}
	if info, err := executable.Stat(); err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return fmt.Errorf("executable descriptor is not a regular executable file")
	}
	if info, err := workingDirectory.Stat(); err != nil || !info.IsDir() {
		return fmt.Errorf("working-directory descriptor is not a directory")
	}
	fds := []int{int(executable.Fd()), int(workingDirectory.Fd())}
	if fds[0] < 0 || fds[1] < 0 || fds[0] == fds[1] {
		return fmt.Errorf("descriptor numbers are invalid or aliased")
	}
	n, oobn, err := conn.WriteMsgUnix([]byte{brokerDescriptorTransferMarker}, syscall.UnixRights(fds...), nil)
	if err != nil {
		return fmt.Errorf("send broker descriptors: %w", err)
	}
	if n != 1 || oobn == 0 {
		return fmt.Errorf("incomplete broker descriptor transfer")
	}
	return nil
}

// ReceiveBrokerDescriptors receives exactly two SCM_RIGHTS descriptors and
// takes ownership of them. MSG_CMSG_CLOEXEC ensures they cannot leak through a
// later exec. Every descriptor received on an invalid frame is closed.
func ReceiveBrokerDescriptors(conn *net.UnixConn) (executable, workingDirectory *os.File, err error) {
	if conn == nil {
		return nil, nil, fmt.Errorf("connected Unix socket is required")
	}
	payload := make([]byte, 1)
	oob := make([]byte, syscall.CmsgSpace(2*4)+syscall.CmsgSpace(4))
	raw, rawErr := conn.SyscallConn()
	if rawErr != nil {
		return nil, nil, fmt.Errorf("access broker socket descriptor: %w", rawErr)
	}
	var n, oobn, flags int
	var readErr error
	if err := raw.Read(func(fd uintptr) bool {
		for {
			n, oobn, flags, _, readErr = syscall.Recvmsg(int(fd), payload, oob, syscall.MSG_CMSG_CLOEXEC)
			if readErr == syscall.EINTR {
				continue
			}
			if readErr == syscall.EAGAIN || readErr == syscall.EWOULDBLOCK {
				return false
			}
			return true
		}
	}); err != nil {
		return nil, nil, fmt.Errorf("wait for broker descriptor frame: %w", err)
	}
	if readErr != nil {
		return nil, nil, fmt.Errorf("receive broker descriptors: %w", readErr)
	}
	var received []int
	msgs, parseErr := syscall.ParseSocketControlMessage(oob[:oobn])
	if parseErr == nil {
		for _, msg := range msgs {
			if msg.Header.Level != syscall.SOL_SOCKET || msg.Header.Type != syscall.SCM_RIGHTS {
				parseErr = fmt.Errorf("unexpected ancillary message")
				continue
			}
			fds, fdErr := syscall.ParseUnixRights(&msg)
			if fdErr == nil {
				received = append(received, fds...)
			} else if msg.Header.Level == syscall.SOL_SOCKET && msg.Header.Type == syscall.SCM_RIGHTS {
				parseErr = fdErr
			}
		}
	}
	closeReceived := func() {
		for _, fd := range received {
			_ = syscall.Close(fd)
		}
	}
	if flags&(syscall.MSG_CTRUNC|syscall.MSG_TRUNC) != 0 || n != 1 || payload[0] != brokerDescriptorTransferMarker || parseErr != nil || len(msgs) != 1 || len(received) != 2 {
		closeReceived()
		return nil, nil, fmt.Errorf("invalid broker descriptor transfer frame")
	}
	files := []*os.File{os.NewFile(uintptr(received[0]), "broker-executable"), os.NewFile(uintptr(received[1]), "broker-working-directory")}
	if files[0] == nil || files[1] == nil {
		closeReceived()
		return nil, nil, fmt.Errorf("received invalid descriptor")
	}
	if info, statErr := files[0].Stat(); statErr != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		_ = files[0].Close()
		_ = files[1].Close()
		return nil, nil, fmt.Errorf("received executable descriptor is not executable")
	}
	if info, statErr := files[1].Stat(); statErr != nil || !info.IsDir() {
		_ = files[0].Close()
		_ = files[1].Close()
		return nil, nil, fmt.Errorf("received working-directory descriptor is not a directory")
	}
	for _, fd := range received {
		flags, _, flagErr := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFD), 0)
		if flagErr != 0 || int(flags)&syscall.FD_CLOEXEC == 0 {
			_ = files[0].Close()
			_ = files[1].Close()
			return nil, nil, fmt.Errorf("received descriptor is not close-on-exec")
		}
	}
	return files[0], files[1], nil
}

// SendVerifiedWorkerDescriptors sends a revalidated read-only artifact
// descriptor plus the already-authorized working-directory descriptor. The
// temporary duplicate is closed after send; SCM_RIGHTS gives the receiver its
// own reference. This is a handoff primitive, not a launch operation.
func SendVerifiedWorkerDescriptors(conn *net.UnixConn, executable *VerifiedExecutableArtifact, workingDirectory *os.File) error {
	if executable == nil || workingDirectory == nil {
		return fmt.Errorf("verified executable capability and working directory are required")
	}
	duplicate, err := executable.DuplicateReadOnlyDescriptor()
	if err != nil {
		return err
	}
	defer duplicate.Close()
	return SendBrokerDescriptors(conn, duplicate, workingDirectory)
}

// ReceiveVerifiedWorkerDescriptors receives the fixed two-FD frame and binds
// the executable descriptor to the receiver's independently loaded registry
// record. Both descriptors are closed on any validation failure.
func ReceiveVerifiedWorkerDescriptors(conn *net.UnixConn, record ExecutableArtifactRecord) (executable, workingDirectory *os.File, err error) {
	executable, workingDirectory, err = ReceiveBrokerDescriptors(conn)
	if err != nil {
		return nil, nil, err
	}
	if err := VerifyTransferredExecutableDescriptor(executable, record); err != nil {
		_ = executable.Close()
		_ = workingDirectory.Close()
		return nil, nil, err
	}
	return executable, workingDirectory, nil
}
