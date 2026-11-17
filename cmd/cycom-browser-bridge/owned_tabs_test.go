package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDirectBridgeOwnershipAndLeases(t *testing.T) {
	tabLeaseMu.Lock()
	saved, savedPath, browser := tabLeases, tabLeasePath, currentBrowser
	tabLeases = make(map[int]tabLease)
	tabLeasePath = filepath.Join(t.TempDir(), "leases.json")
	currentBrowser = "test-brave-session"
	tabLeaseMu.Unlock()
	t.Cleanup(func() {
		tabLeaseMu.Lock()
		tabLeases, tabLeasePath, currentBrowser = saved, savedPath, browser
		tabLeaseMu.Unlock()
	})

	trackBrowserResponse(map[string]any{"action": "open"}, response{OK: true, Result: map[string]any{"id": float64(100), "groupId": float64(-1)}})
	trackBrowserResponse(map[string]any{"action": "open"}, response{OK: true, Result: map[string]any{"id": float64(101)}})
	trackBrowserResponse(map[string]any{"action": "open"}, response{OK: false, Result: map[string]any{"id": float64(102), "groupId": float64(3)}})
	if len(tabLeases) != 0 {
		t.Fatal("non-Agent tab was tracked")
	}

	trackBrowserResponse(map[string]any{"action": "open"}, response{OK: true, Result: map[string]any{"id": float64(200), "groupId": float64(3)}})
	old := tabLeases[200].LastUsed
	if tabLeases[200].GroupID != 3 {
		t.Fatal("missing tracked group")
	}
	if _, err := os.Stat(tabLeasePath); err != nil {
		t.Fatalf("missing persisted lease: %v", err)
	}

	trackBrowserResponse(map[string]any{"action": "tabs"}, response{OK: true})
	if !old.Equal(tabLeases[200].LastUsed) {
		t.Fatal("tabs listing renewed lease incorrectly")
	}
	time.Sleep(time.Millisecond)
	trackBrowserResponse(map[string]any{"action": "navigate", "tabId": float64(200)}, response{OK: true})
	if !tabLeases[200].LastUsed.After(old) {
		t.Fatal("navigation did not refresh lease")
	}
	trackBrowserResponse(map[string]any{"action": "close", "tabId": float64(200)}, response{OK: true})
	if len(tabLeases) != 0 {
		t.Fatal("closed Agent tab was not forgotten")
	}
}
