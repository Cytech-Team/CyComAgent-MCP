//go:build !linux

package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

// errDesktopUnsupported keeps the aggregate desktop tools registered on
// non-Linux hosts while making it explicit that the Linux native backends
// (CyShell IPC and the Any App accessibility bridge) are unavailable.
var errDesktopUnsupported = errors.New("native desktop integration is only available on Linux")

func anyAppBackendPath() (string, bool) { return "", false }

type anyAppClient struct{ binary string }

func (c *anyAppClient) callTool(context.Context, string, json.RawMessage) (any, error) {
	return nil, errDesktopUnsupported
}

func cyshellNativeState(context.Context, sessionbridge.Client) (map[string]any, error) {
	return nil, errDesktopUnsupported
}

func isCyShellSessionEnvironment() bool { return false }

func cyshellIPC(context.Context, sessionbridge.Client, string, string, string, ...string) (string, error) {
	return "", errDesktopUnsupported
}

func parseCyShellIPCJSON(string) (any, error) { return nil, errDesktopUnsupported }

func cyshellQueryUI(context.Context, sessionbridge.Client, string) (any, error) {
	return nil, errDesktopUnsupported
}

func cyshellWindowAction(context.Context, sessionbridge.Client, string, map[string]any) (any, error) {
	return nil, errDesktopUnsupported
}

func windowControl(context.Context, json.RawMessage) (any, error) {
	return nil, errDesktopUnsupported
}

func registerDesktopCompletion(*registry.Registry, broker.Client, sessionbridge.Client) {}
