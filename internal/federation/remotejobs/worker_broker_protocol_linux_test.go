//go:build linux

package remotejobs

import (
	"bytes"
	"encoding/json"
	"testing"
)

func validWorkerBrokerRequest(t *testing.T) workerBrokerStartRequest {
	t.Helper()
	intent := validTestLaunchIntent()
	intent.State = "launching"
	intent.StartAttemptID = "0123456789abcdef0123456789abcdef"
	intent.StartRequestReservedAt = nowForSystemdTest()
	request, err := systemdStartRequestFromIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	out, err := workerBrokerRequestFromStart(request)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWorkerBrokerStartRequestStrictRoundTrip(t *testing.T) {
	want := validWorkerBrokerRequest(t)
	encoded, err := encodeWorkerBrokerStartRequest(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeWorkerBrokerStartRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !got.matchesStartRequest(systemdStartRequest{Version: systemdStartProtocolVersion, CallerNodeID: want.CallerNodeID, RequestID: want.RequestID, RemoteJobID: want.RemoteJobID, LocalJobID: want.LocalJobID, StartAttemptID: want.StartAttemptID, UnitName: want.UnitName, ArtifactName: want.ArtifactName, ArtifactDigest: want.ArtifactDigest, ArtifactDevice: want.ArtifactDevice, ArtifactInode: want.ArtifactInode, PolicyRevision: want.PolicyRevision, PayloadHash: want.PayloadHash}) {
		t.Fatal("round-tripped request lost immutable start identity")
	}
	if bytes.Contains(encoded, []byte("argv")) || bytes.Contains(encoded, []byte("environment")) || bytes.Contains(encoded, []byte("uid")) || bytes.Contains(encoded, []byte("executable_path")) || bytes.Contains(encoded, []byte("properties")) {
		t.Fatalf("worker broker request contains a forbidden authority field: %s", encoded)
	}
}

func TestWorkerBrokerStartRequestRejectsPrivilegeAndIdentityMutation(t *testing.T) {
	base := validWorkerBrokerRequest(t)
	tests := map[string]func(map[string]any){
		"unknown-command-field":  func(m map[string]any) { m["argv"] = []string{"/bin/sh"} },
		"uid-field":              func(m map[string]any) { m["uid"] = 0 },
		"properties-field":       func(m map[string]any) { m["properties"] = []string{"User=root"} },
		"caller-unit":            func(m map[string]any) { m["unit_name"] = "evil.service" },
		"artifact-inode-zero":    func(m map[string]any) { m["artifact_inode"] = float64(0) },
		"bad-attempt":            func(m map[string]any) { m["start_attempt_id"] = "not-an-attempt" },
		"bad-artifact-reference": func(m map[string]any) { m["artifact_name"] = "../root" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(base)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatal(err)
			}
			mutate(object)
			encoded, err = json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeWorkerBrokerStartRequest(encoded); err == nil {
				t.Fatal("mutated worker broker request accepted")
			}
		})
	}
}

func TestWorkerBrokerStartRequestRejectsDuplicateTrailingAndOversizedJSON(t *testing.T) {
	base := validWorkerBrokerRequest(t)
	encoded, err := encodeWorkerBrokerStartRequest(base)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := append([]byte(`{"version":1,"version":1,`), encoded[1:]...)
	if _, err := decodeWorkerBrokerStartRequest(duplicate); err == nil {
		t.Fatal("duplicate JSON key accepted")
	}
	if _, err := decodeWorkerBrokerStartRequest(append(encoded, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if _, err := decodeWorkerBrokerStartRequest(make([]byte, maxWorkerBrokerRequestBytes+1)); err == nil {
		t.Fatal("oversized request accepted")
	}
}

func TestWorkerBrokerRequestConversionRejectsUnsupportedSourceVersion(t *testing.T) {
	request := systemdStartRequest{Version: systemdStartProtocolVersion + 1}
	if _, err := workerBrokerRequestFromStart(request); err == nil {
		t.Fatal("unsupported source protocol version accepted")
	}
}
