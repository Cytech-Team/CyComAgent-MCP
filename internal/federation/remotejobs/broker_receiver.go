package remotejobs

import (
	"fmt"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
)

// BrokerPeerAdmission binds a kernel-authenticated local IPC peer to the
// Federation caller identity under receiver-owned local configuration.
// Implementations must not trust any field from BrokerStartRequest.
type BrokerPeerAdmission interface {
	AuthorizeBrokerPeer(BrokerPeerIdentity, FederationCallerContext) error
}

// BrokerReferenceResolver resolves opaque request references using only
// receiver-owned state. It must not launch processes or mutate the ledger.
type BrokerReferenceResolver interface {
	ResolveBrokerRequest(FederationCallerContext, BrokerStartRequest) (ProcessRequest, error)
}

// BrokerDescriptorReferenceResolver returns already-open descriptors rooted in
// receiver-owned directories. Ownership transfers to HandleDescriptorBound.
type BrokerDescriptorReferenceResolver interface {
	ResolveBrokerDescriptors(FederationCallerContext, BrokerStartRequest) (BrokerResolvedDescriptors, error)
}

// BrokerReceiverHandler composes the authenticated inputs into a durable
// authorization reservation. It deliberately stops at status=accepted.
type BrokerReceiverHandler struct {
	Identity           *nodeidentity.Manager
	Policy             *PolicyStore
	Ledger             *Ledger
	Admission          BrokerPeerAdmission
	Resolver           BrokerReferenceResolver // legacy path; never use for production IPC
	DescriptorResolver BrokerDescriptorReferenceResolver
}

func (h *BrokerReceiverHandler) Handle(peer BrokerPeerIdentity, caller FederationCallerContext, req BrokerStartRequest) (BrokerAuthorization, error) {
	var zero BrokerAuthorization
	if h == nil || h.Identity == nil || h.Policy == nil || h.Ledger == nil || h.Admission == nil || h.Resolver == nil {
		return zero, fmt.Errorf("broker receiver dependencies are incomplete")
	}
	if !caller.valid() || !h.Identity.IsTrusted(caller.NodeID()) {
		return zero, fmt.Errorf("Federation caller trust was revoked or is unavailable")
	}
	if peer.PID <= 0 || peer.PIDFD < 0 || !digestPattern.MatchString(peer.ExecutableSHA256) {
		return zero, fmt.Errorf("kernel-authenticated broker peer identity is incomplete")
	}
	if err := h.Admission.AuthorizeBrokerPeer(peer, caller); err != nil {
		return zero, fmt.Errorf("local broker peer admission denied: %w", err)
	}
	if err := ValidateBrokerStartRequest(req); err != nil {
		return zero, err
	}
	resolved, err := h.Resolver.ResolveBrokerRequest(caller, req)
	if err != nil {
		return zero, fmt.Errorf("resolve receiver-owned broker references: %w", err)
	}
	// Recheck trust after potentially slow local reference resolution.
	if !h.Identity.IsTrusted(caller.NodeID()) {
		return zero, fmt.Errorf("Federation caller trust changed during broker authorization")
	}
	return AuthorizeAndReserve(h.Policy, h.Ledger, caller, req, resolved)
}

type BrokerPeerAdmissionFunc func(BrokerPeerIdentity, FederationCallerContext) error

func (f BrokerPeerAdmissionFunc) AuthorizeBrokerPeer(peer BrokerPeerIdentity, caller FederationCallerContext) error {
	if f == nil {
		return fmt.Errorf("broker peer admission function is nil")
	}
	return f(peer, caller)
}

type BrokerReferenceResolverFunc func(FederationCallerContext, BrokerStartRequest) (ProcessRequest, error)

func (f BrokerReferenceResolverFunc) ResolveBrokerRequest(caller FederationCallerContext, req BrokerStartRequest) (ProcessRequest, error) {
	if f == nil {
		return ProcessRequest{}, fmt.Errorf("broker reference resolver function is nil")
	}
	return f(caller, req)
}

// HandleDescriptorBound is the production-intended receiver path. It requires
// descriptor-owning reference resolution and never falls back to path-only
// authorization. Returned BrokerAuthorization owns the descriptors; caller
// must close it after the broker has consumed them (no dispatch is performed here).
func (h *BrokerReceiverHandler) HandleDescriptorBound(peer BrokerPeerIdentity, caller FederationCallerContext, req BrokerStartRequest) (BrokerAuthorization, error) {
	var zero BrokerAuthorization
	if h == nil || h.Identity == nil || h.Policy == nil || h.Ledger == nil || h.Admission == nil || h.DescriptorResolver == nil {
		return zero, fmt.Errorf("descriptor-bound broker receiver dependencies are incomplete")
	}
	if !caller.valid() || !h.Identity.IsTrusted(caller.NodeID()) {
		return zero, fmt.Errorf("Federation caller trust was revoked or is unavailable")
	}
	if peer.PID <= 0 || peer.PIDFD < 0 || !digestPattern.MatchString(peer.ExecutableSHA256) {
		return zero, fmt.Errorf("kernel-authenticated broker peer identity is incomplete")
	}
	if err := h.Admission.AuthorizeBrokerPeer(peer, caller); err != nil {
		return zero, fmt.Errorf("local broker peer admission denied: %w", err)
	}
	if err := ValidateBrokerStartRequest(req); err != nil {
		return zero, err
	}
	resolved, err := h.DescriptorResolver.ResolveBrokerDescriptors(caller, req)
	if err != nil {
		return zero, fmt.Errorf("resolve receiver-owned descriptor references: %w", err)
	}
	if !h.Identity.IsTrusted(caller.NodeID()) {
		if resolved.Executable != nil {
			_ = resolved.Executable.Close()
		}
		if resolved.WorkingDir != nil {
			_ = resolved.WorkingDir.Close()
		}
		return zero, fmt.Errorf("Federation caller trust changed during descriptor resolution")
	}
	return AuthorizeAndReserveDescriptors(h.Policy, h.Ledger, caller, req, resolved)
}

type BrokerDescriptorReferenceResolverFunc func(FederationCallerContext, BrokerStartRequest) (BrokerResolvedDescriptors, error)

func (f BrokerDescriptorReferenceResolverFunc) ResolveBrokerDescriptors(caller FederationCallerContext, req BrokerStartRequest) (BrokerResolvedDescriptors, error) {
	if f == nil {
		return BrokerResolvedDescriptors{}, fmt.Errorf("descriptor resolver function is nil")
	}
	return f(caller, req)
}
