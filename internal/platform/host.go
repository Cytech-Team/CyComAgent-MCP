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
