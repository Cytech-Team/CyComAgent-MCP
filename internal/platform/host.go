package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// IsTermux reports whether the runtime appears to be running inside Termux.
// GOOS=android is intentionally not sufficient by itself because CyComAgent may
// eventually gain other Android hosts that do not use Termux filesystem paths.
func IsTermux() bool {
	if os.Getenv("TERMUX_VERSION") != "" {
		return true
	}
	prefix := filepath.Clean(os.Getenv("PREFIX"))
	return prefix != "." && strings.Contains(prefix, "/com.termux/")
}

// IsAndroidHybrid reports whether the Android runtime is a root/system host
// that bridges Android app APIs through a separate app-context adapter.
func IsAndroidHybrid() bool {
	return runtime.GOOS == "android" && strings.TrimSpace(os.Getenv("CYCOM_ANDROID_HYBRID")) != ""
}

// HasAndroidAssistant reports whether semantic Android assistant tools can be exposed.
func HasAndroidAssistant() bool {
	return IsTermux() || IsAndroidHybrid()
}

// DefaultShell returns a shell that exists in the current host environment.
// Termux does not provide /bin/sh; its shell lives below $PREFIX/bin.
func DefaultShell() string {
	if shell := strings.TrimSpace(os.Getenv("CYCOM_SHELL")); shell != "" {
		return shell
	}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	if IsTermux() {
		if prefix := strings.TrimSpace(os.Getenv("PREFIX")); prefix != "" {
			candidate := filepath.Join(prefix, "bin", "sh")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	if _, err := os.Stat("/bin/sh"); err == nil {
		return "/bin/sh"
	}
	if shell, err := exec.LookPath("sh"); err == nil {
		return shell
	}
	return "sh"
}

// TermuxBinDirs returns candidate Termux bin directories. On the Android hybrid
// runtime the agent itself is a root/system process, so PREFIX is unset even
// though a Termux installation may exist on the device and hold useful tooling
// (git, node, java, curl). Looking those directories up explicitly keeps those
// binaries discoverable instead of silently reporting them as missing.
func TermuxBinDirs() []string {
	var out []string
	if prefix := strings.TrimSpace(os.Getenv("PREFIX")); prefix != "" {
		out = append(out, filepath.Join(prefix, "bin"))
	}
	if IsAndroidHybrid() {
		out = append(out,
			"/data/data/com.termux/files/usr/bin",
			"/data/data/com.termux/files/bin",
		)
	}
	return out
}

// AugmentPATH prepends any discovered Termux bin directories to PATH so
// exec.LookPath and child processes can find those binaries. Directories that
// do not exist are skipped, and PATH is left untouched when nothing is added so
// non-Android hosts keep their exact environment.
func AugmentPATH() string {
	current := os.Getenv("PATH")
	parts := strings.Split(current, string(os.PathListSeparator))
	existing := map[string]bool{}
	for _, p := range parts {
		if p != "" {
			existing[p] = true
		}
	}
	var added []string
	for _, dir := range TermuxBinDirs() {
		if existing[dir] {
			continue
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		added = append(added, dir)
	}
	if len(added) == 0 {
		return current
	}
	joined := strings.Join(append(added, parts...), string(os.PathListSeparator))
	_ = os.Setenv("PATH", joined)
	return joined
}

// IsDarwin reports whether the runtime is macOS.
func IsDarwin() bool {
	return runtime.GOOS == "darwin"
}

// SupportsLocalPrivilege reports whether CyComAgent supports its local privilege broker on this host.
// The broker is currently Linux-only. Android/Termux and macOS run without the local root broker.
func SupportsLocalPrivilege() bool {
	return runtime.GOOS == "linux" && !IsTermux()
}

// DefaultRootSocket returns the Linux broker path on supported hosts. Non-Linux
// platforms intentionally return an empty path until they have a native privilege adapter.
func DefaultRootSocket() string {
	if !SupportsLocalPrivilege() {
		return ""
	}
	return "/run/cycomagent/root.sock"
}

// TermuxServiceDir returns the runit service directory used by termux-services.
func TermuxServiceDir() string {
	if svdir := strings.TrimSpace(os.Getenv("SVDIR")); svdir != "" {
		return svdir
	}
	if prefix := strings.TrimSpace(os.Getenv("PREFIX")); prefix != "" {
		return filepath.Join(prefix, "var", "service")
	}
	return ""
}
