package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
)

func TestProcessExecTimeoutKillsProcessGroup(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	raw, _ := json.Marshal(map[string]any{"command": "sleep 30 & echo $! > '" + pidfile + "'; wait", "timeout_seconds": 1})
	out, err := processExec(context.Background(), raw, broker.Client{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["timed_out"] != true {
		t.Fatalf("out=%#v", m)
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("child pid %d survived process group timeout", pid)
}

func TestProcessExecStdin(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"command": "cat", "stdin": "hello-stdin"})
	out, err := processExec(context.Background(), raw, broker.Client{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if strings.TrimSpace(m["stdout"].(string)) != "hello-stdin" {
		t.Fatalf("out=%#v", m)
	}
}
