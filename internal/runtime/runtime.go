package runtime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/audit"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	mcpserver "github.com/Cytech-Team/CyComAgent-MCP/internal/mcp"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/plugins"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/policy"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/state"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/targets"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/tools"
)

type Config struct {
	Version      string
	StateDir     string
	PluginDir    string
	RootSocket   string
	StrictMCP    bool
	Instructions string
}

type Runtime struct {
	cfg       Config
	started   time.Time
	server    *mcpserver.Server
	registry  *registry.Registry
	state     *state.Store
	jobs      *jobs.Manager
	plugins   *plugins.Manager
	targets   *targets.Manager
	policy    *policy.Engine
	audit     *audit.Logger
	requests  atomic.Uint64
	healthReq atomic.Uint64
}

func New(cfg Config) (*Runtime, error) {
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.StateDir == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	if cfg.PluginDir == "" {
		cfg.PluginDir = cfg.StateDir + "/plugins.d"
	}
	if !platform.SupportsLocalPrivilege() {
		// Android/Termux is intentionally non-root only. Ignore any supplied broker socket.
		cfg.RootSocket = ""
	} else if cfg.RootSocket == "" {
		cfg.RootSocket = platform.DefaultRootSocket()
	}
	if cfg.Instructions == "" {
		cfg.Instructions = "CyComAgent-MCP exposes generic local-computer primitives. Prefer structured filesystem/process/service/network tools; use process_exec as the universal escape hatch. Long-running work should use process_spawn and job_* handles rather than relying on an MCP session."
	}
	st, err := state.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	jm, err := jobs.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}
	pm := plugins.New(cfg.PluginDir)
	tm, err := targets.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("targets: %w", err)
	}
	pe, err := policy.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	al, err := audit.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	reg := registry.New()
	// Policy runs before execution; audit records both denied and executed calls.
	reg.AddInterceptor(pe)
	reg.AddInterceptor(al)
	root := broker.Client{Socket: cfg.RootSocket}
	session := sessionbridge.NewClient("")
	tools.Register(reg, tools.Dependencies{Jobs: jm, Broker: root, Plugins: pm, State: st, Targets: tm, Audit: al, Policy: pe, StateDir: cfg.StateDir, Version: cfg.Version, Session: session})
	if pe.Snapshot().AllowExternalPlugins {
		loaded, err := pm.LoadInto(reg)
		if err != nil {
			return nil, fmt.Errorf("plugins: %w", err)
		}
		_ = loaded
	}
	srv := &mcpserver.Server{Name: "CyComAgent-MCP", Version: cfg.Version, Instructions: cfg.Instructions, Registry: reg, Strict: cfg.StrictMCP}
	rt := &Runtime{cfg: cfg, started: time.Now(), server: srv, registry: reg, state: st, jobs: jm, plugins: pm, targets: tm, policy: pe, audit: al}
	_ = st.Put("runtime", "identity", map[string]any{"name": "CyComAgent-MCP", "version": cfg.Version, "started_at": rt.started.UTC()})
	return rt, nil
}

func (r *Runtime) MCP() *mcpserver.Server       { return r.server }
func (r *Runtime) Version() string              { return r.cfg.Version }
func (r *Runtime) Registry() *registry.Registry { return r.registry }
func (r *Runtime) StateDir() string             { return r.cfg.StateDir }

func (r *Runtime) Livez(w http.ResponseWriter, _ *http.Request) {
	r.healthReq.Add(1)
	jsonReply(w, http.StatusOK, map[string]any{"status": "alive", "version": r.cfg.Version})
}
func (r *Runtime) Readyz(w http.ResponseWriter, _ *http.Request) {
	r.healthReq.Add(1)
	probe := r.cfg.StateDir + "/.ready"
	if err := os.WriteFile(probe, []byte(time.Now().Format(time.RFC3339Nano)), 0o600); err != nil {
		jsonReply(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "reason": err.Error()})
		return
	}
	_ = os.Remove(probe)
	jsonReply(w, http.StatusOK, map[string]any{"status": "ready", "tools": len(r.registry.List())})
}
func (r *Runtime) Health(w http.ResponseWriter, _ *http.Request) {
	r.healthReq.Add(1)
	hostname, _ := os.Hostname()
	jobsList := r.jobs.List()
	running := 0
	for _, j := range jobsList {
		if strings.HasPrefix(j.Status, "running") || j.Status == "terminating" {
			running++
		}
	}
	payload := map[string]any{"status": "healthy", "version": r.cfg.Version, "pid": os.Getpid(), "hostname": hostname, "goos": goruntime.GOOS, "goarch": goruntime.GOARCH, "uptime_seconds": int64(time.Since(r.started).Seconds()), "state_dir": r.cfg.StateDir, "plugin_dir": r.cfg.PluginDir, "tools": len(r.registry.List()), "plugins": r.plugins.Names(), "targets": r.targets.Count(), "policy_mode": r.policy.Snapshot().Mode, "audit_enabled": true, "jobs_total": len(jobsList), "jobs_running": running}
	if platform.IsTermux() {
		payload["runtime_profile"] = "mobile_assistant"
		payload["android_device_root_support"] = "not_supported"
		payload["android_device_root_planned"] = false
	}
	jsonReply(w, http.StatusOK, payload)
}
func (r *Runtime) Metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	m := r.server.Metrics()
	_, _ = fmt.Fprintf(w, "# HELP cycomagent_uptime_seconds Runtime uptime.\n# TYPE cycomagent_uptime_seconds gauge\ncycomagent_uptime_seconds %d\n", int64(time.Since(r.started).Seconds()))
	_, _ = fmt.Fprintf(w, "# TYPE cycomagent_tools gauge\ncycomagent_tools %d\n", len(r.registry.List()))
	for k, v := range m {
		_, _ = fmt.Fprintf(w, "# TYPE cycomagent_%s counter\ncycomagent_%s %d\n", k, k, v)
	}
}

func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func IsLoopbackAddr(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = strings.Trim(addr[:i], "[]")
	}
	if host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	return false
}

func EnvBool(name string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
func EnvInt(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
