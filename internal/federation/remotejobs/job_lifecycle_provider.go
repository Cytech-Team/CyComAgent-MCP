package remotejobs

import (
	"fmt"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
)

// remoteJobLifecycleLockProvider adapts the remotejobs lifecycle primitive to
// the interface owned by jobs, avoiding a package dependency cycle.
type remoteJobLifecycleLockProvider struct {
	stateDir string
}

var _ jobs.LifecycleLockProvider = remoteJobLifecycleLockProvider{}

func (p remoteJobLifecycleLockProvider) WithJobLifecycleLock(callerNodeID, requestID string, fn func() error) error {
	if p.stateDir == "" {
		return fmt.Errorf("remote lifecycle lock state directory is empty")
	}
	return WithJobLifecycleLock(p.stateDir, callerNodeID, requestID, fn)
}
