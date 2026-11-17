package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
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
		Name: "desktop_capture", Description: "Capture the selected target. agent_workspace captures the isolated Agent Workspace X11 display; current captures the physical desktop; auto prefers a running Agent Workspace. Returns native MCP image content and the saved local path.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"output_path": registry.String("optional PNG output path; defaults under the runtime state directory"),
			"target":      map[string]any{"type": "string", "enum": []string{"auto", "agent_workspace", "current"}, "description": "auto prefers a running Agent Workspace and otherwise uses current; agent_workspace captures the isolated X11 desktop; current captures the physical desktop"},
		}, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true},
		Handler: func(ctx context.Context, raw json.RawMessage) (any, error) { return desktopCapture(ctx, raw, stateDir) },
	}))
	must(r.Add(registry.Tool{
		Name: "desktop_input", Description: "Send input to the selected target. agent_workspace uses the isolated Agent Workspace X11 display and xdotool; current uses the active physical desktop adapters; auto prefers a running Agent Workspace and otherwise uses current.",
		InputSchema: registry.ObjectSchema(map[string]any{
			"action":  map[string]any{"type": "string", "enum": []string{"text", "key", "click", "mouse_move", "scroll"}},
			"target":  map[string]any{"type": "string", "enum": []string{"auto", "agent_workspace", "current"}, "description": "auto prefers a running Agent Workspace and otherwise uses current; agent_workspace targets the isolated X11 desktop; current targets the physical desktop"},
			"text":    registry.String("text to type"),
			"key":     registry.String("key or key chord, adapter-specific names accepted"),
			"button":  registry.Integer("mouse button; 1=left, 2=middle, 3=right"),
			"x":       registry.Integer("absolute x coordinate"),
			"y":       registry.Integer("absolute y coordinate"),
			"delta_x": registry.Integer("horizontal scroll delta"),
			"delta_y": registry.Integer("vertical scroll delta"),
		}, []string{"action"}), Source: "core",
		Handler: desktopInput,
	}))
}

func desktopCapture(ctx context.Context, raw json.RawMessage, stateDir string) (any, error) {
	var in struct {
		OutputPath string `json:"output_path"`
		Target     string `json:"target"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Target == "" {
		in.Target = "auto"
	}
	if in.Target != "current" && in.Target != "auto" && in.Target != "agent_workspace" {
		return nil, fmt.Errorf("unsupported capture target %q; supported targets are auto, agent_workspace, and current", in.Target)
	}
	if in.Target == "auto" {
		if _, _, err := agentWorkspaceEnvironment(); err == nil {
			in.Target = "agent_workspace"
		} else {
			in.Target = "current"
		}
	}
	path := in.OutputPath
	if path == "" {
		dir := filepath.Join(stateDir, "captures")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		path = filepath.Join(dir, fmt.Sprintf("desktop-%d.png", time.Now().UnixMilli()))
	}
	var adapter, display string
	var failures []string
	var err error
	if in.Target == "agent_workspace" {
		adapter, display, failures, err = captureAgentWorkspace(ctx, path)
	} else {
		adapter, failures, err = captureDesktopAdaptive(ctx, path)
	}
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return registry.RichResult{
		Structured: map[string]any{"path": path, "adapter": adapter, "target": in.Target, "display": display, "bytes": len(data), "mime_type": "image/png", "fallback_failures": failures},
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

// desktopInputCandidate mirrors desktopCommandCandidate for input injection so
// the candidate list can be handed to a platform-specific mapper.
type desktopInputCandidate struct {
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
	if runtime.GOOS == "android" {
		// Android exposes the framebuffer through the platform `screencap`
		// binary. GOOS=android satisfies the linux build tag, so the desktop
		// adapters below are compiled in but can never succeed on a phone.
		add("android-screencap", "screencap", "-p", path)
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
	return captureDesktopAdaptiveWithEnv(ctx, path, desktopEnvironment())
}

func isLabwcDesktopEnvironment(env []string) bool {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key != "XDG_CURRENT_DESKTOP" {
			continue
		}
		for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return r == ':' || r == ';' || r == ',' }) {
			if strings.TrimSpace(token) == "labwc" {
				return true
			}
		}
	}
	return false
}

func filterScreenshotCandidates(candidates []desktopCommandCandidate, adapters ...string) []desktopCommandCandidate {
	allowed := make(map[string]struct{}, len(adapters))
	for _, adapter := range adapters {
		allowed[adapter] = struct{}{}
	}
	filtered := make([]desktopCommandCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := allowed[candidate.adapter]; ok {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func captureDesktopAdaptiveWithEnv(ctx context.Context, path string, env []string) (string, []string, error) {
	candidates := screenshotCandidates(path)
	if isLabwcDesktopEnvironment(env) {
		candidates = filterScreenshotCandidates(candidates, "grim")
		if len(candidates) == 0 {
			return "", nil, fmt.Errorf("Labwc screenshot adapter unavailable: grim is required")
		}
	}
	return captureDesktopCandidates(ctx, path, env, candidates)
}

func captureAgentWorkspace(ctx context.Context, path string) (string, string, []string, error) {
	env, display, err := agentWorkspaceEnvironment()
	if err != nil {
		return "", "", nil, err
	}
	var candidates []desktopCommandCandidate
	if p, e := exec.LookPath("import"); e == nil {
		candidates = append(candidates, desktopCommandCandidate{adapter: "agent-workspace-x11-import", cmd: p, args: []string{"-display", display, "-window", "root", path}})
	}
	if p, e := exec.LookPath("ffmpeg"); e == nil {
		candidates = append(candidates, desktopCommandCandidate{adapter: "agent-workspace-x11-ffmpeg", cmd: p, args: []string{"-hide_banner", "-loglevel", "error", "-f", "x11grab", "-i", display, "-frames:v", "1", "-y", path}})
	}
	if p, e := exec.LookPath("scrot"); e == nil {
		candidates = append(candidates, desktopCommandCandidate{adapter: "agent-workspace-x11-scrot", cmd: p, args: []string{path}})
	}
	adapter, failures, err := captureDesktopCandidates(ctx, path, env, candidates)
	return adapter, display, failures, err
}

func captureDesktopCandidates(ctx context.Context, path string, env []string, candidates []desktopCommandCandidate) (string, []string, error) {
	if len(candidates) == 0 {
		return "", nil, fmt.Errorf("no supported screenshot adapter found")
	}
	var failures []string
	for _, candidate := range candidates {
		_ = os.Remove(path)
		attemptCtx, cancel := context.WithTimeout(ctx, desktopAdapterAttemptTimeout)
		cmd := exec.CommandContext(attemptCtx, candidate.cmd, candidate.args...)
		cmd.Env = env
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
		DeltaX int    `json:"delta_x"`
		DeltaY int    `json:"delta_y"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}

	if in.Target == "" {
		in.Target = "auto"
	}
	if in.Target == "auto" {
		if _, _, err := agentWorkspaceEnvironment(); err == nil {
			in.Target = "agent_workspace"
		} else {
			in.Target = "current"
		}
	}
	if in.Target == "agent_workspace" {
		return desktopInputAgentWorkspace(ctx, in.Action, in.Text, in.Key, in.Button, in.X, in.Y, in.DeltaX, in.DeltaY)
	}
	if in.Target != "current" {
		return nil, fmt.Errorf("unsupported desktop target %q", in.Target)
	}

	// Reaching this path means target=current was selected explicitly or auto
	// fell back because no Agent Workspace is running. Use only the physical
	// login-session environment for this target.
	inputEnv := desktopEnvironment()
	var rawFields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &rawFields)
	_, hasX := rawFields["x"]
	_, hasY := rawFields["y"]
	request := desktopInputRequest{
		Action: in.Action, Text: in.Text, Key: in.Key, Button: in.Button,
		X: in.X, Y: in.Y, DeltaX: in.DeltaX, DeltaY: in.DeltaY,
		HasPosition: hasX && hasY,
	}
	if isLabwcDesktopEnvironment(inputEnv) && runtime.GOOS == "linux" {
		return desktopInputLabwc(ctx, request, inputEnv)
	}

	var candidates []desktopInputCandidate
	add := func(adapter, binary string, args ...string) {
		if p, err := exec.LookPath(binary); err == nil {
			candidates = append(candidates, desktopInputCandidate{adapter: adapter, cmd: p, args: args})
		}
	}
	switch request.Action {
	case "text", "key", "click", "mouse_move", "scroll":
	default:
		return nil, fmt.Errorf("unsupported action %q", in.Action)
	}
	if request.Button == 0 {
		request.Button = 1
	}

	// Android injects input through the platform `input` command rather than
	// through a compositor tool, so it gets its own adapter mapping. The
	// Agent Workspace path above never applies here: there is no active Agent Workspace
	// on a phone.
	if runtime.GOOS == "android" {
		android, err := androidInputCandidates(request)
		if err != nil {
			return nil, err
		}
		candidates = android
	} else {
		addDesktopInputCandidates(request, add)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no compatible desktop input adapter found for action %q", in.Action)
	}
	var failures []string
	for _, c := range candidates {
		result, err := runDesktop(ctx, c.adapter, c.cmd, c.args, inputEnv)
		if err == nil {
			return result, nil
		}
		failures = append(failures, err.Error())
	}
	return nil, fmt.Errorf("all desktop input adapters failed for action %q: %s", in.Action, strings.Join(failures, "; "))
}

// addDesktopInputCandidates appends the compositor-backed input adapters for
// desktop Linux and macOS hosts.
func addDesktopInputCandidates(in desktopInputRequest, add func(adapter, binary string, args ...string)) {
	switch in.Action {
	case "text":
		if in.Text == "" {
			return
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
			return
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
	case "scroll":
		if in.DeltaX == 0 && in.DeltaY == 0 {
			return
		}
		add("ydotool", "ydotool", "mousemove", "--wheel", "--", strconv.Itoa(in.DeltaX), strconv.Itoa(in.DeltaY))
	}
}

type desktopInputRequest struct {
	Action      string
	Text        string
	Key         string
	Button      int
	X           int
	Y           int
	DeltaX      int
	DeltaY      int
	HasPosition bool
}

func desktopInputLabwc(ctx context.Context, in desktopInputRequest, env []string) (any, error) {
	run := func(adapter, binary string, args ...string) (any, error) {
		cmd, err := isolatedInputBinary(binary)
		if err != nil {
			return nil, fmt.Errorf("Labwc %s adapter unavailable: %w", adapter, err)
		}
		return runDesktop(ctx, adapter, cmd, args, env)
	}
	pointer := func(command string) (any, error) {
		return sendLabwcPointerCommand(ctx, env, command)
	}
	moveAbsolute := func(x, y int) (any, error) {
		width, height, err := labwcDesktopExtent(ctx, env)
		if err != nil {
			return nil, err
		}
		return pointer(fmt.Sprintf("move_abs %d %d %d %d", x, y, width, height))
	}

	switch in.Action {
	case "text":
		if in.Text == "" {
			return nil, fmt.Errorf("text is required")
		}
		return run("labwc-wlrctl", "wlrctl", "keyboard", "type", in.Text)
	case "key":
		if in.Key == "" {
			return nil, fmt.Errorf("key is required")
		}
		args, err := labwcWtypeKeyArgs(in.Key)
		if err != nil {
			return nil, err
		}
		return run("labwc-wtype", "wtype", args...)
	case "click":
		button := in.Button
		if button == 0 {
			button = 1
		}
		if button < 1 || button > 3 {
			return nil, fmt.Errorf("unsupported mouse button %d", button)
		}
		if in.HasPosition {
			if _, err := moveAbsolute(in.X, in.Y); err != nil {
				return nil, err
			}
		}
		return pointer(fmt.Sprintf("click %d", button))
	case "mouse_move":
		return moveAbsolute(in.X, in.Y)
	case "scroll":
		if in.DeltaX == 0 && in.DeltaY == 0 {
			return nil, fmt.Errorf("scroll requires delta_x or delta_y")
		}
		return pointer(fmt.Sprintf("scroll %d %d", in.DeltaY, in.DeltaX))
	default:
		return nil, fmt.Errorf("unsupported action %q", in.Action)
	}
}

func desktopEnvValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func labwcPointerSocketPath(env []string) (string, error) {
	if explicit := strings.TrimSpace(desktopEnvValue(env, "CYCOM_LABWC_POINTER_SOCKET")); explicit != "" {
		return explicit, nil
	}
	runtimeDir := strings.TrimSpace(desktopEnvValue(env, "XDG_RUNTIME_DIR"))
	if runtimeDir == "" {
		runtimeDir = strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	}
	if runtimeDir == "" {
		return "", errors.New("XDG_RUNTIME_DIR is unavailable for the Labwc pointer socket")
	}
	return filepath.Join(runtimeDir, "cyshell-pointer.sock"), nil
}

func sendLabwcPointerCommand(ctx context.Context, env []string, command string) (any, error) {
	path, err := labwcPointerSocketPath(env)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("Labwc virtual-pointer service unavailable at %s: %w", path, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte(command + "\n")); err != nil {
		return nil, fmt.Errorf("Labwc virtual-pointer command write failed: %w", err)
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("Labwc virtual-pointer response failed: %w", err)
	}
	response := strings.TrimSpace(string(buf[:n]))
	if response != "OK" {
		if response == "" {
			response = "empty response"
		}
		return nil, fmt.Errorf("Labwc virtual-pointer command failed: %s", response)
	}
	verb := strings.Fields(command)
	action := ""
	if len(verb) > 0 {
		action = verb[0]
	}
	return map[string]any{"ok": true, "adapter": "labwc-wlr-pointer", "action": action}, nil
}

type wlrRandrOutput struct {
	Enabled bool `json:"enabled"`
	Modes   []struct {
		Width   int  `json:"width"`
		Height  int  `json:"height"`
		Current bool `json:"current"`
	} `json:"modes"`
	Position struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"position"`
	Transform string  `json:"transform"`
	Scale     float64 `json:"scale"`
}

func labwcDesktopExtent(ctx context.Context, env []string) (int, int, error) {
	binary, err := exec.LookPath("wlr-randr")
	if err != nil {
		return 0, 0, fmt.Errorf("Labwc output geometry adapter unavailable: %w", err)
	}
	cmd := exec.CommandContext(ctx, binary, "--json")
	cmd.Env = env
	data, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("Labwc output geometry query failed: %w", err)
	}
	return labwcDesktopExtentFromJSON(data)
}

func labwcDesktopExtentFromJSON(data []byte) (int, int, error) {
	var outputs []wlrRandrOutput
	if err := json.Unmarshal(data, &outputs); err != nil {
		return 0, 0, fmt.Errorf("decode wlr-randr output: %w", err)
	}
	first := true
	minX, minY, maxX, maxY := 0, 0, 0, 0
	for _, output := range outputs {
		if !output.Enabled {
			continue
		}
		modeWidth, modeHeight := 0, 0
		for _, mode := range output.Modes {
			if mode.Current {
				modeWidth, modeHeight = mode.Width, mode.Height
				break
			}
		}
		if modeWidth <= 0 || modeHeight <= 0 {
			continue
		}
		transform := strings.ToLower(output.Transform)
		if strings.Contains(transform, "90") || strings.Contains(transform, "270") {
			modeWidth, modeHeight = modeHeight, modeWidth
		}
		scale := output.Scale
		if scale <= 0 {
			scale = 1
		}
		width := int(float64(modeWidth)/scale + 0.5)
		height := int(float64(modeHeight)/scale + 0.5)
		left, top := output.Position.X, output.Position.Y
		right, bottom := left+width, top+height
		if first {
			minX, minY, maxX, maxY = left, top, right, bottom
			first = false
			continue
		}
		if left < minX {
			minX = left
		}
		if top < minY {
			minY = top
		}
		if right > maxX {
			maxX = right
		}
		if bottom > maxY {
			maxY = bottom
		}
	}
	if first || maxX <= minX || maxY <= minY {
		return 0, 0, errors.New("Labwc reported no enabled outputs with a current mode")
	}
	return maxX - minX, maxY - minY, nil
}

// androidKeyEventCodes maps the generic key names accepted by desktop_input
// onto Android KEYCODE_* names understood by the `input` command.
var androidKeyEventCodes = map[string]string{
	"enter": "KEYCODE_ENTER", "return": "KEYCODE_ENTER", "tab": "KEYCODE_TAB",
	"space": "KEYCODE_SPACE", "backspace": "KEYCODE_DEL", "delete": "KEYCODE_FORWARD_DEL",
	"escape": "KEYCODE_ESCAPE", "esc": "KEYCODE_ESCAPE",
	"left": "KEYCODE_DPAD_LEFT", "right": "KEYCODE_DPAD_RIGHT",
	"down": "KEYCODE_DPAD_DOWN", "up": "KEYCODE_DPAD_UP",
	"home": "KEYCODE_MOVE_HOME", "end": "KEYCODE_MOVE_END",
	"pageup": "KEYCODE_PAGE_UP", "pagedown": "KEYCODE_PAGE_DOWN",
	"back": "KEYCODE_BACK", "search": "KEYCODE_SEARCH", "menu": "KEYCODE_MENU",
	"power": "KEYCODE_POWER", "volumeup": "KEYCODE_VOLUME_UP", "volumedown": "KEYCODE_VOLUME_DOWN",
	"mute": "KEYCODE_VOLUME_MUTE", "camera": "KEYCODE_CAMERA",
}

// androidInput maps a desktop_input request onto the Android `input` command.
// Text is escaped because `input text` treats spaces as argument separators and
// treats % as an escape introducer.
func androidInputCandidates(in desktopInputRequest) ([]desktopInputCandidate, error) {
	var args []string
	switch in.Action {
	case "text":
		if in.Text == "" {
			return nil, fmt.Errorf("text is required")
		}
		args = []string{"text", escapeAndroidInputText(in.Text)}
	case "key":
		if in.Key == "" {
			return nil, fmt.Errorf("key is required")
		}
		args = []string{"keyevent", androidKeyEvent(in.Key)}
	case "click":
		if in.Button != 1 {
			return nil, fmt.Errorf("android input supports primary click only")
		}
		args = []string{"tap", strconv.Itoa(in.X), strconv.Itoa(in.Y)}
	case "mouse_move":
		// Android has no hover pointer. Reporting that honestly is better than
		// pretending a synthetic swipe moved a cursor.
		return nil, fmt.Errorf("android input does not support mouse_move")
	default:
		return nil, fmt.Errorf("unsupported action %q", in.Action)
	}
	bin, err := exec.LookPath("input")
	if err != nil {
		return nil, fmt.Errorf("android input adapter unavailable: %w", err)
	}
	return []desktopInputCandidate{{adapter: "android-input", cmd: bin, args: args}}, nil
}

func escapeAndroidInputText(text string) string {
	replacer := strings.NewReplacer("%", "%%", " ", "%s")
	return replacer.Replace(text)
}

// androidKeyEvent translates a generic key name into an Android KEYCODE name.
// Names that already look like KEYCODE_* or are numeric pass through untouched
// so callers can always be explicit.
func androidKeyEvent(key string) string {
	trimmed := strings.TrimSpace(key)
	if strings.HasPrefix(strings.ToUpper(trimmed), "KEYCODE_") {
		return strings.ToUpper(trimmed)
	}
	if _, err := strconv.Atoi(trimmed); err == nil {
		return trimmed
	}
	name := strings.ToLower(trimmed)
	if code, ok := androidKeyEventCodes[name]; ok {
		return code
	}
	return "KEYCODE_" + strings.ToUpper(strings.NewReplacer("+", "_", "-", "_", " ", "_").Replace(name))
}

func desktopInputAgentWorkspace(ctx context.Context, action, text, key string, button, x, y, deltaX, deltaY int) (any, error) {
	env, display, err := agentWorkspaceEnvironment()
	if err != nil {
		return nil, err
	}
	bin, err := isolatedInputBinary("xdotool")
	if err != nil {
		return nil, fmt.Errorf("Agent Workspace X11 input adapter unavailable: %w", err)
	}
	var args []string
	switch action {
	case "text":
		if text == "" {
			return nil, fmt.Errorf("text is required")
		}
		args = []string{"type", "--clearmodifiers", "--delay", "0", "--", text}
	case "key":
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("key is required")
		}
		args = []string{"key", "--clearmodifiers", key}
	case "click":
		if button == 0 {
			button = 1
		}
		if button < 1 || button > 3 {
			return nil, fmt.Errorf("unsupported mouse button %d", button)
		}
		args = []string{"mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y), "click", strconv.Itoa(button)}
	case "mouse_move":
		args = []string{"mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y)}
	case "scroll":
		steps := int(math.Abs(float64(deltaY)) / 120)
		if steps == 0 && deltaY != 0 {
			steps = 1
		}
		buttonCode := 5
		if deltaY < 0 {
			buttonCode = 4
		}
		if deltaX != 0 {
			stepsX := int(math.Abs(float64(deltaX)) / 120)
			if stepsX == 0 {
				stepsX = 1
			}
			hbutton := 7
			if deltaX < 0 {
				hbutton = 6
			}
			for i := 0; i < stepsX; i++ {
				args = append(args, "click", strconv.Itoa(hbutton))
			}
		}
		for i := 0; i < steps; i++ {
			args = append(args, "click", strconv.Itoa(buttonCode))
		}
		if len(args) == 0 {
			return nil, fmt.Errorf("scroll requires delta_x or delta_y")
		}
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, desktopAdapterAttemptTimeout)
	defer cancel()
	cmd := exec.CommandContext(attemptCtx, bin, args...)
	cmd.Env = env
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return nil, fmt.Errorf("Agent Workspace X11 input failed: %w: %s", runErr, strings.TrimSpace(string(out)))
	}
	return map[string]any{"ok": true, "adapter": "agent-workspace-x11-xdotool", "target": "agent_workspace", "display": display, "shared_physical_input": false}, nil
}

func isolatedInputBinary(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, ".local", "bin", name)
		if st, statErr := os.Stat(p); statErr == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH or ~/.local/bin", name)
}

func agentWorkspaceEnvironment() ([]string, string, error) {
	bin, err := agentWorkspaceBinary()
	if err != nil {
		return nil, "", fmt.Errorf("Agent Workspace backend unavailable: %w", err)
	}
	baseEnv := desktopEnvironment()
	run := func(args ...string) ([]byte, error) {
		c := exec.Command(bin, args...)
		c.Env = baseEnv
		return c.Output()
	}
	listBytes, err := run("workspace", "list")
	if err != nil {
		return nil, "", fmt.Errorf("Agent Workspace workspace list failed: %w", err)
	}
	var listing struct {
		Workspaces []struct {
			ID      string `json:"id"`
			Running bool   `json:"running"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(listBytes, &listing); err != nil {
		return nil, "", fmt.Errorf("decode Agent Workspace workspace list: %w", err)
	}
	id := strings.TrimSpace(os.Getenv("AGENT_WORKSPACE_ID"))
	if id == "" {
		id = "default"
	}
	running := false
	for _, w := range listing.Workspaces {
		if w.ID == id && w.Running {
			running = true
			break
		}
	}
	if !running {
		return nil, "", fmt.Errorf("Agent Workspace %q is not running; start it from the Agent Workspaces feature after acknowledging the hidden workspace", id)
	}
	envBytes, err := run("workspace", "env", "--id", id)
	if err != nil {
		return nil, "", fmt.Errorf("read Agent Workspace %q environment: %w", id, err)
	}
	var response struct {
		Environment struct {
			Display    string `json:"display"`
			XAuthority string `json:"xauthority_path"`
			RuntimeDir string `json:"runtime_dir"`
			SocketPath string `json:"socket_path"`
			Variables  []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"variables"`
		} `json:"environment"`
	}
	if err := json.Unmarshal(envBytes, &response); err != nil {
		return nil, "", fmt.Errorf("decode Agent Workspace environment: %w", err)
	}
	workspaceEnv := response.Environment
	if workspaceEnv.Display == "" || workspaceEnv.XAuthority == "" {
		return nil, "", fmt.Errorf("Agent Workspace %q returned incomplete DISPLAY/XAUTHORITY", id)
	}
	if st, err := os.Stat(workspaceEnv.XAuthority); err != nil || !st.Mode().IsRegular() {
		return nil, "", fmt.Errorf("Agent Workspace XAUTHORITY file is unavailable: %s", workspaceEnv.XAuthority)
	}
	if workspaceEnv.SocketPath != "" {
		if st, err := os.Stat(workspaceEnv.SocketPath); err != nil || st.Mode()&os.ModeSocket == 0 {
			return nil, "", fmt.Errorf("Agent Workspace control socket is unavailable: %s", workspaceEnv.SocketPath)
		}
	}
	envMap := map[string]string{}
	for _, entry := range baseEnv {
		if k, v, ok := strings.Cut(entry, "="); ok {
			envMap[k] = v
		}
	}
	for _, v := range workspaceEnv.Variables {
		envMap[v.Name] = v.Value
	}
	envMap["DISPLAY"] = workspaceEnv.Display
	envMap["XAUTHORITY"] = workspaceEnv.XAuthority
	envMap["AGENT_WORKSPACE_ID"] = id
	envMap["AGENT_WORKSPACE_RUNTIME_DIR"] = workspaceEnv.RuntimeDir
	envMap["AGENT_WORKSPACE_SOCKET"] = workspaceEnv.SocketPath
	envMap["XDG_SESSION_TYPE"] = "x11"
	envMap["XDG_CURRENT_DESKTOP"] = "agent-workspace"
	delete(envMap, "WAYLAND_DISPLAY")
	if xdpyinfo, lookErr := exec.LookPath("xdpyinfo"); lookErr == nil {
		c := exec.Command(xdpyinfo, "-display", workspaceEnv.Display)
		c.Env = sortedDesktopEnvironment(envMap)
		if out, e := c.CombinedOutput(); e != nil {
			return nil, "", fmt.Errorf("Agent Workspace X11 display %s is not ready: %w: %s", workspaceEnv.Display, e, strings.TrimSpace(string(out)))
		}
	} else {
		return nil, "", fmt.Errorf("xdpyinfo is required to verify Agent Workspace X11 display")
	}
	return sortedDesktopEnvironment(envMap), workspaceEnv.Display, nil
}

func agentWorkspaceBinary() (string, error) {
	if p, err := exec.LookPath("agent-workspace-linux"); err == nil {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, ".local", "bin", "agent-workspace-linux")
		if st, e := os.Stat(p); e == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("agent-workspace-linux binary is not installed")
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

func runDesktop(ctx context.Context, adapter, cmd string, args []string, env []string) (any, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, desktopAdapterAttemptTimeout)
	defer cancel()
	c := exec.CommandContext(attemptCtx, cmd, args...)
	c.Env = env
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
