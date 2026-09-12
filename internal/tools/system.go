package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/plugins"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/policy"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/targets"
)

type systemDeps struct {
	Broker   broker.Client
	Plugins  *plugins.Manager
	Policy   *policy.Engine
	Targets  *targets.Manager
	StateDir string
	Version  string
	Session  sessionbridge.Client
}

func registerSystem(r *registry.Registry, deps systemDeps) {
	must(r.Add(registry.Tool{Name: "system_info", Description: "Inspect the local machine, OS, runtime, user, uptime, common executables and privilege-broker availability.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) {
		return systemInfo(deps), nil
	}}))
	must(r.Add(registry.Tool{Name: "system_env", Description: "Read selected environment variables by name. Variables must be requested explicitly; the full environment is never dumped implicitly.", InputSchema: registry.ObjectSchema(map[string]any{"names": registry.StringArray("environment variable names")}, []string{"names"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: systemEnv}))
	must(r.Add(registry.Tool{Name: "service_control", Description: "Inspect or control the host service manager (systemd on Linux, runit/termux-services on Termux, launchd on macOS).", InputSchema: registry.ObjectSchema(map[string]any{
		"name":       registry.String("service/unit name"),
		"action":     map[string]any{"type": "string", "enum": []string{"status", "start", "stop", "restart", "reload", "enable", "disable", "is-active", "is-enabled"}},
		"user":       registry.Boolean("operate on the user service manager (systemd; macOS launchd adapter is user-domain only)"),
		"privileged": registry.Boolean("execute through the local privilege broker where supported; macOS and Termux adapters are non-privileged"),
	}, []string{"name", "action"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		return serviceControl(ctx, raw, deps.Broker)
	}}))
	must(r.Add(registry.Tool{Name: "network_request", Description: "Make an HTTP/HTTPS request from this machine and return structured status, headers and body.", InputSchema: registry.ObjectSchema(map[string]any{
		"method": registry.String("HTTP method; default GET"), "url": registry.String("absolute URL"),
		"headers": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		"body":    registry.String("request body"), "timeout_seconds": registry.Integer("timeout"), "max_bytes": registry.Integer("response body cap"),
	}, []string{"url"}), Source: "core", Handler: networkRequest}))
	must(r.Add(registry.Tool{Name: "network_tcp", Description: "Probe a TCP host:port from this machine.", InputSchema: registry.ObjectSchema(map[string]any{
		"address": registry.String("host:port"), "timeout_seconds": registry.Integer("timeout; default 5"),
	}, []string{"address"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: networkTCP}))
	must(r.Add(registry.Tool{Name: "network_resolve", Description: "Resolve a hostname using the machine resolver.", InputSchema: registry.ObjectSchema(map[string]any{"host": registry.String("hostname")}, []string{"host"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: networkResolve}))
	must(r.Add(registry.Tool{Name: "capabilities_list", Description: "Describe the runtime's current generic capabilities, registered tools, detected binaries, external plugins and privilege broker.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) {
		return capabilities(r, deps), nil
	}}))
	must(r.Add(registry.Tool{Name: "plugins_reload", Description: "Reload external tool manifests from the plugin directory without restarting the runtime.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Handler: func(context.Context, json.RawMessage) (any, error) {
		if !deps.Policy.Snapshot().AllowExternalPlugins {
			return nil, fmt.Errorf("policy denied external plugins")
		}
		loaded, err := deps.Plugins.ReloadInto(r)
		if err != nil {
			return nil, err
		}
		return map[string]any{"loaded": loaded, "count": len(loaded)}, nil
	}}))
}

func systemInfo(deps systemDeps) map[string]any {
	hostname, _ := os.Hostname()
	wd, _ := os.Getwd()
	user := os.Getenv("USER")
	out := map[string]any{"hostname": hostname, "user": user, "cwd": wd, "goos": runtime.GOOS, "goarch": runtime.GOARCH, "cpus": runtime.NumCPU(), "pid": os.Getpid(), "version": deps.Version, "state_dir": deps.StateDir, "root_broker": deps.Broker.Available(), "termux": platform.IsTermux()}
	out["desktop_session_bridge"] = desktopSessionBridgeSnapshot(deps.Session)
	if platform.IsTermux() {
		out["runtime_profile"] = "mobile_assistant"
		out["android_device_root_support"] = "not_supported"
		out["android_device_root_planned"] = false
	}
	if platform.IsTermux() {
		out["termux_prefix"] = os.Getenv("PREFIX")
		out["termux_version"] = os.Getenv("TERMUX_VERSION")
	}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		out["os_release"] = parseKeyValue(string(data))
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		out["proc_uptime"] = strings.TrimSpace(string(data))
	}
	if runtime.GOOS == "darwin" {
		out["runtime_profile"] = "macos_experimental"
		out["macos_runtime_tested"] = false
		out["local_privilege_support"] = "not_supported"
		if b, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			out["macos_version"] = strings.TrimSpace(string(b))
		}
		if b, err := exec.Command("sw_vers", "-buildVersion").Output(); err == nil {
			out["macos_build"] = strings.TrimSpace(string(b))
		}
		if b, err := exec.Command("sysctl", "-n", "kern.boottime").Output(); err == nil {
			out["kern_boottime"] = strings.TrimSpace(string(b))
		}
	}
	bins := []string{"sh", "bash", "fish", "git", "systemctl", "launchctl", "screencapture", "osascript", "cliclick", "sv", "sv-enable", "sv-disable", "docker", "podman", "python3", "node", "java", "cargo", "go", "ffmpeg", "curl", "wget", "ssh", "scp", "rsync", "nvidia-smi", "termux-battery-status", "termux-clipboard-get", "termux-notification", "termux-camera-photo"}
	found := map[string]string{}
	for _, b := range bins {
		if p, err := exec.LookPath(b); err == nil {
			found[b] = p
		}
	}
	out["binaries"] = found
	return out
}

func systemEnv(_ context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Names []string `json:"names"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, name := range in.Names {
		v, ok := os.LookupEnv(name)
		out[name] = map[string]any{"set": ok, "value": v}
	}
	return out, nil
}

func serviceControl(ctx context.Context, raw json.RawMessage, root broker.Client) (any, error) {
	var in struct {
		Name       string `json:"name"`
		Action     string `json:"action"`
		User       bool   `json:"user"`
		Privileged bool   `json:"privileged"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Name == "" || in.Action == "" {
		return nil, fmt.Errorf("name and action are required")
	}
	allowed := map[string]bool{"status": true, "start": true, "stop": true, "restart": true, "reload": true, "enable": true, "disable": true, "is-active": true, "is-enabled": true}
	if !allowed[in.Action] {
		return nil, fmt.Errorf("unsupported action %q", in.Action)
	}
	if platform.IsTermux() {
		return termuxServiceControl(ctx, in.Name, in.Action, in.Privileged)
	}
	if runtime.GOOS == "darwin" {
		return darwinServiceControl(ctx, in.Name, in.Action, in.Privileged)
	}
	args := []string{}
	if in.User {
		args = append(args, "--user")
	}
	args = append(args, in.Action, in.Name)
	if in.Privileged {
		cmd := "systemctl " + shellJoin(args)
		res, err := root.Exec(ctx, broker.ExecRequest{Command: cmd, TimeoutSeconds: 60, MaxOutputBytes: 1 << 20})
		if err != nil {
			return nil, err
		}
		return res, nil
	}
	return runServiceCommand(ctx, "systemd", in.Name, in.Action, "systemctl", args...)
}

func darwinServiceControl(ctx context.Context, name, action string, privileged bool) (any, error) {
	if privileged {
		return nil, fmt.Errorf("privileged service_control is not supported by the experimental macOS launchd adapter")
	}
	if _, err := exec.LookPath("launchctl"); err != nil {
		return nil, fmt.Errorf("macOS launchd adapter requires launchctl")
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	target := domain + "/" + name
	home, _ := os.UserHomeDir()
	plist := filepath.Join(home, "Library", "LaunchAgents", name+".plist")
	switch action {
	case "status", "is-active":
		return runServiceCommand(ctx, "launchd-user", name, action, "launchctl", "print", target)
	case "start":
		if _, err := os.Stat(plist); err != nil {
			return nil, fmt.Errorf("launchd plist not found for %s: %s", name, plist)
		}
		return runServiceCommand(ctx, "launchd-user", name, action, "launchctl", "bootstrap", domain, plist)
	case "stop":
		return runServiceCommand(ctx, "launchd-user", name, action, "launchctl", "bootout", target)
	case "restart", "reload":
		if _, err := os.Stat(plist); err != nil {
			return nil, fmt.Errorf("launchd plist not found for %s: %s", name, plist)
		}
		_, _ = runServiceCommand(ctx, "launchd-user", name, "stop", "launchctl", "bootout", target)
		return runServiceCommand(ctx, "launchd-user", name, action, "launchctl", "bootstrap", domain, plist)
	case "enable":
		return runServiceCommand(ctx, "launchd-user", name, action, "launchctl", "enable", target)
	case "disable":
		return runServiceCommand(ctx, "launchd-user", name, action, "launchctl", "disable", target)
	case "is-enabled":
		return runServiceCommand(ctx, "launchd-user", name, action, "launchctl", "print-disabled", domain)
	default:
		return nil, fmt.Errorf("unsupported macOS service action %q", action)
	}
}

func termuxServiceControl(ctx context.Context, name, action string, privileged bool) (any, error) {
	if privileged {
		return nil, fmt.Errorf("privileged service_control is not supported by the Termux runit adapter")
	}
	if action == "is-enabled" {
		dir := platform.TermuxServiceDir()
		if dir == "" {
			return nil, fmt.Errorf("cannot determine Termux service directory; PREFIX/SVDIR is unset")
		}
		serviceDir := filepath.Join(dir, name)
		if st, err := os.Stat(serviceDir); err != nil || !st.IsDir() {
			if os.IsNotExist(err) {
				return map[string]any{"adapter": "termux-runit", "name": name, "action": action, "exists": false, "enabled": false, "exit_code": 1}, nil
			}
			if err != nil {
				return nil, err
			}
		}
		_, err := os.Stat(filepath.Join(serviceDir, "down"))
		enabled := os.IsNotExist(err)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return map[string]any{"adapter": "termux-runit", "name": name, "action": action, "exists": true, "enabled": enabled, "exit_code": 0}, nil
	}
	var cmd string
	var args []string
	switch action {
	case "status", "is-active":
		cmd, args = "sv", []string{"status", name}
	case "start":
		cmd, args = "sv", []string{"up", name}
	case "stop":
		cmd, args = "sv", []string{"down", name}
	case "restart":
		cmd, args = "sv", []string{"restart", name}
	case "reload":
		cmd, args = "sv", []string{"hup", name}
	case "enable":
		cmd, args = "sv-enable", []string{name}
	case "disable":
		cmd, args = "sv-disable", []string{name}
	default:
		return nil, fmt.Errorf("unsupported Termux service action %q", action)
	}
	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("Termux service adapter requires %s; install termux-services with: pkg install termux-services", cmd)
	}
	return runServiceCommand(ctx, "termux-runit", name, action, cmd, args...)
}

func runServiceCommand(ctx context.Context, adapter, name, action, command string, args ...string) (any, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	b, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			return nil, err
		}
	}
	return map[string]any{"adapter": adapter, "name": name, "action": action, "exit_code": exit, "output": string(b)}, nil
}

func networkRequest(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Method         string            `json:"method"`
		URL            string            `json:"url"`
		Headers        map[string]string `json:"headers"`
		Body           string            `json:"body"`
		TimeoutSeconds int               `json:"timeout_seconds"`
		MaxBytes       int64             `json:"max_bytes"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.URL == "" {
		return nil, fmt.Errorf("url is required")
	}
	method := strings.ToUpper(in.Method)
	if method == "" {
		method = http.MethodGet
	}
	timeout := 30 * time.Second
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	limit := in.MaxBytes
	if limit <= 0 || limit > 10<<20 {
		limit = 2 << 20
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, in.URL, strings.NewReader(in.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: timeout}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	trunc := int64(len(data)) > limit
	if trunc {
		data = data[:limit]
	}
	return map[string]any{"status_code": resp.StatusCode, "headers": resp.Header, "body": string(data), "truncated": trunc, "duration_ms": time.Since(start).Milliseconds()}, nil
}

func networkTCP(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Address        string `json:"address"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Address == "" {
		return nil, fmt.Errorf("address is required")
	}
	timeout := 5 * time.Second
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", in.Address)
	if err != nil {
		return map[string]any{"address": in.Address, "reachable": false, "error": err.Error(), "duration_ms": time.Since(start).Milliseconds()}, nil
	}
	_ = c.Close()
	return map[string]any{"address": in.Address, "reachable": true, "duration_ms": time.Since(start).Milliseconds()}, nil
}

func networkResolve(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Host string `json:"host"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if in.Host == "" {
		return nil, fmt.Errorf("host is required")
	}
	ips, err := net.DefaultResolver.LookupHost(ctx, in.Host)
	if err != nil {
		return nil, err
	}
	return map[string]any{"host": in.Host, "addresses": ips}, nil
}

func capabilities(r *registry.Registry, deps systemDeps) map[string]any {
	tools := r.List()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	bins := []string{"git", "systemctl", "launchctl", "sv", "sv-enable", "sv-disable", "docker", "podman", "python3", "node", "java", "cargo", "go", "ssh", "scp", "rsync", "nvidia-smi", "grim", "spectacle", "screencapture", "osascript", "cliclick", "xdotool", "ydotool", "wtype", "termux-battery-status", "termux-clipboard-get", "termux-notification", "termux-camera-photo"}
	found := map[string]bool{}
	for _, b := range bins {
		_, err := exec.LookPath(b)
		found[b] = err == nil
	}
	score := func(ok bool, yes int) int {
		if ok {
			return yes
		}
		return 0
	}
	serviceAdapter := map[string]any{"capability": "service.manage", "adapter": "systemd", "score": score(found["systemctl"], 100), "available": found["systemctl"]}
	if platform.IsTermux() {
		serviceAdapter = map[string]any{"capability": "service.manage", "adapter": "termux-runit", "score": score(found["sv"], 100), "available": found["sv"]}
	} else if runtime.GOOS == "darwin" {
		serviceAdapter = map[string]any{"capability": "service.manage", "adapter": "launchd-user", "score": score(found["launchctl"], 100), "available": found["launchctl"]}
	}
	anyAppPath, anyAppAvailable := anyAppInstalledBackend()
	sessionStatus := desktopSessionBridgeSnapshot(deps.Session)
	sessionAvailable, _ := sessionStatus["available"].(bool)
	adapters := []map[string]any{
		{"capability": "session.exec", "adapter": "desktop-session-bridge", "score": score(sessionAvailable, 120), "available": sessionAvailable, "status": sessionStatus},
		{"capability": "desktop.semantic", "adapter": "codex-computer-use-linux", "score": score(anyAppAvailable, 110), "available": anyAppAvailable, "path": anyAppBackendBase(anyAppPath), "tools": anyAppToolPrefixCount(r)},
		{"capability": "local.exec", "adapter": "shell", "score": 100, "available": true},
		{"capability": "privileged.exec", "adapter": "root-broker", "score": score(platform.SupportsLocalPrivilege() && deps.Broker.Available(), 100), "available": platform.SupportsLocalPrivilege() && deps.Broker.Available()},
		{"capability": "remote.exec", "adapter": "openssh", "score": score(found["ssh"], 95), "available": found["ssh"]},
		{"capability": "remote.copy", "adapter": "scp", "score": score(found["scp"], 90), "available": found["scp"]},
		serviceAdapter,
		{"capability": "container.manage", "adapter": "docker", "score": score(found["docker"], 100), "available": found["docker"]},
		{"capability": "container.manage", "adapter": "podman", "score": score(found["podman"], 95), "available": found["podman"]},
		{"capability": "desktop.capture", "adapter": "grim", "score": score(found["grim"], 100), "available": found["grim"]},
		{"capability": "desktop.capture", "adapter": "spectacle", "score": score(found["spectacle"], 95), "available": found["spectacle"]},
		{"capability": "desktop.capture", "adapter": "screencapture", "score": score(runtime.GOOS == "darwin" && found["screencapture"], 100), "available": runtime.GOOS == "darwin" && found["screencapture"]},
		{"capability": "desktop.input", "adapter": "osascript", "score": score(runtime.GOOS == "darwin" && found["osascript"], 90), "available": runtime.GOOS == "darwin" && found["osascript"]},
		{"capability": "desktop.input", "adapter": "cliclick", "score": score(runtime.GOOS == "darwin" && found["cliclick"], 95), "available": runtime.GOOS == "darwin" && found["cliclick"]},
		{"capability": "desktop.input", "adapter": "ydotool", "score": score(found["ydotool"], 100), "available": found["ydotool"]},
		{"capability": "desktop.input", "adapter": "wtype", "score": score(found["wtype"], 90), "available": found["wtype"]},
		{"capability": "desktop.input", "adapter": "xdotool", "score": score(found["xdotool"], 80), "available": found["xdotool"]},
		{"capability": "android.api", "adapter": "termux-api", "score": score(platform.IsTermux() && found["termux-battery-status"], 90), "available": platform.IsTermux() && found["termux-battery-status"]},
	}
	sort.Strings(names)
	return map[string]any{"tools": names, "binaries": found, "adapters": adapters, "plugins": deps.Plugins.Names(), "targets": deps.Targets.List(), "root_broker": deps.Broker.Available(), "desktop_session_bridge": sessionStatus, "local_privilege_supported": platform.SupportsLocalPrivilege(), "android_device_root_support": func() string {
		if runtime.GOOS == "android" || platform.IsTermux() {
			return "not_supported"
		}
		return "n/a"
	}(), "android_device_root_planned": false, "runtime_profile": func() string {
		if platform.IsTermux() {
			return "mobile_assistant"
		}
		return "computer_runtime"
	}(), "policy": deps.Policy.Snapshot(), "platform": runtime.GOOS + "/" + runtime.GOARCH}
}

func desktopSessionBridgeSnapshot(client sessionbridge.Client) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	status, err := client.Status(ctx)
	if err != nil {
		return map[string]any{"available": false, "socket": sessionbridge.DefaultSocketPath()}
	}
	data, err := json.Marshal(status)
	if err != nil {
		return map[string]any{"available": true}
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		return map[string]any{"available": true}
	}
	return out
}

func parseKeyValue(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		var v string
		if err := json.Unmarshal([]byte(parts[1]), &v); err != nil {
			v = strings.Trim(parts[1], "\"")
		}
		out[parts[0]] = v
	}
	return out
}
func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
	}
	return strings.Join(out, " ")
}
