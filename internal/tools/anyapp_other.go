//go:build !linux

package tools

import "github.com/Cytech-Team/CyComAgent-MCP/internal/registry"

func registerAnyApp(*registry.Registry)            {}
func anyAppInstalledBackend() (string, bool)       { return "", false }
func anyAppToolPrefixCount(*registry.Registry) int { return 0 }
func anyAppBackendBase(path string) string         { return path }
