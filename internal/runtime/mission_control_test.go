package runtime

import (
	"context"
	"encoding/json"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/trace"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/workflows"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMissionControlIsReadOnlyAndRedactsTargetSecrets(t *testing.T) {
	dir := t.TempDir()
	secretHost := "private-hostname.invalid"
	secretPath := "/home/user/.ssh/private_key"
	secretEnv := "mission-control-secret-value"
	targets := []map[string]any{{"name": "private-target", "transport": "ssh", "host": secretHost, "user": "operator", "identity_file": secretPath, "env": map[string]string{"TOKEN": secretEnv}, "enabled": true}}
	data, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "targets.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := New(Config{Version: "test", StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := rt.MissionControl()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runtime["status"] != "healthy" {
		t.Fatalf("runtime status: %#v", snapshot.Runtime)
	}
	if snapshot.Counts["tools"] == 0 {
		t.Fatal("expected registered tools")
	}
	found := false
	for _, target := range snapshot.Targets {
		if target.Name == "private-target" {
			found = true
			if target.Transport != "ssh" || !target.Enabled {
				t.Fatalf("bad target projection: %+v", target)
			}
		}
	}
	if !found {
		t.Fatal("target missing from read model")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretHost, secretPath, secretEnv, "identity_file", "private_key"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("sensitive target field leaked (%s): %s", secret, encoded)
		}
	}
	if snapshot.Desktop.Platform == "" {
		t.Fatal("desktop platform missing")
	}
}

func TestMissionControlIncludesSessionInventoryWithoutSecrets(t *testing.T) {
	rt, err := New(Config{Version: "test", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := rt.sessions.Create("visible-label", "agent-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.sessions.AttachTrace(s.ID, "trace-safe"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := rt.MissionControl()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Counts["sessions"] != 1 || len(snapshot.Sessions) != 1 {
		t.Fatalf("session inventory missing: %+v", snapshot.Counts)
	}
	if snapshot.Sessions[0].ID != s.ID || snapshot.Sessions[0].Traces != 1 {
		t.Fatalf("bad session projection: %+v", snapshot.Sessions[0])
	}
}

func TestSessionDetailJoinsLinkedRuntimeActivityAndRedacts(t *testing.T) {
	rt, err := New(Config{Version: "test", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := rt.sessions.Create("parent", "agent", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := rt.sessions.Create("child", "agent", parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.sessions.AttachTrace(parent.ID, "trace-linked"); err != nil {
		t.Fatal(err)
	}
	ctx := trace.WithSessionID(context.Background(), parent.ID)
	ctx = trace.WithTraceID(ctx, "trace-linked")
	ctx = trace.WithCallID(ctx, "call-linked")
	args := json.RawMessage(`{"reason":"test detail"}`)
	if err = rt.audit.BeforeCall(ctx, "process_spawn", args); err != nil {
		t.Fatal(err)
	}
	rt.audit.AfterCall(ctx, "process_spawn", args, 12*time.Millisecond, nil)
	job, err := rt.jobs.StartWithSession("sleep 0.1", "", "/bin/sh", nil, "service", parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := rt.SessionDetail(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Session.ID != parent.ID || len(detail.Children) != 1 || detail.Children[0].ID != child.ID || len(detail.Jobs) != 1 || detail.Jobs[0].ID != job.ID || len(detail.Traces) != 1 || len(detail.Events) != 1 {
		t.Fatalf("incomplete detail projection: %+v", detail)
	}
	encoded, _ := json.Marshal(detail)
	for _, secret := range []string{"sleep 0.1", "/bin/sh", "test detail", "args_sha256", "reason"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("sensitive data leaked (%s): %s", secret, encoded)
		}
	}
}

func TestMissionControlWorkflowGraphProjectionRedactsExecutionDetails(t *testing.T) {
	rt, err := New(Config{Version: "test", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	secretCommand := "curl https://internal.invalid --header SECRET_TOKEN"
	secretCwd := "/home/operator/private-worktree"
	secretShell := "/bin/private-shell"
	wf, err := rt.workflows.Create("graph-safe", "session-visible", []workflows.Step{{ID: "prepare", Command: secretCommand, Cwd: secretCwd, Shell: secretShell}, {ID: "deploy", Command: "echo deploy", DependsOn: []string{"prepare"}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := rt.MissionControl()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workflows) != 1 || snapshot.Workflows[0].ID != wf.ID || len(snapshot.Workflows[0].Steps) != 2 {
		t.Fatalf("workflow graph missing: %+v", snapshot.Workflows)
	}
	if got := snapshot.Workflows[0].Steps[1].DependsOn; len(got) != 1 || got[0] != "prepare" {
		t.Fatalf("dependency missing: %v", got)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretCommand, secretCwd, secretShell, "private-worktree", "SECRET_TOKEN"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("workflow secret leaked (%s)", secret)
		}
	}
}
