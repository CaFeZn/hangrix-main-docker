// Package app is the agent's top-level entry point as resolved from the
// ioc container. main.go does:
//
//	c.Load(... all modules ...)
//	ioc.Get[*app.App](c).Run(ctx)
//
// so this package owns the lifecycle (start banner, run the loop, stop
// banner, error fan-out) while delegating every component-level concern
// to the modules wired below it.
package app

import (
	"context"
	"log"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/mcp"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/prompt"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/runtime"
)

type Deps struct {
	Loop      *runtime.Loop
	Assembled *prompt.Assembled
	MCPBundle *mcp.Bundle
}

type App struct {
	loop      *runtime.Loop
	assembled *prompt.Assembled
	mcpBundle *mcp.Bundle
}

func New(deps *Deps) *App {
	return &App{
		loop:      deps.Loop,
		assembled: deps.Assembled,
		mcpBundle: deps.MCPBundle,
	}
}

// Run executes the agent's main loop until ctx is cancelled or the
// transport's poll returns empty (single-shot wake under workflow mode).
// Init errors are not Run's concern — they surface as panics during
// container construction and are caught by main.go's recover.
//
// Lifecycle log lines go to stderr (operator-visible via the runner's
// container-log forwarder) rather than the agent-message log, since they
// are container-startup events that pre-date a valid session state.
func (a *App) Run(ctx context.Context) error {
	defer a.mcpBundle.Close()

	log.Printf("agent starting; system prompt layers: %v", a.assembled.KeptLayers)
	if err := a.loop.Run(ctx); err != nil {
		log.Printf("agent error: %v", err)
		return err
	}
	log.Printf("agent stopping")
	return nil
}
