// Package repo_silence wires the repository silence system: the
// cross-cutting SilenceGate consumed by every agent-facing surface,
// the Store for persistence, and the Controller for state-machine
// operations.
package repo_silence

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// Module returns the ioc module for repo_silence.
func Module() *ioc.Module {
	m := ioc.NewModule()

	// Persistence.
	m.Provide(infra.NewStore).ToInterface(new(domain.Store))

	// SilenceGate — cross-cutting seam for spawner / LLM proxy / etc.
	m.Provide(service.NewGate).ToInterface(new(domain.SilenceGate))

	// Controller — state-machine entry point for Web UI / scheduler.
	m.Provide(service.NewController).ToInterface(new(domain.Controller))

	// Scheduler — background cron job that enters/exits silence.
	m.Provide(service.NewScheduler).ToInterface(new(server.BackgroundJob))

	// HTTP handler — repo + session silence API routes.
	handlerBinder := m.Provide(handler.NewHandler)
	handlerBinder.ToSelf()
	handlerBinder.ToInterface(new(server.RouteProvider))

	return m
}
