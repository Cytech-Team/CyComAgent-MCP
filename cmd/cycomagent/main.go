package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/audit"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/buildinfo"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/runtime"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

func main() {
	mode := flag.String("mode", envOr("CYCOM_MODE", "http"), "transport: http or stdio")
	addr := flag.String("addr", envOr("CYCOM_ADDR", "127.0.0.1:7331"), "HTTP listen address")
	stateDir := flag.String("state-dir", envOr("CYCOM_STATE_DIR", defaultStateDir()), "persistent state directory")
	pluginDir := flag.String("plugin-dir", envOr("CYCOM_PLUGIN_DIR", ""), "external tool manifest directory")
	rootSocket := flag.String("root-socket", envOr("CYCOM_ROOT_SOCKET", platform.DefaultRootSocket()), "optional local privilege-broker unix socket; currently Linux-only")
	token := flag.String("token", envOr("CYCOM_TOKEN", ""), "optional bearer/X-CyCom-Token for HTTP MCP")
	strict := flag.Bool("strict-mcp", runtime.EnvBool("CYCOM_STRICT_MCP", false), "strictly validate modern MCP routing/version headers")
	allowRemote := flag.Bool("allow-unauthenticated-remote", runtime.EnvBool("CYCOM_ALLOW_UNAUTHENTICATED_REMOTE", false), "allow non-loopback HTTP bind without CYCOM_TOKEN")
	sessionBridge := flag.Bool("session-bridge", false, "run the per-login-session execution bridge instead of the MCP runtime")
	sessionSocket := flag.String("session-socket", envOr("CYCOM_SESSION_SOCKET", sessionbridge.DefaultSocketPath()), "desktop session bridge unix socket")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVer {
		fmt.Println(buildinfo.FullVersion())
		return
	}
	if *sessionBridge {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		log.Printf("CyComAgent desktop session bridge listening on %s", *sessionSocket)
		if err := sessionbridge.Run(ctx, *sessionSocket); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *pluginDir == "" {
		*pluginDir = filepath.Join(*stateDir, "plugins.d")
	}
	// Run before any capability detection so Termux-provided binaries are found
	// on the Android root runtime, where PREFIX is not set for this process.
	platform.AugmentPATH()
	if *mode == "http" && !runtime.IsLoopbackAddr(*addr) && *token == "" && !*allowRemote {
		log.Fatal("refusing unauthenticated non-loopback bind; set CYCOM_TOKEN or explicitly allow it")
	}
	instructions := ""
	if platform.IsAndroidHybrid() {
		instructions = "CyComAgent is running in Android full_hybrid mode as the system/root automation runtime. Prefer assistant_* semantic Android tools for app-context phone actions; these are bridged through a separate Android app-context transport. Use process_exec/fs_*/target_* for root/system operations. The main runtime itself may be root while Android app APIs execute through the bridge. Sensitive phone capabilities remain policy- and Android-permission-gated."
	} else if platform.IsTermux() {
		instructions = "CyComAgent is running in Termux mobile_assistant mode. Prefer assistant_* semantic Android tools for normal phone actions and conversation-like behavior; use android_api_* only for capabilities without a semantic wrapper, and use process_exec/fs_*/target_* for advanced computer-style work. Never attempt Android device root, su, Magisk, bootloader/root escalation, or a local Android root broker. Remote SSH sudo and sudo inside non-root proot/container environments remain valid where explicitly requested and permitted. Sensitive phone capabilities are policy- and Android-permission-gated."
	} else if goruntime.GOOS == "darwin" {
		instructions = "CyComAgent is running in experimental macOS mode. The portable Go core is available; launchd, screencapture and AppleScript/cliclick adapters may require normal macOS TCC permissions. Local privileged execution is intentionally unavailable until a native macOS privilege broker exists."
	} else if goruntime.GOOS == "linux" {
		instructions = "CyComAgent is running in Linux computer_runtime mode. When anyapp_* tools are available, prefer anyapp_get_app_state plus semantic/window-targeted anyapp actions for GUI work; use desktop_capture/desktop_input as generic fallback tools. For process_exec/process_spawn, use execution_context=desktop when launching GUI apps or commands that need the active login session (Polkit, notifications, portals, keyrings, clipboard/compositor access); use service for daemon/headless work. Use structured filesystem/process/service/network tools for non-GUI operations."
	}
	rt, err := runtime.New(runtime.Config{Version: buildinfo.FullVersion(), StateDir: *stateDir, PluginDir: *pluginDir, RootSocket: *rootSocket, StrictMCP: *strict, Instructions: instructions})
	if err != nil {
		log.Fatal(err)
	}
	switch *mode {
	case "stdio":
		// Logs must stay off stdout in stdio mode.
		log.SetOutput(os.Stderr)
		if err := rt.MCP().ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
	case "http":
		runHTTP(rt, *addr, *token)
	default:
		log.Fatalf("unsupported mode %q", *mode)
	}
}

type traceCall struct {
	CallID     string    `json:"call_id"`
	SessionID  string    `json:"session_id,omitempty"`
	Time       time.Time `json:"time"`
	Tool       string    `json:"tool"`
	DurationMS int64     `json:"duration_ms"`
	OK         bool      `json:"ok"`
}
type traceGroup struct {
	TraceID    string      `json:"trace_id"`
	StartedAt  time.Time   `json:"started_at"`
	EndedAt    time.Time   `json:"ended_at"`
	TotalCalls int         `json:"total_calls"`
	Succeeded  int         `json:"succeeded"`
	Failed     int         `json:"failed"`
	Calls      []traceCall `json:"calls"`
}

func groupTraceEvents(events []audit.Event, rawQuery, traceFilter string) []traceGroup {
	groups := make(map[string]*traceGroup)
	order := make([]string, 0)
	query := strings.ToLower(strings.TrimSpace(rawQuery))
	traceFilter = strings.TrimSpace(traceFilter)
	for _, ev := range events {
		id := ev.TraceID
		if id == "" {
			id = "legacy:" + ev.CallID
		}
		if id == "legacy:" {
			id += ev.Time.Format(time.RFC3339Nano)
		}
		if traceFilter != "" && id != traceFilter {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(ev.Tool), query) && !strings.Contains(strings.ToLower(id), query) && !strings.Contains(strings.ToLower(ev.CallID), query) && !strings.Contains(strings.ToLower(ev.SessionID), query) {
			continue
		}
		g := groups[id]
		if g == nil {
			g = &traceGroup{TraceID: id, StartedAt: ev.Time, EndedAt: ev.Time, Calls: make([]traceCall, 0)}
			groups[id] = g
			order = append(order, id)
		}
		if ev.Time.Before(g.StartedAt) {
			g.StartedAt = ev.Time
		}
		if ev.Time.After(g.EndedAt) {
			g.EndedAt = ev.Time
		}
		g.TotalCalls++
		if ev.OK {
			g.Succeeded++
		} else {
			g.Failed++
		}
		g.Calls = append(g.Calls, traceCall{CallID: ev.CallID, SessionID: ev.SessionID, Time: ev.Time, Tool: ev.Tool, DurationMS: ev.DurationMS, OK: ev.OK})
	}
	out := make([]traceGroup, 0, len(order))
	for _, id := range order {
		g := groups[id]
		sort.Slice(g.Calls, func(i, j int) bool { return g.Calls[i].Time.Before(g.Calls[j].Time) })
		out = append(out, *g)
	}
	return out
}

func runHTTP(rt *runtime.Runtime, addr, token string) {
	mux := http.NewServeMux()
	mux.Handle("/mcp", rt.MCP().HTTPHandler(token))
	mux.HandleFunc("/livez", rt.Livez)
	mux.HandleFunc("/readyz", rt.Readyz)
	mux.HandleFunc("/health", rt.Health)
	mux.HandleFunc("/metrics", rt.Metrics)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		inspectorV2Page(w, r)
	})
	mux.HandleFunc("/mission-control", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/", http.StatusTemporaryRedirect) })
	mux.HandleFunc("/mission-control/assets/", missionControlAsset)
	mux.HandleFunc("/mission-control/api/snapshot", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token != "" && !validDashboardToken(req, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		snapshot, err := rt.MissionControl()
		if err != nil {
			http.Error(w, "mission control unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	mux.HandleFunc("/mission-control/api/sessions/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token != "" && !validDashboardToken(req, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := strings.Trim(strings.TrimPrefix(req.URL.Path, "/mission-control/api/sessions/"), "/")
		if id == "" || strings.Contains(id, "/") {
			http.Error(w, "invalid session id", http.StatusBadRequest)
			return
		}
		detail, err := rt.SessionDetail(id)
		if err != nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(detail)
	})
	mux.HandleFunc("/mission-control/api/traces", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token != "" && !validDashboardToken(req, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		if limit <= 0 || limit > 1000 {
			limit = 500
		}
		events, err := rt.AuditTail(limit)
		if err != nil {
			http.Error(w, "trace data unavailable", http.StatusInternalServerError)
			return
		}
		out := groupTraceEvents(events, req.URL.Query().Get("q"), req.URL.Query().Get("trace_id"))

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"traces": out, "count": len(out), "generated_at": time.Now().UTC()})
	})
	mux.HandleFunc("/inspector", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/", http.StatusTemporaryRedirect) })
	mux.HandleFunc("/inspector/assets/", inspectorV2Asset)
	mux.HandleFunc("/inspector/events", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		events, err := inspectorAuditTail(rt, 200)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		active := rt.AuditActive()
		healPath := filepath.Join(rt.StateDir(), "self-heal", "events.jsonl")
		heal := map[string]any{"status": "waiting", "event": "No Self-Healing event recorded yet"}
		if data, readErr := os.ReadFile(healPath); readErr == nil {
			lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
			if len(lines) > 0 && len(lines[len(lines)-1]) > 0 {
				var last map[string]any
				if json.Unmarshal(lines[len(lines)-1], &last) == nil {
					heal = last
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "active": active, "self_healing": heal, "now": time.Now().UTC()})
	})
	srv := &http.Server{Addr: addr, Handler: securityHeaders(requestLog(mux)), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 0, WriteTimeout: 0, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("CyComAgent-MCP %s listening on http://%s/mcp tools=%d", rt.Version(), addr, len(rt.Registry().List()))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func inspectorAuditTail(rt *runtime.Runtime, limit int) ([]audit.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	primary, err := rt.AuditTail(limit)
	if err != nil {
		return nil, err
	}
	events := append([]audit.Event(nil), primary...)
	primaryDir := filepath.Clean(filepath.Join(rt.StateDir(), "audit"))
	seenDirs := map[string]bool{primaryDir: true}
	extraDirs := make([]string, 0, 4)
	if raw := os.Getenv("CYCOM_INSPECTOR_EXTRA_AUDIT_DIRS"); raw != "" {
		extraDirs = append(extraDirs, filepath.SplitList(raw)...)
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		extraDirs = append(extraDirs, filepath.Join(xdg, "cycomagent", "audit"))
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		extraDirs = append(extraDirs, filepath.Join(home, ".local", "state", "cycomagent", "audit"))
	}
	for _, dir := range extraDirs {
		dir = filepath.Clean(dir)
		if dir == "." || dir == "" || seenDirs[dir] {
			continue
		}
		seenDirs[dir] = true
		more, tailErr := audit.TailDir(dir, limit)
		if tailErr != nil {
			continue
		}
		events = append(events, more...)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.After(events[j].Time) })
	out := make([]audit.Event, 0, limit)
	seenEvents := make(map[string]bool, len(events))
	for _, ev := range events {
		key := ev.Time.UTC().Format(time.RFC3339Nano) + "\x00" + ev.Tool + "\x00" + ev.ArgsSHA256 + "\x00" + ev.Reason + "\x00" + ev.Error
		if seenEvents[key] {
			continue
		}
		seenEvents[key] = true
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/livez" && r.URL.Path != "/readyz" {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func defaultStateDir() string {
	if os.Geteuid() == 0 {
		return "/var/lib/cycomagent"
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "cycomagent")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "cycomagent")
	}
	return ".cycomagent"
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("cycomagent ")
}

func validDashboardToken(r *http.Request, expected string) bool {
	provided := r.Header.Get("X-CyCom-Token")
	if provided == "" {
		provided = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
