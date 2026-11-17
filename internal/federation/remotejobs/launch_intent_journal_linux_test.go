//go:build linux

package remotejobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLaunchIntentJournalPersistsStableUnitAndTransitions(t *testing.T) {
	state := t.TempDir()
	ledger, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	records, err := OpenExecutionRecordStore(state)
	if err != nil {
		t.Fatal(err)
	}
	reserved, _, err := ledger.Reserve("node-caller", "request-launch", "payload")
	if err != nil {
		t.Fatal(err)
	}
	intent := sampleExecutionIntent()
	intent.RequestID = reserved.RequestID
	intent.RemoteJobID = reserved.JobID
	intent.PayloadHash = reserved.PayloadHash
	if _, _, err := PrepareExecutionIntent(ledger, records, intent); err != nil {
		t.Fatal(err)
	}
	data := []byte("binary")
	sum := sha256.Sum256(data)
	snapshot := &ExecutableSnapshot{content: data, digest: hex.EncodeToString(sum[:])}
	stageDir := filepath.Join(state, "stage")
	if err := os.Mkdir(stageDir, 0711); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	uid := uint32(os.Getuid())
	stage, err := StageExecutableSnapshot(snapshot, dir, uid, uid+1)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	registry, err := OpenExecutableArtifactRegistry(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Register(stage, snapshot, ledger, records, intent.CallerNodeID, intent.RequestID, uid, uid+1); err != nil {
		t.Fatal(err)
	}
	if err := stage.Retain(); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenLaunchIntentJournal(state)
	if err != nil {
		t.Fatal(err)
	}
	got, created, err := journal.Prepare(registry, ledger, records, intent.CallerNodeID, intent.RequestID)
	if err != nil || !created {
		t.Fatalf("prepare created=%v err=%v", created, err)
	}
	want, err := ExecutionUnitName(intent.LocalJobID, intent.RequestID)
	if err != nil || got.UnitName != want {
		t.Fatalf("unit=%q want=%q err=%v", got.UnitName, want, err)
	}
	if again, created, err := journal.Prepare(registry, ledger, records, intent.CallerNodeID, intent.RequestID); err != nil || created || again.UnitName != got.UnitName {
		t.Fatalf("idempotent prepare created=%v err=%v", created, err)
	}
	activeOutput := func(unit string) []byte {
		return []byte("Id=" + unit + "\nLoadState=loaded\nActiveState=active\nSubState=running\nInvocationID=0123456789abcdef0123456789abcdef\nControlGroup=/user.slice/test.slice/" + unit + "\n")
	}
	inspector := newSystemdUnitInspectorForTest(func(_ context.Context, unit string, _ []string) ([]byte, error) { return activeOutput(unit), nil })
	if _, err := journal.bindObservedSystemdInvocation(context.Background(), inspector, intent.CallerNodeID, intent.RequestID); err == nil {
		t.Fatal("InvocationID bound before launching state")
	}
	launching, err := journal.Transition(intent.CallerNodeID, intent.RequestID, "launching")
	if err != nil {
		t.Fatal(err)
	}
	if len(launching.StartAttemptID) != 32 {
		t.Fatalf("launching state lacks durable start attempt identity: %q", launching.StartAttemptID)
	}
	if _, err := hex.DecodeString(launching.StartAttemptID); err != nil {
		t.Fatalf("malformed start attempt identity: %v", err)
	}
	if again, err := journal.Get(intent.CallerNodeID, intent.RequestID); err != nil || again.StartAttemptID != launching.StartAttemptID {
		t.Fatalf("start attempt identity was not durable: %#v err=%v", again, err)
	}
	if _, err := systemdStartRequestFromIntent(launching); err == nil {
		t.Fatal("start request was constructible before durable one-shot reservation")
	}
	type reservationResult struct {
		request systemdStartRequest
		err     error
	}
	results := make(chan reservationResult, 2)
	var reserveWG sync.WaitGroup
	for i := 0; i < 2; i++ {
		reserveWG.Add(1)
		go func() {
			defer reserveWG.Done()
			request, err := journal.reserveSystemdStartRequest(intent.CallerNodeID, intent.RequestID)
			results <- reservationResult{request: request, err: err}
		}()
	}
	reserveWG.Wait()
	close(results)
	var startRequest systemdStartRequest
	successes := 0
	failures := 0
	for result := range results {
		if result.err == nil {
			successes++
			startRequest = result.request
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent one-shot reservations successes=%d failures=%d", successes, failures)
	}
	if startRequest.StartAttemptID != launching.StartAttemptID || startRequest.UnitName != launching.UnitName {
		t.Fatalf("reserved request identity mismatch: %#v", startRequest)
	}
	reservedRecord, err := journal.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil || reservedRecord.StartRequestReservedAt.IsZero() {
		t.Fatalf("start reservation was not durable: %#v err=%v", reservedRecord, err)
	}
	if _, err := journal.reserveSystemdStartRequest(intent.CallerNodeID, intent.RequestID); err == nil {
		t.Fatal("second start request reservation was accepted")
	}
	if _, err := journal.bindObservedSystemdInvocation(context.Background(), inspector, intent.CallerNodeID, intent.RequestID); err == nil {
		t.Fatal("InvocationID bound without accepted start acknowledgement")
	}
	if _, err := journal.Transition(intent.CallerNodeID, intent.RequestID, "unknown"); err == nil {
		t.Fatal("reserved request transitioned to unknown without a durable unknown acknowledgement")
	}
	badAck, err := newSystemdStartAcknowledgement(startRequest, "accepted", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	badAck.StartAttemptID = "1123456789abcdef0123456789abcdef"
	if _, err := journal.recordSystemdStartAcknowledgement(badAck); err == nil {
		t.Fatal("acknowledgement for a different attempt was accepted")
	}
	goodAck, err := newSystemdStartAcknowledgement(startRequest, "accepted", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	acknowledged, err := journal.recordSystemdStartAcknowledgement(goodAck)
	if err != nil || acknowledged.StartRequestOutcome != "accepted" || acknowledged.StartRequestAckAt.IsZero() {
		t.Fatalf("accepted acknowledgement not persisted: %#v err=%v", acknowledged, err)
	}
	if _, err := journal.recordSystemdStartAcknowledgement(goodAck); err == nil {
		t.Fatal("duplicate acknowledgement was accepted")
	}
	for _, state := range []string{"running", "completed"} {
		if _, err := journal.Transition(intent.CallerNodeID, intent.RequestID, state); err == nil {
			t.Fatalf("transition to %s succeeded without a verified InvocationID", state)
		}
	}
	badObservations := []struct{ name, active, invocation, cgroup string }{
		{"inactive", "inactive", "0123456789abcdef0123456789abcdef", "/user.slice/test.slice/" + got.UnitName},
		{"malformed-invocation", "active", "not-an-invocation-id", "/user.slice/test.slice/" + got.UnitName},
		{"wrong-cgroup", "active", "0123456789abcdef0123456789abcdef", "/user.slice/test.slice/other.service"},
		{"noncanonical-cgroup", "active", "0123456789abcdef0123456789abcdef", "/user.slice/../" + got.UnitName},
	}
	for _, tc := range badObservations {
		t.Run("reject-observation-"+tc.name, func(t *testing.T) {
			bad := newSystemdUnitInspectorForTest(func(_ context.Context, unit string, _ []string) ([]byte, error) {
				return []byte("Id=" + unit + "\nLoadState=loaded\nActiveState=" + tc.active + "\nSubState=running\nInvocationID=" + tc.invocation + "\nControlGroup=" + tc.cgroup + "\n"), nil
			})
			if _, err := journal.bindObservedSystemdInvocation(context.Background(), bad, intent.CallerNodeID, intent.RequestID); err == nil {
				t.Fatalf("unsafe %s observation accepted", tc.name)
			}
		})
	}
	inspector = newSystemdUnitInspectorForTest(func(_ context.Context, unit string, _ []string) ([]byte, error) {
		if unit != got.UnitName {
			return nil, fmt.Errorf("unexpected unit")
		}
		return activeOutput(unit), nil
	})
	bound, err := journal.bindObservedSystemdInvocation(context.Background(), inspector, intent.CallerNodeID, intent.RequestID)
	if err != nil || bound.SystemdInvocationID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("InvocationID binding=%#v err=%v", bound, err)
	}
	if _, err := journal.bindObservedSystemdInvocation(context.Background(), inspector, intent.CallerNodeID, intent.RequestID); err != nil {
		t.Fatalf("idempotent InvocationID bind: %v", err)
	}
	changedInspector := newSystemdUnitInspectorForTest(func(_ context.Context, unit string, _ []string) ([]byte, error) {
		return []byte("Id=" + unit + "\nLoadState=loaded\nActiveState=active\nSubState=running\nInvocationID=1123456789abcdef0123456789abcdef\nControlGroup=/user.slice/test.slice/" + unit + "\n"), nil
	})
	if _, err := journal.bindObservedSystemdInvocation(context.Background(), changedInspector, intent.CallerNodeID, intent.RequestID); err == nil {
		t.Fatal("InvocationID replacement was accepted")
	}
	for _, state := range []string{"running", "completed"} {
		if _, err := journal.Transition(intent.CallerNodeID, intent.RequestID, state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := journal.Transition(intent.CallerNodeID, intent.RequestID, "running"); err == nil {
		t.Fatal("allowed transition out of terminal state")
	}
	persisted, err := journal.Get(intent.CallerNodeID, intent.RequestID)
	if err != nil || persisted.State != "completed" || persisted.UnitName != want {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	if persisted.SystemdInvocationID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("InvocationID was not durably retained: %q", persisted.SystemdInvocationID)
	}
	if _, err := journal.bindObservedSystemdInvocation(context.Background(), inspector, intent.CallerNodeID, intent.RequestID); err == nil {
		t.Fatal("InvocationID changed after terminal transition")
	}
}

func TestLaunchIntentJournalRequiresConsistentArtifact(t *testing.T) {
	state := t.TempDir()
	journal, err := OpenLaunchIntentJournal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Prepare(nil, nil, nil, "node-caller", "request-launch"); err == nil {
		t.Fatal("prepared without stores")
	}
}

func TestLaunchIntentJournalRejectsLaunchingWithoutAttemptIdentity(t *testing.T) {
	rec := validTestLaunchIntent()
	rec.State = "launching"
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var decoded LaunchIntentRecord
	if err := strictLaunchIntentRecord(data, &decoded); err == nil {
		t.Fatal("accepted launching record without durable start attempt identity")
	}
}

func TestLaunchIntentJournalPersistsDefinitiveRejectedAndUnknownAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		outcome, wantState string
		transitionUnknown  bool
	}{
		{"rejected", "failed", false},
		{"unknown", "unknown", false},
		{"accepted", "unknown", true},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			journal, err := OpenLaunchIntentJournal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			intent := validTestLaunchIntent()
			intent.State = "launching"
			intent.StartAttemptID = "0123456789abcdef0123456789abcdef"
			intent.StartRequestReservedAt = time.Now().UTC().Add(-time.Second)
			data, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.store.write(fileKey(intent.CallerNodeID, intent.RequestID)+".json", data, false); err != nil {
				t.Fatal(err)
			}
			request, err := systemdStartRequestFromIntent(intent)
			if err != nil {
				t.Fatal(err)
			}
			ack, err := newSystemdStartAcknowledgement(request, tc.outcome, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			got, err := journal.recordSystemdStartAcknowledgement(ack)
			if err != nil {
				t.Fatal(err)
			}
			if tc.transitionUnknown {
				got, err = journal.Transition(intent.CallerNodeID, intent.RequestID, "unknown")
				if err != nil {
					t.Fatalf("accepted request could not be reconciled to unknown: %v", err)
				}
			}
			if got.State != tc.wantState || got.StartRequestOutcome != tc.outcome || got.StartRequestAckAt.IsZero() {
				t.Fatalf("acknowledgement result=%#v", got)
			}
			if _, err := journal.recordSystemdStartAcknowledgement(ack); err == nil {
				t.Fatal("duplicate terminal acknowledgement accepted")
			}
			if _, err := journal.Get(intent.CallerNodeID, intent.RequestID); err != nil {
				t.Fatalf("persisted terminal record invalid: %v", err)
			}
		})
	}
}

func TestLaunchIntentJournalMarksReservedNoAckAsCrashRecoveryUnknown(t *testing.T) {
	state := t.TempDir()
	journal, err := OpenLaunchIntentJournal(state)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	rec := validTestLaunchIntent()
	rec.State = "launching"
	rec.StartAttemptID = "0123456789abcdef0123456789abcdef"
	rec.StartRequestReservedAt = time.Now().UTC().Add(-time.Minute)
	rec.UpdatedAt = rec.StartRequestReservedAt
	encoded, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.store.write(fileKey(rec.CallerNodeID, rec.RequestID)+".json", encoded, false); err != nil {
		t.Fatal(err)
	}
	unknown, err := journal.markReservedStartUncertain(rec.CallerNodeID, rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.State != "unknown" || unknown.StartRequestUncertainAt.IsZero() || unknown.StartRequestOutcome != "" || !unknown.StartRequestAckAt.IsZero() {
		t.Fatalf("recovery uncertainty fabricated or omitted acknowledgement data: %#v", unknown)
	}
	persisted, err := journal.Get(rec.CallerNodeID, rec.RequestID)
	if err != nil || persisted.State != "unknown" || persisted.StartRequestUncertainAt.IsZero() {
		t.Fatalf("uncertainty not durable: %#v err=%v", persisted, err)
	}
	if _, err := journal.markReservedStartUncertain(rec.CallerNodeID, rec.RequestID); err != nil {
		t.Fatalf("idempotent recovery observation failed: %v", err)
	}
	if _, err := journal.reserveSystemdStartRequest(rec.CallerNodeID, rec.RequestID); err == nil {
		t.Fatal("uncertain attempt was re-reserved")
	}
	request, err := systemdStartRequestFromIntent(persisted)
	if err == nil {
		t.Fatalf("terminal uncertain intent produced a start request: %#v", request)
	}
	ack, err := newSystemdStartAcknowledgement(systemdStartRequest{Version: systemdStartProtocolVersion, CallerNodeID: rec.CallerNodeID, RequestID: rec.RequestID, RemoteJobID: rec.RemoteJobID, LocalJobID: rec.LocalJobID, StartAttemptID: rec.StartAttemptID, UnitName: rec.UnitName, ArtifactDigest: rec.ArtifactDigest, PolicyRevision: rec.PolicyRevision, PayloadHash: rec.PayloadHash}, "accepted", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.recordSystemdStartAcknowledgement(ack); err == nil {
		t.Fatal("late acknowledgement resurrected crash-recovery-unknown attempt")
	}
}

func TestLaunchIntentStrictValidationRejectsFalseRecoveryMarkers(t *testing.T) {
	base := validTestLaunchIntent()
	base.State = "unknown"
	base.StartAttemptID = "0123456789abcdef0123456789abcdef"
	base.StartRequestReservedAt = time.Now().UTC().Add(-time.Minute)
	for name, mutate := range map[string]func(*LaunchIntentRecord){
		"missing-marker": func(r *LaunchIntentRecord) {},
		"wrong-state":    func(r *LaunchIntentRecord) { r.State = "launching"; r.StartRequestUncertainAt = time.Now().UTC() },
		"with-ack": func(r *LaunchIntentRecord) {
			r.StartRequestUncertainAt = time.Now().UTC()
			r.StartRequestOutcome = "accepted"
			r.StartRequestAckAt = r.StartRequestUncertainAt
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := base
			if name != "missing-marker" {
				mutate(&rec)
			}
			data, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			var decoded LaunchIntentRecord
			if err := strictLaunchIntentRecord(data, &decoded); err == nil {
				t.Fatal("invalid recovery marker combination accepted")
			}
		})
	}
}
