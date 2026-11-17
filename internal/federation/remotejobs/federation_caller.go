package remotejobs

import (
	"fmt"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

// FederationCallerContext is a snapshot of an active locally trusted peer.
// It must be created only after the transport has verified the caller's
// purpose-bound Federation signature over the exact request payload.
type FederationCallerContext struct {
	nodeID string
}

func newFederationCallerContext(identity *nodeidentity.Manager, callerNodeID string) (FederationCallerContext, error) {
	if identity == nil {
		return FederationCallerContext{}, fmt.Errorf("receiver Federation identity is required")
	}
	if !validID.MatchString(callerNodeID) {
		return FederationCallerContext{}, fmt.Errorf("authenticated Federation caller ID is invalid")
	}
	if !identity.IsTrusted(callerNodeID) {
		return FederationCallerContext{}, fmt.Errorf("Federation caller is not actively trusted by this receiver")
	}
	return FederationCallerContext{nodeID: callerNodeID}, nil
}

func (caller FederationCallerContext) NodeID() string { return caller.nodeID }
func (caller FederationCallerContext) valid() bool    { return validID.MatchString(caller.nodeID) }
