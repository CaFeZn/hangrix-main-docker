// transport.go is the narrow seam between the runtime loop and the
// wire it speaks. The loop is single-binary single-process; the seam
// exists only so tests can swap in a channel-backed fake transport
// (see fake_transport_test.go) without standing up an HTTP/Connect
// server.
//
// Wake exit semantics. Under the workflow model each wake = one fresh
// container; when the inputs stream stays quiet for the idle-grace
// window, the agent exits and the workflow_job completes. StreamFrame
// signals "wake done" by returning io.EOF, which Loop.Run's
// inbox-select treats as a clean exit.
//
// During the idle-grace window the agent_session row's status stays
// `live` (it never flipped to `idle` per-event — see the Shutdown
// method below). The spawner's enqueueOntoLive path matches against
// that live status and appends new event frames directly to the
// inputs queue; the agent's open stream picks them up in the SAME
// container, avoiding a fresh-container spin-up for every burst-fired
// trigger.
package runtime

import (
	"context"
	"encoding/json"
	"time"

	agentv1 "github.com/hangrix/hangrix/gen/go/hangrix/agent/v1"
)

// idleGrace bounds how long the agent keeps the StreamInputs
// subscription open after its last frame before declaring the wake
// done and exiting. While the timer is running the agent_session row
// stays in the spawner's "live" status set, so a trigger arriving
// within the window is enqueued onto the same /inputs queue
// (enqueueOntoLive) and the server pushes it down this same stream —
// no fresh-container spin-up. Resource trade-off accepted: a single
// idle container holds nothing more than an open Connect stream for
// at most idleGrace.
const idleGrace = 5 * time.Minute

// frameSource is the read surface the loop needs.
//
// FetchHistory is called exactly once at Run-start to seed the LLM
// context with the persisted session timeline. StreamFrame is called
// in a loop by the inbox pump; it blocks until the server pushes the
// next event/control frame or returns io.EOF when the idle-grace
// window elapses.
type frameSource interface {
	FetchHistory(ctx context.Context) ([]*agentv1.HistoryItem, error)
	StreamFrame(ctx context.Context) (*agentv1.StreamInputsResponse, error)
}

// frameSink is the write surface the loop needs. The seven typed
// methods correspond 1:1 to the outbound proto Outbound oneof variants
// (status / log / message / tool_call / done) plus the
// runtime-internal Idle / Suspended dispatches. Shutdown is the
// per-wake exit signal — see its docstring below.
type frameSink interface {
	Status(phase string) error
	Log(level, msg string) error
	Message(role, content string, calls []*agentv1.ToolCall) error
	ToolCall(id, name string, args, result json.RawMessage) error
	Done(turnID string) error
	Idle(runningJobs int) error
	Suspended(expectedExitAt string) error

	// Shutdown is the explicit "this wake is over" signal the loop fires
	// exactly once before returning from Run. The Connect transport
	// POSTs MarkIdle so the spawner sees status=idle for subsequent
	// triggers (which then go down the rewake path, not
	// enqueueOntoLive).
	//
	// Per-turn `Idle(...)` calls during a wake are local-only under
	// Connect (see connectTransport.Idle), so the session row's status
	// does NOT flip mid-wake — that's what lets the 5-minute idle-grace
	// window keep events flowing through enqueueOntoLive into the same
	// agent.
	Shutdown() error
}
