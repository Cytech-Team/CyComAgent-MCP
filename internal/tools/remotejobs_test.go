//go:build linux

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation/remotejobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func TestRemoteJobReservationToolsRequireTrustedProofAndNeverLaunch(t *testing.T) {
	receiver, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	caller, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Trust(caller.Public(), caller.Public().Fingerprint); err != nil {
		t.Fatal(err)
	}
	ledger, err := remotejobs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := registry.New()
	jobStateDir := t.TempDir()
	jobManager, err := jobs.New(jobStateDir)
	if err != nil {
		t.Fatal(err)
	}
	registerRemoteJobs(r, ledger, receiver, jobManager)

	payload := `{"operation":"test-only","command":"must-not-run"}`
	sig, err := remotejobs.SignRequest(caller, receiver.Public().NodeID, "request-42", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"caller_node_id": caller.Public().NodeID, "request_id": "request-42", "payload": payload, "signature": base64.RawStdEncoding.EncodeToString(sig), "reason": "Test authenticated durable reservation without execution"}
	raw, _ := json.Marshal(args)
	first, err := r.Call(context.Background(), "federation_remote_job_reserve", raw)
	if err != nil {
		t.Fatal(err)
	}
	result := first.(map[string]any)
	if result["execution"] != "not_started" || result["created"] != true {
		t.Fatalf("first reservation result=%#v", result)
	}
	second, err := r.Call(context.Background(), "federation_remote_job_reserve", raw)
	if err != nil {
		t.Fatal(err)
	}
	if second.(map[string]any)["created"] != false {
		t.Fatalf("retry was not idempotent: %#v", second)
	}

	statusSig, err := remotejobs.SignRequestForPurpose(caller, "status", receiver.Public().NodeID, "request-42", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	args["signature"] = base64.RawStdEncoding.EncodeToString(statusSig)
	raw, _ = json.Marshal(args)
	status, err := r.Call(context.Background(), "federation_remote_job_status", raw)
	if err != nil {
		t.Fatal(err)
	}
	if status.(map[string]any)["execution"] != "not_started_by_reservation_api" {
		t.Fatalf("status result=%#v", status)
	}
	// Bind a distinct receiver-local Job ID and create an isolated harmless
	// local job. Reconciliation must follow LocalJobID, not the remote rjob ID.
	localID := "local-evidence-42"
	if _, err := ledger.BindLocalJob(caller.Public().NodeID, "request-42", localID); err != nil {
		t.Fatal(err)
	}
	if _, err := jobManager.StartWithIDAndSession(localID, "printf evidence-only", t.TempDir(), "/bin/sh", nil, "service", ""); err != nil {
		t.Fatal(err)
	}

	reconcileSig, err := remotejobs.SignRequestForPurpose(caller, "reconcile", receiver.Public().NodeID, "request-42", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	args["signature"] = base64.RawStdEncoding.EncodeToString(reconcileSig)
	raw, _ = json.Marshal(args)
	reconciliation, err := r.Call(context.Background(), "federation_remote_job_reconcile", raw)
	if err != nil {
		t.Fatal(err)
	}
	reconcileResult := reconciliation.(map[string]any)
	if reconcileResult["state_changed"] != false || reconcileResult["replayed"] != false || reconcileResult["job_evidence_status"] != "found" {
		t.Fatalf("reconciliation must find the bound local job without changing state: %#v", reconcileResult)
	}
	evidence, ok := reconcileResult["job_evidence"].(map[string]any)
	if !ok || evidence["id"] != localID {
		t.Fatalf("wrong local job evidence: %#v", reconcileResult["job_evidence"])
	}
	// Corrupt durable local evidence must be surfaced as an error, not
	// silently downgraded to "not found" or treated as a clean reconciliation.
	if err := os.WriteFile(filepath.Join(jobStateDir, "jobs", localID+".json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Call(context.Background(), "federation_remote_job_reconcile", raw); err == nil {
		t.Fatal("corrupt bound local job evidence was silently accepted")
	}
	rec, err := ledger.Get(caller.Public().NodeID, "request-42")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "accepted" || rec.JobID == "" {
		t.Fatalf("unexpected ledger record: %#v", rec)
	}

	reserveSig, err := remotejobs.SignRequestForPurpose(caller, "reserve", receiver.Public().NodeID, "request-42", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	tampered := map[string]any{"caller_node_id": caller.Public().NodeID, "request_id": "request-42", "payload": payload + "!", "signature": base64.RawStdEncoding.EncodeToString(reserveSig), "reason": "tamper test"}
	tamperedRaw, _ := json.Marshal(tampered)
	if _, err := r.Call(context.Background(), "federation_remote_job_reserve", tamperedRaw); err == nil {
		t.Fatal("tampered payload was accepted")
	}
	if rec, err := ledger.Get(caller.Public().NodeID, "request-42"); err != nil || rec.Status != "accepted" {
		t.Fatalf("tampering changed ledger: rec=%#v err=%v", rec, err)
	}

	for _, tool := range r.List() {
		if tool.Name == "federation_remote_job_reserve" && tool.Annotations["readOnlyHint"] == true {
			t.Fatal("reservation incorrectly advertised as read-only")
		}
		if tool.Name == "federation_remote_job_status" && tool.Annotations["readOnlyHint"] != true {
			t.Fatal("status tool must be read-only")
		}
		if tool.Name == "federation_remote_job_reconcile" && tool.Annotations["readOnlyHint"] != true {
			t.Fatal("reconciliation tool must be read-only")
		}
	}
}

func TestRemoteJobToolsFailClosedForUntrustedCaller(t *testing.T) {
	receiver, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	caller, err := nodeidentity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := remotejobs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := registry.New()
	jobManager, err := jobs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerRemoteJobs(r, ledger, receiver, jobManager)
	payload := `{"operation":"noop"}`
	sig, err := remotejobs.SignRequestForPurpose(caller, "reserve", receiver.Public().NodeID, "request-untrusted", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"caller_node_id": caller.Public().NodeID, "request_id": "request-untrusted", "payload": payload, "signature": base64.RawStdEncoding.EncodeToString(sig), "reason": "untrusted caller test"})
	if _, err := r.Call(context.Background(), "federation_remote_job_reserve", raw); err == nil {
		t.Fatal("untrusted caller was accepted")
	}
	if _, err := ledger.Get(caller.Public().NodeID, "request-untrusted"); err == nil {
		t.Fatal("untrusted caller created a ledger record")
	}
}
