// Package embed exposes CyComAgent as a reusable in-process runtime.
package embed

import (
	"context"
	"encoding/json"

	internalruntime "github.com/Cytech-Team/CyComAgent-MCP/internal/runtime"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
)

type Config struct {
	Version      string
	StateDir     string
	PluginDir    string
	RootSocket   string
	StrictMCP    bool
	Instructions string
}

type Handler func(context.Context, json.RawMessage) (any, error)

type Tool struct {
	Name         string         `json:"name"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
	Source       string         `json:"source,omitempty"`
	Handler      Handler        `json:"-"`
}

type Runtime struct {
	inner *internalruntime.Runtime
}

func New(cfg Config) (*Runtime, error) {
	rt, err := internalruntime.New(internalruntime.Config{
		Version:      cfg.Version,
		StateDir:     cfg.StateDir,
		PluginDir:    cfg.PluginDir,
		RootSocket:   cfg.RootSocket,
		StrictMCP:    cfg.StrictMCP,
		Instructions: cfg.Instructions,
	})
	if err != nil {
		return nil, err
	}
	return &Runtime{inner: rt}, nil
}

func (r *Runtime) Version() string {
	if r == nil || r.inner == nil {
		return ""
	}
	return r.inner.Version()
}

func (r *Runtime) StateDir() string {
	if r == nil || r.inner == nil {
		return ""
	}
	return r.inner.StateDir()
}

func (r *Runtime) ListTools() []Tool {
	if r == nil || r.inner == nil {
		return nil
	}
	items := r.inner.Registry().List()
	out := make([]Tool, 0, len(items))
	for _, item := range items {
		out = append(out, Tool{
			Name:         item.Name,
			Title:        item.Title,
			Description:  item.Description,
			InputSchema:  item.InputSchema,
			OutputSchema: item.OutputSchema,
			Annotations:  item.Annotations,
			Source:       item.Source,
		})
	}
	return out
}

func (r *Runtime) Call(ctx context.Context, name string, args json.RawMessage) (any, error) {
	return r.inner.Registry().Call(ctx, name, args)
}


func (r *Runtime) RegisterTool(tool Tool) error {
	if r == nil || r.inner == nil {
		return context.Canceled
	}
	return r.inner.Registry().Upsert(registry.Tool{
		Name:         tool.Name,
		Title:        tool.Title,
		Description:  tool.Description,
		InputSchema:  tool.InputSchema,
		OutputSchema: tool.OutputSchema,
		Annotations:  tool.Annotations,
		Source:       tool.Source,
		Handler:      registry.Handler(tool.Handler),
	})
}

func (r *Runtime) RemoveSource(source string) {
	if r == nil || r.inner == nil || source == "" {
		return
	}
	r.inner.Registry().RemoveSource(source)
}
