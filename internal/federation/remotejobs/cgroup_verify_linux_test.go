//go:build linux

package remotejobs

import (
	"os"
	"testing"
)

func TestVerifyCgroupAbsenceRequiresInactiveUnitAndCgroupV2(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	unit := SystemdUnitObservation{Status: "active", ControlGroup: "/user.slice/job"}
	if _, err := VerifyCgroupAbsence(root, unit); err == nil {
		t.Fatal("accepted active unit")
	}
	unit.Status = "inactive"
	if _, err := VerifyCgroupAbsence(root, unit); err == nil {
		t.Fatal("accepted non-cgroup filesystem")
	}
}

func TestVerifyCgroupAbsenceReportsMissingExactPath(t *testing.T) {
	root, err := os.Open("/sys/fs/cgroup")
	if err != nil {
		t.Skipf("cgroup mount unavailable: %v", err)
	}
	defer root.Close()
	unit := SystemdUnitObservation{Status: "inactive", ControlGroup: "/cycomagent-test-path-that-must-not-exist"}
	got, err := VerifyCgroupAbsence(root, unit)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "missing" || got.Present {
		t.Fatalf("unexpected missing observation: %#v", got)
	}
}

func TestVerifyCgroupAbsenceRejectsUnsafePaths(t *testing.T) {
	root, err := os.Open("/sys/fs/cgroup")
	if err != nil {
		t.Skipf("cgroup mount unavailable: %v", err)
	}
	defer root.Close()
	for _, path := range []string{"/", "/../proc", "/user.slice/../../proc", "relative"} {
		_, err := VerifyCgroupAbsence(root, SystemdUnitObservation{Status: "inactive", ControlGroup: path})
		if err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
}
