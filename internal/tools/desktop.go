package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	adapter, cmd, args, err := screenshotCommand(path)
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, cmd, args...)
	c.Env = desktopEnvironment()
	if b, err := c.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s capture failed: %w: %s", adapter, err, strings.TrimSpace(string(b)))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return registry.RichResult{
		Structured: map[string]any{"path": path, "adapter": adapter, "bytes": len(data), "mime_type": "image/png"},
		Content: []map[string]any{
			{"type": "text", "text": fmt.Sprintf("Desktop capture via %s (%s)", adapter, path)},
			{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": "image/png"},
		},
	}, nil
}

func screenshotCommand(path string) (string, string, []string, error) {
	if runtime.GOOS == "darwin" {
		if p, err := exec.LookPath("screencapture"); err == nil {
			return "screencapture", p, []string{"-x", path}, nil
		}
	}
	if p, err := exec.LookPath("spectacle"); err == nil {
		return "spectacle", p, []string{"-b", "-n", "-o", path}, nil
	}
	if p, err := exec.LookPath("grim"); err == nil {
		return "grim", p, []string{path}, nil
	}
	if p, err := exec.LookPath("gnome-screenshot"); err == nil {
		return "gnome-screenshot", p, []string{"-f", path}, nil
	}
	if p, err := exec.LookPath("scrot"); err == nil {
		return "scrot", p, []string{path}, nil
	}
	if p, err := exec.LookPath("import"); err == nil {
		return "imagemagick-import", p, []string{"-window", "root", path}, nil
	}
	return "", "", nil, fmt.Errorf("no supported screenshot adapter found (screencapture, spectacle, grim, gnome-screenshot, scrot, import)")
}

func desktopInput(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Action string `json:"action"`
		Text   string `json:"text"`
		Key    string `json:"key"`
		Button int    `json:"button"`
		X      int    `json:"x"`
		Y      int    `json:"y"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
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
	c := exec.CommandContext(ctx, cmd, args...)
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
		// LaunchAgents already inherit the logged-in user's GUI bootstrap domain.
		// TCC (Screen Recording / Accessibility / Automation) remains authoritative.
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
		"XDG_CURRENT_DESKTOP": true, "KDE_FULL_SESSION": true,
	}
	// If a useful desktop environment is already inherited, keep it.
	if envMap["WAYLAND_DISPLAY"] == "" && envMap["DISPLAY"] == "" {
		uid := os.Getuid()
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
			commBytes, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
			comm := strings.TrimSpace(string(commBytes))
			switch comm {
			case "plasmashell", "kwin_wayland", "gnome-shell", "Xorg":
			default:
				continue
			}
			data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
			if err != nil {
				continue
			}
			for _, item := range strings.Split(string(data), "\x00") {
				if i := strings.IndexByte(item, '='); i > 0 && wanted[item[:i]] {
					envMap[item[:i]] = item[i+1:]
				}
			}
			if envMap["WAYLAND_DISPLAY"] != "" || envMap["DISPLAY"] != "" {
				break
			}
		}
	}
	out := make([]string, 0, len(envMap))
	for k, v := range envMap {
		out = append(out, k+"="+v)
	}
	return out
}
