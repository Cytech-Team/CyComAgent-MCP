package desktopruntime

import "testing"

func TestBestIsolationRejectsGlobalFallback(t *testing.T) {
	s := Snapshot{Adapters: []Adapter{{Name: "uinput", Capabilities: []Capability{Input}, Score: 100, Available: true, Isolated: false}, {Name: "virtual", Capabilities: []Capability{Input}, Score: 90, Available: true, Isolated: true}}}
	got, ok := s.Best(Input, true)
	if !ok || got.Name != "virtual" {
		t.Fatalf("got %#v ok=%v", got, ok)
	}
}
