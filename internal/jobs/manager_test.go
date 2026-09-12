package jobs

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestExternalJobPersistsAndCompletes(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.StartExternal("printf external-ok", "", "/bin/sh", nil, "desktop", func(logFile string) (int, int, error) {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, 0, err
		}
		cmd := exec.Command("/bin/sh", "-lc", "printf external-ok")
		cmd.Stdout, cmd.Stderr = f, f
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			_ = f.Close()
			return 0, 0, err
		}
		pgid, _ := syscall.Getpgid(cmd.Process.Pid)
		go func() {
			_ = cmd.Wait()
			_ = f.Close()
		}()
		return cmd.Process.Pid, pgid, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if j.ExecutionContext != "desktop" {
		t.Fatalf("execution_context=%q", j.ExecutionContext)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := m.Get(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Status == "exited-external" {
			text, _, err := m.Tail(j.ID, 20, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if text != "external-ok" {
				t.Fatalf("log=%q", text)
			}
			m2, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := m2.Get(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.ExecutionContext != "desktop" {
				t.Fatalf("recovered execution_context=%q", recovered.ExecutionContext)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("external job did not complete")
}

func TestJobPersistsAndCompletes(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.Start("printf hello", "", "/bin/sh", nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := m.Get(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Status == "exited" {
			text, _, err := m.Tail(j.ID, 20, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if text != "hello" {
				t.Fatalf("log=%q", text)
			}
			m2, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m2.Get(j.ID); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not complete")
}
