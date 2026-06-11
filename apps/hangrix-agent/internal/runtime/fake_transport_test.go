package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	agentv1 "github.com/hangrix/hangrix/gen/go/hangrix/agent/v1"
)

// recordedFrame is the test-side capture of what the loop sent through
// the sink. One per Sink-method call, tagged by `Kind` so assertions
// can scan a slice the same way they used to scan the legacy
// JSON-Lines Outbound stream.
type recordedFrame struct {
	Kind string // status | log | message | tool_call | done | idle | suspended

	// status / suspended
	Phase          string
	ExpectedExitAt string

	// log
	Level string
	Msg   string

	// message
	Role      string
	Content   string
	ToolCalls []*agentv1.ToolCall

	// tool_call
	Name       string
	Args       json.RawMessage
	Result     json.RawMessage
	ToolCallID string

	// done
	TurnID string

	// idle
	RunningJobs int
}

// fakeTransport implements both frameSource and frameSink in-memory.
// Tests drive inbound frames via push* helpers, and read what the
// loop emitted via drain(). Replaces the old io.Pipe + ipc.Reader /
// ipc.Writer + JSON-Lines arrangement — no wire codec involved.
type fakeTransport struct {
	history     []*agentv1.HistoryItem
	historyOnce sync.Once

	// streamCh is the queue the loop's pumpFrames goroutine reads
	// from. Tests push event/control envelopes onto it; closing it
	// signals EOF (clean wake-done) to the pump.
	streamCh chan *agentv1.StreamInputsResponse

	mu       sync.Mutex
	frames   []recordedFrame
	shutdown bool
}

func newFakeTransport(history []*agentv1.HistoryItem) *fakeTransport {
	return &fakeTransport{
		history:  history,
		streamCh: make(chan *agentv1.StreamInputsResponse, 64),
	}
}

// ---- frameSource ----

func (f *fakeTransport) FetchHistory(ctx context.Context) ([]*agentv1.HistoryItem, error) {
	return f.history, nil
}

func (f *fakeTransport) StreamFrame(ctx context.Context) (*agentv1.StreamInputsResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-f.streamCh:
		if !ok {
			return nil, io.EOF
		}
		return resp, nil
	}
}

// pushEvent enqueues a streamed event frame. Tests use this where the
// old code wrote `{"kind":"event","event":...}` on stdin.
func (f *fakeTransport) pushEvent(event string, payload json.RawMessage) {
	f.streamCh <- &agentv1.StreamInputsResponse{
		Body: &agentv1.StreamInputsResponse_Event{
			Event: &agentv1.EventFrame{Event: event, Payload: []byte(payload)},
		},
	}
}

// pushShutdown enqueues a control:shutdown envelope. Tests use this
// where the old code wrote `{"kind":"control","op":"shutdown"}` on
// stdin to end the loop.
func (f *fakeTransport) pushShutdown() {
	f.streamCh <- &agentv1.StreamInputsResponse{
		Body: &agentv1.StreamInputsResponse_Control{
			Control: &agentv1.ControlFrame{Op: agentv1.ControlFrame_OP_SHUTDOWN},
		},
	}
}

// pushSuspend / pushResume mirror the legacy control frames so the
// silence-handling tests (inbox_test.go's suspend/resume coverage)
// keep working.
func (f *fakeTransport) pushSuspend(expectedExitAt, reason string) {
	f.streamCh <- &agentv1.StreamInputsResponse{
		Body: &agentv1.StreamInputsResponse_Control{
			Control: &agentv1.ControlFrame{
				Op:             agentv1.ControlFrame_OP_SUSPEND,
				Reason:         reason,
				ExpectedExitAt: expectedExitAt,
			},
		},
	}
}

func (f *fakeTransport) pushResume() {
	f.streamCh <- &agentv1.StreamInputsResponse{
		Body: &agentv1.StreamInputsResponse_Control{
			Control: &agentv1.ControlFrame{Op: agentv1.ControlFrame_OP_RESUME},
		},
	}
}

// closeStream signals EOF to the pump (matches the legacy
// `stdinW.Close()`).
func (f *fakeTransport) closeStream() { close(f.streamCh) }

// ---- frameSink ----

func (f *fakeTransport) record(rf recordedFrame) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, rf)
}

func (f *fakeTransport) Status(phase string) error {
	f.record(recordedFrame{Kind: "status", Phase: phase})
	return nil
}

func (f *fakeTransport) Log(level, msg string) error {
	f.record(recordedFrame{Kind: "log", Level: level, Msg: msg})
	return nil
}

func (f *fakeTransport) Message(role, content string, calls []*agentv1.ToolCall) error {
	f.record(recordedFrame{Kind: "message", Role: role, Content: content, ToolCalls: calls})
	return nil
}

func (f *fakeTransport) ToolCall(id, name string, args, result json.RawMessage) error {
	f.record(recordedFrame{
		Kind:       "tool_call",
		ToolCallID: id,
		Name:       name,
		Args:       args,
		Result:     result,
	})
	return nil
}

func (f *fakeTransport) Done(turnID string) error {
	f.record(recordedFrame{Kind: "done", TurnID: turnID})
	return nil
}

func (f *fakeTransport) Idle(runningJobs int) error {
	f.record(recordedFrame{Kind: "idle", RunningJobs: runningJobs})
	return nil
}

func (f *fakeTransport) Suspended(expectedExitAt string) error {
	// Sink-side, suspended is emitted as a status frame with
	// phase="suspended" — mirror the wire shape so tests written
	// against status-phase semantics keep their assertions.
	f.record(recordedFrame{Kind: "status", Phase: "suspended", ExpectedExitAt: expectedExitAt})
	return nil
}

func (f *fakeTransport) Shutdown() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdown = true
	return nil
}

// drain returns a copy of every recorded frame in emission order.
// Callable any time, but tests typically wait on the loop's exit
// signal first so the slice is complete.
func (f *fakeTransport) drain() []recordedFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedFrame, len(f.frames))
	copy(out, f.frames)
	return out
}

// shutdownCalled reports whether Loop.Run hit the deferred Shutdown
// branch. Useful for tests that need to confirm clean exit.
func (f *fakeTransport) shutdownCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutdown
}
