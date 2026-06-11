package main

import (
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/app"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// TestBuildContainer pins the container's dependency graph: every
// module's Deps must resolve, the root *App must be retrievable, and
// the env validation in config.NewConfig must accept the required
// triplet (SESSION_TOKEN / PLATFORM_BASE_URL / LLM_MODEL). A new
// module whose Deps name an unregistered type will fail this test
// long before anyone deploys the binary.
func TestBuildContainer(t *testing.T) {
	t.Setenv("HANGRIX_SESSION_TOKEN", "tok")
	t.Setenv("HANGRIX_PLATFORM_BASE_URL", "http://platform.invalid")
	t.Setenv("HANGRIX_LLM_MODEL", "fake-model")
	// HANGRIX_SESSION_ID is parsed eagerly by the Connect transport's
	// constructor (it's the int64 wire field on every RPC). Production
	// always sets it via the spawner-injected job env; the wiring test
	// has to mirror that.
	t.Setenv("HANGRIX_SESSION_ID", "1")

	c := buildContainer()
	a := ioc.Get[*app.App](c)
	if a == nil {
		t.Fatal("buildContainer().Get(*App) returned nil")
	}
}
