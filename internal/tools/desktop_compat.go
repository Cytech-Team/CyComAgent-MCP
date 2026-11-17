package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

// registerDesktopCompatibility exposes stable high-level desktop discovery
// tools independently of the optional CyShell UI. It uses XDG desktop entries
// and the active session's launcher rather than guessing executable names.
func registerDesktopCompatibility(r *registry.Registry) {
	must(r.Add(registry.Tool{Name: "application_search", Description: "Search installed Linux desktop applications and return exact desktop-entry IDs suitable for application_launch.", InputSchema: registry.ObjectSchema(map[string]any{"query": registry.String("application name, generic name, desktop ID, keyword or description"), "max_results": registry.Integer("maximum results; default 10, max 25")}, []string{"query"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: applicationSearch}))
	must(r.Add(registry.Tool{Name: "application_launch", Description: "Launch an installed Linux desktop application by exact desktop-entry ID returned by application_search.", InputSchema: registry.ObjectSchema(map[string]any{"desktop_id": registry.String("exact desktop-entry ID returned by application_search")}, []string{"desktop_id"}), Source: "core", Handler: applicationLaunch}))
}

type desktopEntry struct {
	ID, Name, GenericName, Comment, Exec string
	NoDisplay, Hidden                    bool
}

func desktopEntryDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".local/share/applications"), "/usr/local/share/applications", "/usr/share/applications", "/var/lib/flatpak/exports/share/applications", filepath.Join(home, ".local/share/flatpak/exports/share/applications")}
}

func readDesktopEntry(path string) (desktopEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return desktopEntry{}, err
	}
	defer f.Close()
	e := desktopEntry{ID: strings.TrimSuffix(filepath.Base(path), ".desktop")}
	s := bufio.NewScanner(f)
	in := false
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			in = line == "[Desktop Entry]"
			continue
		}
		if !in || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Name":
			e.Name = v
		case "GenericName":
			e.GenericName = v
		case "Comment":
			e.Comment = v
		case "Exec":
			e.Exec = v
		case "NoDisplay":
			e.NoDisplay = strings.EqualFold(v, "true")
		case "Hidden":
			e.Hidden = strings.EqualFold(v, "true")
		}
	}
	return e, s.Err()
}

func applicationSearch(_ context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	if q == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit := in.MaxResults
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}
	seen := map[string]bool{}
	var matches []map[string]any
	for _, dir := range desktopEntryDirs() {
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".desktop") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".desktop")
			if seen[id] {
				continue
			}
			seen[id] = true
			e, err := readDesktopEntry(filepath.Join(dir, entry.Name()))
			if err != nil || e.Hidden || e.NoDisplay || e.Name == "" || e.Exec == "" {
				continue
			}
			hay := strings.ToLower(strings.Join([]string{e.ID, e.Name, e.GenericName, e.Comment}, " "))
			if !strings.Contains(hay, q) {
				continue
			}
			matches = append(matches, map[string]any{"desktop_id": e.ID, "name": e.Name, "generic_name": e.GenericName, "comment": e.Comment, "source": dir})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return strings.ToLower(fmt.Sprint(matches[i]["name"])) < strings.ToLower(fmt.Sprint(matches[j]["name"]))
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return map[string]any{"query": in.Query, "count": len(matches), "applications": matches}, nil
}

func desktopEntryExecutable(execLine string) string {
	fields := strings.Fields(execLine)
	for len(fields) > 0 {
		base := filepath.Base(fields[0])
		if base == "env" {
			fields = fields[1:]
			continue
		}
		if strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "/") {
			fields = fields[1:]
			continue
		}
		return base
	}
	return ""
}

func processIDsForExecutable(executable string) map[int]bool {
	result := map[int]bool{}
	if executable == "" {
		return result
	}
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(cmdline) == 0 {
			continue
		}
		first := strings.SplitN(string(cmdline), "\x00", 2)[0]
		if filepath.Base(first) == executable {
			result[pid] = true
		}
	}
	return result
}

func applicationLaunch(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		DesktopID string `json:"desktop_id"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.DesktopID)
	if id == "" || strings.ContainsAny(id, "/\\\x00") || strings.Contains(id, "..") {
		return nil, fmt.Errorf("desktop_id must be an exact desktop-entry ID")
	}
	found := false
	foundDir := ""
	for _, dir := range desktopEntryDirs() {
		p := filepath.Join(dir, id+".desktop")
		if _, err := os.Stat(p); err == nil {
			found = true
			foundDir = dir
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("desktop entry %q not found; call application_search first", id)
	}
	launcher, err := exec.LookPath("gtk-launch")
	if err != nil {
		return nil, fmt.Errorf("gtk-launch is not installed; cannot safely launch desktop entry %q", id)
	}
	cmd := exec.CommandContext(ctx, launcher, id)
	cmd.Env = desktopEnvironment()
	// Do not capture stdout/stderr through pipes: gtk-launch hands off to the
	// GUI process, which can inherit those descriptors and keep CombinedOutput
	// blocked until the application exits. A direct /dev/null file lets the
	// launcher finish while the GUI app remains attached to the session.
	devNull, openErr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if openErr != nil {
		return nil, openErr
	}
	cmd.Stdout, cmd.Stderr = devNull, devNull
	err = cmd.Run()
	_ = devNull.Close()
	if err != nil {
		return nil, fmt.Errorf("launch %s through gtk-launch: %w", id, err)
	}
	entry, _ := readDesktopEntry(filepath.Join(foundDir, id+".desktop"))
	binary := desktopEntryExecutable(entry.Exec)
	before := processIDsForExecutable(binary)
	deadline := time.Now().Add(1800 * time.Millisecond)
	var processIDs []int
	for time.Now().Before(deadline) {
		after := processIDsForExecutable(binary)
		processIDs = processIDs[:0]
		for pid := range after {
			processIDs = append(processIDs, pid)
		}
		if len(processIDs) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	sort.Ints(processIDs)
	newProcess := false
	for _, pid := range processIDs {
		if !before[pid] {
			newProcess = true
		}
	}
	return map[string]any{"ok": true, "desktop_id": id, "launcher": "gtk-launch", "launcher_exit_code": 0, "process_found": len(processIDs) > 0, "new_process": newProcess, "verified": len(processIDs) > 0, "process_ids": processIDs, "executable": binary, "note": map[bool]string{true: "Application process detected after launch", false: "Desktop launcher accepted the request, but no matching application process was detected"}[len(processIDs) > 0]}, nil
}
