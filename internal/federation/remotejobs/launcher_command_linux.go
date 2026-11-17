//go:build linux

package remotejobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
)

func newStructuredCommand(ctx context.Context, exeFile, cwdFile *os.File, req ProcessRequest) (*exec.Cmd, func(), error) {
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return nil, nil, fmt.Errorf("kernel-enforced process containment unavailable: systemd-run not found")
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return nil, nil, fmt.Errorf("kernel-enforced process containment unavailable: systemctl not found")
	}
	envBinary, err := exec.LookPath("env")
	if err != nil {
		return nil, nil, fmt.Errorf("kernel-enforced process containment unavailable: env utility not found")
	}
	managerEnv := make([]string, 0, 2)
	for _, key := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		value, ok := os.LookupEnv(key)
		if !ok || value == "" {
			return nil, nil, fmt.Errorf("kernel-enforced process containment unavailable: %s is not configured", key)
		}
		managerEnv = append(managerEnv, key+"="+value)
	}

	unit := fmt.Sprintf("cycomagent-remote-job-%d-%d.scope", os.Getpid(), time.Now().UnixNano())
	executablePath := "/proc/self/fd/3"
	args := []string{
		"--user",
		"--scope",
		"--collect",
		"--quiet",
		"--unit=" + unit,
		"--property=RuntimeMaxSec=" + req.Timeout.String(),
		"--property=KillMode=control-group",
		"--property=SendSIGKILL=yes",
		"--property=TimeoutStopSec=250ms",
		"--",
	}
	args = append(args, envBinary, "-i", "--")
	envKeys := make([]string, 0, len(req.Environment))
	for key := range req.Environment {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		args = append(args, key+"="+req.Environment[key])
	}
	args = append(args, executablePath)
	args = append(args, req.Args...)

	cmd := exec.CommandContext(ctx, systemdRun, args...)
	cmd.Env = managerEnv
	cmd.ExtraFiles = []*os.File{exeFile}
	// systemd-run's scoped child inherits this cwd. The path is resolved from
	// the still-open directory descriptor, not the validated pathname.
	cmd.Dir = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), cwdFile.Fd())
	cmd.SysProcAttr = structuredSysProcAttr()
	cmd.WaitDelay = 2 * time.Second

	var stopOnce sync.Once
	stopContainedJob := func() {
		stopOnce.Do(func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			stop := exec.CommandContext(stopCtx, systemctl, "--user", "stop", unit)
			stop.Env = managerEnv
			_ = stop.Run()
			// If systemd has not created the unit yet, or the manager is
			// unavailable, terminate the client. RuntimeMaxSec remains the
			// final bound if a scope was created concurrently with this stop.
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
	}
	cmd.Cancel = func() error {
		stopContainedJob()
		return nil
	}
	return cmd, stopContainedJob, nil
}
