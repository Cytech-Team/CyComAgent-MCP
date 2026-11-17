//go:build linux

package remotejobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testExecutionPolicy(t *testing.T) (ExecutionPolicy, ProcessRequest, string) {
	t.Helper()
	root := t.TempDir()
	exe := filepath.Join(root, "worker")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	p := ExecutionPolicy{Enabled: true, AllowedCallers: []string{"caller-a"}, AllowedExecutables: []string{exe}, WorkingDirRoots: []string{root}, AllowedEnvKeys: []string{"LANG"}, MaxTimeout: 10 * time.Second, MaxOutputBytes: 4096, MaxArgumentCount: 8, MaxArgumentBytes: 1024}
	req := ProcessRequest{Executable: exe, Args: []string{"--version"}, WorkingDir: work, Environment: map[string]string{"LANG": "C"}, Timeout: time.Second}
	return p, req, root
}

func TestExecutionPolicyDefaultsDeny(t *testing.T) {
	var p ExecutionPolicy
	if err := p.Validate("caller-a", true, ProcessRequest{}); err == nil {
		t.Fatal("zero-value policy must deny")
	}
}

func TestExecutionPolicyRequiresTrustedAndExplicitlyAllowedCaller(t *testing.T) {
	p, req, _ := testExecutionPolicy(t)
	if err := p.Validate("caller-a", false, req); err == nil {
		t.Fatal("untrusted peer accepted")
	}
	if err := p.Validate("caller-b", true, req); err == nil {
		t.Fatal("trusted but unauthorized peer accepted")
	}
	if err := p.Validate("caller-a", true, req); err != nil {
		t.Fatalf("authorized request denied: %v", err)
	}
}

func TestExecutionPolicyRejectsShellAndExecutableSubstitution(t *testing.T) {
	p, req, _ := testExecutionPolicy(t)
	req.Executable = "/bin/sh"
	if err := p.Validate("caller-a", true, req); err == nil {
		t.Fatal("non-allowlisted shell accepted")
	}
	req.Executable = "worker"
	if err := p.Validate("caller-a", true, req); err == nil {
		t.Fatal("relative executable accepted")
	}
}

func TestExecutionPolicyRejectsWorkingDirectoryEscapeAndSymlinkEscape(t *testing.T) {
	p, req, root := testExecutionPolicy(t)
	req.WorkingDir = t.TempDir()
	if err := p.Validate("caller-a", true, req); err == nil {
		t.Fatal("outside working directory accepted")
	}
	outside := t.TempDir()
	link := filepath.Join(root, "work", "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	req.WorkingDir = link
	if err := p.Validate("caller-a", true, req); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestExecutionPolicyBoundsArgsEnvironmentAndTimeout(t *testing.T) {
	p, req, _ := testExecutionPolicy(t)
	req.Args = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if err := p.Validate("caller-a", true, req); err == nil {
		t.Fatal("too many args accepted")
	}
	req.Args = []string{"ok"}
	req.Environment["LD_PRELOAD"] = "/tmp/evil.so"
	if err := p.Validate("caller-a", true, req); err == nil {
		t.Fatal("unapproved environment key accepted")
	}
	delete(req.Environment, "LD_PRELOAD")
	p.AllowedEnvKeys = append(p.AllowedEnvKeys, "")
	req.Environment[""] = "invalid"
	if err := p.Validate("caller-a", true, req); err == nil {
		t.Fatal("empty environment key accepted")
	}
	delete(req.Environment, "")
	req.Timeout = 11 * time.Second
	if err := p.Validate("caller-a", true, req); err == nil {
		t.Fatal("excessive timeout accepted")
	}
}

func TestPolicyStoreDefaultsDisabledAndPersistsAtomically(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenPolicyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got.Enabled {
		t.Fatalf("missing policy must default deny: %#v %v", got, err)
	}
	p, _, _ := testExecutionPolicy(t)
	if err := store.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load()
	if err != nil || !got.Enabled || len(got.AllowedCallers) != 1 {
		t.Fatalf("policy roundtrip: %#v %v", got, err)
	}
	st, err := os.Stat(filepath.Join(dir, "federation", "remote-execution-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("policy file mode=%o", st.Mode().Perm())
	}
	dst, err := os.Stat(filepath.Join(dir, "federation"))
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm() != 0700 {
		t.Fatalf("policy dir mode=%o", dst.Mode().Perm())
	}
}

func TestPolicyStoreRejectsIncompleteEnabledAndCorruptPolicy(t *testing.T) {
	store, err := OpenPolicyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ExecutionPolicy{Enabled: true}); err == nil {
		t.Fatal("incomplete enabled policy accepted")
	}
	if err := os.WriteFile(store.path, []byte(`{"version":1,"policy":{"enabled":true,"unexpected":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("unknown policy field accepted")
	}
}

func TestPolicyStoreUsesMonotonicCompareAndSwapRevision(t *testing.T) {
	store, err := OpenPolicyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := testExecutionPolicy(t)
	rev, err := store.SaveVersioned(p, 0)
	if err != nil || rev != 1 {
		t.Fatalf("create revision=%d err=%v", rev, err)
	}
	loaded, gotRev, err := store.LoadVersioned()
	if err != nil || gotRev != 1 || !loaded.Enabled {
		t.Fatalf("load revision=%d policy=%#v err=%v", gotRev, loaded, err)
	}
	p.MaxOutputBytes = 2048
	rev, err = store.SaveVersioned(p, gotRev)
	if err != nil || rev != 2 {
		t.Fatalf("update revision=%d err=%v", rev, err)
	}
	if _, err = store.SaveVersioned(p, 1); err == nil {
		t.Fatal("stale revision update accepted")
	}
	_, gotRev, err = store.LoadVersioned()
	if err != nil || gotRev != 2 {
		t.Fatalf("revision regressed: %d %v", gotRev, err)
	}
}

func TestPolicyLeaseBlocksRevisionChangeUntilReleased(t *testing.T) {
	store, err := OpenPolicyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy := ExecutionPolicy{Enabled: true, AllowedCallers: []string{"caller"}, AllowedExecutables: []string{"/bin/echo"}, WorkingDirRoots: []string{"/tmp"}, MaxTimeout: time.Second, MaxOutputBytes: 1024, MaxArgumentCount: 4, MaxArgumentBytes: 128}
	revision, err := store.SaveVersioned(policy, 0)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(revision)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Revision != revision || !lease.Policy.Enabled {
		t.Fatalf("unexpected lease snapshot: %#v", lease)
	}
	finished := make(chan error, 1)
	go func() { _, saveErr := store.SaveVersioned(ExecutionPolicy{}, revision); finished <- saveErr }()
	select {
	case err := <-finished:
		t.Fatalf("policy update passed while lease held: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("policy update after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("policy update remained blocked after lease release")
	}
}

func TestPolicyStoreRejectsSymlinkedPolicyAndLockFiles(t *testing.T) {
	root := t.TempDir()
	store, err := OpenPolicyStore(root)
	if err != nil {
		t.Fatal(err)
	}
	federation := filepath.Join(root, "federation")
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(federation, "remote-execution-policy.json")
	if err := os.Symlink(target, policyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("symlinked policy record was accepted")
	}
	if err := os.Remove(policyPath); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(federation, "remote-execution-policy.json.lock")
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("symlinked policy lock was accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "untouched" {
		t.Fatalf("outside target changed: %q err=%v", got, err)
	}
}
