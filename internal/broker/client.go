package broker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

type ExecRequest struct {
	Command        string            `json:"command"`
	Stdin          string            `json:"stdin,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Shell          string            `json:"shell,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	MaxOutputBytes int               `json:"max_output_bytes,omitempty"`
}

type ExecResponse struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Truncated  bool   `json:"truncated"`
	Error      string `json:"error,omitempty"`
}

type Client struct {
	Socket string
}

func (c Client) Available() bool {
	if c.Socket == "" {
		return false
	}
	conn, err := net.DialTimeout("unix", c.Socket, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (c Client) Exec(ctx context.Context, req ExecRequest) (ExecResponse, error) {
	var out ExecResponse
	if c.Socket == "" {
		return out, fmt.Errorf("privilege broker is not configured")
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return out, fmt.Errorf("connect privilege broker: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return out, err
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&out); err != nil {
		return out, err
	}
	if out.Error != "" {
		return out, fmt.Errorf("privilege broker: %s", out.Error)
	}
	return out, nil
}
