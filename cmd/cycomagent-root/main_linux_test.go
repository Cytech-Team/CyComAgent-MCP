//go:build linux

package main

import (
	"os"
	"testing"
)

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
