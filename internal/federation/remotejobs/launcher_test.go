package remotejobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func requireSystemdScope(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux cgroup containment is required")
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		t.Skipf("systemd-run unavailable: %v", err)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skipf("systemctl unavailable: %v", err)
	}
	if _, err := exec.LookPath("env"); err != nil {
		t.Skipf("env utility unavailable: %v", err)
	}
	truePath, err := execLookPath("true")
	if err != nil {
		t.Skipf("true utility unavailable: %v", err)
	}
	managerEnv := make([]string, 0, 2)
	for _, key := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		value := os.Getenv(key)
		if value == "" {
			t.Skipf("user systemd manager unavailable: %s is unset", key)
		}
		managerEnv = append(managerEnv, key+"="+value)
	}
	unit := fmt.Sprintf("cycomagent-test-probe-%d-%d.scope", os.Getpid(), time.Now().UnixNano())
	cmd := exec.Command(systemdRun, "--user", "--scope", "--collect", "--quiet", "--unit="+unit, "--property=RuntimeMaxSec=3s", "--", truePath)
	cmd.Env = managerEnv
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("user systemd scope unavailable: %v: %s", err, output)
	}
}

func launcherPolicy(t *testing.T, exe string, maxOutput int64, timeout time.Duration) (ExecutionPolicy, ProcessRequest) {
	t.Helper()
	requireSystemdScope(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	p := ExecutionPolicy{Enabled: true, AllowedCallers: []string{"caller-a"}, AllowedExecutables: []string{exe}, WorkingDirRoots: []string{root}, MaxTimeout: timeout, MaxOutputBytes: maxOutput, MaxArgumentCount: 8, MaxArgumentBytes: 1024}
	return p, ProcessRequest{Executable: exe, WorkingDir: work, Timeout: timeout}
}

func TestStructuredLauncherRunsArgvAndCapturesBoundedOutput(t *testing.T) {
	exe, err := execLookPath("printf")
	if err != nil {
		t.Skip(err)
	}
	p, req := launcherPolicy(t, exe, 128, 2*time.Second)
	req.Args = []string{"%s", "structured-ok"}
	got, err := Launch(context.Background(), p, "caller-a", true, req)
	if err != nil || string(got.Stdout) != "structured-ok" || got.ExitCode != 0 || got.OutputLimited {
		t.Fatalf("result=%#v err=%v", got, err)
	}
}

func TestStructuredLauncherExecutesCanonicalValidatedPaths(t *testing.T) {
	exe, err := execLookPath("printf")
	if err != nil {
		t.Skip(err)
	}
	p, req := launcherPolicy(t, exe, 128, 2*time.Second)
	root := filepath.Dir(req.WorkingDir)
	exeAlias := filepath.Join(root, "printf-alias")
	cwdAlias := filepath.Join(root, "work-alias")
	if err := os.Symlink(exe, exeAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(req.WorkingDir, cwdAlias); err != nil {
		t.Fatal(err)
	}
	req.Executable, req.WorkingDir = exeAlias, cwdAlias
	req.Args = []string{"%s", "canonical-path-ok"}
	got, err := Launch(context.Background(), p, "caller-a", true, req)
	if err != nil || string(got.Stdout) != "canonical-path-ok" || got.ExitCode != 0 {
		t.Fatalf("canonical path launch result=%#v err=%v", got, err)
	}
}

func TestStructuredLauncherStopsContainedProcessOnTimeout(t *testing.T) {
	exe, err := execLookPath("sleep")
	if err != nil {
		t.Skip(err)
	}
	p, req := launcherPolicy(t, exe, 128, 100*time.Millisecond)
	req.Args = []string{"5"}
	got, err := Launch(context.Background(), p, "caller-a", true, req)
	if err == nil || got.ExitCode != -1 {
		t.Fatalf("timeout must be unknown: %#v err=%v", got, err)
	}
}

func TestStructuredLauncherStopsOnOutputLimit(t *testing.T) {
	exe, err := execLookPath("yes")
	if err != nil {
		t.Skip(err)
	}
	p, req := launcherPolicy(t, exe, 128, 3*time.Second)
	got, err := Launch(context.Background(), p, "caller-a", true, req)
	if !errors.Is(err, ErrOutputLimit) || !got.OutputLimited || got.ExitCode != -1 {
		t.Fatalf("output cap not enforced: %#v err=%v", got, err)
	}
	if int64(len(got.Stdout)+len(got.Stderr)) > p.MaxOutputBytes {
		t.Fatalf("captured output exceeded cap")
	}
}

func execLookPath(name string) (string, error) {
	for _, p := range []string{"/usr/bin/" + name, "/bin/" + name} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0111 != 0 {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}

func TestStructuredLauncherDescriptorExecSupportsShebangScripts(t *testing.T) {
	requireSystemdScope(t)
	if runtime.GOOS != "linux" {
		t.Skip("descriptor execution is Linux-only")
	}
	root := t.TempDir()
	script := filepath.Join(root, "safe-script")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' shebang-ok\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	policy := ExecutionPolicy{Enabled: true, AllowedCallers: []string{"caller-a"}, AllowedExecutables: []string{script}, WorkingDirRoots: []string{root}, MaxTimeout: 2 * time.Second, MaxOutputBytes: 128, MaxArgumentCount: 2, MaxArgumentBytes: 128}
	result, err := Launch(context.Background(), policy, "caller-a", true, ProcessRequest{Executable: script, WorkingDir: work, Timeout: time.Second})
	if err != nil || result.ExitCode != 0 || string(result.Stdout) != "shebang-ok" {
		t.Fatalf("descriptor-bound shebang launch result=%#v err=%v", result, err)
	}
}

func TestStructuredLauncherChildDiesWhenLauncherParentExits(t *testing.T) {
	if os.Getenv("CYCOM_PARENT_DEATH_HELPER") == "1" {
		script := os.Getenv("CYCOM_PARENT_DEATH_SCRIPT")
		marker := os.Getenv("CYCOM_PARENT_DEATH_MARKER")
		root := filepath.Dir(script)
		policy, req := launcherPolicy(t, script, 128, time.Second)
		_ = policy
		req.Args = nil
		go func() { _, _ = Launch(context.Background(), policy, "caller-a", true, req) }()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err == nil {
				os.Exit(77)
			}
			time.Sleep(5 * time.Millisecond)
		}
		_ = root
		os.Exit(78)
	}
	requireSystemdScope(t)
	if runtime.GOOS != "linux" {
		t.Skip("Linux parent-death signal is Linux-only")
	}
	root := t.TempDir()
	script := filepath.Join(root, "long-running-child")
	marker := filepath.Join(root, "child.pid")
	source := "#!/bin/sh\n/usr/bin/setsid /bin/sh -c 'printf %s \"$$\" > \"" + marker + "\"; exec /usr/bin/sleep 30' &\nwait\n"
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStructuredLauncherChildDiesWhenLauncherParentExits$")
	cmd.Env = append(os.Environ(), "CYCOM_PARENT_DEATH_HELPER=1", "CYCOM_PARENT_DEATH_SCRIPT="+script, "CYCOM_PARENT_DEATH_MARKER="+marker)
	if err := cmd.Run(); err == nil {
		t.Fatal("helper did not exit abruptly")
	} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 77 {
		t.Fatalf("helper exit=%v, want 77 after child started", err)
	}
	pidBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		// A killed orphan may briefly remain as a zombie until the system
		// reaper collects it; it is no longer executing in that state.
		if data, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); readErr == nil {
			if closeParen := bytes.LastIndexByte(data, ')'); closeParen >= 0 && len(data) > closeParen+2 && data[closeParen+2] == 'Z' {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child PID %d remained alive after its launcher parent exited", pid)
}

func TestStructuredLauncherTimeoutKillsGrandchildInCgroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process groups are required")
	}
	root := t.TempDir()
	script := filepath.Join(root, "spawn-child.sh")
	marker := filepath.Join(root, "grandchild.pid")
	body := "#!/bin/sh\n/usr/bin/sleep 30 &\necho $! > \"" + marker + "\"\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0700); err != nil {
		t.Fatal(err)
	}
	policy, req := launcherPolicy(t, script, 256, 250*time.Millisecond)
	got, launchErr := Launch(context.Background(), policy, "caller-a", true, req)
	if launchErr == nil || got.ExitCode != -1 {
		t.Fatalf("expected ambiguous timeout, result=%#v err=%v", got, launchErr)
	}
	pidBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("grandchild never started: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if data, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); readErr == nil {
			if closeParen := bytes.LastIndexByte(data, ')'); closeParen >= 0 && len(data) > closeParen+2 && data[closeParen+2] == 'Z' {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("grandchild PID %d survived process-group timeout cancellation", pid)
}

func TestStructuredLauncherDoesNotInheritParentEnvironment(t *testing.T) {
	exe, err := execLookPath("env")
	if err != nil {
		t.Skip(err)
	}
	policy, req := launcherPolicy(t, exe, 512, 2*time.Second)
	policy.AllowedEnvKeys = []string{"CYCOM_REQUEST_VALUE"}
	t.Setenv("CYCOM_PARENT_SECRET", "must-not-inherit")
	req.Environment = map[string]string{"CYCOM_REQUEST_VALUE": "explicit-only"}
	result, err := Launch(context.Background(), policy, "caller-a", true, req)
	if err != nil {
		t.Fatalf("launch: %v (stderr=%q)", err, result.Stderr)
	}
	if got, want := string(result.Stdout), "CYCOM_REQUEST_VALUE=explicit-only\n"; got != want {
		t.Fatalf("environment output=%q, want exactly %q", got, want)
	}
}

func TestStructuredLauncherFailsClosedWithoutSystemdContainment(t *testing.T) {
	exe, err := execLookPath("printf")
	if err != nil {
		t.Skip(err)
	}
	policy, req := launcherPolicy(t, exe, 128, time.Second)
	req.Args = []string{"must-not-run"}
	t.Setenv("PATH", t.TempDir())
	result, err := Launch(context.Background(), policy, "caller-a", true, req)
	if err == nil || !strings.Contains(err.Error(), "systemd-run not found") {
		t.Fatalf("expected fail-closed containment error, result=%#v err=%v", result, err)
	}
	if string(result.Stdout) != "" {
		t.Fatalf("workload ran without cgroup containment: %q", result.Stdout)
	}
}

func TestStructuredLauncherFailsClosedWhenSystemdManagerUnavailable(t *testing.T) {
	exe, err := execLookPath("printf")
	if err != nil {
		t.Skip(err)
	}
	policy, req := launcherPolicy(t, exe, 4096, time.Second)
	req.Args = []string{"must-not-run"}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing-user-bus"))
	result, err := Launch(context.Background(), policy, "caller-a", true, req)
	if err == nil || !strings.Contains(err.Error(), "remote process containment failed") || result.ExitCode != -1 {
		t.Fatalf("expected containment startup failure, result=%#v err=%v", result, err)
	}
	if string(result.Stdout) != "" {
		t.Fatalf("workload ran without a working user manager: %q", result.Stdout)
	}
}
