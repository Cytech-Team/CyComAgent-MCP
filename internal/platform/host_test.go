package platform

import (
	"path/filepath"
	"testing"
)

func TestTermuxDefaults(t *testing.T) {
	t.Setenv("TERMUX_VERSION", "0.119")
	t.Setenv("PREFIX", "/data/data/com.termux/files/usr")
	t.Setenv("TMPDIR", "/data/data/com.termux/files/usr/tmp")
	t.Setenv("SHELL", "")
	t.Setenv("CYCOM_SHELL", "")
	if !IsTermux() {
		t.Fatal("expected Termux detection")
	}
	if SupportsLocalPrivilege() {
		t.Fatal("Termux must not report local privilege support")
	}
	if got := DefaultRootSocket(); got != "" {
		t.Fatalf("DefaultRootSocket()=%q want empty on Termux", got)
	}
	wantService := "/data/data/com.termux/files/usr/var/service"
	if got := TermuxServiceDir(); got != wantService {
		t.Fatalf("TermuxServiceDir()=%q want %q", got, wantService)
	}
}

func TestShellOverride(t *testing.T) {
	t.Setenv("CYCOM_SHELL", filepath.Join(t.TempDir(), "custom-sh"))
	if got := DefaultShell(); got == "" {
		t.Fatal("expected shell override")
	}
}
