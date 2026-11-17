//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestAgentWorkspaceEnvironmentRequiresRunningWorkspace(t *testing.T) {
	d := t.TempDir()
	bin := filepath.Join(d, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "agent-workspace-linux"), "#!/bin/sh\necho '{\"workspaces\":[]}'\n")
	t.Setenv("PATH", bin)
	t.Setenv("HOME", d)
	t.Setenv("AGENT_WORKSPACE_ID", "default")
	if _, _, err := agentWorkspaceEnvironment(); err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("expected a clear not-running error, got %v", err)
	}
}

func TestAgentWorkspaceEnvironmentUsesX11DisplayAndXAuthority(t *testing.T) {
	d := t.TempDir()
	bin := filepath.Join(d, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	xauth := filepath.Join(d, "Xauthority")
	if err := os.WriteFile(xauth, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	cli := "#!/bin/sh\ncase \"$1 $2\" in\n 'workspace list') echo '{\"workspaces\":[{\"id\":\"default\",\"running\":true}]}' ;;\n 'workspace env') echo '{\"environment\":{\"workspace_id\":\"default\",\"display\":\":99\",\"xauthority_path\":\"" + xauth + "\",\"runtime_dir\":\"" + d + "\",\"socket_path\":\"\",\"variables\":[{\"name\":\"AGENT_WORKSPACE_ID\",\"value\":\"default\"}]}}' ;;\n *) exit 2 ;;\nesac\n"
	writeExecutable(t, filepath.Join(bin, "agent-workspace-linux"), cli)
	writeExecutable(t, filepath.Join(bin, "xdpyinfo"), "#!/bin/sh\n[ \"$2\" = \":99\" ]\n")
	t.Setenv("PATH", bin)
	t.Setenv("HOME", d)
	t.Setenv("AGENT_WORKSPACE_ID", "default")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	env, display, err := agentWorkspaceEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if display != ":99" {
		t.Fatalf("display=%q", display)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "DISPLAY=:99") || !strings.Contains(joined, "XAUTHORITY="+xauth) || strings.Contains(joined, "WAYLAND_DISPLAY=") {
		t.Fatalf("unexpected isolated environment: %s", joined)
	}
}

func TestAgentWorkspaceEnvironmentRejectsMissingXAuthority(t *testing.T) {
	d := t.TempDir()
	bin := filepath.Join(d, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "agent-workspace-linux"), "#!/bin/sh\nif [ \"$2\" = list ]; then echo '{\"workspaces\":[{\"id\":\"default\",\"running\":true}]}' ; else echo '{\"environment\":{\"display\":\":99\",\"xauthority_path\":\"/missing/Xauthority\",\"runtime_dir\":\"/tmp\",\"socket_path\":\"\",\"variables\":[]}}'; fi\n")
	t.Setenv("PATH", bin)
	t.Setenv("HOME", d)
	t.Setenv("AGENT_WORKSPACE_ID", "default")
	if _, _, err := agentWorkspaceEnvironment(); err == nil || !strings.Contains(err.Error(), "XAUTHORITY file is unavailable") {
		t.Fatalf("expected XAUTHORITY validation error, got %v", err)
	}
}
