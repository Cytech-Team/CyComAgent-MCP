//go:build !linux

package tools

import (
	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

func registerSessionControl(_ *registry.Registry, _ broker.Client) {}
