//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
)

func main() {
	socket := flag.String("socket", envOr("CYCOM_ROOT_SOCKET", "/run/cycomagent/root.sock"), "unix socket")
	allowUID := flag.Int("allow-uid", envInt("CYCOM_ROOT_ALLOW_UID", -1), "only accept this peer uid")
	allowExe := flag.String("allow-exe", envOr("CYCOM_ROOT_ALLOW_EXE", ""), "optional exact executable path required for the peer process")
	socketMode := flag.Int("socket-mode", 0660, "unix socket mode")
	flag.Parse()
	if os.Geteuid() != 0 {
		fatalf("cycomagent-root must run as root")
	}
	if *allowUID < 0 {
		fatalf("--allow-uid or CYCOM_ROOT_ALLOW_UID is required")
	}
	_ = os.Remove(*socket)
	if err := os.MkdirAll(dirOf(*socket), 0755); err != nil {
		fatalf("mkdir: %v", err)
	}
	ln, err := net.Listen("unix", *socket)
	if err != nil {
		fatalf("listen: %v", err)
	}
	defer ln.Close()
	if err := os.Chmod(*socket, os.FileMode(*socketMode)); err != nil {
		fatalf("chmod socket: %v", err)
	}
	// Make the socket directly accessible to the configured runtime user.
	// Peer credentials are still verified independently on every request.
	if err := os.Chown(*socket, *allowUID, -1); err != nil {
		fatalf("chown socket: %v", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go handle(conn, uint32(*allowUID), *allowExe)
	}
}

func handle(conn net.Conn, allowedUID uint32, allowedExe string) {
	defer conn.Close()
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return
	}
	uid, pid, err := peerCredentials(uc)
	if err != nil || uid != allowedUID {
		_ = json.NewEncoder(conn).Encode(broker.ExecResponse{Error: "peer uid is not authorized"})
		return
	}
	if allowedExe != "" && !peerExecutableAllowed(pid, allowedExe) {
		_ = json.NewEncoder(conn).Encode(broker.ExecResponse{Error: "peer executable is not authorized"})
		return
	}
	var req broker.ExecRequest
	dec := json.NewDecoder(conn)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(broker.ExecResponse{Error: err.Error()})
		return
	}
	out := execute(req)
	_ = json.NewEncoder(conn).Encode(out)
}

func execute(req broker.ExecRequest) broker.ExecResponse {
	if req.Command == "" {
		return broker.ExecResponse{Error: "command is required"}
	}
	shell := req.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	timeout := 120 * time.Second
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	max := req.MaxOutputBytes
	if max <= 0 || max > 10<<20 {
		max = 2 << 20
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.Command(shell, "-lc", req.Command)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	cmd.Env = mergeEnv(req.Env)
	if req.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(req.Stdin)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = max, max
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return broker.ExecResponse{ExitCode: -1, Error: err.Error()}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	timedOut := false
	select {
	case err = <-done:
	case <-ctx.Done():
		timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		err = <-done
	}
	res := broker.ExecResponse{
		Stdout: stdout.String(), Stderr: stderr.String(), DurationMS: time.Since(start).Milliseconds(),
		Truncated: stdout.truncated || stderr.truncated, TimedOut: timedOut,
	}
	if timedOut {
		res.ExitCode = -1
		return res
	}
	if err == nil {
		return res
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res
	}
	res.ExitCode = -1
	res.Error = err.Error()
	return res
}

func peerCredentials(c *net.UnixConn) (uint32, int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var uid uint32
	var pid int
	var inner error
	err = raw.Control(func(fd uintptr) {
		cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if e != nil {
			inner = e
			return
		}
		uid = cred.Uid
		pid = int(cred.Pid)
	})
	if err != nil {
		return 0, 0, err
	}
	return uid, pid, inner
}

func peerExecutableAllowed(pid int, allowed string) bool {
	actual, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return false
	}
	want, err := filepath.EvalSymlinks(allowed)
	if err != nil {
		want = filepath.Clean(allowed)
	}
	got, err := filepath.EvalSymlinks(actual)
	if err != nil {
		got = filepath.Clean(actual)
	}
	return got == want
}

func mergeEnv(extra map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return n, nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return n, nil
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
func (b *cappedBuffer) String() string { return b.buf.String() }
