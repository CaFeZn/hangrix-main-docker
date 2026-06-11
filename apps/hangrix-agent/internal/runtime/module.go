package runtime

import (
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/config"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/prompt"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
	"github.com/hangrix/hangrix/pkg/ioc"
)

// Deps pulls in every direct dependency the runtime loop needs. This
// is the deepest node in the agent's dependency graph below *app.App:
// any module that wires something the loop reaches transitively must
// be loaded into the container before this one's NewProvider runs.
//
// The transport (in / out) is constructed inline by NewProvider rather
// than wired through ioc — it's a single Connect client keyed on Config
// fields and has no fan-out, so a Deps entry would just add ceremony.
// Tests construct Loop directly via NewLoop with *ipc.Reader/*ipc.Writer
// over io.Pipe so the Connect wiring is exercised only in production.
type Deps struct {
	Cfg       *config.Config
	LLM       *llm.Client
	Registry  *tools.Registry
	Assembled *prompt.Assembled
	// Async is the lifecycle handle for local async work (background bash
	// tasks, sleep timers, etc.). The runtime drains its NotificationCh
	// into the LLM context at every drain point (round boundary, idle
	// wait) and calls Cleanup on shutdown so unfinished work doesn't
	// outlive the agent process.
	Async local.AsyncLifecycle
}

func NewProvider(deps *Deps) *Loop {
	transport, err := newConnectTransport(deps.Cfg.PlatformBaseURL, deps.Cfg.SessionID, deps.Cfg.SessionToken)
	if err != nil {
		// Same fail-fast policy as config.NewConfig — a misconfigured
		// transport means the agent has nothing to do; one stderr line +
		// process exit is cleaner than retrying every RPC and printing
		// the same parse error over and over.
		panic(err)
	}
	// Hold the StreamInputs idle grace open while local async work
	// (sleep timers, background bash tasks) is pending. Without this,
	// a sleep(5min) on a quiet stream would race the 5-min grace and
	// exit the agent before the timer ever fired.
	if deps.Async != nil {
		transport.SetKeepAlive(func() bool { return deps.Async.HasRunningJobs() > 0 })
	}
	return NewLoop(
		transport,
		transport,
		deps.LLM,
		deps.Cfg.Model,
		deps.Registry,
		deps.Assembled.Prompt,
		deps.Async,
		deps.Cfg.CompactTokenThreshold,
		time.Duration(deps.Cfg.LLMReasoningTimeoutSeconds)*time.Second,
		deps.Cfg.LLMReasoningTimeoutRetries,
		deps.Cfg.LLMReasoningEffort,
		deps.Cfg.LLMThinking,
	)
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewProvider).ToSelf()
	return m
}
