package targets

import (
	"context"
	"strings"
	"testing"
)

func TestTargetPersistenceAndLocalExec(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Upsert(Target{Name: "box", Transport: "ssh", Host: "example.invalid", Port: 2222, User: "u", Enabled: true, Tags: []string{"test"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 2222 {
		t.Fatalf("port=%d", got.Port)
	}

	m2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := m2.Get("box")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Host != "example.invalid" || !reloaded.Enabled {
		t.Fatalf("reloaded=%+v", reloaded)
	}

	res, err := m2.Exec(context.Background(), "local", ExecOptions{Command: "printf cycom-fullpower"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "cycom-fullpower" {
		t.Fatalf("res=%+v", res)
	}
}

func TestRemoteCommandRejectsBadEnvName(t *testing.T) {
	_, err := buildRemoteCommand(Target{WorkDir: "/tmp"}, ExecOptions{Command: "true", Env: map[string]string{"BAD-NAME": "x"}})
	if err == nil {
		t.Fatal("expected invalid environment variable rejection")
	}
}
