package targets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
)

type Target struct {
	Name         string            `json:"name"`
	Transport    string            `json:"transport"`
	Host         string            `json:"host,omitempty"`
	Port         int               `json:"port,omitempty"`
	User         string            `json:"user,omitempty"`
	IdentityFile string            `json:"identity_file,omitempty"`
	WorkDir      string            `json:"work_dir,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	SSHOptions   []string          `json:"ssh_options,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Enabled      bool              `json:"enabled"`
}

type ExecOptions struct {
	Command        string
	Cwd            string
	Env            map[string]string
	TimeoutSeconds int
	MaxOutputBytes int
	Privileged     bool
}

type ExecResult struct {
	Target     string `json:"target"`
	Transport  string `json:"transport"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Truncated  bool   `json:"truncated"`
}

type Manager struct {
	file    string
	mu      sync.RWMutex
	targets map[string]Target
}

func New(stateDir string) (*Manager, error) {
	m := &Manager{file: filepath.Join(stateDir, "targets.json"), targets: make(map[string]Target)}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load() error {
	data, err := os.ReadFile(m.file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []Target
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse targets: %w", err)
	}
	for _, t := range list {
		if t.Name != "" {
			m.targets[t.Name] = normalize(t)
		}
	}
	return nil
}

func normalize(t Target) Target {
	t.Name = strings.TrimSpace(t.Name)
	if t.Transport == "" {
		t.Transport = "ssh"
	}
	t.Transport = strings.ToLower(strings.TrimSpace(t.Transport))
	if t.Port == 0 && t.Transport == "ssh" {
		t.Port = 22
	}
	if t.Transport == "local" {
		t.Enabled = true
	}
	if t.Transport == "ssh" && !t.Enabled { /* explicit disabled stays disabled */
	}
	return t
}

func (m *Manager) Upsert(t Target) (Target, error) {
	t = normalize(t)
	if t.Name == "" {
		return Target{}, fmt.Errorf("target name is required")
	}
	if t.Name == "local" {
		return Target{}, fmt.Errorf("target name local is reserved")
	}
	if t.Transport != "ssh" {
		return Target{}, fmt.Errorf("unsupported target transport %q", t.Transport)
	}
	if t.Host == "" {
		return Target{}, fmt.Errorf("ssh host is required")
	}
	if strings.HasPrefix(t.Host, "-") || strings.ContainsAny(t.Host, " \t\r\n") {
		return Target{}, fmt.Errorf("invalid ssh host")
	}
	if strings.HasPrefix(t.User, "-") || strings.ContainsAny(t.User, " \t\r\n@") {
		return Target{}, fmt.Errorf("invalid ssh user")
	}
	if t.Port <= 0 || t.Port > 65535 {
		return Target{}, fmt.Errorf("invalid ssh port")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targets[t.Name] = t
	if err := m.persistLocked(); err != nil {
		return Target{}, err
	}
	return t, nil
}

func (m *Manager) Remove(name string) error {
	if name == "local" {
		return fmt.Errorf("local target is implicit and cannot be removed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.targets[name]; !ok {
		return os.ErrNotExist
	}
	delete(m.targets, name)
	return m.persistLocked()
}

func (m *Manager) Get(name string) (Target, error) {
	if name == "" || name == "local" {
		return Target{Name: "local", Transport: "local", Enabled: true}, nil
	}
	m.mu.RLock()
	t, ok := m.targets[name]
	m.mu.RUnlock()
	if !ok {
		return Target{}, os.ErrNotExist
	}
	return clone(t), nil
}

func (m *Manager) List() []Target {
	m.mu.RLock()
	out := make([]Target, 0, len(m.targets)+1)
	out = append(out, Target{Name: "local", Transport: "local", Enabled: true})
	for _, t := range m.targets {
		out = append(out, clone(t))
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) Count() int { return len(m.List()) }

func (m *Manager) Probe(ctx context.Context, name string, timeoutSeconds int) (map[string]any, error) {
	t, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	if !t.Enabled {
		return nil, fmt.Errorf("target %s is disabled", t.Name)
	}
	start := time.Now()
	if t.Transport == "local" {
		return map[string]any{"name": t.Name, "transport": "local", "reachable": true, "latency_ms": time.Since(start).Milliseconds()}, nil
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 8
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	args := m.sshArgs(t, timeoutSeconds)
	args = append(args, destination(t), "true")
	cmd := exec.CommandContext(cctx, "ssh", args...)
	b, err := cmd.CombinedOutput()
	reachable := err == nil
	return map[string]any{"name": t.Name, "transport": t.Transport, "reachable": reachable, "latency_ms": time.Since(start).Milliseconds(), "output": strings.TrimSpace(string(b))}, nil
}

func (m *Manager) Exec(ctx context.Context, name string, in ExecOptions) (ExecResult, error) {
	t, err := m.Get(name)
	if err != nil {
		return ExecResult{}, err
	}
	if !t.Enabled {
		return ExecResult{}, fmt.Errorf("target %s is disabled", t.Name)
	}
	if strings.TrimSpace(in.Command) == "" {
		return ExecResult{}, fmt.Errorf("command is required")
	}
	timeout := in.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	max := in.MaxOutputBytes
	if max <= 0 || max > 20<<20 {
		max = 2 << 20
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if t.Transport == "local" {
		if in.Privileged {
			return ExecResult{}, fmt.Errorf("privileged local execution must be routed through the root broker")
		}
		cmd = exec.Command(platform.DefaultShell(), "-lc", in.Command)
		cwd := in.Cwd
		if cwd == "" {
			cwd = t.WorkDir
		}
		if cwd != "" {
			cmd.Dir = cwd
		}
		cmd.Env = mergedEnv(t.Env, in.Env)
	} else {
		remote, err := buildRemoteCommand(t, in)
		if err != nil {
			return ExecResult{}, err
		}
		args := m.sshArgs(t, min(timeout, 30))
		args = append(args, destination(t), remote)
		cmd = exec.Command("ssh", args...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr capBuffer
	stdout.limit = max
	stderr.limit = max
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return ExecResult{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runErr error
	timedOut := false
	select {
	case runErr = <-done:
	case <-cctx.Done():
		timedOut = errors.Is(cctx.Err(), context.DeadlineExceeded)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		runErr = <-done
	}
	res := ExecResult{Target: t.Name, Transport: t.Transport, Stdout: stdout.String(), Stderr: stderr.String(), DurationMS: time.Since(start).Milliseconds(), TimedOut: timedOut, Truncated: stdout.truncated || stderr.truncated}
	if timedOut {
		res.ExitCode = -1
		return res, nil
	}
	if runErr == nil {
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, runErr
}

func (m *Manager) Copy(ctx context.Context, name, direction, source, dest string, recursive bool, timeoutSeconds int) (map[string]any, error) {
	t, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	if t.Transport != "ssh" {
		return nil, fmt.Errorf("target_copy requires an ssh target")
	}
	if !t.Enabled {
		return nil, fmt.Errorf("target %s is disabled", t.Name)
	}
	if source == "" || dest == "" {
		return nil, fmt.Errorf("source and dest are required")
	}
	if direction != "push" && direction != "pull" {
		return nil, fmt.Errorf("direction must be push or pull")
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 300
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	args := []string{"-q", "-P", strconv.Itoa(t.Port), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=" + strconv.Itoa(min(timeoutSeconds, 30))}
	if recursive {
		args = append(args, "-r")
	}
	if t.IdentityFile != "" {
		args = append(args, "-i", t.IdentityFile)
	}
	for _, o := range t.SSHOptions {
		args = append(args, "-o", o)
	}
	remote := destination(t) + ":"
	if direction == "push" {
		args = append(args, source, remote+dest)
	} else {
		args = append(args, remote+source, dest)
	}
	start := time.Now()
	b, err := exec.CommandContext(cctx, "scp", args...).CombinedOutput()
	if err != nil {
		return map[string]any{"target": t.Name, "direction": direction, "ok": false, "duration_ms": time.Since(start).Milliseconds(), "output": string(b)}, err
	}
	return map[string]any{"target": t.Name, "direction": direction, "ok": true, "duration_ms": time.Since(start).Milliseconds(), "output": string(b)}, nil
}

func (m *Manager) sshArgs(t Target, connectTimeout int) []string {
	if connectTimeout <= 0 {
		connectTimeout = 10
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=" + strconv.Itoa(connectTimeout), "-p", strconv.Itoa(t.Port)}
	if t.IdentityFile != "" {
		args = append(args, "-i", t.IdentityFile)
	}
	for _, o := range t.SSHOptions {
		args = append(args, "-o", o)
	}
	return args
}

func buildRemoteCommand(t Target, in ExecOptions) (string, error) {
	parts := []string{}
	cwd := in.Cwd
	if cwd == "" {
		cwd = t.WorkDir
	}
	if cwd != "" {
		parts = append(parts, "cd "+shellQuote(cwd))
	}
	env := map[string]string{}
	for k, v := range t.Env {
		env[k] = v
	}
	for k, v := range in.Env {
		env[k] = v
	}
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !validEnvName(k) {
				return "", fmt.Errorf("invalid environment variable name %q", k)
			}
			parts = append(parts, "export "+k+"="+shellQuote(env[k]))
		}
	}
	cmd := in.Command
	if in.Privileged {
		cmd = "sudo -n -- /bin/sh -lc " + shellQuote(cmd)
	} else {
		cmd = "/bin/sh -lc " + shellQuote(cmd)
	}
	parts = append(parts, cmd)
	return strings.Join(parts, " && "), nil
}
func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && r >= '0' && r <= '9' {
			return false
		}
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func destination(t Target) string {
	if t.User != "" {
		return t.User + "@" + t.Host
	}
	return t.Host
}
func mergedEnv(a, b map[string]string) []string {
	env := append([]string{}, os.Environ()...)
	for k, v := range a {
		env = append(env, k+"="+v)
	}
	for k, v := range b {
		env = append(env, k+"="+v)
	}
	return env
}
func clone(t Target) Target {
	t.Env = cloneMap(t.Env)
	t.SSHOptions = append([]string{}, t.SSHOptions...)
	t.Tags = append([]string{}, t.Tags...)
	return t
}
func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m *Manager) persistLocked() error {
	list := make([]Target, 0, len(m.targets))
	for _, t := range m.targets {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.file), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.file), ".targets-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, m.file)
}

type capBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return n, nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return n, nil
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
func (b *capBuffer) String() string { return b.buf.String() }
