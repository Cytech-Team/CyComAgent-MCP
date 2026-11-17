//go:build linux

package remotejobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStructuredLauncherTimeoutKillsSetsidEscapedDescendant(t *testing.T) {
	if _, err := os.Stat("/usr/bin/setsid"); err != nil {
		t.Skipf("setsid utility unavailable: %v", err)
	}
	if _, err := os.Stat("/usr/bin/sleep"); err != nil {
		t.Skipf("sleep utility unavailable: %v", err)
	}

	root := t.TempDir()
	script := filepath.Join(root, "spawn-escaped-session.sh")
	marker := filepath.Join(root, "escaped.pid")
	body := fmt.Sprintf("#!/bin/sh\n/usr/bin/setsid /bin/sh -c 'echo $$ > %q; exec /usr/bin/sleep 30' &\nwait\n", marker)
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0700); err != nil {
		t.Fatal(err)
	}

	policy, req := launcherPolicy(t, script, 256, 500*time.Millisecond)
	result, launchErr := Launch(context.Background(), policy, "caller-a", true, req)
	if launchErr == nil || result.ExitCode != -1 {
		t.Fatalf("expected ambiguous timeout, result=%#v err=%v", result, launchErr)
	}

	pidBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("escaped descendant did not reach setsid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid escaped descendant PID %q: %v", pidBytes, err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("setsid descendant PID %d survived cgroup timeout cancellation", pid)
}

func TestStructuredLauncherDocumentsNestedUserManagerEscape(t *testing.T) {
	requireSystemdScope(t)
	xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR")
	dbusAddress := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if xdgRuntimeDir == "" || dbusAddress == "" {
		t.Skip("user systemd manager environment unavailable")
	}

	root := t.TempDir()
	script := filepath.Join(root, "nested-manager-escape.sh")
	marker := filepath.Join(root, "nested.pid")
	unit := fmt.Sprintf("cycomagent-nested-escape-%d-%d.scope", os.Getpid(), time.Now().UnixNano())
	body := fmt.Sprintf("#!/bin/sh\nexport XDG_RUNTIME_DIR=%q\nexport DBUS_SESSION_BUS_ADDRESS=%q\nexport PATH=/usr/bin:/bin\n/usr/bin/systemd-run --user --scope --collect --quiet --unit=%q --property=RuntimeMaxSec=30s -- /bin/sh -c 'echo $$$$ > %q; exec /usr/bin/sleep 30' >/dev/null 2>&1 &\nwait\n", xdgRuntimeDir, dbusAddress, unit, marker)
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		t.Skipf("systemctl unavailable for nested-scope cleanup: %v", err)
	}
	cleanup := func() {
		cmd := exec.Command(systemctl, "--user", "stop", unit)
		cmd.Env = []string{"XDG_RUNTIME_DIR=" + xdgRuntimeDir, "DBUS_SESSION_BUS_ADDRESS=" + dbusAddress}
		_ = cmd.Run()
	}
	defer cleanup()
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0700); err != nil {
		t.Fatal(err)
	}
	policy, req := launcherPolicy(t, script, 512, 700*time.Millisecond)
	result, launchErr := Launch(context.Background(), policy, "caller-a", true, req)
	if launchErr == nil || result.ExitCode != -1 {
		t.Fatalf("expected outer job timeout, result=%#v err=%v", result, launchErr)
	}

	pidBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("nested scope did not start: %v (outer stderr=%q)", err, result.Stderr)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid nested scope PID %q: %v", pidBytes, err)
	}
	if !processAlive(pid) {
		t.Fatalf("expected nested scope PID %d to demonstrate the documented same-user manager escape", pid)
	}

	// This uniquely named transient scope was created by this test. Stop it
	// through the user manager so no test workload is left behind.
	cleanup()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("test-owned nested scope PID %d remained alive after cleanup", pid)
}
