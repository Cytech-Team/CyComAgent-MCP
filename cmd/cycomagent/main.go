package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"syscall"
	"time"

	"github.com/Cytech-Team/CyComAgent-MCP/internal/platform"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/runtime"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
)

var version = "0.4.8-isolated-desktop-dev"

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
		fmt.Println(version)
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
	if *mode == "http" && !runtime.IsLoopbackAddr(*addr) && *token == "" && !*allowRemote {
		log.Fatal("refusing unauthenticated non-loopback bind; set CYCOM_TOKEN or explicitly allow it")
	}
	instructions := ""
	if platform.IsTermux() {
		instructions = "CyComAgent is running in Termux mobile_assistant mode. Prefer assistant_* semantic Android tools for normal phone actions and conversation-like behavior; use android_api_* only for capabilities without a semantic wrapper, and use process_exec/fs_*/target_* for advanced computer-style work. Never attempt Android device root, su, Magisk, bootloader/root escalation, or a local Android root broker. Remote SSH sudo and sudo inside non-root proot/container environments remain valid where explicitly requested and permitted. Sensitive phone capabilities are policy- and Android-permission-gated."
	} else if goruntime.GOOS == "darwin" {
		instructions = "CyComAgent is running in experimental macOS mode. The portable Go core is available; launchd, screencapture and AppleScript/cliclick adapters may require normal macOS TCC permissions. Local privileged execution is intentionally unavailable until a native macOS privilege broker exists."
	} else if goruntime.GOOS == "linux" {
		instructions = "CyComAgent is running in Linux computer_runtime mode. When anyapp_* tools are available, prefer anyapp_get_app_state plus semantic/window-targeted anyapp actions for GUI work; use desktop_capture/desktop_input as generic fallback tools. For process_exec/process_spawn, use execution_context=desktop when launching GUI apps or commands that need the active login session (Polkit, notifications, portals, keyrings, clipboard/compositor access); use service for daemon/headless work. Use structured filesystem/process/service/network tools for non-GUI operations."
	}
	rt, err := runtime.New(runtime.Config{Version: version, StateDir: *stateDir, PluginDir: *pluginDir, RootSocket: *rootSocket, StrictMCP: *strict, Instructions: instructions})
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

func runHTTP(rt *runtime.Runtime, addr, token string) {
	mux := http.NewServeMux()
	mux.Handle("/mcp", rt.MCP().HTTPHandler(token))
	mux.HandleFunc("/livez", rt.Livez)
	mux.HandleFunc("/readyz", rt.Readyz)
	mux.HandleFunc("/health", rt.Health)
	mux.HandleFunc("/metrics", rt.Metrics)
	mux.HandleFunc("/inspector", inspectorPage)
	mux.HandleFunc("/inspector/events", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		events, err := rt.AuditTail(200)
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

func inspectorPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>CyComAgent Live Inspector</title><style>:root{color-scheme:dark}*{box-sizing:border-box}body{margin:0;background:#0b0d12;color:#e8eaf0;font:14px system-ui,sans-serif}.wrap{max-width:1100px;margin:auto;padding:28px}.top{display:flex;justify-content:space-between;align-items:center}.live,.ok{color:#55e68a}.bad{color:#ff6b78}.cards{display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin:22px 0}.card,.event{background:#141821;border:1px solid #252b38;border-radius:14px;padding:16px}.num{font-size:25px;font-weight:700}.event{margin:10px 0;display:grid;grid-template-columns:110px 190px 100px 1fr;gap:14px}.tool{font-family:monospace;font-weight:700}.reason{color:#c7ccd8}.muted{color:#7f8798;font-size:12px}@media(max-width:760px){.cards{grid-template-columns:1fr 1fr}.event{grid-template-columns:1fr}.wrap{padding:16px}}</style></head><body><div class="wrap"><div class="top"><div><h1>CyComAgent Live Inspector</h1><div class="muted">Every tool call must include a reason.</div></div><b class="live">● LIVE</b></div><div class="cards"><div class="card"><div class="muted">Calls</div><div id="calls" class="num">-</div></div><div class="card"><div class="muted">Success</div><div id="success" class="num">-</div></div><div class="card"><div class="muted">Failed</div><div id="failed" class="num">-</div></div><div class="card"><div class="muted">Last update</div><div id="updated" class="num" style="font-size:17px">-</div></div></div><div id="events"></div></div><script>function esc(s){var d=document.createElement('div');d.textContent=s==null?'':String(s);return d.innerHTML}async function tick(){try{var r=await fetch('/inspector/events',{cache:'no-store'}),d=await r.json(),e=d.events||[];document.getElementById('calls').textContent=e.length;document.getElementById('success').textContent=e.filter(function(x){return x.ok}).length;document.getElementById('failed').textContent=e.filter(function(x){return !x.ok}).length;document.getElementById('updated').textContent=new Date().toLocaleTimeString();document.getElementById('events').innerHTML=e.map(function(x){return '<div class="event"><div><div>'+new Date(x.time).toLocaleTimeString()+'</div><div class="muted">'+x.duration_ms+' ms</div></div><div class="tool">'+esc(x.tool)+'</div><div class="'+(x.ok?'ok':'bad')+'">'+(x.ok?'✓ Success':'✕ Failed')+'</div><div><div class="reason">'+esc(x.reason||'Legacy call - no reason recorded')+'</div>'+(x.error?'<div class="bad muted">'+esc(x.error)+'</div>':'')+'</div></div>'}).join('')}catch(e){document.getElementById('updated').textContent='Disconnected'}}tick();setInterval(tick,750);</script></body></html>`))
}
