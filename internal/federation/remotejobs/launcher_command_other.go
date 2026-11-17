//go:build !linux

package remotejobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func newStructuredCommand(ctx context.Context, exeFile, cwdFile *os.File, req ProcessRequest) (*exec.Cmd, func(), error) {
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", req.Args...)
	cmd.ExtraFiles = []*os.File{exeFile}
	cmd.Dir = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), cwdFile.Fd())
	for key, value := range req.Environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.SysProcAttr = structuredSysProcAttr()
	kill := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	cmd.Cancel = func() error {
		kill()
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd, kill, nil
}
