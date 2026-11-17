package tools

import (
	"github.com/Cytech-Team/CyComAgent-MCP/internal/audit"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/broker"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/federation/remotejobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/fleet"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/jobs"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/nodeidentity"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/plugins"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/policy"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/registry"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessionbridge"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/sessions"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/state"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/targets"
	"github.com/Cytech-Team/CyComAgent-MCP/internal/workflows"
)

type Dependencies struct {
	Jobs       *jobs.Manager
	Broker     broker.Client
	Plugins    *plugins.Manager
	State      *state.Store
	Targets    *targets.Manager
	Audit      *audit.Logger
	Policy     *policy.Engine
	StateDir   string
	Version    string
	Session    sessionbridge.Client
	Sessions   *sessions.Manager
	Workflows  *workflows.Manager
	Fleet      *fleet.Manager
	Federation *federation.Client
	Identity   *nodeidentity.Manager
	RemoteJobs *remotejobs.Ledger
}

func Register(r *registry.Registry, deps Dependencies) {
	registerFilesystem(r)
	registerProcess(r, processDeps{Jobs: deps.Jobs, Broker: deps.Broker, Session: deps.Session})
	registerJobs(r, deps.Jobs)
	registerSystem(r, systemDeps{Broker: deps.Broker, Plugins: deps.Plugins, Policy: deps.Policy, Targets: deps.Targets, StateDir: deps.StateDir, Version: deps.Version, Session: deps.Session})
	registerSessionControl(r, deps.Broker)
	registerTermuxAPI(r, deps.Policy)
	registerTermuxAssistant(r)
	registerDesktop(r, deps.StateDir)
	registerDesktopContext(r, deps.Session)
	registerDesktopCompatibility(r)
	registerNativeDesktopCompatibility(r, deps.Session)
	registerDesktopCompletion(r, deps.Broker, deps.Session)
	registerBrowser(r)
	registerAnyApp(r)
	registerState(r, deps.State)
	registerSessions(r, deps.Sessions, deps.Jobs)
	registerWorkflows(r, deps.Workflows)
	registerFleet(r, deps.Fleet, deps.Targets, deps.Federation, deps.Identity)
	registerRemoteJobs(r, deps.RemoteJobs, deps.Identity, deps.Jobs)
	registerRemoteJobCaller(r, deps.Federation)
	registerTargets(r, deps.Targets, deps.Broker)
	registerGovernance(r, deps.Audit, deps.Policy)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
