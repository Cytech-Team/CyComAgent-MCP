//go:build linux

package sessionbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultExecTimeout = 120 * time.Second
	defaultOutputLimit = 2 << 20
	maxOutputLimit     = 10 << 20
)

type Client struct {
	Socket string
}

func DefaultSocketPath() string {
	if path := strings.TrimSpace(os.Getenv("CYCOM_SESSION_SOCKET")); path != "" {
		return path
	}
	if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
		return filepath.Join(runtimeDir, "cycomagent-session.sock")
	}
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "cycomagent-session.sock")
}

func NewClient(socket string) Client {
	if strings.TrimSpace(socket) == "" {
		socket = DefaultSocketPath()
	}
	return Client{Socket: socket}
}

func (c Client) Available() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := c.Status(ctx)
	return err == nil
}

func (c Client) Status(ctx context.Context) (Status, error) {
	var out response
	if err := c.call(ctx, request{Action: "status"}, &out); err != nil {
		return Status{Available: false, Socket: c.socketPath()}, err
	}
	if out.Status == nil {
		return Status{Available: false, Socket: c.socketPath()}, errors.New("session bridge returned no status")
	}
	out.Status.Available = true
	return *out.Status, nil
}

func (c Client) Exec(ctx context.Context, in ExecRequest) (ExecResult, error) {
	var out response
	if err := c.call(ctx, request{Action: "exec", Exec: &in}, &out); err != nil {
		return ExecResult{}, err
	}
	if out.Exec == nil {
		return ExecResult{}, errors.New("session bridge returned no exec result")
	}
	return *out.Exec, nil
}

func (c Client) Spawn(ctx context.Context, in SpawnRequest) (SpawnResult, error) {
	var out response
	if err := c.call(ctx, request{Action: "spawn", Spawn: &in}, &out); err != nil {
		return SpawnResult{}, err
	}
	if out.Spawn == nil {
		return SpawnResult{}, errors.New("session bridge returned no spawn result")
	}
	return *out.Spawn, nil
}

func (c Client) socketPath() string {
	if strings.TrimSpace(c.Socket) != "" {
		return c.Socket
	}
	return DefaultSocketPath()
}

func (c Client) call(ctx context.Context, in request, out *response) error {
	path := c.socketPath()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("desktop session bridge unavailable at %s: %w", path, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(in); err != nil {
		return fmt.Errorf("write desktop session bridge request: %w", err)
	}
	if err := json.NewDecoder(io.LimitReader(conn, 24<<20)).Decode(out); err != nil {
		return fmt.Errorf("read desktop session bridge response: %w", err)
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "desktop session bridge request failed"
		}
		return errors.New(out.Error)
	}
	return nil
}

// Run serves execution requests from inside the graphical login session. The
// bridge itself must be started by the desktop session (XDG autostart,
// compositor autostart, etc.) so children inherit the real logind session
// cgroup. Copying DISPLAY/DBUS variables alone is intentionally not enough.
func Run(ctx context.Context, socket string) error {
	if strings.TrimSpace(socket) == "" {
		socket = DefaultSocketPath()
	}
	cgroup := processCgroup(os.Getpid())
	if sessionFromCgroup(cgroup) == "" && os.Getenv("CYCOM_SESSION_BRIDGE_ALLOW_NONLOGIN") != "1" {
		return fmt.Errorf("desktop session bridge must be launched by a real graphical login session; current cgroup %q has no session-*.scope (use compositor/session autostart, not the CyComAgent or systemd user service)", cgroup)
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return fmt.Errorf("create session bridge socket directory: %w", err)
	}
	if err := removeStaleSocket(socket); err != nil {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen desktop session bridge %s: %w", socket, err)
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0o600); err != nil {
		return fmt.Errorf("chmod desktop session bridge socket: %w", err)
	}

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept desktop session bridge request: %w", err)
		}
		go handleConnection(conn, socket)
	}
}

func removeStaleSocket(path string) error {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket session bridge path %s", path)
	}
	conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("desktop session bridge already running at %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale desktop session bridge socket: %w", err)
	}
	return nil
}

func handleConnection(conn net.Conn, socket string) {
	defer conn.Close()
	if !peerIsAuthorized(conn) {
		_ = json.NewEncoder(conn).Encode(response{OK: false, Error: "desktop session bridge rejected peer identity"})
		return
	}
	var in request
	if err := json.NewDecoder(io.LimitReader(conn, 4<<20)).Decode(&in); err != nil {
		_ = json.NewEncoder(conn).Encode(response{OK: false, Error: "decode request: " + err.Error()})
		return
	}
	out := response{OK: true}
	switch in.Action {
	case "status":
		status := bridgeStatus(socket)
		out.Status = &status
	case "exec":
		if in.Exec == nil {
			out.OK, out.Error = false, "exec request is required"
			break
		}
		result, err := runExec(*in.Exec)
		if err != nil {
			out.OK, out.Error = false, err.Error()
			break
		}
		out.Exec = &result
	case "spawn":
		if in.Spawn == nil {
			out.OK, out.Error = false, "spawn request is required"
			break
		}
		result, err := runSpawn(*in.Spawn)
		if err != nil {
			out.OK, out.Error = false, err.Error()
			break
		}
		out.Spawn = &result
	default:
		out.OK, out.Error = false, fmt.Sprintf("unsupported session bridge action %q", in.Action)
	}
	_ = json.NewEncoder(conn).Encode(out)
}

func peerIsAuthorized(conn net.Conn) bool {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return false
	}
	var cred *syscall.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || sockErr != nil || cred == nil {
		return false
	}
	if int(cred.Uid) != os.Getuid() {
		return false
	}
	peerExe, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(int(cred.Pid)), "exe"))
	if err != nil {
		return false
	}
	selfExe, err := os.Executable()
	if err != nil {
		return false
	}
	peerExe = resolvedExecutablePath(peerExe)
	selfExe = resolvedExecutablePath(selfExe)
	return peerExe != "" && peerExe == selfExe
}

func resolvedExecutablePath(path string) string {
	path = strings.TrimSuffix(path, " (deleted)")
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func bridgeStatus(socket string) Status {
	cgroup := processCgroup(os.Getpid())
	return Status{
		Available:      true,
		PID:            os.Getpid(),
		UID:            os.Getuid(),
		Socket:         socket,
		Cgroup:         cgroup,
		Session:        sessionFromCgroup(cgroup),
		Desktop:        firstNonEmpty(os.Getenv("XDG_CURRENT_DESKTOP"), os.Getenv("XDG_SESSION_DESKTOP"), os.Getenv("DESKTOP_SESSION")),
		SessionType:    os.Getenv("XDG_SESSION_TYPE"),
		WaylandDisplay: os.Getenv("WAYLAND_DISPLAY"),
		Display:        os.Getenv("DISPLAY"),
		EnvironmentHint: map[string]string{
			"XDG_RUNTIME_DIR":          os.Getenv("XDG_RUNTIME_DIR"),
			"DBUS_SESSION_BUS_ADDRESS": os.Getenv("DBUS_SESSION_BUS_ADDRESS"),
		},
	}
}

func runExec(in ExecRequest) (ExecResult, error) {
	if strings.TrimSpace(in.Command) == "" {
		return ExecResult{}, errors.New("command is required")
	}
	timeout := defaultExecTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	max := in.MaxOutputBytes
	if max <= 0 || max > maxOutputLimit {
		max = defaultOutputLimit
	}
	shell := strings.TrimSpace(in.Shell)
	if shell == "" {
		shell = defaultShell()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.Command(shell, "-lc", in.Command)
	if in.Cwd != "" {
		cmd.Dir = in.Cwd
	}
	cmd.Env = mergeEnv(in.Env)
	if in.Stdin != "" {
		cmd.Stdin = strings.NewReader(in.Stdin)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr limitBuffer
	stdout.limit, stderr.limit = max, max
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return ExecResult{}, err
	}
	pid := cmd.Process.Pid
	cgroup := processCgroup(pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		waitErr = <-done
	}
	result := ExecResult{
		ExitCode:   0,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: time.Since(start).Milliseconds(),
		TimedOut:   timedOut,
		Truncated:  stdout.truncated || stderr.truncated,
		PID:        pid,
		Cgroup:     cgroup,
		Session:    sessionFromCgroup(cgroup),
	}
	if timedOut {
		result.ExitCode = -1
		return result, nil
	}
	if waitErr == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return ExecResult{}, waitErr
}

func runSpawn(in SpawnRequest) (SpawnResult, error) {
	if strings.TrimSpace(in.Command) == "" {
		return SpawnResult{}, errors.New("command is required")
	}
	if strings.TrimSpace(in.LogFile) == "" {
		return SpawnResult{}, errors.New("log_file is required")
	}
	if err := os.MkdirAll(filepath.Dir(in.LogFile), 0o700); err != nil {
		return SpawnResult{}, err
	}
	logFile, err := os.OpenFile(in.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return SpawnResult{}, err
	}
	shell := strings.TrimSpace(in.Shell)
	if shell == "" {
		shell = defaultShell()
	}
	cmd := exec.Command(shell, "-lc", in.Command)
	if in.Cwd != "" {
		cmd.Dir = in.Cwd
	}
	cmd.Env = mergeEnv(in.Env)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return SpawnResult{}, err
	}
	pid := cmd.Process.Pid
	pgid, _ := syscall.Getpgid(pid)
	cgroup := processCgroup(pid)
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
	}()
	return SpawnResult{PID: pid, PGID: pgid, Cgroup: cgroup, Session: sessionFromCgroup(cgroup)}, nil
}

func mergeEnv(extra map[string]string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		if i := strings.IndexByte(entry, '='); i > 0 {
			values[entry[:i]] = entry[i+1:]
		}
	}
	for key, value := range extra {
		values[key] = value
	}
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func defaultShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	if _, err := os.Stat("/bin/sh"); err == nil {
		return "/bin/sh"
	}
	if shell, err := exec.LookPath("sh"); err == nil {
		return shell
	}
	return "sh"
}

func processCgroup(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[2] != "" {
			return parts[2]
		}
	}
	return ""
}

func sessionFromCgroup(cgroup string) string {
	for _, part := range strings.Split(cgroup, "/") {
		if strings.HasPrefix(part, "session-") && strings.HasSuffix(part, ".scope") {
			return strings.TrimSuffix(strings.TrimPrefix(part, "session-"), ".scope")
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type limitBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return original, nil
	}
	_, _ = b.buf.Write(p)
	return original, nil
}

func (b *limitBuffer) String() string { return b.buf.String() }
