// Package workflow wires the workflow module: domain, service, infra, and
// handler. The Service is registered as domain.Dispatcher for cross-module
// runner integration. The Handler is registered as server.RouteProvider.
package workflow

import (
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/handler"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// Module returns the ioc module for the workflow feature.
func Module() *ioc.Module {
	m := ioc.NewModule()

	// Infra: Postgres implementation of domain.Store
	m.Provide(infra.NewPostgresRepo).ToInterface(new(domain.Store))

	// Service: business logic; single instance satisfies Dispatcher,
	// TagEventTrigger, WorkflowTokenValidator, and CheckReader for
	// cross-module integration.
	svc := m.Provide(service.New)
	svc.ToSelf()
	svc.ToInterface(new(domain.Dispatcher))
	svc.ToInterface(new(domain.TagEventTrigger))
	svc.ToInterface(new(domain.WorkflowTokenValidator))
	svc.ToInterface(new(domain.CheckReader))
	svc.ToInterface(new(domain.PushEventDispatcher))

	// PushObserver: triggers repo.push_tag workflows on git tag push.
	m.Provide(handler.NewPushObserver).ToInterface(new(repodomain.PushObserver))

	// Handler: HTTP routes
	m.Provide(handler.NewHandler).ToInterface(new(server.RouteProvider))

	// observerWirer registers every container-bound RunStatusObserver
	// with the Service at server-startup time. Late binding via
	// server.OnReady is the standard pattern in this codebase for
	// wiring that can't go through constructor injection — here the
	// cycle is Service → observers → CIStatusObserver → Spawner →
	// AgentRunCreator = Service, broken by gathering the observer
	// slice AFTER the dep graph is built. The wirer itself depends on
	// both ends, but since neither end depends back on the wirer
	// there's no cycle.
	m.Provide(newObserverWirer).ToInterface(new(server.OnReady))

	return m
}

type observerWirerDeps struct {
	Service   *service.Service
	Observers []domain.RunStatusObserver
}

type observerWirer struct {
	svc       *service.Service
	observers []domain.RunStatusObserver
}

func newObserverWirer(deps *observerWirerDeps) *observerWirer {
	return &observerWirer{svc: deps.Service, observers: deps.Observers}
}

// OnReady fires once at server startup, before the listener accepts
// requests. Registers each observer with the workflow Service so
// subsequent status transitions reach them.
func (w *observerWirer) OnReady() error {
	for _, o := range w.observers {
		w.svc.RegisterObserver(o)
	}
	return nil
}
