package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Mode                  string   `json:"mode"`
	AllowTools            []string `json:"allow_tools,omitempty"`
	DenyTools             []string `json:"deny_tools,omitempty"`
	AllowPrivileged       bool     `json:"allow_privileged"`
	AllowRemoteTargets    bool     `json:"allow_remote_targets"`
	AllowExternalPlugins  bool     `json:"allow_external_plugins"`
	AllowSensitiveAndroid bool     `json:"allow_sensitive_android"`
	MaxExecTimeoutSeconds int      `json:"max_exec_timeout_seconds"`
}

type Engine struct {
	file string
	mu   sync.RWMutex
	cfg  Config
}

func Default() Config {
	return Config{
		Mode:                  "full",
		AllowPrivileged:       true,
		AllowRemoteTargets:    true,
		AllowExternalPlugins:  true,
		AllowSensitiveAndroid: false,
		MaxExecTimeoutSeconds: 3600,
	}
}

func New(stateDir string) (*Engine, error) {
	e := &Engine{file: filepath.Join(stateDir, "policy.json"), cfg: Default()}
	if _, err := os.Stat(e.file); os.IsNotExist(err) {
		if err := e.save(Default()); err != nil {
			return nil, err
		}
	}
	if err := e.Reload(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) File() string { return e.file }

func (e *Engine) Snapshot() Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c := e.cfg
	c.AllowTools = append([]string{}, c.AllowTools...)
	c.DenyTools = append([]string{}, c.DenyTools...)
	return c
}

func (e *Engine) Reload() error {
	data, err := os.ReadFile(e.file)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse policy: %w", err)
	}
	if cfg.Mode == "" {
		cfg.Mode = "full"
	}
	switch cfg.Mode {
	case "full", "safe", "readonly":
	default:
		return fmt.Errorf("unknown policy mode %q", cfg.Mode)
	}
	if cfg.MaxExecTimeoutSeconds <= 0 {
		cfg.MaxExecTimeoutSeconds = 3600
	}
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
	return nil
}

func (e *Engine) save(cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(e.file), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(e.file), ".policy-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, e.file)
}

func (e *Engine) BeforeCall(_ context.Context, tool string, args json.RawMessage) error {
	cfg := e.Snapshot()
	for _, p := range cfg.DenyTools {
		if match(p, tool) {
			return fmt.Errorf("policy denied tool %s", tool)
		}
	}
	if len(cfg.AllowTools) > 0 {
		allowed := false
		for _, p := range cfg.AllowTools {
			if match(p, tool) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("policy did not allow tool %s", tool)
		}
	}
	if cfg.Mode == "readonly" && isMutation(tool) {
		return fmt.Errorf("policy readonly mode denied mutating tool %s", tool)
	}
	if cfg.Mode == "safe" && isHighImpact(tool) {
		return fmt.Errorf("policy safe mode denied high-impact tool %s", tool)
	}
	if isSensitiveAndroid(tool) && !cfg.AllowSensitiveAndroid {
		return fmt.Errorf("policy denied sensitive Android capability %s; set allow_sensitive_android=true to opt in", tool)
	}
	var obj map[string]any
	_ = json.Unmarshal(args, &obj)
	if v, _ := obj["privileged"].(bool); v && !cfg.AllowPrivileged {
		return fmt.Errorf("policy denied privileged execution")
	}
	if seconds, ok := numberAsInt(obj["timeout_seconds"]); ok && seconds > cfg.MaxExecTimeoutSeconds {
		return fmt.Errorf("policy timeout limit exceeded: %d > %d seconds", seconds, cfg.MaxExecTimeoutSeconds)
	}
	if cfg.Mode == "readonly" && tool == "network_request" {
		method, _ := obj["method"].(string)
		method = strings.ToUpper(strings.TrimSpace(method))
		if method != "" && method != "GET" && method != "HEAD" && method != "OPTIONS" {
			return fmt.Errorf("policy readonly mode denied mutating HTTP method %s", method)
		}
	}
	if strings.HasPrefix(tool, "target_") && tool != "target_list" && tool != "target_get" && tool != "target_probe" && !cfg.AllowRemoteTargets {
		return fmt.Errorf("policy denied remote target operations")
	}
	return nil
}

func (e *Engine) AfterCall(context.Context, string, json.RawMessage, time.Duration, error) {}

func match(pattern, name string) bool {
	if pattern == name {
		return true
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

func isMutation(tool string) bool {
	if strings.HasPrefix(tool, "fs_") {
		return tool != "fs_list" && tool != "fs_read" && tool != "fs_search" && tool != "fs_stat"
	}
	if strings.HasPrefix(tool, "state_") {
		return tool != "state_get" && tool != "state_list"
	}
	switch tool {
	case "system_info", "system_env", "network_request", "network_tcp", "network_resolve", "capabilities_list", "process_inspect", "job_list", "job_get", "job_tail", "desktop_capture", "audit_tail", "policy_get", "target_list", "target_get", "target_probe", "assistant_device_status", "assistant_location", "assistant_clipboard_get", "assistant_contacts", "assistant_sms_list", "assistant_call_log":
		return false
	default:
		return true
	}
}

func isHighImpact(tool string) bool {
	switch tool {
	case "fs_remove", "process_signal", "job_signal", "service_control", "target_remove", "target_exec", "target_copy", "desktop_input", "plugins_reload", "android_api_call":
		return true
	}
	return false
}

func isSensitiveAndroid(tool string) bool {
	switch tool {
	case "assistant_sms_send", "assistant_phone_call", "assistant_camera_photo", "assistant_microphone_record", "assistant_contacts", "assistant_sms_list", "assistant_call_log":
		return true
	}
	return false
}

func numberAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}
