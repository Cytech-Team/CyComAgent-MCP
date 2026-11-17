package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

func TestSessionIDPersistsOnJob(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.StartWithSession("sleep 0.05", "", "/bin/sh", nil, "service", "ses_0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "ses_0123456789abcdef" {
		t.Fatalf("session id not persisted: %+v", got)
	}
}

func TestStartWithReservedID(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	jobID := "rjob-stable-request-123"
	j, err := m.StartWithIDAndSession(jobID, "printf stable", "", "/bin/sh", nil, "service", "")
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != jobID {
		t.Fatalf("job ID=%q want %q", j.ID, jobID)
	}
	if _, err := m.StartWithIDAndSession(jobID, "printf duplicate", "", "/bin/sh", nil, "service", ""); err == nil {
		t.Fatal("duplicate stable job ID was accepted")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := m.Get(jobID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Status == "exited" {
			got, _, err := m.Tail(jobID, 10, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if got != "stable" {
				t.Fatalf("job output=%q", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("reserved-ID job did not complete")
}

func TestLaunchIntentRecoversAsUnknownAndIsNotReplayed(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := &Job{ID: "rjob-crash-window", Command: "touch /tmp/should-never-run", Shell: "/bin/sh", Status: "launching", LogFile: dir + "/jobs/rjob-crash-window.log", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), LaunchOwnerPID: 2147483647, LaunchOwnerStartTicks: 1, LaunchOwnerBootID: "dead-boot"}
	if err := os.WriteFile(intent.LogFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	err = m.persistLocked(intent)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m2.Get(intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "launch-unknown" {
		t.Fatalf("recovered status=%q", got.Status)
	}
	if _, err := m2.StartWithIDAndSession(intent.ID, "touch /tmp/should-never-run", "", "/bin/sh", nil, "service", ""); err == nil {
		t.Fatal("unknown launch was replayed")
	}
}

func TestInvalidReservedJobIDRejected(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "", "has space", "slash/name"} {
		if _, err := m.StartWithIDAndSession(id, "true", "", "/bin/sh", nil, "service", ""); err == nil {
			t.Fatalf("accepted invalid job ID %q", id)
		}
	}
}

func TestProcessIdentityDetectsPIDReuse(t *testing.T) {
	ticks, boot, err := readProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	j := &Job{PID: os.Getpid(), ProcessStartTicks: ticks, BootID: boot}
	if !jobProcessAlive(j) {
		t.Fatal("current process identity was not recognized")
	}
	j.ProcessStartTicks++
	if jobProcessAlive(j) {
		t.Fatal("mismatched process start ticks accepted as same process")
	}
	j.ProcessStartTicks = ticks
	j.BootID = "00000000-0000-0000-0000-000000000000"
	if jobProcessAlive(j) {
		t.Fatal("mismatched boot ID accepted as same process")
	}
}

func TestSecondRuntimeDoesNotRecoverLiveLaunchOwner(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ticks, boot, err := readProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	intent := &Job{ID: "rjob-live-owner", Command: "sleep 30", Shell: "/bin/sh", Status: "launching", LogFile: dir + "/jobs/rjob-live-owner.log", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), LaunchOwnerPID: os.Getpid(), LaunchOwnerStartTicks: ticks, LaunchOwnerBootID: boot}
	if err := os.WriteFile(intent.LogFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	err = m.persistLocked(intent)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	secondRuntime, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := secondRuntime.Get(intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "launching" {
		t.Fatalf("live launch owner was misclassified: status=%q error=%q", got.Status, got.Error)
	}
	if got.LaunchOwnerPID != os.Getpid() || got.LaunchOwnerStartTicks != ticks || got.LaunchOwnerBootID != boot {
		t.Fatalf("launch owner identity was changed: %+v", got)
	}
}

func TestSecondRuntimeDoesNotTakeOverLiveRunningJob(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sh", "-lc", "sleep 5")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL); _ = child.Wait() }()
	childTicks, childBoot, err := readProcessIdentity(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	ownerTicks, ownerBoot, err := readProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "rjob-running-owner", Command: "sleep 5", Shell: "/bin/sh", PID: child.Process.Pid, PGID: child.Process.Pid, ProcessStartTicks: childTicks, BootID: childBoot, Status: "running", LogFile: dir + "/jobs/rjob-running-owner.log", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), LaunchOwnerPID: os.Getpid(), LaunchOwnerStartTicks: ownerTicks, LaunchOwnerBootID: ownerBoot}
	if err := os.WriteFile(job.LogFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	err = m.persistLocked(job)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	secondRuntime, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := secondRuntime.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" {
		t.Fatalf("second Runtime changed live job state: %q", got.Status)
	}
	if got.LaunchOwnerPID != os.Getpid() {
		t.Fatalf("second Runtime changed owner PID: %d", got.LaunchOwnerPID)
	}
}

func TestJobManagerSubprocessStableIDContention(t *testing.T) {
	if dir := os.Getenv("CYCOM_JOB_CONTENTION_DIR"); dir != "" {
		m, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.StartWithIDAndSession("rjob-shared-contention", "sleep 0.2", dir, "/bin/sh", nil, "test", "")
		if err != nil {
			t.Fatalf("reservation lost: %v", err)
		}
		return
	}
	dir := t.TempDir()
	const workers = 12
	cmds := make([]*exec.Cmd, 0, workers)
	for i := 0; i < workers; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestJobManagerSubprocessStableIDContention$")
		cmd.Env = append(os.Environ(), "CYCOM_JOB_CONTENTION_DIR="+dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	successes := 0
	for _, cmd := range cmds {
		if err := cmd.Wait(); err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one process to reserve and launch stable Job ID, got %d/%d", successes, workers)
	}
	entries, err := os.ReadDir(dir + "/jobs")
	if err != nil {
		t.Fatal(err)
	}
	jsonCount, logCount := 0, 0
	for _, entry := range entries {
		if entry.Name() == "rjob-shared-contention.json" {
			jsonCount++
		}
		if entry.Name() == "rjob-shared-contention.log" {
			logCount++
		}
	}
	if jsonCount != 1 || logCount != 1 {
		t.Fatalf("unexpected stable-ID artifacts: json=%d log=%d", jsonCount, logCount)
	}
}

func TestSecondRuntimeRefreshesJobsCreatedAfterStartup(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := first.StartWithIDAndSession("rjob-cross-runtime-visible", "sleep 0.15", "", "/bin/sh", nil, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := second.Get(job.ID)
	if err != nil {
		t.Fatalf("second Runtime did not refresh Job: %v", err)
	}
	if got.Status != "running" && got.Status != "exited" {
		t.Fatalf("unexpected refreshed status %q", got.Status)
	}
	listed := false
	for _, item := range second.List() {
		if item.ID == job.ID {
			listed = true
		}
	}
	if !listed {
		t.Fatal("second Runtime List did not refresh newly created Job")
	}
}

func TestDiskRefreshDoesNotReplaceNewerInMemorySnapshot(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "rjob-newer-cache", Status: "running", UpdatedAt: time.Now().UTC(), LogFile: dir + "/jobs/rjob-newer-cache.log"}
	if err := os.WriteFile(job.LogFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.persistLocked(job); err != nil {
		t.Fatal(err)
	}
	m.jobs[job.ID] = clone(job)
	older := clone(job)
	older.Status = "launching"
	older.UpdatedAt = job.UpdatedAt.Add(-time.Second)
	if err := m.persistLocked(older); err != nil {
		t.Fatal(err)
	}
	m.mergeDiskSnapshot(older)
	got, err := m.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" {
		t.Fatalf("older disk snapshot replaced newer cache: %q", got.Status)
	}
}

func TestConcurrentManagersPersistAtomicJobSnapshots(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 24
	start := make(chan struct{})
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		manager := first
		if i%2 != 0 {
			manager = second
		}
		go func(i int, m *Manager) {
			<-start
			job := &Job{ID: "rjob-concurrent-persist", Command: fmt.Sprintf("writer-%d", i), Shell: "/bin/sh", Status: "running", LogFile: filepath.Join(dir, "jobs", "rjob-concurrent-persist.log"), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			err := m.persistLocked(job)
			errs <- err
		}(i, manager)
	}
	close(start)
	for i := 0; i < writers; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "jobs", "rjob-concurrent-persist.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got Job
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("persisted JSON is corrupt: %v", err)
	}
	if got.ID != "rjob-concurrent-persist" {
		t.Fatalf("wrong persisted ID %q", got.ID)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestConcurrentManagersUseFreshRecordForTransactions(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := &Job{ID: "rjob-transaction", Command: "base", Shell: "/bin/sh", Status: "running", LogFile: filepath.Join(dir, "jobs", "rjob-transaction.log"), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := first.persistLocked(base); err != nil {
		t.Fatal(err)
	}
	const updates = 40
	start := make(chan struct{})
	errs := make(chan error, updates)
	for i := 0; i < updates; i++ {
		manager := first
		if i%2 != 0 {
			manager = second
		}
		go func(i int, m *Manager) {
			<-start
			_, err := m.updateJob("rjob-transaction", func(j *Job) (bool, error) {
				j.Command += fmt.Sprintf("|%d", i)
				j.UpdatedAt = time.Now().UTC()
				return true, nil
			})
			errs <- err
		}(i, manager)
	}
	close(start)
	for i := 0; i < updates; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, err := first.Get("rjob-transaction")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got.Command, "|"); n != updates {
		t.Fatalf("lost transaction updates: got %d of %d; command=%q", n, updates, got.Command)
	}
}

func TestConcurrentRuntimeRecoverySerializesDeadLaunchIntent(t *testing.T) {
	dir := t.TempDir()
	seed, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	intent := &Job{ID: "rjob-recovery-race", Command: "must-not-replay", Shell: "/bin/sh", Status: "launching", LogFile: filepath.Join(dir, "jobs", "rjob-recovery-race.log"), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), LaunchOwnerPID: 2147483647, LaunchOwnerStartTicks: 1, LaunchOwnerBootID: "dead-boot"}
	if err := seed.persistLocked(intent); err != nil {
		t.Fatal(err)
	}
	const runtimes = 12
	start := make(chan struct{})
	errs := make(chan error, runtimes)
	for i := 0; i < runtimes; i++ {
		go func() { <-start; _, err := New(dir); errs <- err }()
	}
	close(start)
	for i := 0; i < runtimes; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	final, err := seed.Get(intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != "launch-unknown" {
		t.Fatalf("recovery race produced status %q", final.Status)
	}
	if final.LaunchOwnerPID != 0 || final.LaunchOwnerStartTicks != 0 || final.LaunchOwnerBootID != "" {
		t.Fatalf("dead launch owner identity not cleared: %+v", final)
	}
}

func TestPruneReloadsLatestDiskStateBeforeDeleting(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).UTC()
	job := &Job{ID: "rjob-prune-race", Command: "keep-running", Shell: "/bin/sh", Status: "completed", LogFile: filepath.Join(dir, "jobs", "rjob-prune-race.log"), CreatedAt: old, UpdatedAt: old}
	if err := first.persistLocked(job); err != nil {
		t.Fatal(err)
	}
	first.mu.Lock()
	first.jobs[job.ID] = clone(job)
	first.mu.Unlock()
	if _, err := second.updateJob(job.ID, func(j *Job) (bool, error) { j.Status = "running"; j.UpdatedAt = time.Now().UTC(); return true, nil }); err != nil {
		t.Fatal(err)
	}
	removed, err := first.Prune(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("Prune removed %d jobs using stale cache", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "jobs", job.ID+".json")); err != nil {
		t.Fatalf("active job record was deleted: %v", err)
	}
}

func TestExternalLauncherErrorPreservesUnknownIntent(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	job, err := m.StartExternalWithSession("possibly-launched", "", "/bin/sh", nil, "desktop", "", func(string) (int, int, error) { calls++; return 0, 0, fmt.Errorf("bridge reply lost after dispatch") })
	if err == nil {
		t.Fatal("expected ambiguous launch error")
	}
	if calls != 1 {
		t.Fatalf("external launcher calls=%d", calls)
	}
	if job == nil || job.Status != "launch-unknown" {
		t.Fatalf("expected durable launch-unknown, got %#v", job)
	}
	got, err := m.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "launch-unknown" {
		t.Fatalf("persisted status=%q", got.Status)
	}
	if !strings.Contains(got.Error, "reconcile before retry") {
		t.Fatalf("missing reconciliation warning: %q", got.Error)
	}
}

func TestJobAtomicCrashHelper(t *testing.T) {
	if os.Getenv("CYCOM_JOB_CRASH_HELPER") != "1" {
		return
	}
	dir := os.Getenv("CYCOM_JOB_CRASH_DIR")
	stage := os.Getenv("CYCOM_JOB_CRASH_STAGE")
	m, err := New(dir)
	if err != nil {
		os.Exit(80)
	}
	if stage == "after-process-start" {
		pidFile := os.Getenv("CYCOM_JOB_CRASH_PID_FILE")
		m.crashHook = func(at string) {
			if at != stage {
				return
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(pidFile); err == nil {
					os.Exit(91)
				}
				time.Sleep(5 * time.Millisecond)
			}
			os.Exit(84)
		}
		_, _ = m.StartWithIDAndSession("rjob-after-start-crash", "echo $$ > "+pidFile+"; exec sleep 60", "", "/bin/sh", nil, "test", "")
		os.Exit(83)
	}
	m.crashHook = func(at string) {
		if at == stage {
			os.Exit(91)
		}
	}
	j, err := m.Get("rjob-atomic-crash")
	if err != nil {
		os.Exit(81)
	}
	j.Status = "completed"
	j.UpdatedAt = time.Now().UTC().Add(time.Second)
	if err := m.persistLocked(j); err != nil {
		os.Exit(82)
	}
	os.Exit(83)
}

func TestJobAtomicWriteSurvivesProcessCrashAtEveryDurabilityBoundary(t *testing.T) {
	stages := []struct{ name, want string }{
		{"temp-created", "accepted"},
		{"temp-written", "accepted"},
		{"temp-synced", "accepted"},
		{"renamed", "completed"},
		{"directory-synced", "completed"},
	}
	for _, tc := range stages {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			m, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			base := &Job{ID: "rjob-atomic-crash", Command: "no-op", Shell: "/bin/sh", Status: "accepted", LogFile: filepath.Join(dir, "jobs", "rjob-atomic-crash.log"), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if err := m.persistLocked(base); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestJobAtomicCrashHelper$")
			cmd.Env = append(os.Environ(), "CYCOM_JOB_CRASH_HELPER=1", "CYCOM_JOB_CRASH_DIR="+dir, "CYCOM_JOB_CRASH_STAGE="+tc.name)
			err = cmd.Run()
			ee, ok := err.(*exec.ExitError)
			if !ok || ee.ExitCode() != 91 {
				t.Fatalf("helper did not crash at %s: %v", tc.name, err)
			}
			recovered, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			got, err := recovered.Get(base.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Fatalf("status after crash=%q want %q", got.Status, tc.want)
			}
			matches, err := filepath.Glob(filepath.Join(dir, "jobs", base.ID+".tmp-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("recovery left abandoned temp files: %v", matches)
			}
		})
	}
}

func TestLaunchCrashAfterProcessStartNeverReplays(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "child.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestJobAtomicCrashHelper$")
	cmd.Env = append(os.Environ(), "CYCOM_JOB_CRASH_HELPER=1", "CYCOM_JOB_CRASH_DIR="+dir, "CYCOM_JOB_CRASH_STAGE=after-process-start", "CYCOM_JOB_CRASH_PID_FILE="+pidFile)
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 91 {
		t.Fatalf("helper did not crash after process start: %v", err)
	}
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-pid, syscall.SIGKILL)
	recovered, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := recovered.Get("rjob-after-start-crash")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "launch-unknown" {
		t.Fatalf("crash recovery status=%q", job.Status)
	}
	if job.PID != 0 {
		t.Fatalf("uncommitted PID unexpectedly recorded: %d", job.PID)
	}
	if _, err := recovered.StartWithIDAndSession(job.ID, "touch /tmp/duplicate-execution-forbidden", "", "/bin/sh", nil, "test", ""); err == nil {
		t.Fatal("same stable ID was allowed to launch again")
	}
}

func TestExternalLauncherInvalidPIDBecomesUnknown(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	job, err := m.StartExternalWithSession("possibly-launched", "", "/bin/sh", nil, "desktop", "", func(string) (int, int, error) { calls++; return 0, 0, nil })
	if err == nil {
		t.Fatal("expected invalid PID error")
	}
	if calls != 1 {
		t.Fatalf("launcher calls=%d", calls)
	}
	if job == nil || job.Status != "launch-unknown" {
		t.Fatalf("expected launch-unknown, got %#v", job)
	}
	got, err := m.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "launch-unknown" {
		t.Fatalf("persisted status=%q", got.Status)
	}
}

func TestStructuredJobPersistsIntentBeforeShellFreeRunner(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	job, err := m.StartStructuredWithID("remote-structured-1", "/usr/bin/printf", t.TempDir(), map[string]string{"LANG": "C"}, "service", "", func() (StructuredResult, error) {
		called = true
		intent, err := m.ReadSnapshot("remote-structured-1")
		if err != nil {
			t.Fatal(err)
		}
		if intent.Status != "launching" || intent.Shell != "structured" || intent.Command != "/usr/bin/printf" {
			t.Fatalf("runner observed invalid pre-launch intent: %#v", intent)
		}
		return StructuredResult{ExitCode: 0, Stdout: []byte("structured-ok")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("structured runner was not invoked")
	}
	if job.Status != "exited" || job.ExitCode == nil || *job.ExitCode != 0 {
		t.Fatalf("unexpected structured completion: %#v", job)
	}
	if job.Shell != "structured" || job.Env["LANG"] != "C" {
		t.Fatalf("structured metadata lost: %#v", job)
	}
	got, _, err := m.Tail(job.ID, 10, 4096)
	if err != nil || got != "structured-ok" {
		t.Fatalf("log=%q err=%v", got, err)
	}
	if _, err := m.StartStructuredWithID(job.ID, "/usr/bin/false", t.TempDir(), nil, "service", "", func() (StructuredResult, error) {
		t.Fatal("duplicate ID runner must not run")
		return StructuredResult{}, nil
	}); err == nil {
		t.Fatal("duplicate structured Job ID must be rejected")
	}
}

func TestStructuredJobNonzeroExitAndUnknownOutcomeAreDistinct(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nonzero, err := m.StartStructuredWithID("remote-structured-2", "/usr/bin/false", t.TempDir(), nil, "service", "", func() (StructuredResult, error) {
		return StructuredResult{ExitCode: 23, Stderr: []byte("failed")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if nonzero.Status != "failed" || nonzero.ExitCode == nil || *nonzero.ExitCode != 23 {
		t.Fatalf("nonzero exit misclassified: %#v", nonzero)
	}
	unknown, err := m.StartStructuredWithID("remote-structured-3", "/usr/bin/sleep", t.TempDir(), nil, "service", "", func() (StructuredResult, error) {
		return StructuredResult{ExitCode: -1, Stdout: []byte("partial")}, fmt.Errorf("timeout")
	})
	if err == nil || unknown.Status != "launch-unknown" || unknown.ExitCode == nil || *unknown.ExitCode != -1 {
		t.Fatalf("ambiguous result must be unknown: job=%#v err=%v", unknown, err)
	}
}

func TestStructuredJobHardCapsUntrustedRunnerOutput(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, structuredLogLimit+128)
	for i := range payload {
		payload[i] = 'x'
	}
	job, err := m.StartStructuredWithID("structured-output-cap", "/usr/bin/true", t.TempDir(), nil, "service", "", func() (StructuredResult, error) {
		return StructuredResult{ExitCode: 0, Stdout: payload}, nil
	})
	if err == nil || job == nil || job.Status != "launch-unknown" {
		t.Fatalf("oversized output must be unknown: job=%#v err=%v", job, err)
	}
	log, _, err := m.Tail(job.ID, 100, structuredLogLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != structuredLogLimit {
		t.Fatalf("persisted output length=%d, want hard cap %d", len(log), structuredLogLimit)
	}
}

func TestStructuredJobCrashRecoverySubprocess(t *testing.T) {
	if os.Getenv("CYCOM_STRUCTURED_CRASH_HELPER") == "1" {
		stateDir := os.Getenv("CYCOM_STRUCTURED_CRASH_STATE")
		m, err := New(stateDir)
		if err != nil {
			os.Exit(81)
		}
		m.crashHook = func(stage string) {
			if stage == os.Getenv("CYCOM_STRUCTURED_CRASH_STAGE") {
				os.Exit(82)
			}
		}
		_, _ = m.StartStructuredWithID("structured-crash-case", "/usr/bin/true", t.TempDir(), nil, "service", "", func() (StructuredResult, error) {
			return StructuredResult{ExitCode: 0}, nil
		})
		os.Exit(83)
	}
	for _, stage := range []string{"before-structured-runner", "after-structured-runner"} {
		t.Run(stage, func(t *testing.T) {
			stateDir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestStructuredJobCrashRecoverySubprocess$")
			cmd.Env = append(os.Environ(), "CYCOM_STRUCTURED_CRASH_HELPER=1", "CYCOM_STRUCTURED_CRASH_STATE="+stateDir, "CYCOM_STRUCTURED_CRASH_STAGE="+stage)
			if err := cmd.Run(); err == nil {
				t.Fatal("helper should exit at injected crash boundary")
			}
			m, err := New(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			job, err := m.ReadSnapshot("structured-crash-case")
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != "launch-unknown" {
				t.Fatalf("recovered status=%q, want launch-unknown", job.Status)
			}
			if job.LaunchOwnerPID != 0 || job.LaunchOwnerStartTicks != 0 || job.LaunchOwnerBootID != "" {
				t.Fatalf("recovery retained launch owner: %#v", job)
			}
		})
	}
}

func TestManagerRejectsSymlinkedJobsDirectory(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "jobs")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root); err == nil {
		t.Fatal("expected symlinked jobs directory to be rejected")
	}
}

func TestStructuredJobDoesNotFollowReplacedLogSymlink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("O_NOFOLLOW assertion is Linux-specific")
	}
	stateDir := t.TempDir()
	m, err := New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = m.StartStructuredWithID("log-symlink-case", "/usr/bin/true", t.TempDir(), nil, "service", "", func() (StructuredResult, error) {
		logPath := filepath.Join(stateDir, "jobs", "log-symlink-case.log")
		if err := os.Remove(logPath); err != nil {
			return StructuredResult{}, err
		}
		if err := os.Symlink(sentinel, logPath); err != nil {
			return StructuredResult{}, err
		}
		return StructuredResult{ExitCode: 0, Stdout: []byte("must-not-overwrite")}, nil
	})
	if err == nil {
		t.Fatal("expected symlinked log replacement to make result unknown")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "untouched" {
		t.Fatalf("sentinel was modified: %q", got)
	}
	job, err := m.ReadSnapshot("log-symlink-case")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "launch-unknown" {
		t.Fatalf("status=%q, want launch-unknown", job.Status)
	}
}

type countingLifecycleProvider struct{ calls int }

func (p *countingLifecycleProvider) WithJobLifecycleLock(callerNodeID, requestID string, fn func() error) error {
	if callerNodeID != "caller-owner" || requestID != "request-owner" {
		return fmt.Errorf("unexpected lifecycle identity %q/%q", callerNodeID, requestID)
	}
	p.calls++
	return fn()
}

func TestRemoteOwnedJobMutationsRequireLifecycleProvider(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartStructuredWithIDOwned("remote-owned-lock-legacy", "/usr/bin/true", t.TempDir(), nil, "service", "", "caller-owner", "request-owner", func() (StructuredResult, error) {
		return StructuredResult{ExitCode: 0}, nil
	}); err == nil {
		t.Fatal("legacy owned start accepted without authorization")
	}
	provider := &countingLifecycleProvider{}
	if err := m.SetLifecycleLockProvider(provider); err != nil {
		t.Fatal(err)
	}
	var job *Job
	err = m.WithLifecycleAuthorization("caller-owner", "request-owner", func(auth LifecycleAuthorization) error {
		var startErr error
		job, startErr = m.StartStructuredWithIDAuthorized(auth, "remote-owned-lock", "/usr/bin/true", t.TempDir(), nil, "service", "", "caller-owner", "request-owner", func() (StructuredResult, error) {
			return StructuredResult{ExitCode: 0}, nil
		})
		return startErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.RemoteCallerNodeID != "caller-owner" || job.RemoteRequestID != "request-owner" {
		t.Fatalf("remote ownership not persisted: %#v", job)
	}
	var expired LifecycleAuthorization
	_ = m.WithLifecycleAuthorization("caller-owner", "request-owner", func(auth LifecycleAuthorization) error { expired = auth; return nil })
	if _, err := m.StartStructuredWithIDAuthorized(expired, "remote-owned-mismatch", "/usr/bin/true", t.TempDir(), nil, "service", "", "caller-other", "request-owner", func() (StructuredResult, error) { return StructuredResult{ExitCode: 0}, nil }); err == nil {
		t.Fatal("lifecycle authorization accepted a mismatched caller identity")
	}
	if _, err := m.StartStructuredWithIDAuthorized(expired, "remote-owned-expired", "/usr/bin/true", t.TempDir(), nil, "service", "", "caller-owner", "request-owner", func() (StructuredResult, error) { return StructuredResult{ExitCode: 0}, nil }); err == nil {
		t.Fatal("expired lifecycle authorization was accepted")
	}
	if err := m.SetLifecycleLockProvider(provider); err != nil {
		t.Fatalf("idempotent provider installation failed: %v", err)
	}
	if err := m.SetLifecycleLockProvider(&countingLifecycleProvider{}); err == nil {
		t.Fatal("replacement lifecycle provider was accepted")
	}
	if _, err := m.updateJob(job.ID, func(j *Job) (bool, error) { j.Error = "locked-write"; return true, nil }); err != nil {
		t.Fatal(err)
	}
	if provider.calls < 3 {
		t.Fatalf("provider calls=%d, want authorization and mutation lock calls", provider.calls)
	}
	if _, err := m.updateJob(job.ID, func(j *Job) (bool, error) {
		j.RemoteCallerNodeID = "attacker-controlled"
		return true, nil
	}); err == nil {
		t.Fatal("remote ownership caller was mutable")
	}
	if _, err := m.updateJobUnderLifecycleLock(job.ID, func(j *Job) (bool, error) {
		j.RemoteRequestID = "attacker-controlled"
		return true, nil
	}); err == nil {
		t.Fatal("remote ownership request was mutable through the locked path")
	}
	unchanged, err := m.ReadSnapshot(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.RemoteCallerNodeID != "caller-owner" || unchanged.RemoteRequestID != "request-owner" {
		t.Fatalf("ownership binding changed after rejected mutation: %q/%q", unchanged.RemoteCallerNodeID, unchanged.RemoteRequestID)
	}
	if n, err := m.Prune(-1); err != nil || n != 0 {
		t.Fatalf("Prune removed remote-owned job: n=%d err=%v", n, err)
	}
	if _, err := m.ReadSnapshot(job.ID); err != nil {
		t.Fatalf("remote-owned Job was pruned: %v", err)
	}
}

func TestRemoteOwnedJobMutationFailsWithoutLifecycleProvider(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := &Job{ID: "remote-seeded", Command: "/usr/bin/true", Cwd: t.TempDir(), Shell: "structured", Status: "exited", LogFile: filepath.Join(m.dir, "remote-seeded.log"), CreatedAt: now, UpdatedAt: now, RemoteCallerNodeID: "caller-owner", RemoteRequestID: "request-owner"}
	if err := m.writeJobAtomic(job); err != nil {
		t.Fatal(err)
	}
	if _, err := m.updateJob(job.ID, func(j *Job) (bool, error) { j.Error = "should-not-write"; return true, nil }); err == nil {
		t.Fatal("remote mutation succeeded without lifecycle provider")
	}
}
