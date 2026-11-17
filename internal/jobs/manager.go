package jobs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
)

type Job struct {
	ID                    string            `json:"id"`
	Command               string            `json:"command"`
	Cwd                   string            `json:"cwd,omitempty"`
	Env                   map[string]string `json:"env,omitempty"`
	Shell                 string            `json:"shell"`
	PID                   int               `json:"pid"`
	PGID                  int               `json:"pgid,omitempty"`
	ProcessStartTicks     uint64            `json:"process_start_ticks,omitempty"`
	BootID                string            `json:"boot_id,omitempty"`
	Status                string            `json:"status"`
	ExitCode              *int              `json:"exit_code,omitempty"`
	LogFile               string            `json:"log_file"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	Error                 string            `json:"error,omitempty"`
	ExecutionContext      string            `json:"execution_context,omitempty"`
	SessionID             string            `json:"session_id,omitempty"`
	LaunchOwnerPID        int               `json:"launch_owner_pid,omitempty"`
	LaunchOwnerStartTicks uint64            `json:"launch_owner_start_ticks,omitempty"`
	LaunchOwnerBootID     string            `json:"launch_owner_boot_id,omitempty"`
	RemoteCallerNodeID    string            `json:"remote_caller_node_id,omitempty"`
	RemoteRequestID       string            `json:"remote_request_id,omitempty"`
}

var validJobID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// StructuredResult is the bounded, shell-free execution result supplied by a
// trusted structured runner. ExitCode < 0 or an error means the outcome is
// ambiguous and must never be retried automatically.
type StructuredResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

type StructuredRunner func() (StructuredResult, error)

// structuredLogLimit bounds durable output even when a trusted runner violates
// its own output contract. Oversized results are retained only up to this cap
// and marked launch-unknown so they cannot be mistaken for complete output.
const structuredLogLimit = 1 << 20

type LifecycleLockProvider interface {
	WithJobLifecycleLock(callerNodeID, requestID string, fn func() error) error
}

// LifecycleAuthorization is a short-lived lock-ownership capability minted only
// while the configured lifecycle provider holds the matching per-job lock. It
// proves lock scope and identity, not peer authentication or dispatch approval;
// the coordinator must verify those independently before entering the callback.
// Its fields are private and the capability expires when the callback returns.
type LifecycleAuthorization struct{ state *lifecycleAuthorizationState }
type lifecycleAuthorizationState struct {
	manager      *Manager
	callerNodeID string
	requestID    string
	active       atomic.Bool
}

func (m *Manager) WithLifecycleAuthorization(callerNodeID, requestID string, fn func(LifecycleAuthorization) error) error {
	if m == nil || fn == nil || !validJobID.MatchString(callerNodeID) || !validJobID.MatchString(requestID) {
		return fmt.Errorf("valid lifecycle identity and authorization callback are required")
	}
	provider := m.lifecycleProvider()
	if provider == nil {
		return fmt.Errorf("remote Job start refused: lifecycle lock provider is not configured")
	}
	return provider.WithJobLifecycleLock(callerNodeID, requestID, func() error {
		state := &lifecycleAuthorizationState{manager: m, callerNodeID: callerNodeID, requestID: requestID}
		state.active.Store(true)
		defer state.active.Store(false)
		return fn(LifecycleAuthorization{state: state})
	})
}

func (m *Manager) validateLifecycleAuthorization(auth LifecycleAuthorization, callerNodeID, requestID string) error {
	if auth.state == nil || auth.state.manager != m || !auth.state.active.Load() || auth.state.callerNodeID != callerNodeID || auth.state.requestID != requestID {
		return fmt.Errorf("remote Job start requires an active matching lifecycle authorization")
	}
	return nil
}

type Manager struct {
	dir        string
	store      jobStore
	mu         sync.RWMutex
	providerMu sync.RWMutex
	lifecycle  LifecycleLockProvider
	jobs       map[string]*Job
	seq        atomic.Uint64
	crashHook  func(string) // nil in production; subprocess crash-injection tests only
}

// SetLifecycleLockProvider installs the cross-package lifecycle lock adapter.
// Configure it before remote jobs are dispatched; replacing it while jobs are
// mutating is unsupported.
func (m *Manager) SetLifecycleLockProvider(provider LifecycleLockProvider) error {
	if m == nil || provider == nil {
		return fmt.Errorf("lifecycle lock provider is required")
	}
	m.providerMu.Lock()
	defer m.providerMu.Unlock()
	if m.lifecycle != nil {
		if sameLifecycleProvider(m.lifecycle, provider) {
			return nil
		}
		return fmt.Errorf("lifecycle lock provider is already configured and cannot be replaced")
	}
	m.lifecycle = provider
	return nil
}

func sameLifecycleProvider(a, b LifecycleLockProvider) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if !va.IsValid() || !vb.IsValid() || va.Type() != vb.Type() {
		return false
	}
	if va.Kind() == reflect.Pointer || va.Kind() == reflect.Slice || va.Kind() == reflect.Func || va.Kind() == reflect.Chan {
		return va.Pointer() == vb.Pointer()
	}
	if va.Type().Comparable() {
		return va.Interface() == vb.Interface()
	}
	return false
}

func (m *Manager) lifecycleProvider() LifecycleLockProvider {
	m.providerMu.RLock()
	defer m.providerMu.RUnlock()
	return m.lifecycle
}

// Recover reruns durable Job recovery. Remote-owned records are processed only
// after a lifecycle provider has been installed; callers should configure the
// provider before invoking this method.
func (m *Manager) Recover() error {
	if m == nil {
		return fmt.Errorf("job manager is required")
	}
	return m.recover()
}

func New(stateDir string) (*Manager, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	dir := filepath.Join(stateDir, "jobs")
	store, err := openJobStore(dir)
	if err != nil {
		return nil, err
	}
	m := &Manager{dir: dir, store: store, jobs: make(map[string]*Job)}
	if err := m.recover(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Start(command, cwd, shell string, env map[string]string) (*Job, error) {
	return m.StartWithContext(command, cwd, shell, env, "service")
}

func (m *Manager) StartWithContext(command, cwd, shell string, env map[string]string, executionContext string) (*Job, error) {
	return m.StartWithSession(command, cwd, shell, env, executionContext, "")
}
func (m *Manager) StartWithSession(command, cwd, shell string, env map[string]string, executionContext, sessionID string) (*Job, error) {
	id := fmt.Sprintf("job-%d-%d", time.Now().UnixMilli(), m.seq.Add(1))
	return m.StartWithIDAndSession(id, command, cwd, shell, env, executionContext, sessionID)
}

// StartWithIDAndSession starts a service-context process using a caller-reserved
// stable ID. O_EXCL prevents two independent runtimes from claiming the same
// job log/ID. The ID must be a single safe filename component.
func (m *Manager) StartWithIDAndSession(id, command, cwd, shell string, env map[string]string, executionContext, sessionID string) (*Job, error) {
	if !validJobID.MatchString(id) {
		return nil, fmt.Errorf("job ID must be 1-128 safe filename characters")
	}
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command is required")
	}
	if shell == "" {
		shell = platform.DefaultShell()
	}
	if strings.TrimSpace(executionContext) == "" {
		executionContext = "service"
	}
	logFile := filepath.Join(m.dir, id+".log")

	var result *Job
	var cmd *exec.Cmd
	var logf *os.File
	m.mu.Lock()
	var err error
	err = m.store.lock(id, func() error {
		exists, err := m.store.existsRecord(id)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("job ID %q already exists", id)
		}
		logf, err = m.store.createLog(id, true)
		if err != nil {
			return err
		}
		cleanup := func(cause error) error {
			_ = logf.Close()
			_ = m.store.remove(id + ".json")
			_ = m.store.remove(id + ".log")
			return cause
		}
		ownerTicks, ownerBootID, err := readProcessIdentity(os.Getpid())
		if err != nil {
			return cleanup(fmt.Errorf("capture launch owner identity: %w", err))
		}
		now := time.Now().UTC()
		intent := &Job{ID: id, Command: command, Cwd: cwd, Env: env, Shell: shell, Status: "launching", LogFile: logFile, CreatedAt: now, UpdatedAt: now, ExecutionContext: executionContext, SessionID: sessionID, LaunchOwnerPID: os.Getpid(), LaunchOwnerStartTicks: ownerTicks, LaunchOwnerBootID: ownerBootID}
		// Commit launch intent before the process can possibly exist.
		if err := m.writeJobAtomic(intent); err != nil {
			return cleanup(fmt.Errorf("persist launch intent: %w", err))
		}
		m.jobs[id] = clone(intent)
		cmd = exec.Command(shell, "-lc", command)
		if cwd != "" {
			cmd.Dir = cwd
		}
		cmd.Env = mergedEnv(env)
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			delete(m.jobs, id)
			return cleanup(err)
		}
		m.crashAt("after-process-start")
		pgid, _ := syscall.Getpgid(cmd.Process.Pid)
		startTicks, bootID, identityErr := readProcessIdentity(cmd.Process.Pid)
		committed := clone(intent)
		committed.PID, committed.PGID, committed.Status, committed.UpdatedAt = cmd.Process.Pid, pgid, "running", time.Now().UTC()
		if identityErr == nil {
			committed.ProcessStartTicks, committed.BootID = startTicks, bootID
		} else {
			committed.Error = "process exited or identity unavailable before process identity could be captured: " + identityErr.Error()
		}
		if err := m.writeJobAtomic(committed); err != nil {
			result = clone(committed)
			m.jobs[id] = clone(intent)
			return fmt.Errorf("process %d launched but job metadata commit failed; launch outcome requires reconciliation: %w", cmd.Process.Pid, err)
		}
		m.jobs[id] = clone(committed)
		result = clone(committed)
		return nil
	})
	m.mu.Unlock()
	if err != nil {
		if cmd == nil || cmd.Process == nil {
			return nil, err
		}
		// A child exists but durable commit failed: never retry or remove its intent.
		go func() { waitErr := cmd.Wait(); _ = logf.Close(); m.finishJob(id, waitErr) }()
		return result, err
	}
	go func() { waitErr := cmd.Wait(); _ = logf.Close(); m.finishJob(id, waitErr) }()
	return result, nil
}

// StartStructuredWithID persists a durable launch intent before invoking a
// caller-supplied shell-free runner. The runner executes outside Manager locks;
// a crash while it runs leaves a launching intent that recovery marks unknown.
// This API intentionally does not claim live PID signalling/monitoring.
func (m *Manager) StartStructuredWithID(id, executable, cwd string, env map[string]string, executionContext, sessionID string, run StructuredRunner) (*Job, error) {
	return m.StartStructuredWithIDOwned(id, executable, cwd, env, executionContext, sessionID, "", "", run)
}

// StartStructuredWithIDOwned is retained for source compatibility with local
// callers. It refuses non-empty remote ownership; remote-owned starts must use
// StartStructuredWithIDAuthorized with a live capability.
func (m *Manager) StartStructuredWithIDOwned(id, executable, cwd string, env map[string]string, executionContext, sessionID, callerNodeID, requestID string, run StructuredRunner) (*Job, error) {
	if callerNodeID != "" || requestID != "" {
		return nil, fmt.Errorf("remote-owned Job start requires LifecycleAuthorization")
	}
	return m.startStructuredWithID(id, executable, cwd, env, executionContext, sessionID, "", "", run)
}

func (m *Manager) StartStructuredWithIDAuthorized(auth LifecycleAuthorization, id, executable, cwd string, env map[string]string, executionContext, sessionID, callerNodeID, requestID string, run StructuredRunner) (*Job, error) {
	if err := m.validateLifecycleAuthorization(auth, callerNodeID, requestID); err != nil {
		return nil, err
	}
	return m.startStructuredWithID(id, executable, cwd, env, executionContext, sessionID, callerNodeID, requestID, run)
}

func (m *Manager) startStructuredWithID(id, executable, cwd string, env map[string]string, executionContext, sessionID, callerNodeID, requestID string, run StructuredRunner) (*Job, error) {
	if (callerNodeID == "") != (requestID == "") || (callerNodeID != "" && (!validJobID.MatchString(callerNodeID) || !validJobID.MatchString(requestID))) {
		return nil, fmt.Errorf("remote ownership requires both valid caller and request IDs")
	}
	if !validJobID.MatchString(id) {
		return nil, fmt.Errorf("job ID must be 1-128 safe filename characters")
	}
	if strings.TrimSpace(executable) == "" || strings.TrimSpace(cwd) == "" || run == nil {
		return nil, fmt.Errorf("structured executable, working directory, and runner are required")
	}
	if strings.TrimSpace(executionContext) == "" {
		executionContext = "service"
	}
	logFile := filepath.Join(m.dir, id+".log")
	var intent *Job
	m.mu.Lock()
	var err error
	err = m.store.lock(id, func() error {
		exists, err := m.store.existsRecord(id)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("job ID %q already exists", id)
		}
		logf, err := m.store.createLog(id, false)
		if err != nil {
			return err
		}
		if err := logf.Close(); err != nil {
			_ = m.store.remove(id + ".log")
			return err
		}
		ticks, bootID, err := readProcessIdentity(os.Getpid())
		if err != nil {
			_ = m.store.remove(id + ".log")
			return fmt.Errorf("capture structured launch owner identity: %w", err)
		}
		now := time.Now().UTC()
		intent = &Job{ID: id, Command: executable, Cwd: cwd, Env: cloneEnv(env), Shell: "structured", Status: "launching", LogFile: logFile, CreatedAt: now, UpdatedAt: now, ExecutionContext: executionContext, SessionID: sessionID, LaunchOwnerPID: os.Getpid(), LaunchOwnerStartTicks: ticks, LaunchOwnerBootID: bootID, RemoteCallerNodeID: callerNodeID, RemoteRequestID: requestID}
		if err := m.writeJobAtomic(intent); err != nil {
			_ = m.store.remove(id + ".log")
			return fmt.Errorf("persist structured launch intent: %w", err)
		}
		m.jobs[id] = clone(intent)
		return nil
	})
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	m.crashAt("before-structured-runner")
	result, runErr := run()
	m.crashAt("after-structured-runner")
	outputSize := len(result.Stdout) + len(result.Stderr)
	outputOverflow := outputSize > structuredLogLimit
	if outputOverflow {
		remaining := structuredLogLimit
		if len(result.Stdout) > remaining {
			result.Stdout = result.Stdout[:remaining]
			result.Stderr = nil
		} else {
			remaining -= len(result.Stdout)
			if len(result.Stderr) > remaining {
				result.Stderr = result.Stderr[:remaining]
			}
		}
	}
	logf, logErr := m.store.openLog(id, true)
	if logErr == nil {
		if len(result.Stdout) > 0 {
			_, logErr = logf.Write(result.Stdout)
		}
		if logErr == nil && len(result.Stderr) > 0 {
			_, logErr = logf.Write(result.Stderr)
		}
		if logErr == nil {
			logErr = logf.Sync()
		}
		closeErr := logf.Close()
		if logErr == nil {
			logErr = closeErr
		}
	}
	final, updateErr := m.updateJobUnderLifecycleLock(id, func(j *Job) (bool, error) {
		if j.Status != "launching" {
			return false, fmt.Errorf("structured job left launching state unexpectedly")
		}
		j.UpdatedAt = time.Now().UTC()
		j.LaunchOwnerPID, j.LaunchOwnerStartTicks, j.LaunchOwnerBootID = 0, 0, ""
		if runErr != nil || result.ExitCode < 0 || logErr != nil || outputOverflow {
			j.Status = "launch-unknown"
			j.ExitCode = intPtr(-1)
			j.Error = "structured execution outcome unknown; reconcile before retry"
			if runErr != nil {
				j.Error += ": " + runErr.Error()
			}
			if logErr != nil {
				j.Error += "; log persistence: " + logErr.Error()
			}
			if outputOverflow {
				j.Error += "; structured output exceeded Job Manager hard limit"
			}
		} else {
			j.ExitCode = intPtr(result.ExitCode)
			if result.ExitCode == 0 {
				j.Status = "exited"
			} else {
				j.Status = "failed"
			}
		}
		return true, nil
	})
	if updateErr != nil {
		return final, fmt.Errorf("persist structured execution outcome; reconciliation required: %w", updateErr)
	}
	if runErr != nil {
		return final, fmt.Errorf("structured execution outcome unknown: %w", runErr)
	}
	if logErr != nil {
		return final, fmt.Errorf("structured execution log persistence failed: %w", logErr)
	}
	if outputOverflow {
		return final, fmt.Errorf("structured execution output exceeded %d-byte Job Manager limit", structuredLogLimit)
	}
	return final, nil
}

func cloneEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func intPtr(v int) *int { return &v }

// StartExternal registers a process launched by another execution domain (for
// example the graphical session bridge). The durable job store still owns the
// metadata, logs and signalling surface even though the runtime is not the
// process parent and therefore cannot recover an exact exit status.
func (m *Manager) StartExternal(command, cwd, shell string, env map[string]string, executionContext string, launch func(logFile string) (pid, pgid int, err error)) (*Job, error) {
	return m.StartExternalWithSession(command, cwd, shell, env, executionContext, "", launch)
}
func (m *Manager) StartExternalWithSession(command, cwd, shell string, env map[string]string, executionContext, sessionID string, launch func(logFile string) (pid, pgid int, err error)) (*Job, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command is required")
	}
	if launch == nil {
		return nil, fmt.Errorf("external job launcher is required")
	}
	if shell == "" {
		shell = platform.DefaultShell()
	}
	if strings.TrimSpace(executionContext) == "" {
		executionContext = "user"
	}
	id := fmt.Sprintf("job-%d-%d", time.Now().UnixMilli(), m.seq.Add(1))
	logFile := filepath.Join(m.dir, id+".log")
	var result *Job
	var pid int
	m.mu.Lock()
	var err error
	err = m.store.lock(id, func() error {
		f, err := m.store.createLog(id, true)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		ownerTicks, ownerBootID, err := readProcessIdentity(os.Getpid())
		if err != nil {
			_ = m.store.remove(id + ".log")
			return fmt.Errorf("capture launch owner identity: %w", err)
		}
		now := time.Now().UTC()
		intent := &Job{ID: id, Command: command, Cwd: cwd, Env: env, Shell: shell, Status: "launching", LogFile: logFile, CreatedAt: now, UpdatedAt: now, ExecutionContext: executionContext, SessionID: sessionID, LaunchOwnerPID: os.Getpid(), LaunchOwnerStartTicks: ownerTicks, LaunchOwnerBootID: ownerBootID}
		if err := m.writeJobAtomic(intent); err != nil {
			_ = m.store.remove(id + ".log")
			return fmt.Errorf("persist external launch intent: %w", err)
		}
		m.jobs[id] = clone(intent)
		launchedPID, pgid, launchErr := launch(logFile)
		if launchErr != nil {
			// A bridge error may mean the request was accepted but its reply was
			// lost. Preserve the durable intent as unknown; deleting it would let
			// a caller retry and potentially launch the same work twice.
			unknown := clone(intent)
			unknown.Status = "launch-unknown"
			unknown.Error = "external launcher returned an ambiguous error; reconcile before retry: " + launchErr.Error()
			unknown.LaunchOwnerPID, unknown.LaunchOwnerStartTicks, unknown.LaunchOwnerBootID = 0, 0, ""
			unknown.UpdatedAt = time.Now().UTC()
			if persistErr := m.writeJobAtomic(unknown); persistErr != nil {
				m.jobs[id] = clone(intent)
				return fmt.Errorf("external launcher error (%v) and could not persist unknown outcome (%w)", launchErr, persistErr)
			}
			m.jobs[id] = clone(unknown)
			result = clone(unknown)
			return fmt.Errorf("external launch outcome is unknown; reconcile before retry: %w", launchErr)
		}
		if launchedPID <= 0 {
			unknown := clone(intent)
			unknown.Status = "launch-unknown"
			unknown.Error = fmt.Sprintf("external launcher returned invalid pid %d; reconcile before retry", launchedPID)
			unknown.LaunchOwnerPID, unknown.LaunchOwnerStartTicks, unknown.LaunchOwnerBootID = 0, 0, ""
			unknown.UpdatedAt = time.Now().UTC()
			if persistErr := m.writeJobAtomic(unknown); persistErr != nil {
				m.jobs[id] = clone(intent)
				return fmt.Errorf("invalid external PID (%d) and could not persist unknown outcome (%w)", launchedPID, persistErr)
			}
			m.jobs[id] = clone(unknown)
			result = clone(unknown)
			return fmt.Errorf("external launcher returned invalid pid %d; launch outcome requires reconciliation", launchedPID)
		}
		pid = launchedPID
		startTicks, bootID, identityErr := readProcessIdentity(pid)
		committed := clone(intent)
		committed.PID, committed.PGID, committed.Status, committed.UpdatedAt = pid, pgid, "running", time.Now().UTC()
		if identityErr == nil {
			committed.ProcessStartTicks, committed.BootID = startTicks, bootID
		} else {
			committed.Error = "process exited or identity unavailable before process identity could be captured: " + identityErr.Error()
		}
		if err := m.writeJobAtomic(committed); err != nil {
			result = clone(committed)
			return fmt.Errorf("external process %d launched but metadata commit failed; reconciliation required: %w", pid, err)
		}
		m.jobs[id] = clone(committed)
		result = clone(committed)
		return nil
	})
	m.mu.Unlock()
	if result != nil && pid > 0 {
		go m.monitorExternal(id, pid)
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (m *Manager) finishJob(id string, err error) {
	_, _ = m.updateJob(id, func(current *Job) (bool, error) {
		if current.Status != "running" && current.Status != "terminating" {
			return false, nil
		}
		current.UpdatedAt = time.Now().UTC()
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = -1
				current.Error = err.Error()
			}
		}
		current.ExitCode = &code
		current.LaunchOwnerPID, current.LaunchOwnerStartTicks, current.LaunchOwnerBootID = 0, 0, ""
		if current.Status == "terminating" {
			current.Status = "terminated"
		} else {
			current.Status = "exited"
		}
		return true, nil
	})
}

func (m *Manager) monitorExternal(id string, pid int) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.RLock()
		snapshot := clone(m.jobs[id])
		m.mu.RUnlock()
		if snapshot != nil && jobProcessAlive(snapshot) {
			continue
		}
		_, _ = m.updateJob(id, func(j *Job) (bool, error) {
			if j.Status != "running" && j.Status != "running-recovered" && j.Status != "terminating" {
				return false, nil
			}
			code := -1
			j.ExitCode = &code
			if j.Status == "terminating" {
				j.Status = "terminated"
			} else {
				j.Status = "exited-external"
			}
			j.LaunchOwnerPID, j.LaunchOwnerStartTicks, j.LaunchOwnerBootID = 0, 0, ""
			j.UpdatedAt = time.Now().UTC()
			return true, nil
		})
		return
	}
}

func (m *Manager) List() []*Job {
	// Refresh from atomic on-disk snapshots so a Runtime that was started
	// earlier can observe jobs created or updated by another Runtime sharing
	// this StateDir. The in-memory cache remains useful for active monitors.
	entries, err := m.store.entries()
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			if !validJobID.MatchString(id) {
				continue
			}
			if disk, err := m.readJobFile(id); err == nil {
				m.mergeDiskSnapshot(disk)
			}
		}
	}
	m.mu.RLock()
	out := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, clone(j))
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (m *Manager) Get(id string) (*Job, error) {
	if !validJobID.MatchString(id) {
		return nil, os.ErrNotExist
	}
	if disk, err := m.readJobFile(id); err == nil {
		m.mergeDiskSnapshot(disk)
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		m.mu.Lock()
		delete(m.jobs, id)
		m.mu.Unlock()
		return nil, os.ErrNotExist
	}
	m.mu.RLock()
	snapshot := clone(m.jobs[id])
	m.mu.RUnlock()
	if snapshot == nil {
		return nil, os.ErrNotExist
	}
	return snapshot, nil
}

// ReadSnapshot reads and validates the durable Job record without changing the
// Manager cache. It is intended for forensic/reconciliation paths.
func (m *Manager) ReadSnapshot(id string) (*Job, error) {
	if !validJobID.MatchString(id) {
		return nil, os.ErrNotExist
	}
	disk, err := m.readJobFile(id)
	if err != nil {
		return nil, err
	}
	return clone(disk), nil
}

func (m *Manager) readJobFile(id string) (*Job, error) {
	data, err := m.store.readRecord(id)
	if err != nil {
		return nil, err
	}
	var disk Job
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("decode job %s: %w", id, err)
	}
	if disk.ID != id {
		return nil, fmt.Errorf("job file %s contains mismatched ID %q", id, disk.ID)
	}
	if disk.LogFile != "" && filepath.Clean(disk.LogFile) != filepath.Join(m.dir, id+".log") {
		return nil, fmt.Errorf("job file %s contains an unexpected log path", id)
	}
	return &disk, nil
}

func (m *Manager) mergeDiskSnapshot(disk *Job) {
	if disk == nil {
		return
	}
	m.mu.Lock()
	current := m.jobs[disk.ID]
	// Never replace a newer in-memory transition with an older snapshot
	// observed while another Runtime is between its write steps.
	if current == nil || !current.UpdatedAt.After(disk.UpdatedAt) {
		m.jobs[disk.ID] = clone(disk)
	}
	m.mu.Unlock()
}

func (m *Manager) Signal(id, signal string) (*Job, error) {
	sig, err := parseSignal(signal)
	if err != nil {
		return nil, err
	}
	var target int
	_, err = m.updateJob(id, func(j *Job) (bool, error) {
		if j.ProcessStartTicks != 0 && j.BootID != "" && !jobProcessAlive(j) {
			return false, fmt.Errorf("job process identity no longer matches recorded PID; refusing to signal a potentially reused PID")
		}
		target = j.PID
		if j.PGID > 0 {
			target = -j.PGID
		}
		if sig == syscall.SIGTERM || sig == syscall.SIGKILL || sig == syscall.SIGINT {
			j.Status = "terminating"
		}
		j.UpdatedAt = time.Now().UTC()
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	if err := syscall.Kill(target, sig); err != nil {
		return nil, err
	}
	return m.Get(id)
}

func (m *Manager) Tail(id string, lines, maxBytes int) (string, bool, error) {
	j, err := m.Get(id)
	if err != nil {
		return "", false, err
	}
	if lines <= 0 {
		lines = 200
	}
	if maxBytes <= 0 || maxBytes > 2<<20 {
		maxBytes = 256 << 10
	}
	if j.LogFile != filepath.Join(m.dir, id+".log") {
		return "", false, fmt.Errorf("job %s contains an unexpected log path", id)
	}
	f, err := m.store.openLog(id, false)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	start := st.Size() - int64(maxBytes)
	truncated := start > 0
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", false, err
	}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64<<10), 2<<20)
	ring := make([]string, 0, lines)
	for scan.Scan() {
		if len(ring) == lines {
			copy(ring, ring[1:])
			ring[len(ring)-1] = scan.Text()
		} else {
			ring = append(ring, scan.Text())
		}
	}
	if err := scan.Err(); err != nil {
		return "", truncated, err
	}
	return strings.Join(ring, "\n"), truncated, nil
}

func (m *Manager) Prune(olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		olderThan = 7 * 24 * time.Hour
	}
	cutoff := time.Now().Add(-olderThan)
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for id := range m.jobs {
		err := m.store.lock(id, func() error {
			current, err := m.readJobFile(id)
			if errors.Is(err, os.ErrNotExist) {
				delete(m.jobs, id)
				return nil
			}
			if err != nil {
				return err
			}
			if current.RemoteCallerNodeID != "" || current.RemoteRequestID != "" {
				// Remote Job retention is governed by the remote lifecycle protocol;
				// generic Prune has no safe ownership lock boundary.
				m.jobs[id] = clone(current)
				return nil
			}
			if strings.HasPrefix(current.Status, "running") || current.Status == "terminating" || current.Status == "launching" || current.UpdatedAt.After(cutoff) {
				m.jobs[id] = clone(current)
				return nil
			}
			if err := m.store.remove(id + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if current.LogFile != "" {
				if err := m.store.remove(id + ".log"); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			delete(m.jobs, id)
			removed++
			return nil
		})
		if err != nil {
			return removed, fmt.Errorf("prune job %s: %w", id, err)
		}
	}
	return removed, nil
}

func (m *Manager) recover() error {
	entries, err := m.store.entries()
	if err != nil {
		return err
	}
	// A process can die before its deferred temp-file cleanup runs. Remove
	// abandoned atomic-write temps only while holding the same per-job lock,
	// so a concurrent writer can never lose its active temp file.
	for _, entry := range entries {
		name := entry.Name()
		marker := strings.Index(name, ".tmp-")
		if entry.IsDir() || marker <= 0 {
			continue
		}
		id := name[:marker]
		if !validJobID.MatchString(id) {
			continue
		}
		if err := m.store.lock(id, func() error { return m.store.removeTemps(id) }); err != nil {
			return fmt.Errorf("cleanup abandoned job temp %s: %w", id, err)
		}
	}
	entries, err = m.store.entries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validJobID.MatchString(id) {
			continue
		}
		snapshot, readErr := m.readJobFile(id)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read job %s for recovery: %w", id, readErr)
		}
		if (snapshot.RemoteCallerNodeID != "" || snapshot.RemoteRequestID != "") && m.lifecycleProvider() == nil {
			// Defer remote-owned recovery until the application installs the
			// cross-package lifecycle provider. Never mutate it under only the
			// Job Manager store lock.
			continue
		}
		job, err := m.updateJob(id, func(j *Job) (bool, error) {
			if j.ID != id {
				return false, fmt.Errorf("job filename/id mismatch")
			}
			changed := false
			if j.Status == "launching" && !launchOwnerAlive(j) {
				j.Status = "launch-unknown"
				j.Error = "launch owner stopped during process launch; outcome requires reconciliation and must not be replayed"
				j.LaunchOwnerPID, j.LaunchOwnerStartTicks, j.LaunchOwnerBootID = 0, 0, ""
				j.UpdatedAt = time.Now().UTC()
				changed = true
			}
			if j.Status == "running" || j.Status == "terminating" || j.Status == "running-recovered" {
				if launchOwnerAlive(j) {
					// A live Runtime owns this job; leave its state and monitor alone.
				} else if jobProcessAlive(j) {
					if j.Status != "running-recovered" || j.LaunchOwnerPID != 0 || j.LaunchOwnerStartTicks != 0 || j.LaunchOwnerBootID != "" {
						j.Status = "running-recovered"
						j.LaunchOwnerPID, j.LaunchOwnerStartTicks, j.LaunchOwnerBootID = 0, 0, ""
						j.UpdatedAt = time.Now().UTC()
						changed = true
					}
				} else {
					j.Status = "exited-while-runtime-offline"
					code := -1
					j.ExitCode = &code
					j.LaunchOwnerPID, j.LaunchOwnerStartTicks, j.LaunchOwnerBootID = 0, 0, ""
					j.UpdatedAt = time.Now().UTC()
					changed = true
				}
			}
			return changed, nil
		})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("recover job %s: %w", id, err)
		}
		if job != nil && job.Status == "running-recovered" {
			go m.monitorRecovered(job.ID, job.PID)
		}
	}
	return nil
}

func (m *Manager) monitorRecovered(id string, pid int) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.RLock()
		snapshot := clone(m.jobs[id])
		m.mu.RUnlock()
		if snapshot != nil && jobProcessAlive(snapshot) {
			continue
		}
		_, _ = m.updateJob(id, func(j *Job) (bool, error) {
			if j.Status != "running-recovered" {
				return false, nil
			}
			code := -1
			j.ExitCode = &code
			j.Status = "exited-after-recovery"
			j.LaunchOwnerPID, j.LaunchOwnerStartTicks, j.LaunchOwnerBootID = 0, 0, ""
			j.UpdatedAt = time.Now().UTC()
			return true, nil
		})
		return
	}
}

func (m *Manager) crashAt(stage string) {
	if m.crashHook != nil {
		m.crashHook(stage)
	}
}

func (m *Manager) writeJobAtomic(j *Job) error {
	if j == nil || !validJobID.MatchString(j.ID) {
		return fmt.Errorf("invalid job record")
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := m.store.writeRecord(j.ID, data, m.crashAt); err != nil {
		return err
	}
	return nil
}

func (m *Manager) persistLocked(j *Job) error {
	if j == nil {
		return fmt.Errorf("invalid job record")
	}
	return m.store.lock(j.ID, func() error { return m.writeJobAtomic(j) })
}

// updateJob performs a complete cross-process read-modify-write transaction.
// Lock order is always Manager.mu then the per-job flock; no callback may call
// back into Manager while this lock is held.
func (m *Manager) updateJob(id string, mutate func(*Job) (bool, error)) (*Job, error) {
	if !validJobID.MatchString(id) {
		return nil, os.ErrNotExist
	}
	// Atomic job snapshots make this ownership discovery safe without holding
	// Manager.mu or the per-job lock. The record is reloaded after lifecycle
	// acquisition by updateJobUnderLifecycleLock.
	snapshot, err := m.readJobFile(id)
	if err != nil {
		return nil, err
	}
	if snapshot.RemoteCallerNodeID != "" || snapshot.RemoteRequestID != "" {
		if snapshot.RemoteCallerNodeID == "" || snapshot.RemoteRequestID == "" {
			return nil, fmt.Errorf("incomplete remote job ownership binding")
		}
		provider := m.lifecycleProvider()
		if provider == nil {
			return nil, fmt.Errorf("remote Job mutation refused: lifecycle lock provider is not configured")
		}
		var result *Job
		err := provider.WithJobLifecycleLock(snapshot.RemoteCallerNodeID, snapshot.RemoteRequestID, func() error {
			var updateErr error
			result, updateErr = m.updateJobUnderLifecycleLock(id, func(current *Job) (bool, error) {
				if current.RemoteCallerNodeID != snapshot.RemoteCallerNodeID || current.RemoteRequestID != snapshot.RemoteRequestID {
					return false, fmt.Errorf("remote Job ownership binding changed during mutation")
				}
				return mutate(current)
			})
			return updateErr
		})
		return result, err
	}
	return m.updateJobUnderLifecycleLock(id, mutate)
}

// updateJobUnderLifecycleLock is for callers already holding the remote
// lifecycle lock, or for non-remote jobs. It always reloads under the store lock.
func (m *Manager) updateJobUnderLifecycleLock(id string, mutate func(*Job) (bool, error)) (*Job, error) {
	if !validJobID.MatchString(id) {
		return nil, os.ErrNotExist
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result *Job
	var err error
	err = m.store.lock(id, func() error {
		current, err := m.readJobFile(id)
		if err != nil {
			return err
		}
		ownerCaller, ownerRequest := current.RemoteCallerNodeID, current.RemoteRequestID
		if (ownerCaller == "") != (ownerRequest == "") {
			return fmt.Errorf("incomplete remote Job ownership binding")
		}
		changed, err := mutate(current)
		if err != nil {
			return err
		}
		if current.RemoteCallerNodeID != ownerCaller || current.RemoteRequestID != ownerRequest {
			return fmt.Errorf("Job ownership binding is immutable")
		}
		if changed {
			if err := m.writeJobAtomic(current); err != nil {
				return err
			}
		}
		m.jobs[id] = clone(current)
		result = clone(current)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// launchOwnerAlive determines whether a durable launching intent still belongs
// to a live Runtime process. This prevents a second Runtime sharing the same
// StateDir from incorrectly recovering another process's active launch.
func launchOwnerAlive(j *Job) bool {
	if j == nil || j.LaunchOwnerPID <= 0 || j.LaunchOwnerStartTicks == 0 || j.LaunchOwnerBootID == "" {
		return false
	}
	ticks, boot, err := readProcessIdentity(j.LaunchOwnerPID)
	return err == nil && ticks == j.LaunchOwnerStartTicks && boot == j.LaunchOwnerBootID
}

// readProcessIdentity captures Linux process start ticks and boot ID. Together
// they distinguish a still-running process from PID reuse across runtime restarts.
func readProcessIdentity(pid int) (uint64, string, error) {
	if pid <= 0 {
		return 0, "", fmt.Errorf("invalid process ID")
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, "", err
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return 0, "", fmt.Errorf("malformed /proc process stat")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) <= 19 {
		return 0, "", fmt.Errorf("incomplete /proc process stat")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, "", err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return 0, "", err
	}
	return ticks, strings.TrimSpace(string(boot)), nil
}

func jobProcessAlive(j *Job) bool {
	if j == nil || j.PID <= 0 {
		return false
	}
	if j.ProcessStartTicks == 0 || j.BootID == "" {
		return processAlive(j.PID)
	} // legacy records
	ticks, boot, err := readProcessIdentity(j.PID)
	return err == nil && ticks == j.ProcessStartTicks && boot == j.BootID
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func clone(j *Job) *Job {
	if j == nil {
		return nil
	}
	c := *j
	if j.Env != nil {
		c.Env = make(map[string]string, len(j.Env))
		for k, v := range j.Env {
			c.Env[k] = v
		}
	}
	return &c
}

func parseSignal(s string) (syscall.Signal, error) {
	switch strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(s), "SIG")) {
	case "", "TERM":
		return syscall.SIGTERM, nil
	case "INT":
		return syscall.SIGINT, nil
	case "HUP":
		return syscall.SIGHUP, nil
	case "KILL":
		return syscall.SIGKILL, nil
	case "USR1":
		return syscall.SIGUSR1, nil
	case "USR2":
		return syscall.SIGUSR2, nil
	default:
		return 0, fmt.Errorf("unsupported signal %q", s)
	}
}

func mergedEnv(extra map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
