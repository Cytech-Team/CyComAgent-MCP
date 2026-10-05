package tools

import (
	"fmt"
	"os"
	"strings"
)

const isolatedAgentWorkspaceBusAddress = "unix:path=/dev/null"

// agentWorkspaceAutoEnabled controls whether automatic desktop routing prefers
// the isolated Agent Workspace. Explicit target=current / target=agent_workspace
// always wins. Default true preserves the existing isolated-by-default behavior.
func agentWorkspaceAutoEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("CYCOM_AGENT_WORKSPACE_AUTO")))
	switch value {
	case "0", "false", "no", "off", "current", "desktop":
		return false
	default:
		return true
	}
}

// automaticDesktopTarget keeps automatic desktop operations isolated by
// default. The physical desktop is reachable through auto routing only when
// the operator explicitly opts out with CYCOM_AGENT_WORKSPACE_AUTO=0 (or one
// of its documented false values).
func automaticDesktopTarget() (target string, env []string, display string, err error) {
	if !agentWorkspaceAutoEnabled() {
		return "current", nil, "", nil
	}
	env, display, err = cycomHeadlessEnvironment()
	if err != nil {
		return "", nil, "", fmt.Errorf("Agent Workspace is unavailable for automatic desktop routing: %w; select target=current to explicitly use the physical desktop or set CYCOM_AGENT_WORKSPACE_AUTO=0 to opt out of isolation", err)
	}
	return "agent_workspace", env, display, nil
}

func normalizeDesktopTarget(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "auto", nil
	case "current", "desktop":
		return "current", nil
	case "agent_workspace", "agent-workspace", "workspace", "headless":
		// "headless" remains accepted as a compatibility alias but is no
		// longer advertised to clients or shown to users.
		return "agent_workspace", nil
	default:
		return "", fmt.Errorf("unsupported desktop target %q", value)
	}
}
