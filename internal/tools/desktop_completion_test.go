//go:build linux

package tools

import "testing"

func TestParsePercentBounds(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		bad  bool
	}{{"0", 0, false}, {"50", 50, false}, {"100", 100, false}, {"101", 0, true}, {"-1", 0, true}, {"50%", 50, false}, {"abc", 0, true}} {
		got, err := parsePercent(tc.in)
		if (err != nil) != tc.bad {
			t.Fatalf("parsePercent(%q) error=%v", tc.in, err)
		}
		if !tc.bad && got != tc.want {
			t.Fatalf("parsePercent(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestVolumePercent(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"Volume: 1.00", "100"}, {"Volume: 0.50", "50"}, {"Volume: 0.00", "0"}, {"not available", "0"}} {
		if got := volumePercent(tc.in); got != tc.want {
			t.Fatalf("volumePercent(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
