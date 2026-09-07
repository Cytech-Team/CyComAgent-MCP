package tools

import (
	"github.com/Cytech-Team/CyComAgent-MCP/internal/audit"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/plugins"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/policy"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/state"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/targets"
)

type Dependencies struct {
	Jobs     *jobs.Manager
	Broker   broker.Client
	Plugins  *plugins.Manager
	State    *state.Store
	Targets  *targets.Manager
	Audit    *audit.Logger
	Policy   *policy.Engine
	StateDir string
	Version  string
}

func Register(r *registry.Registry, deps Dependencies) {
	registerFilesystem(r)
	registerProcess(r, processDeps{Jobs: deps.Jobs, Broker: deps.Broker})
	registerJobs(r, deps.Jobs)
	registerSystem(r, systemDeps{Broker: deps.Broker, Plugins: deps.Plugins, Policy: deps.Policy, Targets: deps.Targets, StateDir: deps.StateDir, Version: deps.Version})
	registerTermuxAPI(r, deps.Policy)
	registerTermuxAssistant(r)
	registerDesktop(r, deps.StateDir)
	registerAnyApp(r)
	registerState(r, deps.State)
	registerTargets(r, deps.Targets, deps.Broker)
	registerGovernance(r, deps.Audit, deps.Policy)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
