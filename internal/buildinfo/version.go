package buildinfo

import (
	_ "embed"
	"runtime/debug"
	"strings"
)

// versionText is the single source of truth for the CyComAgent release version.
//
//go:embed VERSION
var versionText string

func Version() string {
	return strings.TrimSpace(versionText)
}

func FullVersion() string {
	base := Version()
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return base
	}
	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision != "" {
		base += "+" + revision
	}
	if modified {
		base += ".dirty"
	}
	return base
}
