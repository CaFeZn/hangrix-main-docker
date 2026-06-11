package server

import (
	"context"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
)

// RouteProvider is implemented by any module that contributes routes to the
// HTTP server. Implementations are collected via the ioc container and their
// routes are registered on the shared chi router at server construction time.
type RouteProvider interface {
	RegisterRoutes(r chi.Router)
}

// BackgroundJob is implemented by any module that needs a long-lived
// goroutine alongside the HTTP server — periodic sweepers, reapers,
// queue workers, etc. Implementations are collected by ioc and each
// gets Start called once during ListenAndServe with a context that
// stays alive for the lifetime of the server. Implementations must
// return promptly on ctx.Done() so future graceful shutdown wiring
// composes cleanly.
type BackgroundJob interface {
	Start(ctx context.Context)
}

// OnReady is implemented by components that need a synchronous hook
// after the ioc graph is fully built but BEFORE the listener accepts
// requests. Hooks fire in ioc-resolution order; any error aborts
// startup.
//
// Use case: late-bound wiring that can't go through constructor
// injection because of a circular dep. Example: workflow.Service
// gathers its RunStatusObservers via an OnReady hook because
// CIStatusObserver indirectly depends back on workflow.Service.
//
// Unlike BackgroundJob (which fires in a goroutine), OnReady runs on
// the main startup goroutine — keep the work small. Anything
// long-running belongs in BackgroundJob.
type OnReady interface {
	OnReady() error
}

type Server struct {
	addr   string
	router http.Handler
	jobs   []BackgroundJob
	ready  []OnReady
}

type ServerDeps struct {
	Config    *config.Config
	Providers []RouteProvider
	Jobs      []BackgroundJob
	Ready     []OnReady
}

func NewServer(deps *ServerDeps) *Server {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	for _, p := range deps.Providers {
		p.RegisterRoutes(r)
	}

	return &Server{
		addr:   deps.Config.Server.Addr,
		router: r,
		jobs:   deps.Jobs,
		ready:  deps.Ready,
	}
}

func (s *Server) ListenAndServe() error {
	// Fire synchronous OnReady hooks first so any late-bound wiring
	// (observer registration etc.) is in place before requests arrive.
	for _, r := range s.ready {
		if err := r.OnReady(); err != nil {
			return err
		}
	}
	// Background jobs run for the lifetime of the process. We hand each
	// a Background context — graceful shutdown is not yet wired into the
	// HTTP server, so plumbing a real cancel here would be misleading.
	// When that lands, swap context.Background() for the shared shutdown
	// ctx and the jobs will already honour it.
	ctx := context.Background()
	for _, j := range s.jobs {
		go j.Start(ctx)
	}
	log.Printf("hangrix listening on %s", s.addr)
	return http.ListenAndServe(s.addr, s.router)
}
