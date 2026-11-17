//go:build linux

package remotejobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func validTestLaunchIntent() LaunchIntentRecord {
	return LaunchIntentRecord{Version: launchIntentJournalVersion, CallerNodeID: "node-caller", RequestID: "request-1", RemoteJobID: "remote-1", LocalJobID: "local-1", UnitName: func() string { n, _ := ExecutionUnitName("local-1", "request-1"); return n }(), ArtifactName: "exec-0123456789abcdef0123456789abcdef", ArtifactDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ArtifactDevice: 1, ArtifactInode: 2, PolicyRevision: 1, PayloadHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", State: "prepared", CreatedAt: nowForSystemdTest(), UpdatedAt: nowForSystemdTest()}
}

func nowForSystemdTest() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func TestSystemdUnitInspectorClassifiesExactUnitStates(t *testing.T) {
	cases := []struct{ name, output, want string }{
		{"active", "Id=cycomagent-job-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service\nLoadState=loaded\nActiveState=active\nSubState=running\nInvocationID=abc\nControlGroup=/user.slice/job\n", "active"},
		{"inactive", "Id=cycomagent-job-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service\nLoadState=loaded\nActiveState=inactive\nSubState=dead\nInvocationID=\nControlGroup=\n", "inactive"},
		{"not-found", "Id=cycomagent-job-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service\nLoadState=not-found\nActiveState=inactive\nSubState=dead\nInvocationID=\nControlGroup=\n", "not_found"},
		{"activating-without-cgroup", "Id=cycomagent-job-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service\nLoadState=loaded\nActiveState=activating\nSubState=start\nInvocationID=abc\nControlGroup=\n", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent := validTestLaunchIntent()
			output := strings.ReplaceAll(tc.output, "cycomagent-job-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.service", intent.UnitName)
			inspector := newSystemdUnitInspectorForTest(func(_ context.Context, unit string, props []string) ([]byte, error) {
				if unit != intent.UnitName || len(props) != 6 {
					t.Fatalf("unexpected query unit=%q props=%v", unit, props)
				}
				return []byte(output), nil
			})
			got, err := inspector.Inspect(context.Background(), intent)
			if err != nil || got.Status != tc.want {
				t.Fatalf("got=%#v err=%v want=%s", got, err, tc.want)
			}
		})
	}
}

func TestSystemdUnitInspectorFailsClosedOnMismatchOrQueryError(t *testing.T) {
	intent := validTestLaunchIntent()
	inspector := newSystemdUnitInspectorForTest(func(context.Context, string, []string) ([]byte, error) {
		return []byte("Id=wrong.service\nLoadState=loaded\nActiveState=inactive\nSubState=dead\nInvocationID=\nControlGroup=\n"), nil
	})
	if _, err := inspector.Inspect(context.Background(), intent); err == nil {
		t.Fatal("accepted mismatched unit")
	}
	inspector = newSystemdUnitInspectorForTest(func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("manager unavailable") })
	if _, err := inspector.Inspect(context.Background(), intent); err == nil {
		t.Fatal("accepted failed systemd query")
	}
}

func TestParseSystemdShowPropertiesRejectsDuplicateAndMissing(t *testing.T) {
	if _, err := parseSystemdShowProperties([]byte("Id=x\nId=x\n")); err == nil {
		t.Fatal("accepted duplicate properties")
	}
	if _, err := parseSystemdShowProperties([]byte("Id=x\n")); err == nil {
		t.Fatal("accepted incomplete properties")
	}
}

func TestValidateRecoveryUnitIdentityRequiresInvocationAndCanonicalCgroup(t *testing.T) {
	intent := validTestLaunchIntent()
	intent.SystemdInvocationID = "0123456789abcdef0123456789abcdef"
	unit := SystemdUnitObservation{
		UnitName: intent.UnitName, LoadState: "loaded", ActiveState: "inactive",
		Status: "inactive", InvocationID: "0123456789abcdef0123456789abcdef",
		ControlGroup: "/user.slice/user-1000.slice/user@1000.service/app.slice/" + intent.UnitName,
	}
	if err := validateRecoveryUnitIdentity(intent, unit); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*SystemdUnitObservation)
	}{
		{"wrong-unit", func(u *SystemdUnitObservation) { u.UnitName = "other.service" }},
		{"active", func(u *SystemdUnitObservation) { u.ActiveState = "active" }},
		{"missing-invocation", func(u *SystemdUnitObservation) { u.InvocationID = "" }},
		{"mismatched-invocation", func(u *SystemdUnitObservation) { u.InvocationID = "fedcba9876543210fedcba9876543210" }},
		{"malformed-durable-invocation", func(u *SystemdUnitObservation) { intent.SystemdInvocationID = "not-a-uuid" }},
		{"wrong-cgroup-unit", func(u *SystemdUnitObservation) { u.ControlGroup = "/user.slice/other.service" }},
		{"noncanonical-cgroup", func(u *SystemdUnitObservation) { u.ControlGroup = "/user.slice/../" + intent.UnitName }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := unit
			tc.mutate(&candidate)
			if err := validateRecoveryUnitIdentity(intent, candidate); err == nil {
				t.Fatal("unsafe systemd identity accepted")
			}
		})
	}
}
