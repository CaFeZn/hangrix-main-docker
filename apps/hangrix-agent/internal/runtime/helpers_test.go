package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/runtime"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	agentv1 "github.com/hangrix/hangrix/gen/go/hangrix/agent/v1"
)

// fakeAsyncLifecycle is a hand-rolled stand-in for local.AsyncLifecycle.
// The runtime tests don't want to spawn real bash subprocesses or sleep
// timers; instead they want full control over when notifications fire
// and how many jobs are "running" at a given moment.
type fakeAsyncLifecycle struct {
	notifications chan string
	running       atomic.Int32
	cleanupCount  atomic.Int32
}

func newFakeAsync() *fakeAsyncLifecycle {
	return &fakeAsyncLifecycle{notifications: make(chan string, 16)}
}

func (f *fakeAsyncLifecycle) NotificationCh() <-chan string                         { return f.notifications }
func (f *fakeAsyncLifecycle) HasRunningJobs() int                                   { return int(f.running.Load()) }
func (f *fakeAsyncLifecycle) Cleanup(ctx context.Context)                           { f.cleanupCount.Add(1) }
func (f *fakeAsyncLifecycle) Schedule(time.Duration, string) string                 { return "sleep_fake" }
func (f *fakeAsyncLifecycle) ScheduleWithID(id string, d time.Duration, msg string) {}
func (f *fakeAsyncLifecycle) CancelSchedule(string)                                 {}

// loopOpts configure the runtime loop for a specific test scenario.
// Defaults match the legacy `0, 0, 0, "", ""` call sites.
type loopOpts struct {
	history          []*agentv1.HistoryItem
	compactThreshold int
	reasoningTimeout time.Duration
	reasoningRetries int
	reasoningEffort  string
	thinking         string
	runTimeout       time.Duration
}

type loopOpt func(*loopOpts)

func withReasoning(timeout time.Duration, retries int) loopOpt {
	return func(o *loopOpts) {
		o.reasoningTimeout = timeout
		o.reasoningRetries = retries
	}
}

// loopHarness bundles the fake transport, the running Loop's exit
// channel, and the test context so each test body reads top-to-bottom
// instead of repeating the io.Pipe + goroutine ceremony.
type loopHarness struct {
	fake    *fakeTransport
	loopErr chan error
	cancel  context.CancelFunc
}

// startLoop spins up a Loop wired to a fresh fakeTransport. The
// transport plays the role of both source (events/control via
// fake.push*) and sink (recorded via fake.drain).
func startLoop(
	t *testing.T,
	llmClient *llm.Client,
	registry *tools.Registry,
	async runtimeAsync,
	opts ...loopOpt,
) *loopHarness {
	t.Helper()
	cfg := loopOpts{runTimeout: 10 * time.Second}
	for _, o := range opts {
		o(&cfg)
	}
	fake := newFakeTransport(cfg.history)
	loop := runtime.NewLoop(
		fake, fake,
		llmClient, "gpt-4o-mini",
		registry, "system prompt for test",
		async,
		cfg.compactThreshold,
		cfg.reasoningTimeout,
		cfg.reasoningRetries,
		cfg.reasoningEffort,
		cfg.thinking,
	)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.runTimeout)
	loopErr := make(chan error, 1)
	go func() { loopErr <- loop.Run(ctx) }()
	return &loopHarness{fake: fake, loopErr: loopErr, cancel: cancel}
}

// runtimeAsync is the subset of local.AsyncLifecycle the runtime
// constructor accepts. Kept as a named alias here so the test harness
// avoids importing the full local package transitively when callers
// only need a stub.
type runtimeAsync interface {
	NotificationCh() <-chan string
	HasRunningJobs() int
	Cleanup(ctx context.Context)
	Schedule(time.Duration, string) string
	ScheduleWithID(id string, d time.Duration, msg string)
	CancelSchedule(string)
}

// shutdown drives a clean control:shutdown through the transport and
// waits for Loop.Run to return. Returns whatever the loop returned —
// nil on a clean exit, non-nil for self-terminating error paths.
// Frames are guaranteed complete on return.
func (h *loopHarness) shutdown(t *testing.T) ([]recordedFrame, error) {
	t.Helper()
	// streamCh is buffered (64); even if the loop has already exited
	// (e.g. self-terminated on timeout) the send doesn't block.
	h.fake.pushShutdown()
	select {
	case err := <-h.loopErr:
		return h.fake.drain(), err
	case <-time.After(5 * time.Second):
		h.cancel()
		t.Fatal("loop did not exit within 5s after shutdown")
		return nil, nil
	}
}

// waitForReady polls fake.drain() until the loop's startup "ready"
// status frame appears. Tests use it where the legacy io.Pipe code
// needed to read frames in real time to know the agent had finished
// FetchHistory and parked on the stream.
func (h *loopHarness) waitForReady(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, f := range h.fake.drain() {
			if f.Kind == "status" && f.Phase == "ready" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent never emitted ready status")
}
