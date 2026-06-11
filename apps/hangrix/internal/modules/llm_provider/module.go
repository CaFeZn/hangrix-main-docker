// Package llm_provider wires the LLM-provider registry: the Postgres repo
// (which simultaneously satisfies Repo and Lookup) plus the admin-only
// HTTP handler. The narrow Lookup interface is what the M6a proxy holds;
// agent-identity session tokens now live in modules/runner instead of
// being minted here.
package llm_provider

import (
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

func Module() *ioc.Module {
	m := ioc.NewModule()
	// The Postgres impl satisfies Repo + GroupRepo; bind one provider to
	// both. Lookup is now satisfied by GroupRouter (service layer) instead
	// of directly by PostgresRepo, because model-group resolution requires
	// combining provider + group data.
	binder := m.Provide(infra.NewPostgresRepo)
	binder.ToInterface(new(domain.Repo))
	binder.ToInterface(new(domain.GroupRepo))
	binder.ToInterface(new(domain.ModelRepo))
	// Bind to *infra.PostgresRepo as well so the admin handler can reach
	// the usage-read path (not on the cross-module interface — only this
	// module's handler renders the usage table).
	binder.ToSelf()

	// GroupRouter implements domain.Lookup by composing Repo + GroupRepo.
	// The proxy module depends on domain.Lookup and is transparent to this switch.
	m.Provide(service.NewGroupRouter).ToInterface(new(domain.Lookup))

	// Background reaper: hard-deletes usage-log rows older than
	// config.llm.usage_retention on a periodic ticker.
	m.Provide(service.NewReaper).ToInterface(new(server.BackgroundJob))

	m.Provide(service.NewModelWriter).ToSelf()

	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))
	return m
}
