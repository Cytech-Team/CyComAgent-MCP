//go:build !linux

package sessionbridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Client struct {
	Socket string
}

func DefaultSocketPath() string {
	if path := strings.TrimSpace(os.Getenv("CYCOM_SESSION_SOCKET")); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), "cycomagent-session.sock")
}

func NewClient(socket string) Client { return Client{Socket: socket} }
func (Client) Available() bool       { return false }
func (Client) Status(context.Context) (Status, error) {
	return Status{Available: false}, fmt.Errorf("desktop session bridge is currently Linux-only")
}
func (Client) Exec(context.Context, ExecRequest) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("desktop session bridge is currently Linux-only")
}
func (Client) Spawn(context.Context, SpawnRequest) (SpawnResult, error) {
	return SpawnResult{}, fmt.Errorf("desktop session bridge is currently Linux-only")
}
func Run(context.Context, string) error {
	return fmt.Errorf("desktop session bridge is currently Linux-only")
}
