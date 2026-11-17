package vmevidence

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateStrictJSON(t *testing.T) {
	valid := []byte("{\"schema_version\":\"1.0\",\"cases\":[{\"case_id\":\"a\"}]}")
	if err := ValidateStrictJSON(valid); err != nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
	bad := [][]byte{
		[]byte("{\"x\":1,\"x\":2}"),
		[]byte("{} {}"),
		{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'},
		[]byte("[[[[[[[[[[[[[[[[[0]]]]]]]]]]]]]]]"),
	}
	for i, raw := range bad {
		if err := ValidateStrictJSON(raw); err == nil {
			t.Errorf("case %d unexpectedly accepted", i)
		}
	}
}

func TestVerifyDetachedSignatureExactBytes(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("{\"schema_version\":\"1.0\",\"run_id\":\"fixture\"}")
	digest := sha256.Sum256(raw)
	env := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "runner-test", Digest: hex.EncodeToString(digest[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, digest[:]))}
	keys := map[string]ed25519.PublicKey{"runner-test": pub}
	if err := VerifyDetachedSignature(raw, env, keys); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := VerifyDetachedSignature(append(raw, ' '), env, keys); err == nil {
		t.Fatal("accepted modified exact bytes")
	}
	if err := VerifyDetachedSignature(raw, env, nil); err == nil {
		t.Fatal("accepted unknown key")
	}
}

func TestValidateStrictJSONLimits(t *testing.T) {
	tooLarge := make([]byte, MaxManifestBytes+1)
	if err := ValidateStrictJSON(tooLarge); err == nil {
		t.Fatal("accepted oversized manifest")
	}
	longString := []byte("{\"x\":\"" + strings.Repeat("a", MaxStringBytes+1) + "\"}")
	if err := ValidateStrictJSON(longString); err == nil {
		t.Fatal("accepted oversized string")
	}
}

func validManifestJSON() []byte {
	raw := []byte(`{
"schema_version":"1.0",
"run_id":"run-fixture",
"run_nonce":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
"test_plan_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
"started_at_utc":"2026-11-08T01:00:00Z",
"finished_at_utc":"2026-11-08T01:01:00Z",
"host":{"runner_id":"runner-fixture","runner_version":"1","hypervisor":"qemu","hypervisor_version":"1","host_arch":"amd64","host_kernel":"test-kernel","isolation_profile_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
"vm":{"machine_type":"q35","cpu_model":"test-cpu","vcpu_count":2,"memory_mib":1024,"image_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","overlay_id":"overlay-fixture","network_devices":[],"host_mounts":[],"guest_agent_enabled":false},
"guest":{"architecture":"amd64","kernel_release":"test","kernel_build_id":"build","systemd_version":"test","cgroup_mode":"v2","lsm":"none","seccomp_available":true,"mount_profile_digest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
"fixtures":[],
"cases":[{"case_id":"case-1","test_name":"fixture-test","expected":"EXECUTED","observed":"EXECUTED","result":"BLOCKED","evidence":[],"transitions":[{"sequence":1,"state":"received","host_monotonic_ns":1}]}],
"host_events":[{"event_id":"event-1","event_type":"HOST_OBSERVATION","host_monotonic_ns":1,"case_id":"case-1","actor":"HOST_RUNNER","result":"RECORDED"}],
"teardown":{"vm_process_absent":false,"overlay_absent":false,"temporary_resources_absent":false,"verified_by":"HOST_RUNNER","evidence_digest":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
"decision":"BLOCKED"
}`)
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		panic(err)
	}
	return compact.Bytes()
}

func TestDecodeManifestStructuralV1(t *testing.T) {
	m, err := DecodeManifest(validManifestJSON())
	if err != nil {
		t.Fatalf("valid structural manifest rejected: %v", err)
	}
	if m.SchemaVersion != "1.0" || m.RunID != "run-fixture" || len(m.Cases) != 1 {
		t.Fatalf("unexpected decoded manifest: %+v", m)
	}
}

func TestDecodeManifestRejectsUnknownFieldsAndUnsafeVMProfile(t *testing.T) {
	raw := validManifestJSON()
	unknown := append([]byte(nil), raw...)
	unknown = bytes.Replace(unknown, []byte(`"run_id":"run-fixture"`), []byte(`"run_id":"run-fixture","surprise":true`), 1)
	if _, err := DecodeManifest(unknown); err == nil {
		t.Fatal("accepted unknown field")
	}

	networked := bytes.Replace(raw, []byte(`"network_devices":[]`), []byte(`"network_devices":["tap0"]`), 1)
	if _, err := DecodeManifest(networked); err == nil {
		t.Fatal("accepted VM with network device")
	}

	badTime := bytes.Replace(raw, []byte("2026-11-08T01:00:00Z"), []byte("2026-11-08T08:00:00+07:00"), 1)
	if _, err := DecodeManifest(badTime); err == nil {
		t.Fatal("accepted non-UTC timestamp")
	}
}

func TestDecodeManifestRejectsDuplicateIDsAndNonMonotonicTransitions(t *testing.T) {
	raw := validManifestJSON()
	duplicateCase := []byte(`{"case_id":"case-1","test_name":"duplicate","expected":"x","observed":"x","result":"BLOCKED","evidence":[],"transitions":[]}`)
	duplicate := bytes.Replace(raw, []byte(` }],"host_events":[`), append([]byte(` },`), append(duplicateCase, []byte(`],"host_events":[`)...)...), 1)
	if bytes.Equal(duplicate, raw) {
		duplicate = bytes.Replace(raw, []byte(`}],"host_events":[`), append([]byte(`},`), append(duplicateCase, []byte(`],"host_events":[`)...)...), 1)
	}
	if _, err := DecodeManifest(duplicate); err == nil {
		t.Fatal("accepted duplicate case ID")
	}

	nonMonotonic := bytes.Replace(raw, []byte(`"transitions":[{"sequence":1,"state":"received","host_monotonic_ns":1}]`), []byte(`"transitions":[{"sequence":2,"state":"received","host_monotonic_ns":2},{"sequence":1,"state":"running","host_monotonic_ns":1}]`), 1)
	if _, err := DecodeManifest(nonMonotonic); err == nil {
		t.Fatal("accepted non-monotonic transition sequence")
	}
}

func TestDecodeManifestRequiresExactCanonicalBytes(t *testing.T) {
	raw := validManifestJSON()
	if _, err := DecodeManifest(append([]byte(" "), raw...)); err == nil {
		t.Fatal("accepted non-canonical leading whitespace")
	}
	nullBool := bytes.Replace(raw, []byte(`"guest_agent_enabled":false`), []byte(`"guest_agent_enabled":null`), 1)
	if _, err := DecodeManifest(nullBool); err == nil {
		t.Fatal("accepted null for required boolean")
	}
}

func signedPlanFixture(t *testing.T) ([]byte, SignatureEnvelope, map[string]ed25519.PublicKey) {
	t.Helper()
	raw := []byte(`{"schema_version":"1.0","plan_id":"plan-fixture","cases":[{"case_id":"case-1","test_name":"fixture-test","expected_outcome":"EXECUTED","required_evidence":[{"kind":"HOST_VM_CONFIG","allowed_sources":["HOST_RUNNER"]}],"required_transitions":["received","validated","terminal"]}],"platform":{"architecture":"amd64","cgroup_mode":"v2","require_seccomp":true}}`)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	env := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "trusted-plan", Digest: hex.EncodeToString(digest[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, digest[:]))}
	return raw, env, map[string]ed25519.PublicKey{"trusted-plan": pub}
}

func TestDecodeTrustedTestPlanAndDecision(t *testing.T) {
	raw, env, keys := signedPlanFixture(t)
	plan, err := DecodeTrustedTestPlan(raw, env, keys)
	if err != nil {
		t.Fatalf("trusted plan rejected: %v", err)
	}
	m, err := DecodeManifest(validManifestJSON())
	if err != nil {
		t.Fatal(err)
	}
	m.TestPlanSHA256 = ManifestDigest(raw)
	m.Cases[0].Result = "PASS"
	m.Cases[0].Evidence = []EvidenceRecord{{Source: "HOST_RUNNER", Kind: "HOST_VM_CONFIG", Digest: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", SummaryCode: "ASSERTION_MET"}}
	m.Cases[0].Transitions = []TransitionRecord{{Sequence: 1, State: "received"}, {Sequence: 2, State: "validated"}, {Sequence: 3, State: "terminal"}}
	m.HostEvents = append(m.HostEvents,
		HostEventRecord{EventID: "start", EventType: "VM_START", HostMonotonicNS: 1, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
		HostEventRecord{EventID: "vm-destroy", EventType: "VM_DESTROY", HostMonotonicNS: 2, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
		HostEventRecord{EventID: "overlay-destroy", EventType: "OVERLAY_DESTROY", HostMonotonicNS: 3, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
	)
	m.Teardown.VMProcessAbsent = true
	m.Teardown.OverlayAbsent = true
	m.Teardown.TemporaryResourcesAbsent = true
	report, err := EvaluateAgainstTrustedPlan(m, plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision != "PASS" {
		t.Fatalf("expected derived PASS, got %+v", report)
	}

	m.Cases[0].Evidence[0].Source = "GUEST_UNTRUSTED_LOG"
	report, err = EvaluateAgainstTrustedPlan(m, plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision != "BLOCKED" {
		t.Fatalf("untrusted log satisfied assertion: %+v", report)
	}

	m.TestPlanSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := EvaluateAgainstTrustedPlan(m, plan); err == nil {
		t.Fatal("accepted manifest not bound to trusted plan digest")
	}
}

func TestDecodeTrustedTestPlanRejectsUntrustedAndInvalidEnums(t *testing.T) {
	raw, env, keys := signedPlanFixture(t)
	if _, err := DecodeTrustedTestPlan(raw, env, nil); err == nil {
		t.Fatal("accepted untrusted plan key")
	}
	bad := bytes.Replace(raw, []byte("HOST_VM_CONFIG"), []byte("ARBITRARY_KIND"), 1)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys = map[string]ed25519.PublicKey{"trusted-plan": pub}
	d := sha256.Sum256(bad)
	env.Digest = hex.EncodeToString(d[:])
	env.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, d[:]))
	if _, err := DecodeTrustedTestPlan(bad, env, keys); err == nil {
		t.Fatal("accepted unknown evidence enum")
	}
}

func TestMemoryTrustStorePurposeValidityAndRevocation(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC)
	store, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "key-a", Purpose: KeyPurposeManifest, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupTrustedKey("key-a", KeyPurposeManifest, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LookupTrustedKey("key-a", KeyPurposeTestPlan, now); err == nil {
		t.Fatal("key crossed purpose boundary")
	}
	if _, err := store.LookupTrustedKey("key-a", KeyPurposeManifest, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired key accepted")
	}
	if _, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "dup", Purpose: KeyPurposeManifest, PublicKey: pub, NotBefore: now, NotAfter: now.Add(time.Hour)}, {KeyID: "dup", Purpose: KeyPurposeManifest, PublicKey: pub, NotBefore: now, NotAfter: now.Add(time.Hour)}}); err == nil {
		t.Fatal("duplicate trust key accepted")
	}
}

func TestVerifyAndReserveManifestRejectsReplayConcurrently(t *testing.T) {
	raw := validManifestJSON()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC)
	digest := sha256.Sum256(raw)
	envelope := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "manifest-key", Digest: hex.EncodeToString(digest[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, digest[:]))}
	store, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "manifest-key", Purpose: KeyPurposeManifest, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	nonces := NewMemoryNonceStore()
	const callers = 16
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := VerifyAndReserveManifest(raw, envelope, store, nonces, now); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("expected exactly one nonce reservation, got %d", got)
	}
}

func TestDecodeTrustedPlanWithPurposeScopedStore(t *testing.T) {
	raw, envelope, keys := signedPlanFixture(t)
	now := time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC)
	store, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "trusted-plan", Purpose: KeyPurposeTestPlan, PublicKey: keys["trusted-plan"], NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTrustedTestPlanWithStore(raw, envelope, now, store); err != nil {
		t.Fatalf("scoped trusted plan rejected: %v", err)
	}
}

func TestMemoryNonceStoreRejectsRunIDReuseWithDifferentNonce(t *testing.T) {
	store := NewMemoryNonceStore()
	now := time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC)
	first := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	second := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := store.ReserveRunNonce("runner-a", first, "run-unique", now); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveRunNonce("runner-a", second, "run-unique", now.Add(time.Second)); err == nil {
		t.Fatal("accepted reused run ID with a different nonce")
	}
	if err := store.ReserveRunNonce("runner-b", first, "run-unique", now); err != nil {
		t.Fatalf("runner scopes should be independent: %v", err)
	}
}

func TestVerifyManifestArtifactsChecksReferencedBytes(t *testing.T) {
	m, err := DecodeManifest(validManifestJSON())
	if err != nil {
		t.Fatal(err)
	}
	fixtureBytes := []byte("synthetic-fixture")
	evidenceBytes := []byte("host-observed-evidence")
	teardownBytes := []byte("host-teardown-proof")
	digest := func(data []byte) string {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	m.Fixtures = []FixtureRecord{{FixtureID: "fixture-1", Kind: "NON_ELF", SHA256: digest(fixtureBytes), SizeBytes: uint64(len(fixtureBytes)), ExpectedPolicy: "REJECT"}}
	m.Cases[0].Evidence = []EvidenceRecord{{Source: "HOST_RUNNER", Kind: "HOST_VM_CONFIG", Digest: digest(evidenceBytes), SummaryCode: "OBSERVATION_RECORDED"}}
	m.Teardown.EvidenceDigest = digest(teardownBytes)
	artifacts := map[string][]byte{
		digest(fixtureBytes):  fixtureBytes,
		digest(evidenceBytes): evidenceBytes,
		digest(teardownBytes): teardownBytes,
	}
	report, err := VerifyManifestArtifacts(m, artifacts)
	if err != nil {
		t.Fatalf("valid artifacts rejected: %v", err)
	}
	if report.ArtifactCount != 3 || report.TotalBytes != int64(len(fixtureBytes)+len(evidenceBytes)+len(teardownBytes)) {
		t.Fatalf("unexpected verification report: %+v", report)
	}

	mutated := map[string][]byte{}
	for key, value := range artifacts {
		mutated[key] = append([]byte(nil), value...)
	}
	mutated[digest(evidenceBytes)][0] ^= 0xff
	if _, err := VerifyManifestArtifacts(m, mutated); err == nil {
		t.Fatal("accepted bytes that do not match the referenced digest")
	}
	if _, err := VerifyManifestArtifacts(m, map[string][]byte{digest(fixtureBytes): fixtureBytes}); err == nil {
		t.Fatal("accepted missing evidence and teardown artifacts")
	}
	withExtra := map[string][]byte{}
	for key, value := range artifacts {
		withExtra[key] = value
	}
	extra := []byte("unreferenced")
	withExtra[digest(extra)] = extra
	if _, err := VerifyManifestArtifacts(m, withExtra); err == nil {
		t.Fatal("accepted unreferenced artifact")
	}
}

func TestVerifyManifestArtifactsChecksFixtureSize(t *testing.T) {
	m, err := DecodeManifest(validManifestJSON())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("fixture")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	m.Fixtures = []FixtureRecord{{FixtureID: "fixture-size", Kind: "NON_ELF", SHA256: digest, SizeBytes: uint64(len(data) + 1), ExpectedPolicy: "REJECT"}}
	if _, err := VerifyManifestArtifacts(m, map[string][]byte{digest: data, m.Teardown.EvidenceDigest: []byte("teardown")}); err == nil {
		t.Fatal("accepted fixture with mismatched declared size")
	}
}

func TestVerifyHostEventJournalBindsSignedIndependentRecords(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeManifest(validManifestJSON())
	if err != nil {
		t.Fatal(err)
	}
	m.HostEvents = []HostEventRecord{
		{EventID: "start", EventType: "VM_START", HostMonotonicNS: 10, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
		{EventID: "stop", EventType: "VM_DESTROY", HostMonotonicNS: 20, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
		{EventID: "overlay", EventType: "OVERLAY_DESTROY", HostMonotonicNS: 30, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
	}
	journal := HostEventJournal{SchemaVersion: "1.0", RunnerID: m.Host.RunnerID, RunID: m.RunID, RunNonce: m.RunNonce, JournalSequence: 1, PreviousJournalSHA256: strings.Repeat("0", 64), Events: append([]HostEventRecord(nil), m.HostEvents...)}
	raw, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	envelope := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "journal-key", Digest: hex.EncodeToString(digest[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, digest[:]))}
	now := time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC)
	store, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "journal-key", Purpose: KeyPurposeHostJournal, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := VerifyHostEventJournal(raw, envelope, now, store, m)
	if err != nil {
		t.Fatalf("valid signed journal rejected: %v", err)
	}
	if report.EventCount != 3 || report.RunID != m.RunID || report.RunnerID != m.Host.RunnerID {
		t.Fatalf("unexpected report: %+v", report)
	}

	// A separately signed journal that disagrees with the manifest must fail.
	journal.Events[1].Result = "FAILED"
	tampered, _ := json.Marshal(journal)
	tamperedDigest := sha256.Sum256(tampered)
	tamperedEnvelope := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "journal-key", Digest: hex.EncodeToString(tamperedDigest[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, tamperedDigest[:]))}
	if _, err := VerifyHostEventJournal(tampered, tamperedEnvelope, now, store, m); err == nil {
		t.Fatal("accepted signed journal that disagrees with manifest")
	}
}

func TestVerifyHostEventJournalRejectsWrongPurposeAndLifecycleOrder(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeManifest(validManifestJSON())
	if err != nil {
		t.Fatal(err)
	}
	m.HostEvents = []HostEventRecord{
		{EventID: "start", EventType: "VM_START", HostMonotonicNS: 10, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
		{EventID: "overlay", EventType: "OVERLAY_DESTROY", HostMonotonicNS: 20, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
		{EventID: "stop", EventType: "VM_DESTROY", HostMonotonicNS: 30, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
	}
	journal := HostEventJournal{SchemaVersion: "1.0", RunnerID: m.Host.RunnerID, RunID: m.RunID, RunNonce: m.RunNonce, JournalSequence: 1, PreviousJournalSHA256: strings.Repeat("0", 64), Events: append([]HostEventRecord(nil), m.HostEvents...)}
	raw, _ := json.Marshal(journal)
	digest := sha256.Sum256(raw)
	envelope := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "wrong-purpose", Digest: hex.EncodeToString(digest[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, digest[:]))}
	now := time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC)
	wrongStore, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "wrong-purpose", Purpose: KeyPurposeManifest, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHostEventJournal(raw, envelope, now, wrongStore, m); err == nil {
		t.Fatal("accepted key scoped to MANIFEST for HOST_JOURNAL")
	}
	journalStore, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "wrong-purpose", Purpose: KeyPurposeHostJournal, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHostEventJournal(raw, envelope, now, journalStore, m); err == nil {
		t.Fatal("accepted overlay destruction before VM destruction")
	}
}

func TestVerifyHostEventJournalChainRejectsReplayAndAcceptsNextCheckpoint(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeManifest(validManifestJSON())
	if err != nil {
		t.Fatal(err)
	}
	m.HostEvents = []HostEventRecord{
		{EventID: "start", EventType: "VM_START", HostMonotonicNS: 10, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
		{EventID: "stop", EventType: "VM_DESTROY", HostMonotonicNS: 20, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
		{EventID: "overlay", EventType: "OVERLAY_DESTROY", HostMonotonicNS: 30, CaseID: "case-1", Actor: "HOST_RUNNER", Result: "RECORDED"},
	}
	journal := HostEventJournal{SchemaVersion: "1.0", RunnerID: m.Host.RunnerID, RunID: m.RunID, RunNonce: m.RunNonce, JournalSequence: 1, PreviousJournalSHA256: strings.Repeat("0", 64), Events: append([]HostEventRecord(nil), m.HostEvents...)}
	raw, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	envelope := SignatureEnvelope{Algorithm: "Ed25519-SHA256", KeyID: "chain-key", Digest: hex.EncodeToString(digest[:]), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, digest[:]))}
	now := time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC)
	store, err := NewMemoryTrustStore([]TrustedKey{{KeyID: "chain-key", Purpose: KeyPurposeHostJournal, PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	genesis := HostJournalCheckpoint{RunnerID: m.Host.RunnerID, RunID: m.RunID, RunNonce: m.RunNonce, Sequence: 0, JournalSHA256: strings.Repeat("0", 64)}
	verified, err := VerifyHostEventJournalChain(raw, envelope, now, store, m, genesis)
	if err != nil {
		t.Fatalf("valid first journal rejected: %v", err)
	}
	if verified.NextCheckpoint.Sequence != 1 || verified.NextCheckpoint.JournalSHA256 != ManifestDigest(raw) {
		t.Fatalf("bad next checkpoint: %+v", verified.NextCheckpoint)
	}
	if _, err := VerifyHostEventJournalChain(raw, envelope, now, store, m, verified.NextCheckpoint); err == nil {
		t.Fatal("accepted replayed journal against advanced checkpoint")
	}
	wrong := genesis
	wrong.JournalSHA256 = strings.Repeat("1", 64)
	if _, err := VerifyHostEventJournalChain(raw, envelope, now, store, m, wrong); err == nil {
		t.Fatal("accepted journal with incorrect predecessor hash")
	}

	authority, err := NewMemoryCheckpointAuthority([]HostJournalCheckpoint{genesis})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := VerifyAndAdvanceHostEventJournalChain(raw, envelope, now, store, m, authority)
	if err != nil {
		t.Fatalf("verify-and-advance rejected valid journal: %v", err)
	}
	if committed.NextCheckpoint != verified.NextCheckpoint {
		t.Fatalf("verify-and-advance returned unexpected checkpoint: %+v", committed.NextCheckpoint)
	}
	stored, err := authority.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
	if err != nil || stored != verified.NextCheckpoint {
		t.Fatalf("checkpoint not persisted in reference authority: %+v err=%v", stored, err)
	}
	if _, err := VerifyAndAdvanceHostEventJournalChain(raw, envelope, now, store, m, authority); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("replayed journal should fail chain verification, got %v", err)
	}

	unknownCommitInner, err := NewMemoryCheckpointAuthority([]HostJournalCheckpoint{genesis})
	if err != nil {
		t.Fatal(err)
	}
	uncertain := &commitThenErrorAuthority{inner: unknownCommitInner}
	if _, err := VerifyAndAdvanceHostEventJournalChain(raw, envelope, now, store, m, uncertain); !errors.Is(err, ErrCheckpointOutcomeUnknown) {
		t.Fatalf("lost acknowledgement should preserve unknown-commit classification, got %v", err)
	}
	stored, err = unknownCommitInner.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
	if err != nil || stored != verified.NextCheckpoint {
		t.Fatalf("unknown-commit simulation did not preserve committed head: %+v err=%v", stored, err)
	}
}

func TestMemoryCheckpointAuthorityCompareAndAdvanceIsAtomic(t *testing.T) {
	genesis := HostJournalCheckpoint{RunnerID: "runner-a", RunID: "run-a", RunNonce: strings.Repeat("a", 64), Sequence: 0, JournalSHA256: strings.Repeat("0", 64)}
	next := HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: genesis.RunID, RunNonce: genesis.RunNonce, Sequence: 1, JournalSHA256: strings.Repeat("1", 64)}
	authority, err := NewMemoryCheckpointAuthority([]HostJournalCheckpoint{genesis})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.LoadCheckpoint(genesis.RunnerID, "missing-run", genesis.RunNonce); !errors.Is(err, ErrCheckpointNotFound) {
		t.Fatalf("missing checkpoint should have a typed not-found outcome, got %v", err)
	}
	loaded, err := authority.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
	if err != nil || loaded != genesis {
		t.Fatalf("load genesis: checkpoint=%+v err=%v", loaded, err)
	}

	const contenders = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if authority.CompareAndAdvance(genesis, next) == nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("expected exactly one CAS winner, got %d", got)
	}
	loaded, err = authority.LoadCheckpoint(genesis.RunnerID, genesis.RunID, genesis.RunNonce)
	if err != nil || loaded != next {
		t.Fatalf("checkpoint did not advance exactly once: checkpoint=%+v err=%v", loaded, err)
	}
	if err := authority.CompareAndAdvance(genesis, next); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("stale expected checkpoint should be a typed conflict, got %v", err)
	}
}

func TestMemoryCheckpointAuthorityRejectsInvalidOrCrossRunAdvance(t *testing.T) {
	genesis := HostJournalCheckpoint{RunnerID: "runner-a", RunID: "run-a", RunNonce: strings.Repeat("a", 64), Sequence: 0, JournalSHA256: strings.Repeat("0", 64)}
	if _, err := NewMemoryCheckpointAuthority([]HostJournalCheckpoint{genesis, genesis}); err == nil {
		t.Fatal("accepted duplicate initial checkpoint")
	}
	authority, err := NewMemoryCheckpointAuthority([]HostJournalCheckpoint{genesis})
	if err != nil {
		t.Fatal(err)
	}
	crossRun := HostJournalCheckpoint{RunnerID: genesis.RunnerID, RunID: "run-b", RunNonce: strings.Repeat("b", 64), Sequence: 1, JournalSHA256: strings.Repeat("1", 64)}
	if err := authority.CompareAndAdvance(genesis, crossRun); err == nil {
		t.Fatal("accepted cross-run checkpoint advance")
	}
	badSequence := genesis
	badSequence.Sequence = 2
	badSequence.JournalSHA256 = strings.Repeat("2", 64)
	if err := authority.CompareAndAdvance(genesis, badSequence); err == nil {
		t.Fatal("accepted skipped sequence")
	}
}

type commitThenErrorAuthority struct{ inner CheckpointAuthority }

func (a *commitThenErrorAuthority) LoadCheckpoint(runnerID, runID, runNonce string) (HostJournalCheckpoint, error) {
	return a.inner.LoadCheckpoint(runnerID, runID, runNonce)
}

func (a *commitThenErrorAuthority) CompareAndAdvance(expected, next HostJournalCheckpoint) error {
	if err := a.inner.CompareAndAdvance(expected, next); err != nil {
		return err
	}
	return fmt.Errorf("%w: simulated lost commit acknowledgement", ErrCheckpointOutcomeUnknown)
}
