package remotejobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
)

func launchAsJobTest(t *testing.T, manager *jobs.Manager, stateDir, localJobID string, policy ExecutionPolicy, caller string, trusted bool, request ProcessRequest, executionContext, sessionID, requestID string) (*jobs.Job, error) {
	t.Helper()
	if err := manager.SetLifecycleLockProvider(remoteJobLifecycleLockProvider{stateDir: stateDir}); err != nil {
		return nil, err
	}
	var job *jobs.Job
	err := manager.WithLifecycleAuthorization(caller, requestID, func(auth jobs.LifecycleAuthorization) error {
		var launchErr error
		job, launchErr = LaunchAsJob(context.Background(), manager, localJobID, policy, caller, trusted, request, executionContext, sessionID, requestID, auth)
		return launchErr
	})
	return job, err
}

func TestLaunchAsJobPersistsStructuredExecution(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("Linux procfs descriptor execution unavailable")
	}
	exe, err := execLookPath("printf")
	if err != nil {
		t.Skip(err)
	}
	policy, request := launcherPolicy(t, exe, 256, 2*time.Second)
	request.Args = []string{"%s", "remote-job-ok"}
	stateDir := t.TempDir()
	manager, err := jobs.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := launchAsJobTest(t, manager, stateDir, "remote-job-integration", policy, "caller-a", true, request, "service", "session-1", "request-integration")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "exited" || job.ExitCode == nil || *job.ExitCode != 0 {
		t.Fatalf("unexpected Job result: %#v", job)
	}
	if job.Shell != "structured" || job.ExecutionContext != "service" || job.SessionID != "session-1" {
		t.Fatalf("structured metadata not persisted: %#v", job)
	}
	owned, err := launchAsJobTest(t, manager, stateDir, "remote-job-owned-binding", policy, "caller-a", true, request, "service", "session-1", "request-a")
	if err != nil {
		t.Fatal(err)
	}
	if owned.RemoteCallerNodeID != "caller-a" || owned.RemoteRequestID != "request-a" {
		t.Fatalf("remote ownership binding not persisted: %#v", owned)
	}
	log, _, err := manager.Tail(job.ID, 10, 4096)
	if err != nil || log != "remote-job-ok" {
		t.Fatalf("log=%q err=%v", log, err)
	}
}

func TestLaunchAsJobRejectsPolicyBeforeAnyProcessAndKeepsIntent(t *testing.T) {
	exe, err := execLookPath("printf")
	if err != nil {
		t.Skip(err)
	}
	policy, request := launcherPolicy(t, exe, 256, 2*time.Second)
	stateDir := t.TempDir()
	manager, err := jobs.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = false
	job, err := launchAsJobTest(t, manager, stateDir, "remote-job-denied", policy, "caller-a", true, request, "service", "", "request-denied")
	if err == nil || job == nil || job.Status != "launch-unknown" {
		t.Fatalf("policy denial should be preserved as conservative unknown intent: job=%#v err=%v", job, err)
	}
	if _, err := manager.ReadSnapshot("remote-job-denied"); err != nil {
		t.Fatalf("durable intent missing: %v", err)
	}
	if errors.Is(err, ErrOutputLimit) {
		t.Fatal("policy denial must not be classified as output limit")
	}
}

func TestLaunchAsJobRequiresManager(t *testing.T) {
	_, err := LaunchAsJob(context.Background(), nil, "id", ExecutionPolicy{}, "caller", false, ProcessRequest{}, "", "", "request", jobs.LifecycleAuthorization{})
	if !errors.Is(err, ErrJobManagerRequired) {
		t.Fatalf("err=%v", err)
	}
}

func TestLaunchAsJobPersistsUnknownOnTimeout(t *testing.T) {
	exe, err := execLookPath("sleep")
	if err != nil {
		t.Skip(err)
	}
	policy, request := launcherPolicy(t, exe, 256, 100*time.Millisecond)
	request.Args = []string{"5"}
	stateDir := filepath.Join(t.TempDir(), "state")
	manager, err := jobs.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := launchAsJobTest(t, manager, stateDir, "remote-job-timeout", policy, "caller-a", true, request, "service", "", "request-timeout")
	if err == nil || job == nil || job.Status != "launch-unknown" || job.ExitCode == nil || *job.ExitCode != -1 {
		t.Fatalf("timeout must persist unknown: job=%#v err=%v", job, err)
	}
}

func TestLaunchAsJobRequiresLifecycleAuthorization(t *testing.T) {
	manager, err := jobs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = LaunchAsJob(context.Background(), manager, "remote-job-no-auth", ExecutionPolicy{}, "caller-a", true, ProcessRequest{}, "service", "", "request-no-auth", jobs.LifecycleAuthorization{})
	if err == nil {
		t.Fatal("remote Job start accepted without an active lifecycle authorization")
	}
}
