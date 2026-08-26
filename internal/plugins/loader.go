package plugins

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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

type Manifest struct {
	Name           string            `json:"name"`
	Title          string            `json:"title,omitempty"`
	Description    string            `json:"description"`
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	InputSchema    map[string]any    `json:"input_schema"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	MaxOutputBytes int               `json:"max_output_bytes,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

type Manager struct {
	Dir       string
	mu        sync.RWMutex
	manifests map[string]Manifest
}

func New(dir string) *Manager { return &Manager{Dir: dir, manifests: make(map[string]Manifest)} }

func (m *Manager) LoadInto(r *registry.Registry) ([]string, error) {
	if m.Dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return nil, err
	}
	loaded := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(m.Dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var mf Manifest
		if err := json.Unmarshal(data, &mf); err != nil {
			continue
		}
		if mf.Name == "" || mf.Command == "" || mf.Description == "" {
			continue
		}
		if mf.InputSchema == nil {
			mf.InputSchema = registry.ObjectSchema(nil, nil)
		}
		if !filepath.IsAbs(mf.Command) {
			if resolved, err := exec.LookPath(mf.Command); err == nil {
				mf.Command = resolved
			}
		}
		manifest := mf
		err = r.Add(registry.Tool{
			Name: manifest.Name, Title: manifest.Title, Description: manifest.Description,
			InputSchema: manifest.InputSchema, Source: "plugin:" + path,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				return execute(ctx, manifest, raw)
			},
		})
		if err != nil {
			continue
		}
		m.mu.Lock()
		m.manifests[mf.Name] = mf
		m.mu.Unlock()
		loaded = append(loaded, mf.Name)
	}
	sort.Strings(loaded)
	return loaded, nil
}

func (m *Manager) Names() []string {
	m.mu.RLock()
	out := make([]string, 0, len(m.manifests))
	for name := range m.manifests {
		out = append(out, name)
	}
	m.mu.RUnlock()
	sort.Strings(out)
	return out
}

func (m *Manager) ReloadInto(r *registry.Registry) ([]string, error) {
	r.RemoveSourcePrefix("plugin:")
	m.mu.Lock()
	m.manifests = make(map[string]Manifest)
	m.mu.Unlock()
	return m.LoadInto(r)
}

func execute(ctx context.Context, mf Manifest, raw json.RawMessage) (any, error) {
	timeout := 60 * time.Second
	if mf.TimeoutSeconds > 0 {
		timeout = time.Duration(mf.TimeoutSeconds) * time.Second
	}
	max := mf.MaxOutputBytes
	if max <= 0 || max > 10<<20 {
		max = 2 << 20
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.Command(mf.Command, mf.Args...)
	env := append([]string{}, os.Environ()...)
	for k, v := range mf.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(raw)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = max, max
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-done:
	case <-cctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		runErr = <-done
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("external tool %s timed out after %s", mf.Name, timeout)
		}
		return nil, cctx.Err()
	}
	if runErr != nil {
		return nil, fmt.Errorf("external tool %s failed: %v: %s", mf.Name, runErr, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return map[string]any{"ok": true, "truncated": stdout.truncated || stderr.truncated}, nil
	}
	var value any
	if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
		return map[string]any{"stdout": stdout.String(), "stderr": stderr.String(), "truncated": stdout.truncated || stderr.truncated}, nil
	}
	return value, nil
}

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remain := b.limit - b.buf.Len()
	if remain <= 0 {
		b.truncated = true
		return n, nil
	}
	if len(p) > remain {
		_, _ = b.buf.Write(p[:remain])
		b.truncated = true
		return n, nil
	}
	_, _ = b.buf.Write(p)
	return n, nil
}
func (b *cappedBuffer) String() string { return b.buf.String() }
func (b *cappedBuffer) Len() int       { return b.buf.Len() }
func (b *cappedBuffer) Bytes() []byte  { return b.buf.Bytes() }
