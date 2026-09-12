package main

import (
	"context"
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

var version = "0.4.7-session-routing-dev"

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
