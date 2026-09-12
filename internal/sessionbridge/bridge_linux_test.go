//go:build linux

package sessionbridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBridgeRejectsNonLoginCgroup(t *testing.T) {
	if sessionFromCgroup(processCgroup(os.Getpid())) != "" {
		t.Skip("test process already belongs to a real login session")
	}
	t.Setenv("CYCOM_SESSION_BRIDGE_ALLOW_NONLOGIN", "")
	err := Run(context.Background(), filepath.Join(t.TempDir(), "rejected.sock"))
	if err == nil || !strings.Contains(err.Error(), "session-*.scope") {
		t.Fatalf("expected non-login cgroup rejection, got %v", err)
	}
}

func startTestBridge(t *testing.T) (Client, context.CancelFunc) {
	t.Helper()
	t.Setenv("CYCOM_SESSION_BRIDGE_ALLOW_NONLOGIN", "1")
	socket := filepath.Join(t.TempDir(), "session.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, socket) }()
	client := NewClient(socket)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := client.Status(probeCtx)
		probeCancel()
		if err == nil {
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("bridge shutdown: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Error("bridge did not stop")
				}
			})
			return client, cancel
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatalf("session bridge did not become ready")
	return Client{}, func() {}
}

func TestBridgeExecRoundTrip(t *testing.T) {
	client, _ := startTestBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := client.Exec(ctx, ExecRequest{
		Command: "printf '%s:' \"$CYCOM_BRIDGE_TEST\"; cat",
		Stdin:   "stdin-ok",
		Env:     map[string]string{"CYCOM_BRIDGE_TEST": "env-ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.Stdout != "env-ok:stdin-ok" || res.TimedOut {
		t.Fatalf("unexpected exec result: %#v", res)
	}
	if res.PID <= 0 || strings.TrimSpace(res.Cgroup) == "" {
		t.Fatalf("missing process metadata: %#v", res)
	}
}

func TestBridgeExecTimeoutKillsProcessGroup(t *testing.T) {
	client, _ := startTestBridge(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	res, err := client.Exec(ctx, ExecRequest{
		Command:        "sleep 30 & echo $! > '" + pidFile + "'; wait",
		TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("unexpected timeout result: %#v", res)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var child int
	if _, err := fmtSscanf(strings.TrimSpace(string(data)), &child); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(child, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("child %d survived bridge timeout", child)
}

func TestBridgeSpawnWritesLog(t *testing.T) {
	client, _ := startTestBridge(t)
	logFile := filepath.Join(t.TempDir(), "spawn.log")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := client.Spawn(ctx, SpawnRequest{Command: "printf spawn-ok", LogFile: logFile})
	if err != nil {
		t.Fatal(err)
	}
	if res.PID <= 0 || res.PGID <= 0 {
		t.Fatalf("invalid spawn metadata: %#v", res)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(logFile)
		if string(data) == "spawn-ok" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(logFile)
	t.Fatalf("spawn log=%q", data)
}

// tiny scanner helper keeps this test independent of strconv error wording.
func fmtSscanf(value string, out *int) (int, error) {
	return fmt.Sscanf(value, "%d", out)
}
