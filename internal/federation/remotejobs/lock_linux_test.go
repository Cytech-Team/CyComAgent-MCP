//go:build linux

package remotejobs

import (
	"os/exec"
	"testing"
	"time"
)

func TestOwnerAliveRejectsZombieProcess(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 0.1; exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	ticks, boot, err := readProcessIdentity(pid)
	if err != nil {
		_ = cmd.Wait()
		t.Fatalf("capture child identity: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !ownerAlive(pid, ticks, boot) {
			if err := cmd.Wait(); err != nil {
				t.Fatalf("reap exited child: %v", err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = cmd.Wait()
	t.Fatalf("zombie/exited process %d was still considered a live owner", pid)
}
