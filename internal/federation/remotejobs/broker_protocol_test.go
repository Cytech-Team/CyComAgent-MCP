package remotejobs

import (
	"encoding/json"
	"strings"
	"testing"
)

func validBrokerRequest() BrokerStartRequest {
	return BrokerStartRequest{
		Version: ExecutionBrokerProtocolVersion, JobID: "job-123", RequestID: "req-456",
		PolicyRevision: 7, ExecutableDigest: strings.Repeat("a", 64),
		Argv: []string{"/approved/tool", "--version"}, WorkingDirRef: "workdir-1",
		Environment: map[string]string{"LANG": "C"}, TimeoutMillis: 5000,
		MaxOutputBytes: 1 << 20, InputRef: "input-1",
	}
}

func TestBrokerProtocolRoundTrip(t *testing.T) {
	want := validBrokerRequest()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeBrokerStartRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.JobID != want.JobID || got.RequestID != want.RequestID || got.PolicyRevision != want.PolicyRevision || got.ExecutableDigest != want.ExecutableDigest || len(got.Argv) != len(want.Argv) || got.WorkingDirRef != want.WorkingDirRef {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestBrokerProtocolRejectsPrivilegeBearingAndMalformedFields(t *testing.T) {
	tests := []struct {
		name string
		edit func(*BrokerStartRequest)
	}{
		{"wrong version", func(r *BrokerStartRequest) { r.Version++ }},
		{"invalid job id", func(r *BrokerStartRequest) { r.JobID = "../root" }},
		{"missing request id", func(r *BrokerStartRequest) { r.RequestID = "" }},
		{"missing policy revision", func(r *BrokerStartRequest) { r.PolicyRevision = 0 }},
		{"invalid digest", func(r *BrokerStartRequest) { r.ExecutableDigest = "/bin/sh" }},
		{"empty argv", func(r *BrokerStartRequest) { r.Argv = nil }},
		{"NUL argv", func(r *BrokerStartRequest) { r.Argv = []string{"tool\x00evil"} }},
		{"invalid workdir reference", func(r *BrokerStartRequest) { r.WorkingDirRef = "../home" }},
		{"NUL environment", func(r *BrokerStartRequest) { r.Environment = map[string]string{"LANG": "C\x00PATH"} }},
		{"invalid environment key", func(r *BrokerStartRequest) { r.Environment = map[string]string{"BAD-KEY": "value"} }},
		{"environment key begins with digit", func(r *BrokerStartRequest) { r.Environment = map[string]string{"1BAD": "value"} }},
		{"invalid timeout", func(r *BrokerStartRequest) { r.TimeoutMillis = 0 }},
		{"excessive timeout", func(r *BrokerStartRequest) { r.TimeoutMillis = 24*60*60*1000 + 1 }},
		{"invalid output cap", func(r *BrokerStartRequest) { r.MaxOutputBytes = 0 }},
		{"invalid input reference", func(r *BrokerStartRequest) { r.InputRef = "../../etc/shadow" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validBrokerRequest()
			tt.edit(&req)
			if err := ValidateBrokerStartRequest(req); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestBrokerProtocolRejectsUnknownPrivilegeFieldsAndTrailingJSON(t *testing.T) {
	base, err := json.Marshal(validBrokerRequest())
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.TrimSuffix(string(base), "}") + `,"unit_name":"attacker.service"}`
	if _, err := DecodeBrokerStartRequest([]byte(withUnknown)); err == nil {
		t.Fatal("unknown unit_name field was accepted")
	}
	if _, err := DecodeBrokerStartRequest(append(base, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	duplicate := strings.Replace(string(base), `"version":1`, `"version":1,"version":1`, 1)
	if _, err := DecodeBrokerStartRequest([]byte(duplicate)); err == nil {
		t.Fatal("duplicate top-level field was accepted")
	}
	duplicateEnv := strings.Replace(string(base), `"LANG":"C"`, `"LANG":"C","LANG":"attacker"`, 1)
	if _, err := DecodeBrokerStartRequest([]byte(duplicateEnv)); err == nil {
		t.Fatal("duplicate nested environment key was accepted")
	}
	if _, err := DecodeBrokerStartRequest(make([]byte, maxBrokerRequestBytes+1)); err == nil {
		t.Fatal("oversized request was accepted")
	}
}

func TestBrokerProtocolBoundsArgumentsAndEnvironment(t *testing.T) {
	req := validBrokerRequest()
	req.Argv = []string{strings.Repeat("x", maxBrokerArgBytes+1)}
	if err := ValidateBrokerStartRequest(req); err == nil {
		t.Fatal("oversized argv accepted")
	}
	req = validBrokerRequest()
	req.Environment = map[string]string{"LARGE": strings.Repeat("x", maxBrokerEnvBytes)}
	if err := ValidateBrokerStartRequest(req); err == nil {
		t.Fatal("oversized environment accepted")
	}
	req = validBrokerRequest()
	req.Environment = make(map[string]string, maxBrokerEnvCount+1)
	for i := 0; i < maxBrokerEnvCount+1; i++ {
		req.Environment[string(rune('A'+i))] = "1"
	}
	if err := ValidateBrokerStartRequest(req); err == nil {
		t.Fatal("excessive environment entry count accepted")
	}
}

func TestExecutionUnitNameIsOpaqueStableAndValidated(t *testing.T) {
	first, err := ExecutionUnitName("job-123", "req-456")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExecutionUnitName("job-123", "req-456")
	if err != nil {
		t.Fatal(err)
	}
	other, err := ExecutionUnitName("job-123", "req-457")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("unit name is not stable: %q != %q", first, second)
	}
	if first == other {
		t.Fatal("different request produced same unit name")
	}
	if strings.Contains(first, "job-123") || strings.Contains(first, "req-456") || !strings.HasSuffix(first, ".service") {
		t.Fatalf("unit name is not opaque: %q", first)
	}
	if _, err := ExecutionUnitName("../bad", "req-1"); err == nil {
		t.Fatal("invalid job ID accepted")
	}
}
