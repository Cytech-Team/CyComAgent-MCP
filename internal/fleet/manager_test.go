package fleet

import (
	"encoding/json"
	"testing"
)

func TestCompareVersion(t *testing.T) {
	cases := []struct{ installed, latest, want string }{{"0.4.8+abcdef123456", "0.4.8", "current"}, {"0.4.7+abcdef123456", "0.4.8", "version_mismatch"}, {"0.4.8+abcdef123456.dirty", "0.4.8", "current"}, {"0.4.8+123456789abc", "0.4.8", "behind"}, {"0.4.8+abcdef123456", "", "unknown"}}
	for _, tc := range cases {
		if got := compareBuild(tc.installed, tc.latest, "abcdef1234567890"); got != tc.want {
			t.Fatalf("compareVersion(%q,%q)=%q want %q", tc.installed, tc.latest, got, tc.want)
		}
	}
}
func TestTailscaleStatusDecodesPeerInventory(t *testing.T) {
	raw := []byte(`{"Self":{"HostName":"NamKrub","DNSName":"namkrub.tail.example.","OS":"linux","TailscaleIPs":["100.64.0.1"],"Online":true},"Peer":{"id":{"HostName":"home-server","DNSName":"home-server.tail.example.","OS":"linux","TailscaleIPs":["100.64.0.2"],"Online":true,"LastSeen":"2026-11-08T00:00:00Z"}}}`)
	var got status
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Self.HostName != "NamKrub" || len(got.Peer) != 1 {
		t.Fatalf("bad status decode: %+v", got)
	}
	for _, p := range got.Peer {
		if p.HostName != "home-server" || !p.Online || len(p.TailscaleIPs) != 1 {
			t.Fatalf("bad peer decode: %+v", p)
		}
	}
}
