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

// SupportsLocalPrivilege reports whether CyComAgent supports its local privilege broker on this host.
// Android/Termux is intentionally non-root only. Rooted Android is not supported and is not planned.
func SupportsLocalPrivilege() bool {
	return runtime.GOOS != "android" && !IsTermux()
}

// DefaultRootSocket returns the Linux broker path on supported hosts. Android/Termux
// intentionally returns an empty path so no local root broker can be configured by default.
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
