package buildinfo

import (
	"strings"
	"testing"
)

func TestVersionSource(t *testing.T) {
	if got := Version(); got != "0.4.8-isolated-desktop-dev" {
		t.Fatalf("Version()=%q", got)
	}
	if got := FullVersion(); !strings.HasPrefix(got, Version()) {
		t.Fatalf("FullVersion()=%q does not start with %q", got, Version())
	}
}
