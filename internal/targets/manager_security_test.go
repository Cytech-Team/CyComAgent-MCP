package targets

import (
	"strings"
	"testing"
)

func assertStrictHostKeyFirst(t *testing.T, args []string) {
	t.Helper()
	first := -1
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" && strings.HasPrefix(strings.ToLower(args[i+1]), "stricthostkeychecking=") {
			first = i + 1
			break
		}
	}
	if first < 0 || args[first] != "StrictHostKeyChecking=yes" {
		t.Fatalf("strict host key policy missing or not first: %#v", args)
	}
	for i := first + 1; i+1 < len(args); i++ {
		if args[i] == "-o" && strings.HasPrefix(strings.ToLower(args[i+1]), "stricthostkeychecking=") {
			t.Fatalf("target overrode host key policy: %#v", args)
		}
	}
}

func TestSSHHostKeyVerificationCannotBeOverriddenByTargetOptions(t *testing.T) {
	m := &Manager{}
	target := Target{Name: "peer", Transport: "ssh", Host: "127.0.0.1", Port: 22, Enabled: true, SSHOptions: []string{"StrictHostKeyChecking=no", "stricthostkeychecking=accept-new", "UserKnownHostsFile=/dev/null"}}
	assertStrictHostKeyFirst(t, m.sshArgs(target, 5))
	if isHostKeyPolicyOption("StrictHostKeyChecking no") == false || isHostKeyPolicyOption("ConnectTimeout=5") {
		t.Fatal("host-key option parser is incorrect")
	}
}
