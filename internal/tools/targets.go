package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/targets"
)

func registerTargets(r *registry.Registry, tm *targets.Manager, root broker.Client) {
	must(r.Add(registry.Tool{Name: "target_list", Description: "List configured machine targets. The implicit local target always exists; SSH targets are durable configuration, not live MCP sessions.", InputSchema: registry.ObjectSchema(nil, nil), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(context.Context, json.RawMessage) (any, error) { return map[string]any{"targets": tm.List()}, nil }}))
	must(r.Add(registry.Tool{Name: "target_get", Description: "Inspect one configured machine target by name.", InputSchema: registry.ObjectSchema(map[string]any{"name": registry.String("target name; local is implicit")}, []string{"name"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return tm.Get(in.Name)
	}}))
	must(r.Add(registry.Tool{Name: "target_upsert", Description: "Create or update a durable SSH machine target. Authentication uses the system SSH agent/config or an identity file; passwords are intentionally not stored.", InputSchema: registry.ObjectSchema(map[string]any{
		"name": registry.String("stable target name"), "transport": map[string]any{"type": "string", "enum": []string{"ssh"}}, "host": registry.String("SSH hostname/IP"), "port": registry.Integer("SSH port; default 22"), "user": registry.String("SSH username"), "identity_file": registry.String("optional private key path"), "work_dir": registry.String("default remote working directory"), "env": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, "ssh_options": registry.StringArray("additional OpenSSH -o options"), "tags": registry.StringArray("arbitrary target tags"), "enabled": registry.Boolean("enable target; defaults true")}, []string{"name", "host"}), Source: "core", Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Name         string            `json:"name"`
			Transport    string            `json:"transport"`
			Host         string            `json:"host"`
			Port         int               `json:"port"`
			User         string            `json:"user"`
			IdentityFile string            `json:"identity_file"`
			WorkDir      string            `json:"work_dir"`
			Env          map[string]string `json:"env"`
			SSHOptions   []string          `json:"ssh_options"`
			Tags         []string          `json:"tags"`
			Enabled      *bool             `json:"enabled"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		return tm.Upsert(targets.Target{Name: in.Name, Transport: in.Transport, Host: in.Host, Port: in.Port, User: in.User, IdentityFile: in.IdentityFile, WorkDir: in.WorkDir, Env: in.Env, SSHOptions: in.SSHOptions, Tags: in.Tags, Enabled: enabled})
	}}))
	must(r.Add(registry.Tool{Name: "target_remove", Description: "Remove a durable remote target definition. This does not alter the remote machine.", InputSchema: registry.ObjectSchema(map[string]any{"name": registry.String("target name")}, []string{"name"}), Source: "core", Annotations: map[string]any{"destructiveHint": true}, Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := tm.Remove(in.Name); err != nil {
			return nil, err
		}
		return map[string]any{"name": in.Name, "removed": true}, nil
	}}))
	must(r.Add(registry.Tool{Name: "target_probe", Description: "Probe whether a local/SSH target is reachable without creating persistent connection state.", InputSchema: registry.ObjectSchema(map[string]any{"name": registry.String("target name"), "timeout_seconds": registry.Integer("probe timeout; default 8")}, []string{"name"}), Source: "core", Annotations: map[string]any{"readOnlyHint": true}, Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Name    string `json:"name"`
			Timeout int    `json:"timeout_seconds"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		return tm.Probe(ctx, in.Name, in.Timeout)
	}}))
	must(r.Add(registry.Tool{Name: "target_exec", Description: "Execute a command on the implicit local target or a durable SSH target. SSH is invoked per operation, so tunnel/MCP restarts cannot erase a required connection session.", InputSchema: registry.ObjectSchema(map[string]any{"name": registry.String("target name; local allowed"), "command": registry.String("shell command"), "cwd": registry.String("working directory override"), "env": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, "timeout_seconds": registry.Integer("execution timeout"), "max_output_bytes": registry.Integer("stdout/stderr cap"), "privileged": registry.Boolean("use sudo -n on the target")}, []string{"name", "command"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Name       string            `json:"name"`
			Command    string            `json:"command"`
			Cwd        string            `json:"cwd"`
			Env        map[string]string `json:"env"`
			Timeout    int               `json:"timeout_seconds"`
			Max        int               `json:"max_output_bytes"`
			Privileged bool              `json:"privileged"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if (in.Name == "" || in.Name == "local") && in.Privileged && !platform.SupportsLocalPrivilege() {
			return nil, fmt.Errorf("Android device-root escalation is not supported on Termux and is not planned; remote SSH sudo remains supported")
		}
		if (in.Name == "" || in.Name == "local") && in.Privileged {
			res, err := root.Exec(ctx, broker.ExecRequest{Command: in.Command, Cwd: in.Cwd, Env: in.Env, TimeoutSeconds: in.Timeout, MaxOutputBytes: in.Max})
			if err != nil {
				return nil, err
			}
			return map[string]any{"target": "local", "transport": "root-broker", "result": res}, nil
		}
		return tm.Exec(ctx, in.Name, targets.ExecOptions{Command: in.Command, Cwd: in.Cwd, Env: in.Env, TimeoutSeconds: in.Timeout, MaxOutputBytes: in.Max, Privileged: in.Privileged})
	}}))
	must(r.Add(registry.Tool{Name: "target_copy", Description: "Copy a file/directory to or from an SSH target using the system scp implementation.", InputSchema: registry.ObjectSchema(map[string]any{"name": registry.String("SSH target name"), "direction": map[string]any{"type": "string", "enum": []string{"push", "pull"}}, "source": registry.String("source path"), "dest": registry.String("destination path"), "recursive": registry.Boolean("copy directories recursively"), "timeout_seconds": registry.Integer("timeout; default 300")}, []string{"name", "direction", "source", "dest"}), Source: "core", Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Name      string `json:"name"`
			Direction string `json:"direction"`
			Source    string `json:"source"`
			Dest      string `json:"dest"`
			Recursive bool   `json:"recursive"`
			Timeout   int    `json:"timeout_seconds"`
		}
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if in.Name == "local" {
			return nil, fmt.Errorf("target_copy is for ssh targets; use fs_* locally")
		}
		return tm.Copy(ctx, in.Name, in.Direction, in.Source, in.Dest, in.Recursive, in.Timeout)
	}}))
}
