//go:build linux

package remotejobs

import (
	"strings"
	"testing"
	"time"
)

func TestBuildWorkerServiceProfileUsesDedicatedIdentityAndFixedIsolation(t *testing.T) {
	profile, err := BuildWorkerServiceProfile(991, 991, 30*time.Second, "/run/cycomagent-namkrub/root.sock")
	if err != nil {
		t.Fatal(err)
	}
	if profile.User != "991" || profile.Group != "991" {
		t.Fatalf("unexpected worker identity: user=%q group=%q", profile.User, profile.Group)
	}
	joined := strings.Join(profile.Properties, "\n")
	for _, required := range []string{
		"KillMode=control-group", "SendSIGKILL=yes", "RuntimeMaxSec=30s",
		"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes",
		"PrivateNetwork=yes", "ProtectProc=invisible", "ProcSubset=pid",
		"RestrictNamespaces=yes", "RestrictAddressFamilies=AF_UNIX",
		"DevicePolicy=closed", "ProtectClock=yes", "ProtectHostname=yes",
		"CapabilityBoundingSet=", "AmbientCapabilities=",
		"UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("worker profile missing %q", required)
		}
	}
	if len(profile.InaccessiblePaths) != 3 || profile.InaccessiblePaths[0] != "/run/user" || profile.InaccessiblePaths[1] != "/run/dbus/system_bus_socket" || profile.InaccessiblePaths[2] != "/run/cycomagent-namkrub/root.sock" {
		t.Fatalf("unexpected inaccessible path list: %#v", profile.InaccessiblePaths)
	}
	for _, forbidden := range []string{"cycomagent-root", "ExecStart=", "User=0", "Group=0"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("worker profile contains forbidden setting %q", forbidden)
		}
	}
	for _, property := range profile.Properties {
		if strings.HasPrefix(property, "UnsetEnvironment=") {
			continue
		}
		if strings.Contains(property, "DBUS_SESSION_BUS_ADDRESS") || strings.Contains(property, "XDG_RUNTIME_DIR") {
			t.Errorf("session bus variable leaked into worker property: %q", property)
		}
	}
}

func TestBuildWorkerServiceProfileRejectsRootAndInvalidLimits(t *testing.T) {
	cases := []struct {
		name     string
		uid, gid uint32
		timeout  time.Duration
	}{
		{name: "root uid", uid: 0, gid: 991, timeout: time.Second},
		{name: "root gid", uid: 991, gid: 0, timeout: time.Second},
		{name: "zero timeout", uid: 991, gid: 991},
		{name: "excessive timeout", uid: 991, gid: 991, timeout: 24*time.Hour + time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildWorkerServiceProfile(tc.uid, tc.gid, tc.timeout, "/run/cycomagent-namkrub/root.sock"); err == nil {
				t.Fatal("invalid worker profile was accepted")
			}
		})
	}
}

func TestBuildWorkerServiceProfileRejectsUntrustedOrUnsafeSocketPaths(t *testing.T) {
	for _, path := range []string{"", "relative/root.sock", "/run/cycomagent/../root.sock", "/run/cycomagent-user/root.sock extra", "/tmp/cycomagent/not-root.sock"} {
		t.Run(path, func(t *testing.T) {
			if _, err := BuildWorkerServiceProfile(991, 991, time.Second, path); err == nil {
				t.Fatalf("unsafe broker socket path accepted: %q", path)
			}
		})
	}
}
