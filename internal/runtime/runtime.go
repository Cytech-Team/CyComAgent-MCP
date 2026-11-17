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
	"github.com/Cytech-Team/CyComAgent-MCP/internal/desktopruntime"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation/remotejobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/fleet"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	mcpserver "github.com/Cytech-Team/CyComAgent-MCP/internal/mcp"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/plugins"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/policy"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessions"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/state"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/targets"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/tools"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/workflows"
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
	sessions  *sessions.Manager
	workflows *workflows.Manager
	fleet     *fleet.Manager
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
	sm, err := sessions.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	al, err := audit.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	wf, err := workflows.New(cfg.StateDir, jm)
	if err != nil {
		return nil, fmt.Errorf("workflows: %w", err)
	}
	fm := fleet.New(cfg.Version)
	go fm.Refresh()
	reg := registry.New()
	// Policy runs before execution; audit records both denied and executed calls.
	reg.AddInterceptor(pe)
	reg.AddInterceptor(al)
	root := broker.Client{Socket: cfg.RootSocket}
	session := sessionbridge.NewClient("")
	identity, err := nodeidentity.New(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("federation identity: %w", err)
	}
	fc := federation.New(tm, identity)
	rjl, err := remotejobs.Open(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("remote job ledger: %w", err)
	}
	tools.Register(reg, tools.Dependencies{Jobs: jm, Broker: root, Plugins: pm, State: st, Targets: tm, Audit: al, Policy: pe, StateDir: cfg.StateDir, Version: cfg.Version, Session: session, Sessions: sm, Workflows: wf, Fleet: fm, Federation: fc, Identity: identity, RemoteJobs: rjl})
	if pe.Snapshot().AllowExternalPlugins {
		loaded, err := pm.LoadInto(reg)
		if err != nil {
			return nil, fmt.Errorf("plugins: %w", err)
		}
		_ = loaded
	}
	srv := &mcpserver.Server{Name: "CyComAgent-MCP", Version: cfg.Version, Instructions: cfg.Instructions, Registry: reg, Strict: cfg.StrictMCP, CompatMissingReason: true, Sessions: sm}
	rt := &Runtime{cfg: cfg, started: time.Now(), server: srv, registry: reg, state: st, jobs: jm, plugins: pm, targets: tm, policy: pe, audit: al, sessions: sm, workflows: wf, fleet: fm}
	_ = st.Put("runtime", "identity", map[string]any{"name": "CyComAgent-MCP", "version": cfg.Version, "started_at": rt.started.UTC()})
	return rt, nil
}

type MissionControlSnapshot struct {
	Sessions    []SessionSummary    `json:"sessions"`
	Workflows   []WorkflowSummary   `json:"workflows"`
	Fleet       fleet.Snapshot      `json:"fleet"`
	GeneratedAt time.Time           `json:"generated_at"`
	Runtime     map[string]any      `json:"runtime"`
	Counts      map[string]int      `json:"counts"`
	Tools       []ToolSummary       `json:"tools"`
	Jobs        []JobSummary        `json:"jobs"`
	Targets     []TargetSummary     `json:"targets"`
	Plugins     []string            `json:"plugins"`
	Active      []ActiveCallSummary `json:"active_calls"`
	Events      []AuditSummary      `json:"recent_events"`
	Desktop     DesktopSummary      `json:"desktop"`
}

type WorkflowSummary struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	SessionID string                `json:"session_id,omitempty"`
	Status    string                `json:"status"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
	Steps     []WorkflowStepSummary `json:"steps"`
}
type WorkflowStepSummary struct {
	ID        string    `json:"id"`
	DependsOn []string  `json:"depends_on,omitempty"`
	Status    string    `json:"status"`
	JobID     string    `json:"job_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SessionSummary struct {
	ID             string    `json:"id"`
	ParentID       string    `json:"parent_id,omitempty"`
	Name           string    `json:"name,omitempty"`
	Owner          string    `json:"owner,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	Children       int       `json:"children"`
	Traces         int       `json:"traces"`
}
type ToolSummary struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
}
type JobSummary struct {
	ID               string    `json:"id"`
	Status           string    `json:"status"`
	PID              int       `json:"pid"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	ExecutionContext string    `json:"execution_context,omitempty"`
	SessionID        string    `json:"session_id,omitempty"`
}
type TargetSummary struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Enabled   bool   `json:"enabled"`
}
type AuditSummary struct {
	TraceID    string    `json:"trace_id,omitempty"`
	CallID     string    `json:"call_id,omitempty"`
	Time       time.Time `json:"time"`
	Tool       string    `json:"tool"`
	DurationMS int64     `json:"duration_ms"`
	OK         bool      `json:"ok"`
	SessionID  string    `json:"session_id,omitempty"`
}
type ActiveCallSummary struct {
	TraceID string    `json:"trace_id,omitempty"`
	CallID  string    `json:"call_id"`
	Time    time.Time `json:"time"`
	Tool    string    `json:"tool"`
}
type DesktopSummary struct {
	Platform    string                   `json:"platform"`
	SessionType string                   `json:"session_type,omitempty"`
	Desktop     string                   `json:"desktop,omitempty"`
	Adapters    []desktopruntime.Adapter `json:"adapters"`
}

// MissionControl returns a read-only, deliberately redacted projection of live runtime state.
// It never exposes job commands/environments, SSH hosts/identity paths, or display socket values.
func workflowSummaries(m *workflows.Manager) []WorkflowSummary {
	items, err := m.List()
	if err != nil {
		return []WorkflowSummary{}
	}
	out := make([]WorkflowSummary, 0, len(items))
	for _, w := range items {
		x := WorkflowSummary{ID: w.ID, Name: w.Name, SessionID: w.SessionID, Status: w.Status, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, Steps: make([]WorkflowStepSummary, 0, len(w.Steps))}
		for _, s := range w.Steps {
			x.Steps = append(x.Steps, WorkflowStepSummary{ID: s.ID, DependsOn: append([]string(nil), s.DependsOn...), Status: s.Status, JobID: s.JobID, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt})
		}
		out = append(out, x)
	}
	return out
}

func (r *Runtime) MissionControl() (MissionControlSnapshot, error) {
	jobsList := r.jobs.List()
	jobSummaries := make([]JobSummary, 0, len(jobsList))
	running := 0
	for _, j := range jobsList {
		jobSummaries = append(jobSummaries, JobSummary{ID: j.ID, Status: j.Status, PID: j.PID, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, ExecutionContext: j.ExecutionContext, SessionID: j.SessionID})
		if strings.HasPrefix(j.Status, "running") || j.Status == "terminating" {
			running++
		}
	}
	targetsList := r.targets.List()
	targetSummaries := make([]TargetSummary, 0, len(targetsList))
	for _, t := range targetsList {
		targetSummaries = append(targetSummaries, TargetSummary{Name: t.Name, Transport: t.Transport, Enabled: t.Enabled})
	}
	toolsList := r.registry.List()
	toolSummaries := make([]ToolSummary, 0, len(toolsList))
	for _, t := range toolsList {
		toolSummaries = append(toolSummaries, ToolSummary{Name: t.Name, Title: t.Title, Description: t.Description, Source: t.Source})
	}
	events, err := r.audit.Tail(100)
	if err != nil {
		return MissionControlSnapshot{}, err
	}
	eventSummaries := make([]AuditSummary, 0, len(events))
	for _, e := range events {
		eventSummaries = append(eventSummaries, AuditSummary{TraceID: e.TraceID, CallID: e.CallID, Time: e.Time, Tool: e.Tool, DurationMS: e.DurationMS, OK: e.OK, SessionID: e.SessionID})
	}
	plugins := r.plugins.Names()
	sessionRecords := r.sessions.List()
	sessionSummaries := make([]SessionSummary, 0, len(sessionRecords))
	for _, ss := range sessionRecords {
		sessionSummaries = append(sessionSummaries, SessionSummary{ID: ss.ID, ParentID: ss.ParentID, Name: ss.Name, Owner: ss.Owner, Status: ss.Status, CreatedAt: ss.CreatedAt, UpdatedAt: ss.UpdatedAt, LastActivityAt: ss.LastActivityAt, Children: len(ss.Children), Traces: len(ss.TraceIDs)})
	}
	activeRaw := r.audit.Active()
	active := make([]ActiveCallSummary, 0, len(activeRaw))
	for _, a := range activeRaw {
		active = append(active, ActiveCallSummary{TraceID: a.TraceID, CallID: a.ID, Time: a.Time, Tool: a.Tool})
	}
	policyMode := r.policy.Snapshot().Mode
	hostname, _ := os.Hostname()
	runtimeInfo := map[string]any{"status": "healthy", "version": r.cfg.Version, "hostname": hostname, "pid": os.Getpid(), "goos": goruntime.GOOS, "goarch": goruntime.GOARCH, "uptime_seconds": int64(time.Since(r.started).Seconds()), "policy_mode": policyMode, "state_store": "available", "audit": "available"}
	desktop := desktopruntime.Detect()
	return MissionControlSnapshot{GeneratedAt: time.Now().UTC(), Runtime: runtimeInfo, Counts: map[string]int{"tools": len(toolSummaries), "jobs_total": len(jobSummaries), "jobs_running": running, "targets": len(targetSummaries), "plugins": len(plugins), "active_calls": len(active), "sessions": len(sessionSummaries), "recent_events": len(eventSummaries)}, Tools: toolSummaries, Jobs: jobSummaries, Targets: targetSummaries, Plugins: plugins, Active: active, Events: eventSummaries, Sessions: sessionSummaries, Workflows: workflowSummaries(r.workflows), Fleet: r.fleet.Snapshot(), Desktop: DesktopSummary{Platform: desktop.Platform, SessionType: desktop.SessionType, Desktop: desktop.Desktop, Adapters: desktop.Adapters}}, nil
}

func (r *Runtime) MCP() *mcpserver.Server       { return r.server }
func (r *Runtime) Version() string              { return r.cfg.Version }
func (r *Runtime) Registry() *registry.Registry { return r.registry }
func (r *Runtime) StateDir() string             { return r.cfg.StateDir }

type SessionDetail struct {
	Session  SessionSummary   `json:"session"`
	Parent   *SessionSummary  `json:"parent,omitempty"`
	Children []SessionSummary `json:"children"`
	Jobs     []JobSummary     `json:"jobs"`
	Traces   []string         `json:"traces"`
	Events   []AuditSummary   `json:"events"`
}

// SessionDetail returns a redacted read-only view of one Session and its linked runtime activity.
func (r *Runtime) SessionDetail(id string) (SessionDetail, error) {
	ss, err := r.sessions.Get(id)
	if err != nil {
		return SessionDetail{}, err
	}
	summary := func(v sessions.Session) SessionSummary {
		return SessionSummary{ID: v.ID, ParentID: v.ParentID, Name: v.Name, Owner: v.Owner, Status: v.Status, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, LastActivityAt: v.LastActivityAt, Children: len(v.Children), Traces: len(v.TraceIDs)}
	}
	out := SessionDetail{Session: summary(ss), Children: []SessionSummary{}, Jobs: []JobSummary{}, Traces: append([]string(nil), ss.TraceIDs...), Events: []AuditSummary{}}
	if ss.ParentID != "" {
		if p, e := r.sessions.Get(ss.ParentID); e == nil {
			v := summary(p)
			out.Parent = &v
		}
	}
	for _, childID := range ss.Children {
		if child, e := r.sessions.Get(childID); e == nil {
			out.Children = append(out.Children, summary(child))
		}
	}
	for _, j := range r.jobs.List() {
		if j.SessionID == id {
			out.Jobs = append(out.Jobs, JobSummary{ID: j.ID, Status: j.Status, PID: j.PID, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, ExecutionContext: j.ExecutionContext, SessionID: j.SessionID})
		}
	}
	events, e := r.audit.Tail(1000)
	if e != nil {
		return SessionDetail{}, e
	}
	for _, ev := range events {
		if ev.SessionID == id {
			out.Events = append(out.Events, AuditSummary{TraceID: ev.TraceID, CallID: ev.CallID, Time: ev.Time, Tool: ev.Tool, DurationMS: ev.DurationMS, OK: ev.OK, SessionID: ev.SessionID})
		}
	}
	return out, nil
}

func (r *Runtime) AuditTail(limit int) ([]audit.Event, error) { return r.audit.Tail(limit) }
func (r *Runtime) AuditActive() []audit.ActiveCall            { return r.audit.Active() }

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
	if platform.IsAndroidHybrid() {
		payload["runtime_profile"] = "android_full_hybrid"
		payload["android_device_root_support"] = "runtime_root"
		payload["android_device_root_planned"] = true
	} else if platform.IsTermux() {
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
