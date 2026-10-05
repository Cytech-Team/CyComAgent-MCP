package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

const defaultExecTimeout = 120 * time.Second
const maxProcessOutput = 10 << 20

type processDeps struct {
	Jobs    *jobs.Manager
	Broker  broker.Client
	Session sessionbridge.Client
}

func registerProcess(r *registry.Registry, deps processDeps) {
	executionContext := map[string]any{
		"type":        "string",
		"enum":        []string{"auto", "service", "user", "desktop", "system"},
		"description": "execution domain; auto routes recognized desktop/session commands through the active bridge with the Agent Workspace environment by default, service stays in the CyComAgent service, explicit user/desktop retain login-session bridge behavior, and system requires privileged=true/root broker",
	}
	desktopTarget := map[string]any{
		"type":        "string",
		"enum":        []string{"auto", "agent_workspace", "current"},
		"description": "desktop target for desktop side effects; auto uses Agent Workspace for automatic desktop commands and input commands, failing closed if unavailable; agent_workspace forces it, current explicitly selects the physical desktop; Agent Workspace blocks the physical session bus, and CYCOM_AGENT_WORKSPACE_AUTO=0 opts out of isolation",
	}
	props := map[string]any{
		"command":           registry.String("shell command to execute"),
		"stdin":             registry.String("optional UTF-8 stdin for the command"),
		"cwd":               registry.String("working directory"),
		"env":               map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"timeout_seconds":   registry.Integer("timeout; default 120 seconds"),
		"shell":             registry.String("shell executable; defaults to the platform shell"),
		"max_output_bytes":  registry.Integer("cap for stdout and stderr"),
		"privileged":        registry.Boolean("execute through the optional root broker"),
		"execution_context": executionContext,
		"desktop_target":    desktopTarget,
	}
	must(r.Add(registry.Tool{Name: "process_exec", Description: "Execute a shell command synchronously. With execution_context=auto, recognized desktop/session commands run through the isolated Agent Workspace and fail if it is unavailable. D-Bus-dependent actions fail there instead of reaching the physical login bus. Explicit desktop/user keeps active-login-session routing; service stays in the agent service except recognized pointer/keyboard input commands, which remain workspace-isolated by default. Set desktop_target=current only when physical desktop access is intended. Use execution_context=desktop for GUI apps, Polkit-authorized desktop actions, notifications, keyrings, portals, clipboard/compositor commands, or anything that must belong to the active graphical login session.", InputSchema: registry.ObjectSchema(props, []string{"command"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		return processExec(ctx, raw, deps.Broker, deps.Session)
	}}))
	must(r.Add(registry.Tool{Name: "process_spawn", Description: "Start a persistent background job. Use execution_context=desktop for GUI/session applications so they are born in the active graphical login session rather than the CyComAgent service cgroup.", InputSchema: registry.ObjectSchema(map[string]any{
		"command": registry.String("shell command to start"), "cwd": registry.String("working directory"),
		"env":               map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"shell":             registry.String("shell executable; defaults to the platform shell"),
		"execution_context": executionContext,
		"desktop_target":    desktopTarget,
	}, []string{"command"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in processSpawnInput
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return processSpawn(ctx, in, deps)
	}}))
	must(r.Add(registry.Tool{Name: "process_inspect", Description: "Inspect an OS process by PID.", InputSchema: registry.ObjectSchema(map[string]any{
		"pid": registry.Integer("process id"),
	}, []string{"pid"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: processInspect}))
	must(r.Add(registry.Tool{Name: "process_signal", Description: "Send TERM, INT, HUP, KILL, USR1 or USR2 to a process.", InputSchema: registry.ObjectSchema(map[string]any{
		"pid": registry.Integer("process id"), "signal": registry.String("signal name; default TERM"),
	}, []string{"pid"}), Source: "core", Handler: processSignal}))
}

type processExecInput struct {
	Command          string            `json:"command"`
	Stdin            string            `json:"stdin"`
	Cwd              string            `json:"cwd"`
	Env              map[string]string `json:"env"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
	Shell            string            `json:"shell"`
	MaxOutputBytes   int               `json:"max_output_bytes"`
	Privileged       bool              `json:"privileged"`
	ExecutionContext string            `json:"execution_context"`
	DesktopTarget    string            `json:"desktop_target"`
}
type processSpawnInput struct {
	Command          string            `json:"command"`
	Cwd              string            `json:"cwd"`
	Env              map[string]string `json:"env"`
	Shell            string            `json:"shell"`
	ExecutionContext string            `json:"execution_context"`
	DesktopTarget    string            `json:"desktop_target"`
}
type processPIDInput struct {
	PID int `json:"pid"`
}
type processSignalInput struct {
	PID    int    `json:"pid"`
	Signal string `json:"signal"`
}

type processDesktopRoute struct {
	Target         string
	Env            map[string]string
	WaylandDisplay string
}

func prepareProcessDesktopRoute(requested, command, executionContext string, extra map[string]string) (processDesktopRoute, error) {
	target, err := normalizeDesktopTarget(requested)
	if err != nil {
		return processDesktopRoute{}, fmt.Errorf("unsupported desktop_target %q", requested)
	}
	if target == "auto" {
		if !processCommandNeedsWorkspaceRoute(executionContext, command) {
			return processDesktopRoute{}, nil
		}
		target, _, _, err = automaticDesktopTarget()
		if err != nil {
			return processDesktopRoute{}, err
		}
	}
	if target == "current" {
		return processDesktopRoute{Target: "current", Env: extra}, nil
	}
	if unsafeGlobalHeadlessInputCommand(command) {
		return processDesktopRoute{}, fmt.Errorf("refusing global input injector on desktop_target=agent_workspace; use compositor-scoped wlrctl/wtype/whydotool, or explicitly set desktop_target=current to control the physical desktop")
	}
	env, display, err := headlessProcessEnv(extra)
	if err != nil {
		return processDesktopRoute{}, fmt.Errorf("Agent Workspace is unavailable: %w; use desktop_target=current only when physical desktop control is intended", err)
	}
	return processDesktopRoute{Target: "agent_workspace", Env: env, WaylandDisplay: display}, nil
}

func processCommandNeedsWorkspaceRoute(executionContext, command string) bool {
	switch executionContext {
	case "", "auto":
		return looksLikeDesktopCommand(command)
	case "service":
		// Preserve the existing safety rule for explicit service commands that
		// inject pointer or keyboard input. Other service commands remain in the
		// service context even if they have desktop-related names.
		return looksLikeDesktopInputCommand(command)
	default:
		// Explicit desktop/user contexts retain their active-login-session
		// bridge semantics. Unknown contexts are rejected by the context router.
		return false
	}
}

func headlessProcessEnv(extra map[string]string) (map[string]string, string, error) {
	headlessEnv, display, err := cycomHeadlessEnvironment()
	if err != nil {
		return nil, "", err
	}
	out := map[string]string{}
	for key, value := range extra {
		out[key] = value
	}
	allowed := map[string]bool{
		"XDG_RUNTIME_DIR": true, "WAYLAND_DISPLAY": true, "XDG_CURRENT_DESKTOP": true,
		"XDG_SESSION_DESKTOP": true, "XDG_SESSION_TYPE": true, "CYCOM_AI_DESKTOP": true,
		"DBUS_SESSION_BUS_ADDRESS": true,
	}
	for _, entry := range headlessEnv {
		key, value, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			out[key] = value
		}
	}
	out["DISPLAY"] = ""
	// Do not preserve the physical session bus address carried by either the
	// service environment or a caller-provided env map.
	out["DBUS_SESSION_BUS_ADDRESS"] = isolatedAgentWorkspaceBusAddress
	out["CYCOM_ANYAPP_ISOLATED"] = "1"
	return out, display, nil
}

func normalizedInputCommand(command string) string {
	return strings.Join(strings.Fields(strings.ToLower(command)), " ")
}

func containsInputSubcommand(command, program string, subcommands ...string) bool {
	value := normalizedInputCommand(command)
	for _, subcommand := range subcommands {
		needle := program + " " + subcommand
		searchFrom := 0
		for searchFrom < len(value) {
			relative := strings.Index(value[searchFrom:], needle)
			if relative < 0 {
				break
			}
			index := searchFrom + relative
			if index == 0 || !commandNameByte(value[index-1]) {
				return true
			}
			searchFrom = index + len(program)
		}
	}
	return false
}

func commandNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '_' || value == '-' || value == '.'
}

func looksLikeDesktopInputCommand(command string) bool {
	value := normalizedInputCommand(command)
	if invokesGlobalInputInjector(command) || invokesWhydotool(command) {
		return true
	}
	if strings.Contains(value, "wtype ") || strings.HasSuffix(value, "wtype") {
		return true
	}
	return containsInputSubcommand(value, "wlrctl", "pointer", "keyboard") ||
		containsInputSubcommand(value, "whydotool", "mousemove", "click", "type", "key", "stdin") ||
		containsInputSubcommand(value, "ydotool", "mousemove", "click", "type", "key") ||
		containsInputSubcommand(value, "xdotool", "mousemove", "mousemove_relative", "click", "mousedown", "mouseup", "type", "key")
}

func unsafeGlobalHeadlessInputCommand(command string) bool {
	return invokesGlobalInputInjector(command)
}

func invokesGlobalInputInjector(command string) bool {
	return invokesExecutable(command, isGlobalInputInjector)
}

func invokesWhydotool(command string) bool {
	return invokesExecutable(command, func(token string) bool {
		return strings.EqualFold(filepath.Base(token), "whydotool")
	})
}

func invokesExecutable(command string, matches func(string) bool) bool {
	for _, tokens := range splitShellCommands(command) {
		index := shellCommandExecutableIndex(tokens)
		if index >= len(tokens) {
			continue
		}
		if matches(tokens[index]) {
			return true
		}
		if commandStringIndex := shellCommandStringIndex(tokens, index); commandStringIndex >= 0 && invokesExecutable(tokens[commandStringIndex], matches) {
			return true
		}
	}
	return false
}

func splitShellCommands(command string) [][]string {
	var commands [][]string
	var tokens []string
	var word strings.Builder
	var quote rune
	escaped := false
	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		tokens = append(tokens, word.String())
		word.Reset()
	}
	flushCommand := func() {
		flushWord()
		if len(tokens) != 0 {
			commands = append(commands, tokens)
			tokens = nil
		}
	}
	for _, r := range command {
		if escaped {
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			quote = r
		case ' ', '\t', '\r':
			flushWord()
		case ';', '|', '&', '(', ')', '\n':
			flushCommand()
		default:
			word.WriteRune(r)
		}
	}
	if escaped {
		word.WriteRune('\\')
	}
	flushCommand()
	return commands
}

func shellCommandExecutableIndex(tokens []string) int {
	index := 0
	for depth := 0; depth < 8 && index < len(tokens); depth++ {
		for index < len(tokens) && isShellAssignment(tokens[index]) {
			index++
		}
		if index >= len(tokens) {
			return index
		}
		name := filepath.Base(tokens[index])
		switch name {
		case "env":
			index++
			for index < len(tokens) {
				if isShellAssignment(tokens[index]) {
					index++
					continue
				}
				if tokens[index] == "--" {
					index++
					break
				}
				if !strings.HasPrefix(tokens[index], "-") {
					break
				}
				option := strings.SplitN(tokens[index], "=", 2)[0]
				index++
				if optionNeedsArgument(option, "-u", "--unset", "-C", "--chdir", "-S", "--split-string") && index < len(tokens) {
					index++
				}
			}
		case "sudo", "doas", "pkexec":
			index++
			for index < len(tokens) {
				if tokens[index] == "--" {
					index++
					break
				}
				if !strings.HasPrefix(tokens[index], "-") {
					break
				}
				option := strings.SplitN(tokens[index], "=", 2)[0]
				index++
				if optionNeedsArgument(option, "-u", "--user", "-g", "--group", "-h", "--host", "-p", "--prompt", "-C", "--close-from", "-R", "--chroot", "-T", "--command-timeout", "-D", "--chdir") && index < len(tokens) {
					index++
				}
			}
		case "command", "exec":
			index++
			for index < len(tokens) && strings.HasPrefix(tokens[index], "-") {
				if tokens[index] == "--" {
					index++
					break
				}
				option := tokens[index]
				index++
				if optionNeedsArgument(option, "-a", "--argv0") && index < len(tokens) {
					index++
				}
			}
		case "nohup":
			index++
		case "nice":
			index++
			if index < len(tokens) && (tokens[index] == "-n" || tokens[index] == "--adjustment") {
				index++
				if index < len(tokens) {
					index++
				}
			}
		case "timeout":
			index++
			for index < len(tokens) && strings.HasPrefix(tokens[index], "-") {
				option := strings.SplitN(tokens[index], "=", 2)[0]
				index++
				if optionNeedsArgument(option, "-k", "--kill-after", "-s", "--signal") && index < len(tokens) {
					index++
				}
			}
			if index < len(tokens) { // timeout duration
				index++
			}
		default:
			return index
		}
	}
	return index
}

func optionNeedsArgument(option string, options ...string) bool {
	for _, candidate := range options {
		if option == candidate {
			return true
		}
	}
	return false
}

func isShellAssignment(token string) bool {
	equals := strings.IndexByte(token, '=')
	if equals <= 0 || strings.HasPrefix(token, "-") {
		return false
	}
	for i, r := range token[:equals] {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func isGlobalInputInjector(token string) bool {
	name := filepath.Base(token)
	return strings.EqualFold(name, "ydotool") || strings.EqualFold(name, "xdotool")
}

func shellCommandStringIndex(tokens []string, executableIndex int) int {
	name := filepath.Base(tokens[executableIndex])
	if name != "sh" && name != "bash" && name != "dash" && name != "zsh" && name != "fish" {
		return -1
	}
	for index := executableIndex + 1; index < len(tokens); index++ {
		if tokens[index] == "-c" || tokens[index] == "--command" || (strings.HasPrefix(tokens[index], "-") && strings.Contains(tokens[index][1:], "c")) {
			if index+1 < len(tokens) {
				return index + 1
			}
			return -1
		}
		if !strings.HasPrefix(tokens[index], "-") {
			return -1
		}
	}
	return -1
}

func annotateProcessDesktopRoute(out map[string]any, route processDesktopRoute) {
	if route.Target == "" {
		return
	}
	out["desktop_target"] = route.Target
	out["shared_physical_input"] = route.Target == "current"
	if route.WaylandDisplay != "" {
		out["wayland_display"] = route.WaylandDisplay
	}
}

func processExec(ctx context.Context, raw json.RawMessage, root broker.Client, session sessionbridge.Client) (any, error) {
	var in processExecInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Command) == "" {
		return nil, fmt.Errorf("command is required")
	}
	timeout := defaultExecTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	max := in.MaxOutputBytes
	if max <= 0 || max > maxProcessOutput {
		max = 2 << 20
	}
	shell := in.Shell
	if shell == "" {
		shell = platform.DefaultShell()
	}
	route, err := prepareProcessDesktopRoute(in.DesktopTarget, in.Command, in.ExecutionContext, in.Env)
	if err != nil {
		return nil, err
	}
	if route.Env != nil {
		in.Env = route.Env
	}

	desktopAvailable := false
	if executionContextNeedsBridgeProbe(in.ExecutionContext, in.Command, in.Privileged) {
		desktopAvailable = session.Available()
	}
	executionContext, err := resolveExecutionContext(in.ExecutionContext, in.Command, in.Privileged, desktopAvailable)
	if err != nil {
		return nil, err
	}
	if executionContext == "desktop" || executionContext == "user" {
		if in.Privileged {
			return nil, fmt.Errorf("privileged execution cannot use execution_context=%s; use execution_context=system", executionContext)
		}
		res, err := session.Exec(ctx, sessionbridge.ExecRequest{
			Command: in.Command, Stdin: in.Stdin, Cwd: in.Cwd, Env: in.Env, Shell: shell,
			TimeoutSeconds: int(timeout / time.Second), MaxOutputBytes: max,
		})
		if err != nil {
			return nil, err
		}
		out := map[string]any{
			"exit_code": res.ExitCode, "stdout": res.Stdout, "stderr": res.Stderr,
			"duration_ms": res.DurationMS, "timed_out": res.TimedOut, "truncated": res.Truncated,
			"privileged": false, "execution_context": executionContext, "session_bridge": true,
			"pid": res.PID, "cgroup": res.Cgroup, "session": res.Session,
		}
		annotateProcessDesktopRoute(out, route)
		return out, nil
	}
	if executionContext == "system" && !in.Privileged {
		return nil, fmt.Errorf("execution_context=system requires privileged=true so policy and the root broker remain authoritative")
	}

	if in.Privileged && !platform.SupportsLocalPrivilege() {
		if platform.IsTermux() {
			return nil, fmt.Errorf("Android device-root escalation is not supported on Termux and is not planned; ordinary user-space commands (including sudo inside a non-root container/proot when available) remain allowed")
		}
		if platform.IsDarwin() {
			return nil, fmt.Errorf("local privileged execution is not supported by the experimental macOS runtime; use ordinary user-space commands or a remote SSH target")
		}
		return nil, fmt.Errorf("local privileged execution is not supported on this platform")
	}
	if in.Privileged {
		res, err := root.Exec(ctx, broker.ExecRequest{Command: in.Command, Stdin: in.Stdin, Cwd: in.Cwd, Env: in.Env, Shell: shell, TimeoutSeconds: int(timeout / time.Second), MaxOutputBytes: max})
		if err != nil {
			return nil, err
		}
		out := map[string]any{
			"exit_code": res.ExitCode, "stdout": res.Stdout, "stderr": res.Stderr,
			"duration_ms": res.DurationMS, "timed_out": res.TimedOut, "truncated": res.Truncated,
			"privileged": true, "execution_context": executionContext,
		}
		annotateProcessDesktopRoute(out, route)
		return out, nil
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.Command(shell, "-lc", in.Command)
	if in.Cwd != "" {
		cmd.Dir = in.Cwd
	}
	cmd.Env = mergedEnv(in.Env)
	if in.Stdin != "" {
		cmd.Stdin = strings.NewReader(in.Stdin)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = max, max
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-cctx.Done():
		timedOut = errors.Is(cctx.Err(), context.DeadlineExceeded)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr = <-done
	}
	duration := time.Since(start)
	out := map[string]any{
		"exit_code": 0, "stdout": stdout.String(), "stderr": stderr.String(),
		"duration_ms": duration.Milliseconds(), "timed_out": timedOut,
		"truncated": stdout.truncated || stderr.truncated, "privileged": false,
		"execution_context": executionContext,
	}
	annotateProcessDesktopRoute(out, route)
	if timedOut {
		out["exit_code"] = -1
		return out, nil
	}
	if waitErr == nil {
		return out, nil
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		out["exit_code"] = ee.ExitCode()
		return out, nil
	}
	return nil, waitErr
}

func processSpawn(ctx context.Context, in processSpawnInput, deps processDeps) (any, error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, fmt.Errorf("command is required")
	}
	route, err := prepareProcessDesktopRoute(in.DesktopTarget, in.Command, in.ExecutionContext, in.Env)
	if err != nil {
		return nil, err
	}
	if route.Env != nil {
		in.Env = route.Env
	}
	desktopAvailable := false
	if executionContextNeedsBridgeProbe(in.ExecutionContext, in.Command, false) {
		desktopAvailable = deps.Session.Available()
	}
	executionContext, err := resolveExecutionContext(in.ExecutionContext, in.Command, false, desktopAvailable)
	if err != nil {
		return nil, err
	}
	if executionContext == "system" {
		return nil, fmt.Errorf("process_spawn does not support execution_context=system; use process_exec with privileged=true or a managed service")
	}
	if executionContext == "desktop" || executionContext == "user" {
		shell := in.Shell
		if shell == "" {
			shell = platform.DefaultShell()
		}
		return deps.Jobs.StartExternal(in.Command, in.Cwd, shell, in.Env, executionContext, func(logFile string) (int, int, error) {
			res, err := deps.Session.Spawn(ctx, sessionbridge.SpawnRequest{
				Command: in.Command, Cwd: in.Cwd, Env: in.Env, Shell: shell, LogFile: logFile,
			})
			if err != nil {
				return 0, 0, err
			}
			return res.PID, res.PGID, nil
		})
	}
	return deps.Jobs.StartWithContext(in.Command, in.Cwd, in.Shell, in.Env, executionContext)
}

func executionContextNeedsBridgeProbe(requested, command string, privileged bool) bool {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if privileged || requested == "service" || requested == "system" {
		return false
	}
	if requested == "desktop" || requested == "user" {
		return true
	}
	return (requested == "" || requested == "auto") && looksLikeDesktopCommand(command)
}

func resolveExecutionContext(requested, command string, privileged, desktopAvailable bool) (string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		requested = "auto"
	}
	switch requested {
	case "auto":
		if privileged {
			return "system", nil
		}
		if desktopAvailable && looksLikeDesktopCommand(command) {
			return "desktop", nil
		}
		return "service", nil
	case "service":
		if privileged {
			return "system", nil
		}
		return "service", nil
	case "user", "desktop":
		if privileged {
			return "", fmt.Errorf("execution_context=%s cannot be privileged", requested)
		}
		if !desktopAvailable {
			return "", fmt.Errorf("execution_context=%s requires an active CyCom desktop session bridge", requested)
		}
		return requested, nil
	case "system":
		if !privileged {
			return "", fmt.Errorf("execution_context=system requires privileged=true")
		}
		return "system", nil
	default:
		return "", fmt.Errorf("unsupported execution_context %q", requested)
	}
}

// Auto routing is deliberately conservative. Unknown commands stay in the
// service context; callers can always request desktop explicitly. These are
// commands whose semantics depend on the active graphical/login session rather
// than merely on DISPLAY being present.
func looksLikeDesktopCommand(command string) bool {
	value := strings.ToLower(command)
	markers := []string{
		"noctalia", "powerprofilesctl", "notify-send", "xdg-open", "gtk-launch",
		"wl-copy", "wl-paste", "grim", "slurp", "wtype", "ydotool", "xdotool",
		"wlrctl", "whydotool",
		"secret-tool", "kwallet", "kdialog", "zenity", "dbus-send --session",
		"gdbus call --session", "busctl --user", "systemctl --user import-environment",
	}
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func processInspect(ctx context.Context, raw json.RawMessage) (any, error) {
	var in processPIDInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.PID <= 0 {
		return nil, fmt.Errorf("pid must be positive")
	}
	cmd := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(in.PID), "-o", "pid=,ppid=,user=,stat=,etime=,comm=,args=")
	b, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return map[string]any{"pid": in.PID, "exists": false}, nil
		}
		return nil, err
	}
	return map[string]any{"pid": in.PID, "exists": true, "ps": strings.TrimSpace(string(b))}, nil
}

func processSignal(_ context.Context, raw json.RawMessage) (any, error) {
	var in processSignalInput
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.PID <= 0 {
		return nil, fmt.Errorf("pid must be positive")
	}
	sig, err := parseSignal(in.Signal)
	if err != nil {
		return nil, err
	}
	if err := syscall.Kill(in.PID, sig); err != nil {
		return nil, err
	}
	return map[string]any{"pid": in.PID, "signal": sig.String(), "sent": true}, nil
}

func parseSignal(s string) (syscall.Signal, error) {
	switch strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(s), "SIG")) {
	case "", "TERM":
		return syscall.SIGTERM, nil
	case "INT":
		return syscall.SIGINT, nil
	case "HUP":
		return syscall.SIGHUP, nil
	case "KILL":
		return syscall.SIGKILL, nil
	case "USR1":
		return syscall.SIGUSR1, nil
	case "USR2":
		return syscall.SIGUSR2, nil
	default:
		return 0, fmt.Errorf("unsupported signal %q", s)
	}
}
