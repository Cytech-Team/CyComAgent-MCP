package vmevidence

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type TestPlan struct {
	digest        string
	SchemaVersion string               `json:"schema_version"`
	PlanID        string               `json:"plan_id"`
	Cases         []PlannedCase        `json:"cases"`
	Platform      PlatformRequirements `json:"platform"`
}
type PlannedCase struct {
	CaseID              string                `json:"case_id"`
	TestName            string                `json:"test_name"`
	ExpectedOutcome     string                `json:"expected_outcome"`
	RequiredEvidence    []EvidenceRequirement `json:"required_evidence"`
	RequiredTransitions []string              `json:"required_transitions"`
}
type EvidenceRequirement struct {
	Kind           string   `json:"kind"`
	AllowedSources []string `json:"allowed_sources"`
}
type PlatformRequirements struct {
	Architecture   string `json:"architecture"`
	CgroupMode     string `json:"cgroup_mode"`
	RequireSeccomp bool   `json:"require_seccomp"`
}

var (
	fixtureKinds     = setOf("ELF_STATIC", "ELF_DYNAMIC", "SCRIPT", "NON_ELF")
	fixturePolicies  = setOf("ALLOW", "REJECT")
	caseOutcomes     = setOf("EXECUTED", "REJECTED", "TIMED_OUT", "NOT_RUN", "BLOCKED")
	caseResults      = setOf("PASS", "FAIL", "BLOCKED")
	evidenceSources  = setOf("HOST_RUNNER", "TRUSTED_GUEST_CHANNEL", "GUEST_UNTRUSTED_LOG")
	evidenceKinds    = setOf("HOST_VM_CONFIG", "HOST_PROCESS_LIFECYCLE", "HOST_TEARDOWN", "HOST_ISOLATION", "GUEST_KERNEL_FACTS", "SYSCALL_RESULT", "RESOURCE_LIMIT_RESULT", "EXECUTION_RESULT", "DIAGNOSTIC_LOG")
	summaryCodes     = setOf("ASSERTION_MET", "ASSERTION_FAILED", "OBSERVATION_RECORDED", "UNAVAILABLE", "MISMATCH")
	transitionStates = setOf("received", "validated", "reserved", "child_gated", "containment_confirmed", "released", "running", "terminal", "rejected", "unknown")
	hostEventTypes   = setOf("VM_START", "VM_PAUSE", "VM_RESET", "VM_POWER_OFF", "VM_DESTROY", "OVERLAY_DESTROY", "WATCHDOG_TIMEOUT", "HOST_OBSERVATION")
)

func setOf(v ...string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, s := range v {
		m[s] = struct{}{}
	}
	return m
}
func inSet(m map[string]struct{}, s string) bool { _, ok := m[s]; return ok }

func DecodeTrustedTestPlan(raw []byte, envelope SignatureEnvelope, keys map[string]ed25519.PublicKey) (TestPlan, error) {
	var p TestPlan
	if err := VerifyDetachedSignature(raw, envelope, keys); err != nil {
		return p, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return TestPlan{}, fmt.Errorf("%w: test plan schema: %v", ErrInvalidEvidence, err)
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(p); err != nil {
		return TestPlan{}, err
	}
	if !bytes.Equal(raw, bytes.TrimSuffix(b.Bytes(), []byte("\n"))) {
		return TestPlan{}, fmt.Errorf("%w: test plan is not canonical", ErrInvalidEvidence)
	}
	if err := validateTestPlan(p); err != nil {
		return TestPlan{}, err
	}
	p.digest = ManifestDigest(raw)
	return p, nil
}

// DecodeTrustedTestPlanWithStore verifies a plan using a purpose-scoped trust store.
func DecodeTrustedTestPlanWithStore(raw []byte, envelope SignatureEnvelope, at time.Time, store TrustStore) (TestPlan, error) {
	if err := VerifyWithTrustStore(raw, envelope, KeyPurposeTestPlan, at, store); err != nil {
		return TestPlan{}, err
	}
	// Signature has already been checked against the scoped trust store. The
	// local verification uses the same resolved key only after store approval.
	key, err := store.LookupTrustedKey(envelope.KeyID, KeyPurposeTestPlan, at)
	if err != nil {
		return TestPlan{}, fmt.Errorf("%w: trusted plan key lookup: %v", ErrInvalidEvidence, err)
	}
	return DecodeTrustedTestPlan(raw, envelope, map[string]ed25519.PublicKey{envelope.KeyID: key})
}

func validateTestPlan(p TestPlan) error {
	bad := func(s string) error { return fmt.Errorf("%w: test plan %s", ErrInvalidEvidence, s) }
	if p.SchemaVersion != "1.0" || p.PlanID == "" || len(p.Cases) == 0 || len(p.Cases) > 256 {
		return bad("identity or case count invalid")
	}
	if p.Platform.Architecture == "" || p.Platform.CgroupMode != "v2" {
		return bad("platform requirements invalid")
	}
	seen := map[string]struct{}{}
	for _, c := range p.Cases {
		if c.CaseID == "" || c.TestName == "" || !inSet(caseOutcomes, c.ExpectedOutcome) {
			return bad("case identity or expected outcome invalid")
		}
		if _, ok := seen[c.CaseID]; ok {
			return bad("duplicate case ID")
		}
		seen[c.CaseID] = struct{}{}
		if len(c.RequiredEvidence) == 0 || len(c.RequiredEvidence) > 32 || len(c.RequiredTransitions) > 64 {
			return bad("case requirements count invalid")
		}
		kinds := map[string]struct{}{}
		for _, r := range c.RequiredEvidence {
			if !inSet(evidenceKinds, r.Kind) || len(r.AllowedSources) == 0 {
				return bad("evidence requirement invalid")
			}
			if _, ok := kinds[r.Kind]; ok {
				return bad("duplicate evidence requirement")
			}
			kinds[r.Kind] = struct{}{}
			srcs := map[string]struct{}{}
			for _, src := range r.AllowedSources {
				if !inSet(evidenceSources, src) || src == "GUEST_UNTRUSTED_LOG" {
					return bad("untrusted evidence source cannot satisfy assertion")
				}
				if _, ok := srcs[src]; ok {
					return bad("duplicate allowed source")
				}
				srcs[src] = struct{}{}
			}
		}
		for _, state := range c.RequiredTransitions {
			if !inSet(transitionStates, state) {
				return bad("unknown transition state")
			}
		}
	}
	return nil
}

type DecisionReport struct {
	Decision string
	Reasons  []string
}

// EvaluateAgainstTrustedPlan derives a decision; Manifest.Decision is only a claim.
func EvaluateAgainstTrustedPlan(m Manifest, p TestPlan) (DecisionReport, error) {
	if p.digest == "" || m.TestPlanSHA256 != p.digest {
		return DecisionReport{}, fmt.Errorf("%w: manifest does not bind the exact trusted test plan", ErrInvalidEvidence)
	}
	if err := validateManifestStructure(m); err != nil {
		return DecisionReport{}, err
	}
	if err := validateTestPlan(p); err != nil {
		return DecisionReport{}, err
	}
	plan := map[string]PlannedCase{}
	for _, c := range p.Cases {
		plan[c.CaseID] = c
	}
	got := map[string]CaseRecord{}
	for _, c := range m.Cases {
		if _, ok := plan[c.CaseID]; !ok {
			return DecisionReport{}, fmt.Errorf("%w: unplanned case %s", ErrInvalidEvidence, c.CaseID)
		}
		got[c.CaseID] = c
	}
	r := DecisionReport{Decision: "PASS"}
	if m.Guest.Architecture != p.Platform.Architecture || m.Guest.CgroupMode != p.Platform.CgroupMode || (p.Platform.RequireSeccomp && !m.Guest.SeccompAvailable) {
		r.Decision = "BLOCKED"
		r.Reasons = append(r.Reasons, "guest platform does not satisfy trusted test plan")
	}
	block := func(reason string) {
		r.Reasons = append(r.Reasons, reason)
		if r.Decision != "FAIL" {
			r.Decision = "BLOCKED"
		}
	}
	for id, spec := range plan {
		c, ok := got[id]
		if !ok {
			block("missing planned case: " + id)
			continue
		}
		if c.TestName != spec.TestName || !inSet(caseOutcomes, c.Expected) || !inSet(caseOutcomes, c.Observed) || !inSet(caseResults, c.Result) {
			block("case schema mismatch: " + id)
			continue
		}
		if c.Expected != spec.ExpectedOutcome || c.Observed != spec.ExpectedOutcome || c.Result == "FAIL" {
			r.Reasons = append(r.Reasons, "case assertion failed: "+id)
			r.Decision = "FAIL"
			continue
		}
		if c.Result != "PASS" {
			block("case not proven: " + id)
			continue
		}
		for _, req := range spec.RequiredEvidence {
			found := false
			for _, ev := range c.Evidence {
				if ev.Kind == req.Kind && inSet(setOf(req.AllowedSources...), ev.Source) && ev.SummaryCode == "ASSERTION_MET" {
					found = true
					break
				}
			}
			if !found {
				block("missing trusted evidence " + req.Kind + " for " + id)
			}
		}
		states := []string{}
		for _, tr := range c.Transitions {
			states = append(states, tr.State)
		}
		j := 0
		for _, state := range states {
			if j < len(spec.RequiredTransitions) && state == spec.RequiredTransitions[j] {
				j++
			}
		}
		if j != len(spec.RequiredTransitions) {
			block("required transition sequence missing for " + id)
		}
	}
	if !m.Teardown.VMProcessAbsent || !m.Teardown.OverlayAbsent || !m.Teardown.TemporaryResourcesAbsent || m.Teardown.VerifiedBy != "HOST_RUNNER" {
		block("host teardown not fully verified")
	}
	requiredEvents := map[string]bool{"VM_START": false, "VM_DESTROY": false, "OVERLAY_DESTROY": false}
	for _, event := range m.HostEvents {
		if _, required := requiredEvents[event.EventType]; required && event.Actor == "HOST_RUNNER" && event.Result == "RECORDED" {
			requiredEvents[event.EventType] = true
		}
	}
	for eventType, observed := range requiredEvents {
		if !observed {
			block("missing host lifecycle event: " + eventType)
		}
	}
	if r.Decision == "PASS" && (m.VM.GuestAgentEnabled || len(m.VM.NetworkDevices) > 0 || len(m.VM.HostMounts) > 0) {
		block("VM isolation profile incomplete")
	}
	sort.Strings(r.Reasons)
	return r, nil
}
