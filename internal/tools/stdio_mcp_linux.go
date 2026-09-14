//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

// stdioMCPState is the shared subprocess/JSON-RPC seam used by optional
// Linux MCP backends. Callers provide backend-specific argv, environment,
// process labels, and client identity while this type owns serialization,
// request/response matching, cancellation teardown, and restart state.
type stdioMCPState struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	decoder *json.Decoder
	nextID  uint64
	envSig  string
}

func (s *stdioMCPState) ensureStartedLocked(ctx context.Context, binary string, args, env []string, envSig, label, clientName string) error {
	if s.cmd != nil && s.cmd.Process != nil && s.envSig == envSig {
		return nil
	}
	s.stopLocked()

	cmd := exec.Command(binary, args...)
	cmd.Env = env
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("%s stdin: %w", label, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("%s stdout: %w", label, err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start %s backend: %w", label, err)
	}
	s.cmd = cmd
	s.stdin = stdin
	s.decoder = json.NewDecoder(stdout)
	s.envSig = envSig
	s.nextID = 0

	if _, err := s.rpcLocked(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    clientName,
			"version": "1",
		},
	}, label); err != nil {
		s.stopLocked()
		return fmt.Errorf("initialize %s backend: %w", label, err)
	}
	if err := s.writeLocked(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}); err != nil {
		s.stopLocked()
		return fmt.Errorf("notify %s initialized: %w", label, err)
	}
	return nil
}

func (s *stdioMCPState) rpcLocked(ctx context.Context, method string, params any, label string) (json.RawMessage, error) {
	if s.cmd == nil || s.stdin == nil || s.decoder == nil {
		return nil, fmt.Errorf("%s backend is not running", label)
	}
	s.nextID++
	id := s.nextID
	if err := s.writeLocked(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		s.stopLocked()
		return nil, fmt.Errorf("write %s %s: %w", label, method, err)
	}

	type response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data,omitempty"`
		} `json:"error,omitempty"`
		Method string `json:"method,omitempty"`
	}
	type decoded struct {
		resp response
		err  error
	}
	ch := make(chan decoded, 1)
	decoder := s.decoder
	go func() {
		for {
			var resp response
			if err := decoder.Decode(&resp); err != nil {
				ch <- decoded{err: err}
				return
			}
			if len(resp.ID) == 0 {
				continue
			}
			var got uint64
			if err := json.Unmarshal(resp.ID, &got); err != nil || got != id {
				continue
			}
			ch <- decoded{resp: resp}
			return
		}
	}()

	select {
	case <-ctx.Done():
		s.stopLocked()
		return nil, ctx.Err()
	case out := <-ch:
		if out.err != nil {
			s.stopLocked()
			return nil, fmt.Errorf("read %s %s: %w", label, method, out.err)
		}
		if out.resp.Error != nil {
			upstream := &registry.JSONRPCError{
				Code:    out.resp.Error.Code,
				Message: out.resp.Error.Message,
				Data:    append(json.RawMessage(nil), out.resp.Error.Data...),
			}
			return nil, fmt.Errorf("%s RPC %s failed: %w", label, method, upstream)
		}
		return out.resp.Result, nil
	}
}

func (s *stdioMCPState) writeLocked(value any) error {
	if s.stdin == nil {
		return io.ErrClosedPipe
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.stdin.Write(data)
	return err
}

func (s *stdioMCPState) stopLocked() {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
	s.cmd = nil
	s.stdin = nil
	s.decoder = nil
	s.envSig = ""
}
