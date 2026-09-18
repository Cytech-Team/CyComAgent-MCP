//go:build linux

package main

import (
	"os"
	"testing"
)

func TestNormalizeProcExeAfterAtomicUpgrade(t *testing.T) {
	got := normalizeProcExe("/usr/local/bin/cycomagent (deleted)")
	if got != "/usr/local/bin/cycomagent" {
		t.Fatalf("unexpected normalized path: %q", got)
	}
}

func TestPeerExecutableAllowedForSelf(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !peerExecutableAllowed(os.Getpid(), exe) {
		t.Fatalf("self executable should be allowed: %s", exe)
	}
	if peerExecutableAllowed(os.Getpid(), "/definitely/not/the/test/binary") {
		t.Fatal("unexpected executable match")
	}
}
