package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Direct Bridge tab leases are independent from the legacy-port router.
// Never infer ownership merely from the CyCom group name: only successful
// Bridge open responses with a group ID create a lease.
const idleTabTTL = 3 * time.Minute
const tabSweepInterval = 30 * time.Second

type tabLease struct {
	GroupID  int       `json:"group_id"`
	LastUsed time.Time `json:"last_used"`
}
type tabLeaseFile struct {
	Browser string           `json:"browser"`
	Tabs    map[int]tabLease `json:"tabs"`
}

var tabLeaseMu sync.Mutex
var tabLeases = make(map[int]tabLease)
var tabLeasePath string
var currentBrowser string

func liveBraveIdentity() string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		pid := entry.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if err != nil {
			continue
		}
		cmd := string(bytes.ReplaceAll(raw, []byte{0}, []byte{' '}))
		if !(strings.HasPrefix(cmd, "/opt/brave-bin/brave ") || strings.TrimSpace(cmd) == "/opt/brave-bin/brave") ||
			strings.Contains(cmd, "--type=") || strings.Contains(cmd, "cycomagent/browser-profile") {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
		if err != nil {
			continue
		}
		parts := strings.Fields(string(stat)[strings.LastIndex(string(stat), ")")+1:])
		if len(parts) > 19 {
			return pid + ":" + parts[19]
		}
	}
	return ""
}

func initTabLeases() {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	tabLeasePath = filepath.Join(dir, "cycom-browser-bridge", "owned-tabs.json")
	currentBrowser = liveBraveIdentity()
	data, err := os.ReadFile(tabLeasePath)
	if err != nil || currentBrowser == "" {
		return
	}
	var saved tabLeaseFile
	if json.Unmarshal(data, &saved) != nil || saved.Browser != currentBrowser {
		return
	}
	tabLeaseMu.Lock()
	defer tabLeaseMu.Unlock()
	for id, lease := range saved.Tabs {
		if id > 0 && lease.GroupID >= 0 && !lease.LastUsed.IsZero() {
			tabLeases[id] = lease
		}
	}
}

// Called only with tabLeaseMu held; /run/user is writable despite the user
// service's ProtectHome=read-only configuration.
func saveTabLeasesLocked() {
	if tabLeasePath == "" {
		return
	}
	data, err := json.Marshal(tabLeaseFile{Browser: currentBrowser, Tabs: tabLeases})
	if err != nil {
		return
	}
	tmp := tabLeasePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("CyCom lease write failed: %v", err)
		return
	}
	if err := os.Rename(tmp, tabLeasePath); err != nil {
		log.Printf("CyCom lease save failed: %v", err)
	}
}

func intField(obj map[string]any, key string) (int, bool) {
	n, ok := obj[key].(float64)
	if !ok || n < 0 || n > float64(int(^uint(0)>>1)) || n != float64(int(n)) {
		return 0, false
	}
	return int(n), true
}

func trackBrowserResponse(request map[string]any, result response) {
	if !result.OK {
		return
	}
	action, _ := request["action"].(string)
	tabLeaseMu.Lock()
	defer tabLeaseMu.Unlock()
	now := time.Now()
	switch action {
	case "open":
		out, ok := result.Result.(map[string]any)
		if !ok {
			return
		}
		id, okID := intField(out, "id")
		group, okGroup := intField(out, "groupId")
		if okID && id > 0 && okGroup {
			tabLeases[id] = tabLease{GroupID: group, LastUsed: now}
			saveTabLeasesLocked()
		}
	case "close":
		if id, ok := intField(request, "tabId"); ok {
			delete(tabLeases, id)
			saveTabLeasesLocked()
		}
	case "tabs":
		// Listing tabs does not constitute using all Agent tabs.
	default:
		if id, ok := intField(request, "tabId"); ok {
			if lease, owned := tabLeases[id]; owned {
				lease.LastUsed = now
				tabLeases[id] = lease
				saveTabLeasesLocked()
			}
		}
	}
}

func bridgeRequest(ctx context.Context, payload map[string]any) (response, error) {
	addr := os.Getenv("CYCOM_BROWSER_BRIDGE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:17373"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/api", bytes.NewReader(raw))
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer res.Body.Close()
	var out response
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return response{}, err
	}
	if res.StatusCode != http.StatusOK || !out.OK {
		return response{}, fmt.Errorf("extension rejected %s", payload["action"])
	}
	return out, nil
}

type cleanupResult struct {
	Closed    int `json:"closed"`
	Preserved int `json:"preserved"`
	Remaining int `json:"remaining"`
}

var sweepMu sync.Mutex

// finish=true closes all *background* Agent-owned tabs immediately.
// A periodic sweep does the same only for leases idle for >= 3 minutes.
func sweepOwnedTabs(ctx context.Context, finish bool) (cleanupResult, error) {
	sweepMu.Lock()
	defer sweepMu.Unlock()
	tabLeaseMu.Lock()
	now := time.Now()
	candidates := make(map[int]tabLease)
	for id, lease := range tabLeases {
		if finish || now.Sub(lease.LastUsed) >= idleTabTTL {
			candidates[id] = lease
		}
	}
	tabLeaseMu.Unlock()
	if len(candidates) == 0 {
		tabLeaseMu.Lock()
		remaining := len(tabLeases)
		tabLeaseMu.Unlock()
		return cleanupResult{Remaining: remaining}, nil
	}
	tabs, err := bridgeRequest(ctx, map[string]any{"action": "tabs"})
	if err != nil {
		return cleanupResult{}, err
	}
	list, ok := tabs.Result.([]any)
	if !ok {
		return cleanupResult{}, errors.New("Bridge tabs result invalid")
	}
	live := make(map[int]map[string]any)
	for _, entry := range list {
		if row, ok := entry.(map[string]any); ok {
			if id, ok := intField(row, "id"); ok {
				live[id] = row
			}
		}
	}
	result := cleanupResult{}
	for id, snapshot := range candidates {
		tabLeaseMu.Lock()
		lease, owned := tabLeases[id]
		if !owned || lease.GroupID != snapshot.GroupID || (!finish && time.Since(lease.LastUsed) < idleTabTTL) {
			tabLeaseMu.Unlock()
			continue
		}
		row, present := live[id]
		group, valid := intField(row, "groupId")
		if !present || !valid || group != lease.GroupID {
			// A person moved the tab out of its original Agent group.
			delete(tabLeases, id)
			saveTabLeasesLocked()
			tabLeaseMu.Unlock()
			continue
		}
		if active, _ := row["active"].(bool); active {
			lease.LastUsed = time.Now()
			tabLeases[id] = lease
			saveTabLeasesLocked()
			tabLeaseMu.Unlock()
			result.Preserved++
			continue
		}
		tabLeaseMu.Unlock()
		_, err := bridgeRequest(ctx, map[string]any{"action": "close", "tabId": id})
		if err != nil {
			result.Preserved++
			continue
		}
		result.Closed++
	}
	tabLeaseMu.Lock()
	result.Remaining = len(tabLeases)
	tabLeaseMu.Unlock()
	return result, nil
}

func idleTabSweeper() {
	ticker := time.NewTicker(tabSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		summary, err := sweepOwnedTabs(ctx, false)
		cancel()
		if err != nil {
			log.Printf("CyCom idle tab cleanup deferred: %v", err)
		} else if summary.Closed != 0 {
			log.Printf("CyCom cleaned up %d idle Agent tab(s)", summary.Closed)
		}
	}
}

func finishTabsHandler(w http.ResponseWriter, r *http.Request) {
	result, err := sweepOwnedTabs(r.Context(), true)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		http.Error(w, "Agent tab cleanup unavailable", http.StatusServiceUnavailable)
		return
	}
	json.NewEncoder(w).Encode(response{OK: true, Result: result})
}
