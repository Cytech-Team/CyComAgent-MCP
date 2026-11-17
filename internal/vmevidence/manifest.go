package vmevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

var (
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	noncePattern  = regexp.MustCompile(`^[0-9a-f]{64,}$`)
)

type Manifest struct {
	SchemaVersion  string            `json:"schema_version"`
	RunID          string            `json:"run_id"`
	RunNonce       string            `json:"run_nonce"`
	TestPlanSHA256 string            `json:"test_plan_sha256"`
	StartedAtUTC   string            `json:"started_at_utc"`
	FinishedAtUTC  string            `json:"finished_at_utc"`
	Host           HostRecord        `json:"host"`
	VM             VMRecord          `json:"vm"`
	Guest          GuestRecord       `json:"guest"`
	Fixtures       []FixtureRecord   `json:"fixtures"`
	Cases          []CaseRecord      `json:"cases"`
	HostEvents     []HostEventRecord `json:"host_events"`
	Teardown       TeardownRecord    `json:"teardown"`
	Decision       string            `json:"decision"`
}

type HostRecord struct {
	RunnerID               string `json:"runner_id"`
	RunnerVersion          string `json:"runner_version"`
	Hypervisor             string `json:"hypervisor"`
	HypervisorVersion      string `json:"hypervisor_version"`
	HostArch               string `json:"host_arch"`
	HostKernel             string `json:"host_kernel"`
	IsolationProfileDigest string `json:"isolation_profile_digest"`
}
type VMRecord struct {
	MachineType       string   `json:"machine_type"`
	CPUModel          string   `json:"cpu_model"`
	VCPUCount         uint32   `json:"vcpu_count"`
	MemoryMiB         uint32   `json:"memory_mib"`
	ImageSHA256       string   `json:"image_sha256"`
	OverlayID         string   `json:"overlay_id"`
	NetworkDevices    []string `json:"network_devices"`
	HostMounts        []string `json:"host_mounts"`
	GuestAgentEnabled bool     `json:"guest_agent_enabled"`
}
type GuestRecord struct {
	Architecture       string `json:"architecture"`
	KernelRelease      string `json:"kernel_release"`
	KernelBuildID      string `json:"kernel_build_id"`
	SystemdVersion     string `json:"systemd_version"`
	CgroupMode         string `json:"cgroup_mode"`
	LSM                string `json:"lsm"`
	SeccompAvailable   bool   `json:"seccomp_available"`
	MountProfileDigest string `json:"mount_profile_digest"`
}
type FixtureRecord struct {
	FixtureID      string `json:"fixture_id"`
	Kind           string `json:"kind"`
	SHA256         string `json:"sha256"`
	SizeBytes      uint64 `json:"size_bytes"`
	ELFClass       string `json:"elf_class"`
	ELFData        string `json:"elf_data"`
	ELFMachine     string `json:"elf_machine"`
	ELFType        string `json:"elf_type"`
	ExpectedPolicy string `json:"expected_policy"`
}
type CaseRecord struct {
	CaseID      string             `json:"case_id"`
	TestName    string             `json:"test_name"`
	Expected    string             `json:"expected"`
	Observed    string             `json:"observed"`
	Result      string             `json:"result"`
	Evidence    []EvidenceRecord   `json:"evidence"`
	Transitions []TransitionRecord `json:"transitions"`
}
type EvidenceRecord struct {
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	Digest      string `json:"digest"`
	SummaryCode string `json:"summary_code"`
}
type TransitionRecord struct {
	Sequence         uint32  `json:"sequence"`
	State            string  `json:"state"`
	HostMonotonicNS  *uint64 `json:"host_monotonic_ns,omitempty"`
	GuestMonotonicNS *uint64 `json:"guest_monotonic_ns,omitempty"`
}
type HostEventRecord struct {
	EventID         string `json:"event_id"`
	EventType       string `json:"event_type"`
	HostMonotonicNS uint64 `json:"host_monotonic_ns"`
	CaseID          string `json:"case_id"`
	Actor           string `json:"actor"`
	Result          string `json:"result"`
}
type TeardownRecord struct {
	VMProcessAbsent          bool   `json:"vm_process_absent"`
	OverlayAbsent            bool   `json:"overlay_absent"`
	TemporaryResourcesAbsent bool   `json:"temporary_resources_absent"`
	VerifiedBy               string `json:"verified_by"`
	EvidenceDigest           string `json:"evidence_digest"`
}

// DecodeManifest validates bounded JSON and the frozen structural V1 profile.
// It does not decide whether any case or the aggregate run passed.
func DecodeManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if err := ValidateStrictJSON(raw); err != nil {
		return m, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest schema: %v", ErrInvalidEvidence, err)
	}
	var canonical bytes.Buffer
	enc := json.NewEncoder(&canonical)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return Manifest{}, fmt.Errorf("%w: canonical encoding: %v", ErrInvalidEvidence, err)
	}
	encoded := bytes.TrimSuffix(canonical.Bytes(), []byte("\n"))
	if !bytes.Equal(raw, encoded) {
		return Manifest{}, fmt.Errorf("%w: manifest is not canonical V1 JSON", ErrInvalidEvidence)
	}
	if err := validateManifestStructure(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func validateManifestStructure(m Manifest) error {
	bad := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidEvidence, reason) }
	if m.SchemaVersion != "1.0" {
		return bad("unsupported schema_version")
	}
	for name, value := range map[string]string{"run_id": m.RunID, "host.runner_id": m.Host.RunnerID, "host.runner_version": m.Host.RunnerVersion, "host.hypervisor": m.Host.Hypervisor, "host.hypervisor_version": m.Host.HypervisorVersion, "host.host_arch": m.Host.HostArch, "host.host_kernel": m.Host.HostKernel, "vm.machine_type": m.VM.MachineType, "vm.cpu_model": m.VM.CPUModel, "vm.overlay_id": m.VM.OverlayID, "guest.architecture": m.Guest.Architecture, "guest.kernel_release": m.Guest.KernelRelease, "guest.kernel_build_id": m.Guest.KernelBuildID, "guest.systemd_version": m.Guest.SystemdVersion, "guest.cgroup_mode": m.Guest.CgroupMode, "guest.lsm": m.Guest.LSM} {
		if value == "" {
			return bad("missing required field " + name)
		}
	}
	if !noncePattern.MatchString(m.RunNonce) || len(m.RunNonce)%2 != 0 {
		return bad("run_nonce must be at least 256-bit lowercase hex")
	}
	for name, digest := range map[string]string{"test_plan_sha256": m.TestPlanSHA256, "host.isolation_profile_digest": m.Host.IsolationProfileDigest, "vm.image_sha256": m.VM.ImageSHA256, "guest.mount_profile_digest": m.Guest.MountProfileDigest} {
		if !sha256Pattern.MatchString(digest) {
			return bad("invalid SHA-256 field " + name)
		}
	}
	started, err := time.Parse(time.RFC3339Nano, m.StartedAtUTC)
	if err != nil || started.Location() != time.UTC || started.Format(time.RFC3339Nano) != m.StartedAtUTC {
		return bad("started_at_utc must be canonical RFC3339 UTC")
	}
	finished, err := time.Parse(time.RFC3339Nano, m.FinishedAtUTC)
	if err != nil || finished.Location() != time.UTC || finished.Format(time.RFC3339Nano) != m.FinishedAtUTC || finished.Before(started) {
		return bad("finished_at_utc must be canonical UTC and not precede start")
	}
	if m.VM.VCPUCount == 0 || m.VM.MemoryMiB == 0 {
		return bad("VM CPU and memory must be non-zero")
	}
	if len(m.VM.NetworkDevices) != 0 || len(m.VM.HostMounts) != 0 || m.VM.GuestAgentEnabled {
		return bad("VM violates default no-network/no-host-mount/no-agent profile")
	}
	if len(m.Fixtures) > 64 || len(m.Cases) == 0 || len(m.Cases) > 256 || len(m.HostEvents) > 1024 {
		return bad("manifest collection limit exceeded")
	}
	fixtures := make(map[string]struct{}, len(m.Fixtures))
	for _, f := range m.Fixtures {
		if f.FixtureID == "" || !inSet(fixtureKinds, f.Kind) || !inSet(fixturePolicies, f.ExpectedPolicy) || !sha256Pattern.MatchString(f.SHA256) {
			return bad("invalid fixture record")
		}
		if _, ok := fixtures[f.FixtureID]; ok {
			return bad("duplicate fixture_id")
		}
		fixtures[f.FixtureID] = struct{}{}
	}
	cases := make(map[string]struct{}, len(m.Cases))
	for _, c := range m.Cases {
		if c.CaseID == "" || c.TestName == "" || !inSet(caseOutcomes, c.Expected) || !inSet(caseOutcomes, c.Observed) || !inSet(caseResults, c.Result) {
			return bad("incomplete case record")
		}
		if _, ok := cases[c.CaseID]; ok {
			return bad("duplicate case_id")
		}
		cases[c.CaseID] = struct{}{}
		if len(c.Evidence) > 32 || len(c.Transitions) > 64 {
			return bad("per-case evidence or transition limit exceeded")
		}
		for _, e := range c.Evidence {
			if !inSet(evidenceSources, e.Source) || !inSet(evidenceKinds, e.Kind) || !inSet(summaryCodes, e.SummaryCode) || !sha256Pattern.MatchString(e.Digest) {
				return bad("invalid evidence record")
			}
		}
		var lastHost uint64
		var lastSeq uint32
		for i, tr := range c.Transitions {
			if tr.Sequence == 0 || (i > 0 && tr.Sequence <= lastSeq) || !inSet(transitionStates, tr.State) {
				return bad("invalid transition ordering")
			}
			if tr.HostMonotonicNS != nil {
				if i > 0 && *tr.HostMonotonicNS < lastHost {
					return bad("host monotonic transition time decreased")
				}
				lastHost = *tr.HostMonotonicNS
			}
			lastSeq = tr.Sequence
		}
	}
	if len(m.HostEvents) > 0 {
		var last uint64
		ids := map[string]struct{}{}
		for i, e := range m.HostEvents {
			if e.EventID == "" || !inSet(hostEventTypes, e.EventType) || e.CaseID == "" || e.Actor != "HOST_RUNNER" || e.Result == "" {
				return bad("invalid host event")
			}
			if _, ok := ids[e.EventID]; ok {
				return bad("duplicate host event ID")
			}
			ids[e.EventID] = struct{}{}
			if i > 0 && e.HostMonotonicNS < last {
				return bad("host event monotonic time decreased")
			}
			last = e.HostMonotonicNS
		}
	}
	if m.Teardown.VerifiedBy != "HOST_RUNNER" || !sha256Pattern.MatchString(m.Teardown.EvidenceDigest) {
		return bad("invalid teardown record")
	}
	if m.Decision != "PASS" && m.Decision != "FAIL" && m.Decision != "BLOCKED" {
		return bad("invalid aggregate decision")
	}
	return nil
}

// ManifestDigest returns the digest of the exact encoded bytes.
func ManifestDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
