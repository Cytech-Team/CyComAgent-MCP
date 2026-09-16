package desktopruntime

import (
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

type Capability string

const (
	Capture         Capability = "desktop.capture"
	Input           Capability = "desktop.input"
	Semantic        Capability = "desktop.semantic"
	Window          Capability = "desktop.window"
	IsolatedDesktop Capability = "desktop.isolated"
)

type Adapter struct {
	Name         string       `json:"name"`
	Family       string       `json:"family"`
	Capabilities []Capability `json:"capabilities"`
	Score        int          `json:"score"`
	Available    bool         `json:"available"`
	Isolated     bool         `json:"isolated"`
	Reason       string       `json:"reason,omitempty"`
}

type Snapshot struct {
	Platform       string    `json:"platform"`
	SessionType    string    `json:"session_type,omitempty"`
	Desktop        string    `json:"desktop,omitempty"`
	WaylandDisplay string    `json:"wayland_display,omitempty"`
	Display        string    `json:"display,omitempty"`
	Adapters       []Adapter `json:"adapters"`
}

func Detect() Snapshot {
	s := Snapshot{Platform: runtime.GOOS + "/" + runtime.GOARCH, SessionType: os.Getenv("XDG_SESSION_TYPE"), Desktop: os.Getenv("XDG_CURRENT_DESKTOP"), WaylandDisplay: os.Getenv("WAYLAND_DISPLAY"), Display: os.Getenv("DISPLAY")}
	has := func(bin string) bool { _, err := exec.LookPath(bin); return err == nil }
	add := func(a Adapter) {
		if a.Available {
			a.Reason = "detected"
		}
		s.Adapters = append(s.Adapters, a)
	}
	if runtime.GOOS == "linux" {
		wayland := s.WaylandDisplay != "" || strings.EqualFold(s.SessionType, "wayland")
		x11 := s.Display != "" || strings.EqualFold(s.SessionType, "x11")
		wlroots := wayland && (has("wlr-randr") || has("grim"))
		add(Adapter{Name: "wlroots", Family: "wayland", Capabilities: []Capability{Capture, Input, Window, IsolatedDesktop}, Score: 120, Available: wlroots, Isolated: true})
		add(Adapter{Name: "libei", Family: "wayland", Capabilities: []Capability{Input}, Score: 115, Available: wayland && (has("ei-debug-events") || has("libei-test-client")), Isolated: true})
		add(Adapter{Name: "portal", Family: "wayland", Capabilities: []Capability{Capture, Input}, Score: 110, Available: wayland && has("gdbus"), Isolated: true})
		add(Adapter{Name: "x11-xtest", Family: "x11", Capabilities: []Capability{Capture, Input, Window}, Score: 100, Available: x11 && has("xdotool")})
		add(Adapter{Name: "uinput", Family: "linux", Capabilities: []Capability{Input}, Score: 40, Available: has("ydotool"), Reason: "global fallback; may share physical desktop input"})
	}
	if runtime.GOOS == "darwin" {
		add(Adapter{Name: "macos", Family: "darwin", Capabilities: []Capability{Capture, Input, Window}, Score: 100, Available: has("osascript") || has("screencapture")})
	}
	sort.SliceStable(s.Adapters, func(i, j int) bool { return s.Adapters[i].Score > s.Adapters[j].Score })
	return s
}

func (s Snapshot) Best(cap Capability, requireIsolation bool) (Adapter, bool) {
	for _, a := range s.Adapters {
		if !a.Available || (requireIsolation && !a.Isolated) {
			continue
		}
		for _, c := range a.Capabilities {
			if c == cap {
				return a, true
			}
		}
	}
	return Adapter{}, false
}
