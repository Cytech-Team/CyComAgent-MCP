//go:build linux

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

// registerSessionControl exposes high-level, sudo-less machine/session actions.
// Privileged Linux actions go through cycomagent-root's authenticated Unix socket;
// callers never need a password prompt or an unrestricted sudo invocation.
func registerSessionControl(r *registry.Registry, root broker.Client) {
	destructive := map[string]any{"destructiveHint": true}
	must(r.Add(registry.Tool{Name: "machine_restart", Description: "Restart the Linux machine through the CyComAgent privilege broker; no sudo/password prompt is required.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: destructive, Handler: func(ctx context.Context, _ json.RawMessage) (any, error) { return rootSystemctl(ctx, root, "reboot") }}))
	must(r.Add(registry.Tool{Name: "machine_poweroff", Description: "Power off the Linux machine through the CyComAgent privilege broker; no sudo/password prompt is required.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: destructive, Handler: func(ctx context.Context, _ json.RawMessage) (any, error) { return rootSystemctl(ctx, root, "poweroff") }}))
	must(r.Add(registry.Tool{Name: "session_logout", Description: "Terminate a login session. Defaults to the current graphical/login session; optionally accepts a logind session id.", InputSchema: registry.ObjectSchema(map[string]any{"session": registry.String("optional logind session id")}, nil), Source: "core", Annotations: destructive, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) { return logoutSession(ctx, root, raw) }}))
	must(r.Add(registry.Tool{Name: "tty_control", Description: "Manage a Linux virtual terminal/getty. Actions: status, start, stop, restart, activate. Example tty=tty1.", InputSchema: registry.ObjectSchema(map[string]any{"tty": registry.String("virtual terminal such as tty1"), "action": map[string]any{"type": "string", "enum": []string{"status", "start", "stop", "restart", "activate"}}}, []string{"tty", "action"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) { return ttyControl(ctx, root, raw) }}))
	must(r.Add(registry.Tool{Name: "display_outputs", Description: "List display outputs visible to the active graphical session, including physical and headless outputs when the compositor exposes them.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: displayOutputs}))
	must(r.Add(registry.Tool{Name: "display_output_control", Description: "Enable or disable one display output by exact name in the active graphical session (for example HEADLESS-1, eDP-1, HDMI-A-1). Uses available compositor-neutral command adapters and fails safely when none is available.", InputSchema: registry.ObjectSchema(map[string]any{"output": registry.String("exact output name"), "enabled": registry.Boolean("true to enable, false to disable")}, []string{"output", "enabled"}), Source: "core", Handler: displayOutputControl}))
}

func rootSystemctl(ctx context.Context, root broker.Client, action string) (any, error) {
	if !root.Available() {
		return nil, fmt.Errorf("CyComAgent privilege broker is unavailable")
	}
	res, err := root.Exec(ctx, broker.ExecRequest{Command: "systemctl " + action, TimeoutSeconds: 30, MaxOutputBytes: 1 << 20})
	if err != nil {
		return nil, err
	}
	return map[string]any{"action": action, "sudo_less": true, "transport": "cycomagent-root", "result": res}, nil
}

func logoutSession(ctx context.Context, root broker.Client, raw json.RawMessage) (any, error) {
	var in struct {
		Session string `json:"session"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	session := strings.TrimSpace(in.Session)
	if session == "" {
		session = os.Getenv("XDG_SESSION_ID")
	}
	if session == "" {
		if b, err := exec.Command("loginctl", "list-sessions", "--no-legend").Output(); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				f := strings.Fields(line)
				if len(f) >= 3 && f[2] == os.Getenv("USER") {
					session = f[0]
					break
				}
			}
		}
	}
	if session == "" {
		return nil, fmt.Errorf("could not determine login session")
	}
	if strings.ContainsAny(session, " /\t\n") {
		return nil, fmt.Errorf("invalid session id")
	}
	res, err := root.Exec(ctx, broker.ExecRequest{Command: "loginctl terminate-session " + shellJoin([]string{session}), TimeoutSeconds: 15, MaxOutputBytes: 1 << 20})
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": session, "sudo_less": true, "result": res}, nil
}

func ttyControl(ctx context.Context, root broker.Client, raw json.RawMessage) (any, error) {
	var in struct{ TTY, Action string }
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	tty := strings.TrimSpace(in.TTY)
	action := strings.TrimSpace(in.Action)
	if !strings.HasPrefix(tty, "tty") {
		return nil, fmt.Errorf("tty must look like tty1")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(tty, "tty"))
	if err != nil || n < 1 || n > 63 {
		return nil, fmt.Errorf("invalid tty %q", tty)
	}
	unit := "getty@" + tty + ".service"
	var cmd string
	switch action {
	case "status", "start", "stop", "restart":
		cmd = "systemctl " + action + " " + unit
	case "activate":
		cmd = "chvt " + strconv.Itoa(n)
	default:
		return nil, fmt.Errorf("unsupported tty action %q", action)
	}
	if !root.Available() {
		return nil, fmt.Errorf("CyComAgent privilege broker is unavailable")
	}
	res, err := root.Exec(ctx, broker.ExecRequest{Command: cmd, TimeoutSeconds: 30, MaxOutputBytes: 1 << 20})
	if err != nil {
		return nil, err
	}
	return map[string]any{"tty": tty, "action": action, "sudo_less": true, "result": res}, nil
}

func displayOutputs(ctx context.Context, _ json.RawMessage) (any, error) {
	type cand struct {
		name, bin string
		args      []string
	}
	cs := []cand{}
	if p, e := exec.LookPath("wlr-randr"); e == nil {
		cs = append(cs, cand{"wlr-randr", p, nil})
	}
	if p, e := exec.LookPath("kscreen-doctor"); e == nil {
		cs = append(cs, cand{"kscreen-doctor", p, []string{"-o"}})
	}
	if p, e := exec.LookPath("xrandr"); e == nil {
		cs = append(cs, cand{"xrandr", p, []string{"--query"}})
	}
	for _, c := range cs {
		ac, cancel := context.WithTimeout(ctx, desktopAdapterAttemptTimeout)
		cmd := exec.CommandContext(ac, c.bin, c.args...)
		cmd.Env = desktopEnvironment()
		b, e := cmd.CombinedOutput()
		cancel()
		if e == nil {
			return map[string]any{"adapter": c.name, "output": string(b)}, nil
		}
	}
	return nil, fmt.Errorf("no working display output adapter found")
}

func displayOutputControl(ctx context.Context, raw json.RawMessage) (any, error) {
	var in struct {
		Output  string `json:"output"`
		Enabled bool   `json:"enabled"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Output)
	if name == "" || strings.ContainsAny(name, "\n\r\x00") {
		return nil, fmt.Errorf("invalid output")
	}
	type cand struct {
		name, bin string
		args      []string
	}
	var cs []cand
	if p, e := exec.LookPath("wlr-randr"); e == nil {
		state := "--off"
		if in.Enabled {
			state = "--on"
		}
		cs = append(cs, cand{"wlr-randr", p, []string{"--output", name, state}})
	}
	if p, e := exec.LookPath("kscreen-doctor"); e == nil {
		state := "disable"
		if in.Enabled {
			state = "enable"
		}
		cs = append(cs, cand{"kscreen-doctor", p, []string{"output." + name + "." + state}})
	}
	if p, e := exec.LookPath("xrandr"); e == nil {
		if in.Enabled {
			cs = append(cs, cand{"xrandr", p, []string{"--output", name, "--auto"}})
		} else {
			cs = append(cs, cand{"xrandr", p, []string{"--output", name, "--off"}})
		}
	}
	var failures []string
	for _, c := range cs {
		ac, cancel := context.WithTimeout(ctx, desktopAdapterAttemptTimeout)
		cmd := exec.CommandContext(ac, c.bin, c.args...)
		cmd.Env = desktopEnvironment()
		b, e := cmd.CombinedOutput()
		cancel()
		if e == nil {
			return map[string]any{"adapter": c.name, "output": name, "enabled": in.Enabled}, nil
		}
		failures = append(failures, c.name+": "+strings.TrimSpace(string(b)))
	}
	return nil, fmt.Errorf("all display adapters failed: %s", strings.Join(failures, "; "))
}
