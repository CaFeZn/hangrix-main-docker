// Package agent_session wires the per-role session orchestrator: takes
// issue lifecycle / comment / push / review events, reads
// `.hangrix/agents.yml` at the host repo's base-branch tip, and produces
// one agent_sessions row per matching role. Persistence is owned by the
// runner module; this module only adds the higher-level semantics
// (snapshot fields, idempotent spawn, archive-on-close, audit query
// view).
package agent_session

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"

	workflowsvc "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/service"
)

func Module() *ioc.Module {
	m := ioc.NewModule()

	// Concrete implementations bound to their narrow domain interface
	// each — the issue module depends only on domain.Spawner +
	// domain.Archiver; the admin handler depends only on
	// domain.Auditor. None of them sees the wider service struct.
	m.Provide(service.NewGitBlobReader).ToInterface(new(domain.HostBlobReader))

	// Bind workflow.Service as the spawner's narrow AgentRunCreator.
	// Defined here (not in workflow/module.go) so the interface stays
	// owned by its consumer; the workflow module doesn't need to know
	// the agent-spawn contract exists.
	//
	// The wrapper takes a *runCreatorDeps struct rather than
	// *workflowsvc.Service directly. pkg/ioc inspects every field of
	// any pointer-to-struct parameter and requires each to be
	// IoC-resolvable (interface, pointer-to-struct, or slice of either)
	// — passing *Service directly would have it treat Service's
	// internal fields (mutex, etc.) as deps and panic.
	m.Provide(func(deps *runCreatorDeps) service.AgentRunCreator {
		return deps.Workflow
	}).ToInterface(new(service.AgentRunCreator))

	// Spawner depends on actor domain.Resolver for trigger attribution.
	// ioc resolves it from the actor module.
	m.Provide(service.NewSpawner).ToInterface(new(domain.Spawner))
	m.Provide(service.NewArchiver).ToInterface(new(domain.Archiver))
	m.Provide(service.NewAuditor).ToInterface(new(domain.Auditor))
	m.Provide(service.NewController).ToInterface(new(domain.Controller))
	m.Provide(service.NewReaper).ToInterface(new(server.BackgroundJob))

	m.Provide(handler.NewAdminHandler).ToInterface(new(server.RouteProvider))
	return m
}

// runCreatorDeps is the Deps struct for the AgentRunCreator wrapper
// above. Exists only so the wrapper's parameter is a struct whose
// fields ioc can validate, rather than *workflowsvc.Service directly.
type runCreatorDeps struct {
	Workflow *workflowsvc.Service
}
