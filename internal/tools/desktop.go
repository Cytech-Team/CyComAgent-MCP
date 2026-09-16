package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func registerDesktop(r *registry.Registry, stateDir string) {
	must(r.Add(registry.Tool{
		Name: "desktop_capture", Description: "Capture the current desktop using an available local screenshot adapter. Returns native MCP image content plus the saved local path.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"output_path": registry.String("optional PNG output path; defaults under the runtime state directory"),
		}, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) { return desktopCapture(ctx, raw, stateDir) },
	}))
	must(r.Add(registry.Tool{
		Name: "desktop_input", Description: "Send generic desktop input using the best available adapter. Supports text, key, click and mouse_move actions.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"text", "key", "click", "mouse_move"}},
			"target": map[string]any{"type": "string", "enum": []string{"current", "headless", "auto"}, "description": "desktop target; headless routes to the isolated CyCom AI compositor without sharing physical input"},
			"text":   registry.String("text to type"),
			"key":    registry.String("key or key chord, adapter-specific names accepted"),
			"button": registry.Integer("mouse button; 1=left, 2=middle, 3=right"),
			"x":      registry.Integer("x coordinate"), "y": registry.Integer("y coordinate"),
		}, []string{"action"}), Source: "core",
		Handler: desktopInput,
	}))
}

func desktopCapture(ctx context.Context, raw json.RawMessage, stateDir string) (any, error) {
	var in struct {
		OutputPath string `json:"output_path"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	path := in.OutputPath
	if path == "" {
		dir := filepath.Join(stateDir, "captures")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		path = filepath.Join(dir, fmt.Sprintf("desktop-%d.png", time.Now().UnixMilli()))
	}
	adapter, failures, err := captureDesktopAdaptive(ctx, path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return registry.RichResult{
		Structured: map[string]any{"path": path, "adapter": adapter, "bytes": len(data), "mime_type": "image/png", "fallback_failures": failures},
		Content: []map[string]any{
			{"type": "text", "text": fmt.Sprintf("Desktop capture via %s (%s)", adapter, path)},
			{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": "image/png"},
		},
	}, nil
}

type desktopCommandCandidate struct {
	adapter string
	cmd     string
	args    []string
}

const desktopAdapterAttemptTimeout = 8 * time.Second

func screenshotCandidates(path string) []desktopCommandCandidate {
	var out []desktopCommandCandidate
	add := func(adapter, binary string, args ...string) {
		if p, err := exec.LookPath(binary); err == nil {
			out = append(out, desktopCommandCandidate{adapter: adapter, cmd: p, args: args})
		}
	}
	if runtime.GOOS == "darwin" {
		add("screencapture", "screencapture", "-x", path)
		return out
	}
	// Prefer desktop-integrated/portal-aware tools when installed, then generic
	// Wayland, then X11. Every attempt is bounded and failures fall through.
	add("spectacle", "spectacle", "-b", "-n", "-o", path)
	add("grim", "grim", path)
	add("gnome-screenshot", "gnome-screenshot", "-f", path)
	add("scrot", "scrot", path)
	add("imagemagick-import", "import", "-window", "root", path)
	return out
}

func captureDesktopAdaptive(ctx context.Context, path string) (string, []string, error) {
	candidates := screenshotCandidates(path)
	if len(candidates) == 0 {
		return "", nil, fmt.Errorf("no supported screenshot adapter found")
	}
	var failures []string
	for _, candidate := range candidates {
		_ = os.Remove(path)
		attemptCtx, cancel := context.WithTimeout(ctx, desktopAdapterAttemptTimeout)
		cmd := exec.CommandContext(attemptCtx, candidate.cmd, candidate.args...)
		cmd.Env = desktopEnvironment() // rediscover live session for every attempt
		output, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			if st, statErr := os.Stat(path); statErr == nil && st.Size() > 0 {
				return candidate.adapter, failures, nil
			}
			err = fmt.Errorf("capture produced no image")
		}
		reason := strings.TrimSpace(string(output))
		if errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
			reason = "timed out"
		}
		if reason == "" {
			reason = err.Error()
		}
		failures = append(failures, candidate.adapter+": "+reason)
	}
	return "", failures, fmt.Errorf("all desktop capture adapters failed: %s", strings.Join(failures, "; "))
}

func desktopInput(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Action string `json:"action"`
		Target string `json:"target"`
		Text   string `json:"text"`
		Key    string `json:"key"`
		Button int    `json:"button"`
		X      int    `json:"x"`
		Y      int    `json:"y"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}

	if in.Target == "" {
		in.Target = "current"
	}
	if in.Target == "headless" {
		return desktopInputHeadless(ctx, in.Action, in.Text, in.Key, in.Button, in.X, in.Y)
	}
	if in.Target != "current" && in.Target != "auto" {
		return nil, fmt.Errorf("unsupported desktop target %q", in.Target)
	}

	type candidate struct {
		adapter string
		cmd     string
		args    []string
	}
	var candidates []candidate
	add := func(adapter, binary string, args ...string) {
		if p, err := exec.LookPath(binary); err == nil {
			candidates = append(candidates, candidate{adapter: adapter, cmd: p, args: args})
		}
	}

	switch in.Action {
	case "text":
		if in.Text == "" {
			return nil, fmt.Errorf("text is required")
		}
		// wtype is ideal when the compositor supports virtual-keyboard;
		// ydotool is the compositor-independent uinput fallback.
		if runtime.GOOS == "darwin" {
			add("osascript", "osascript", "-e", "on run argv", "-e", "tell application \"System Events\" to keystroke (item 1 of argv)", "-e", "end run", in.Text)
		}
		add("wtype", "wtype", in.Text)
		add("ydotool", "ydotool", "type", "--", in.Text)
		add("xdotool", "xdotool", "type", "--clearmodifiers", "--", in.Text)
	case "key":
		if in.Key == "" {
			return nil, fmt.Errorf("key is required")
		}
		if runtime.GOOS == "darwin" {
			if script, ok := macOSKeyAppleScript(in.Key); ok {
				add("osascript", "osascript", "-e", script)
			}
		}
		add("wtype", "wtype", "-k", in.Key)
		add("xdotool", "xdotool", "key", "--clearmodifiers", in.Key)
		// Raw ydotool key events (for example "28:1 28:0") are accepted
		// as the final fallback for callers that explicitly use them.
		add("ydotool", "ydotool", append([]string{"key"}, strings.Fields(in.Key)...)...)
	case "click":
		button := in.Button
		if button == 0 {
			button = 1
		}
		if runtime.GOOS == "darwin" {
			target := "c:."
			if in.X != 0 || in.Y != 0 {
				target = fmt.Sprintf("c:%d,%d", in.X, in.Y)
			}
			add("cliclick", "cliclick", target)
		}
		add("xdotool", "xdotool", "click", strconv.Itoa(button))
		code := "0xC0" // left
		if button == 2 {
			code = "0xC2" // middle
		}
		if button == 3 {
			code = "0xC1" // right
		}
		add("ydotool", "ydotool", "click", code)
	case "mouse_move":
		if runtime.GOOS == "darwin" {
			add("cliclick", "cliclick", fmt.Sprintf("m:%d,%d", in.X, in.Y))
		}
		add("xdotool", "xdotool", "mousemove", strconv.Itoa(in.X), strconv.Itoa(in.Y))
		add("ydotool", "ydotool", "mousemove", "--absolute", "-x", strconv.Itoa(in.X), "-y", strconv.Itoa(in.Y))
	default:
		return nil, fmt.Errorf("unsupported action %q", in.Action)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no compatible desktop input adapter found for action %q", in.Action)
	}
	var failures []string
	for _, c := range candidates {
		result, err := runDesktop(ctx, c.adapter, c.cmd, c.args)
		if err == nil {
			return result, nil
		}
		failures = append(failures, err.Error())
	}
	return nil, fmt.Errorf("all desktop input adapters failed for action %q: %s", in.Action, strings.Join(failures, "; "))
}

func desktopInputHeadless(ctx context.Context, action, text, key string, button, x, y int) (any, error) {
	env, display, err := cycomHeadlessEnvironment()
	if err != nil {
		return nil, err
	}
	var cmd string
	var args []string
	switch action {
	case "text":
		if text == "" {
			return nil, fmt.Errorf("text is required")
		}
		cmd, err = exec.LookPath("wlrctl")
		args = []string{"keyboard", "type", text}
	case "key":
		if key == "" {
			return nil, fmt.Errorf("key is required")
		}
		// wtype is a virtual-keyboard Wayland client and stays inside this compositor.
		cmd, err = exec.LookPath("wtype")
		args = []string{"-k", key}
	case "click":
		if button == 0 {
			button = 1
		}
		name := map[int]string{1: "left", 2: "middle", 3: "right"}[button]
		if name == "" {
			return nil, fmt.Errorf("unsupported mouse button %d", button)
		}
		cmd, err = exec.LookPath("wlrctl")
		args = []string{"pointer", "click", name}
	case "mouse_move":
		cmd, err = exec.LookPath("wlrctl")
		args = []string{"pointer", "move", strconv.Itoa(x), strconv.Itoa(y)}
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
	if err != nil {
		return nil, fmt.Errorf("isolated Wayland input adapter unavailable: %w", err)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, desktopAdapterAttemptTimeout)
	defer cancel()
	c := exec.CommandContext(attemptCtx, cmd, args...)
	c.Env = env
	out, runErr := c.CombinedOutput()
	if runErr != nil {
		return nil, fmt.Errorf("isolated-wayland failed: %w: %s", runErr, strings.TrimSpace(string(out)))
	}
	return map[string]any{"ok": true, "adapter": "isolated-wayland", "target": "headless", "wayland_display": display, "shared_physical_input": false}, nil
}

func cycomHeadlessEnvironment() ([]string, string, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	stateDir := os.Getenv("CYCOM_AI_DESKTOP_STATE")
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".local", "state", "cycom-ai-desktop")
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "wayland-display"))
	if err != nil {
		return nil, "", fmt.Errorf("isolated AI desktop unavailable: %w", err)
	}
	display := strings.TrimSpace(string(data))
	if display == "" {
		return nil, "", fmt.Errorf("isolated AI desktop has no Wayland display")
	}
	st, err := os.Stat(filepath.Join(runtimeDir, display))
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return nil, "", fmt.Errorf("isolated AI Wayland socket %s is unavailable", display)
	}
	envMap := map[string]string{}
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i > 0 {
			envMap[e[:i]] = e[i+1:]
		}
	}
	envMap["XDG_RUNTIME_DIR"] = runtimeDir
	envMap["WAYLAND_DISPLAY"] = display
	envMap["XDG_CURRENT_DESKTOP"] = "labwc-ai"
	envMap["XDG_SESSION_DESKTOP"] = "labwc-ai"
	envMap["XDG_SESSION_TYPE"] = "wayland"
	envMap["CYCOM_AI_DESKTOP"] = "1"
	delete(envMap, "DISPLAY")
	return sortedDesktopEnvironment(envMap), display, nil
}

func macOSKeyAppleScript(key string) (string, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(key)), "+")
	if len(parts) == 0 {
		return "", false
	}
	base := strings.TrimSpace(parts[len(parts)-1])
	mods := []string{}
	for _, raw := range parts[:len(parts)-1] {
		switch strings.TrimSpace(raw) {
		case "cmd", "command", "meta", "super":
			mods = append(mods, "command down")
		case "ctrl", "control":
			mods = append(mods, "control down")
		case "alt", "option":
			mods = append(mods, "option down")
		case "shift":
			mods = append(mods, "shift down")
		default:
			return "", false
		}
	}
	codes := map[string]int{
		"enter": 36, "return": 36, "tab": 48, "space": 49, "backspace": 51,
		"delete": 51, "escape": 53, "esc": 53, "left": 123, "right": 124,
		"down": 125, "up": 126, "home": 115, "end": 119, "pageup": 116, "pagedown": 121,
	}
	using := ""
	if len(mods) > 0 {
		using = " using {" + strings.Join(mods, ", ") + "}"
	}
	if code, ok := codes[base]; ok {
		return fmt.Sprintf("tell application \\\"System Events\\\" to key code %d%s", code, using), true
	}
	if len([]rune(base)) == 1 {
		escaped := strings.ReplaceAll(strings.ReplaceAll(base, "\\\\", "\\\\\\\\"), "\\\"", "\\\\\\\"")
		return fmt.Sprintf("tell application \\\"System Events\\\" to keystroke \\\"%s\\\"%s", escaped, using), true
	}
	return "", false
}

func runDesktop(ctx context.Context, adapter, cmd string, args []string) (any, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, desktopAdapterAttemptTimeout)
	defer cancel()
	c := exec.CommandContext(attemptCtx, cmd, args...)
	c.Env = desktopEnvironment()
	b, err := c.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", adapter, err, strings.TrimSpace(string(b)))
	}
	return map[string]any{"ok": true, "adapter": adapter}, nil
}

// desktopEnvironment merges the service environment with a live graphical
// session owned by the same UID. This lets a system service started before
// login discover Wayland/X11/DBus variables later without being restarted.
func desktopEnvironment() []string {
	if runtime.GOOS == "darwin" {
		return os.Environ()
	}
	envMap := map[string]string{}
	for _, entry := range os.Environ() {
		if i := strings.IndexByte(entry, '='); i > 0 {
			envMap[entry[:i]] = entry[i+1:]
		}
	}
	wanted := map[string]bool{
		"DISPLAY": true, "WAYLAND_DISPLAY": true, "XDG_RUNTIME_DIR": true,
		"DBUS_SESSION_BUS_ADDRESS": true, "XAUTHORITY": true,
		"XDG_CURRENT_DESKTOP": true, "XDG_SESSION_TYPE": true,
		"XDG_SESSION_DESKTOP": true, "DESKTOP_SESSION": true, "KDE_FULL_SESSION": true,
	}

	// Do not key session discovery to a compositor allow-list. Desktop stacks
	// change frequently and custom compositors are common. Instead inspect every
	// same-UID process and score environments by evidence that they belong to a
	// live graphical session. This covers wlroots compositors, KDE, GNOME,
	// COSMIC, X11, nested/headless compositors, and future/custom desktops.
	if envMap["WAYLAND_DISPLAY"] == "" && envMap["DISPLAY"] == "" {
		uid := os.Getuid()
		bestScore := -1
		best := map[string]string{}
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil || pid <= 1 {
				continue
			}
			info, err := os.Stat(filepath.Join("/proc", e.Name()))
			if err != nil {
				continue
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != uid {
				continue
			}
			data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
			if err != nil {
				continue
			}
			candidate := map[string]string{}
			for _, item := range strings.Split(string(data), "\x00") {
				if i := strings.IndexByte(item, '='); i > 0 && wanted[item[:i]] {
					candidate[item[:i]] = item[i+1:]
				}
			}
			score := desktopSessionEnvScore(candidate, uid)
			if score > bestScore {
				bestScore, best = score, candidate
			}
		}
		if bestScore > 0 {
			for k, v := range best {
				if v != "" {
					envMap[k] = v
				}
			}
		}
	}

	uid := os.Getuid()
	runtimeDir := fmt.Sprintf("/run/user/%d", uid)
	if envMap["XDG_RUNTIME_DIR"] == "" {
		if st, err := os.Stat(runtimeDir); err == nil && st.IsDir() {
			envMap["XDG_RUNTIME_DIR"] = runtimeDir
		}
	}
	if envMap["DBUS_SESSION_BUS_ADDRESS"] == "" {
		if _, err := os.Stat(filepath.Join(runtimeDir, "bus")); err == nil {
			envMap["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + filepath.Join(runtimeDir, "bus")
		}
	}
	if envMap["WAYLAND_DISPLAY"] == "" {
		if matches, _ := filepath.Glob(filepath.Join(runtimeDir, "wayland-*")); len(matches) > 0 {
			sort.Strings(matches)
			for _, match := range matches {
				if strings.HasSuffix(match, ".lock") {
					continue
				}
				if st, err := os.Stat(match); err == nil && st.Mode()&os.ModeSocket != 0 {
					envMap["WAYLAND_DISPLAY"] = filepath.Base(match)
					if envMap["XDG_SESSION_TYPE"] == "" {
						envMap["XDG_SESSION_TYPE"] = "wayland"
					}
					break
				}
			}
		}
	}
	if envMap["DISPLAY"] == "" {
		if matches, _ := filepath.Glob("/tmp/.X11-unix/X*"); len(matches) > 0 {
			sort.Strings(matches)
			base := filepath.Base(matches[0])
			if strings.HasPrefix(base, "X") {
				envMap["DISPLAY"] = ":" + strings.TrimPrefix(base, "X")
			}
		}
	}
	if envMap["XAUTHORITY"] == "" {
		if matches, _ := filepath.Glob(filepath.Join(runtimeDir, "xauth_*")); len(matches) > 0 {
			sort.Strings(matches)
			envMap["XAUTHORITY"] = matches[0]
		}
	}
	return sortedDesktopEnvironment(envMap)
}

func desktopSessionEnvScore(env map[string]string, uid int) int {
	score := 0
	if wd := env["WAYLAND_DISPLAY"]; wd != "" {
		score += 100
		path := wd
		if !filepath.IsAbs(path) {
			runtimeDir := env["XDG_RUNTIME_DIR"]
			if runtimeDir == "" {
				runtimeDir = fmt.Sprintf("/run/user/%d", uid)
			}
			path = filepath.Join(runtimeDir, wd)
		}
		if st, err := os.Stat(path); err == nil && st.Mode()&os.ModeSocket != 0 {
			score += 100
		}
	}
	if env["DISPLAY"] != "" {
		score += 80
	}
	if env["DBUS_SESSION_BUS_ADDRESS"] != "" {
		score += 20
	}
	if env["XDG_SESSION_TYPE"] != "" {
		score += 10
	}
	if env["XDG_CURRENT_DESKTOP"] != "" || env["XDG_SESSION_DESKTOP"] != "" || env["DESKTOP_SESSION"] != "" {
		score += 10
	}
	return score
}

func sortedDesktopEnvironment(envMap map[string]string) []string {
	out := make([]string, 0, len(envMap))
	for k, v := range envMap {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func processNameExistsForUID(uid int, wanted string) bool {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		info, err := os.Stat(filepath.Join("/proc", entry.Name()))
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != uid {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if err == nil && strings.TrimSpace(string(comm)) == wanted {
			return true
		}
	}
	return false
}
