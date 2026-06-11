// connect_transport.go is the agent's typed Connect-Go client side for
// hangrix.agent.v1.AgentService. Implements both frameSource and
// frameSink against the generated agentv1connect client.
//
// Streaming. StreamInputs is server-streaming. A relay goroutine
// reads each StreamInputsResponse off the wire and pushes it onto an
// internal buffered channel; StreamFrame selects on (channel, idle
// timer) so the 5-minute idle-grace window is preserved without
// polling. When the idle timer fires StreamFrame returns io.EOF —
// same exit signal the loop's pump goroutine treats as wake-done.
//
// Lifetime. The stream is opened lazily on first StreamFrame.
// Shutdown cancels the stream context and POSTs MarkIdle once. Both
// are best-effort: any dangling state is the OS's problem after the
// agent process exits.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/hangrix/hangrix/gen/go/hangrix/agent/v1"
	"github.com/hangrix/hangrix/gen/go/hangrix/agent/v1/agentv1connect"
)

// connectTransport implements both frameSource and frameSink against
// hangrix.agent.v1.AgentService. One instance per agent process; the
// internal stream is opened lazily on the first StreamFrame call.
type connectTransport struct {
	sessionID int64
	client    agentv1connect.AgentServiceClient

	// streamMu guards the lazy-open of stream + its companion channels.
	// After open the fields are read-only; the relay goroutine writes
	// to frameQ / streamErr without further locking.
	streamMu     sync.Mutex
	stream       *connect.ServerStreamForClient[agentv1.StreamInputsResponse]
	streamCtx    context.Context
	streamCancel context.CancelFunc
	frameQ       chan *agentv1.StreamInputsResponse
	streamErr    chan error

	// lastFrameAt is the wall-clock time of the most recent frame
	// StreamFrame returned. Drives the 5-minute idle-grace window —
	// StreamFrame returns io.EOF once time.Since(lastFrameAt) >=
	// idleGrace.
	lastFrameAt time.Time

	// keepAlive lets the runtime hold the idle grace open while local
	// async work (pending sleep timers, background bash tasks) is in
	// flight. Without this, the grace would fire mid-sleep — the local
	// timer would never get a chance to push its completion notification
	// into the loop because the loop would have already exited on EOF.
	// Set by NewProvider; nil during tests that construct the transport
	// directly (those don't drive async work either).
	keepAlive func() bool
}

// newConnectTransport constructs the agent client. baseURL is the
// platform base; sessionID is the decimal agent_sessions.id (we keep
// it stringly-typed in Config and parse here); token is the hgxs_
// bearer that every RPC carries. Returns nil and an error when the
// session id can't be parsed — keeping that check here means the
// constructor fails fast rather than the first RPC.
func newConnectTransport(baseURL, sessionIDStr, token string) (*connectTransport, error) {
	sid, err := strconv.ParseInt(sessionIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("connect_transport: invalid HANGRIX_SESSION_ID %q: %w", sessionIDStr, err)
	}
	// Timeout=0 because streaming connections live for the full wake
	// (up to idleGrace). Per-call timeouts ride on the per-RPC context.
	httpClient := &http.Client{Timeout: 0}
	client := agentv1connect.NewAgentServiceClient(httpClient, baseURL,
		connect.WithInterceptors(&bearerAgentClient{token: token}))
	return &connectTransport{
		sessionID:   sid,
		client:      client,
		lastFrameAt: time.Now(),
	}, nil
}

// ---- bearer interceptor (client side) ----

// bearerAgentClient attaches Authorization: Bearer <session token> to
// every outgoing call, on both unary and streaming code paths.
// Connect's convenience UnaryInterceptorFunc covers only the unary
// branch — streaming calls (StreamInputs) need the full interface.
type bearerAgentClient struct{ token string }

func (b *bearerAgentClient) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+b.token)
		return next(ctx, req)
	}
}

func (b *bearerAgentClient) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+b.token)
		return conn
	}
}

func (b *bearerAgentClient) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// ---- frameSource ----

// FetchHistory fetches the persisted session timeline up front. One
// unary call; the loop seeds its in-memory Context with the result
// before opening the stream.
func (t *connectTransport) FetchHistory(ctx context.Context) ([]*agentv1.HistoryItem, error) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := t.client.FetchHistory(callCtx, connect.NewRequest(&agentv1.FetchHistoryRequest{
		SessionId: t.sessionID,
	}))
	if err != nil {
		return nil, fmt.Errorf("connect_transport: fetch history: %w", err)
	}
	return resp.Msg.GetMessages(), nil
}

// StreamFrame returns the next streamed event/control frame. Opens
// the stream on first call. Returns io.EOF when the idle-grace window
// elapses or the server cleanly closes the stream; the pump goroutine
// in Loop.Run treats both as "wake done".
//
// keepAlive override: while the runtime reports pending local async
// work (sleep timer, background bash task), the idle-grace window is
// held open — otherwise a sleep(5min) on a quiet stream would EOF the
// wake before the timer ever fired, and the agent process would exit
// with the completion notification still queued behind it.
func (t *connectTransport) StreamFrame(ctx context.Context) (*agentv1.StreamInputsResponse, error) {
	if err := t.ensureStream(); err != nil {
		return nil, err
	}

	for {
		holdingOpen := t.keepAlive != nil && t.keepAlive()
		if holdingOpen {
			// Refresh so the EOF check below never trips while local
			// work is in flight. The wake-done decision belongs to the
			// runtime loop (it sees the completion notification first);
			// the transport just keeps the stream subscription alive.
			t.lastFrameAt = time.Now()
		}

		timeout := idleGrace - time.Since(t.lastFrameAt)
		if timeout <= 0 {
			return nil, io.EOF
		}
		// Re-check the predicate periodically so a true→false flip
		// (last job finished, no fresh frames since) lets idleGrace fire
		// within ~30s instead of stretching to the next stream frame.
		// 30s is a balance: short enough to feel responsive on wake
		// teardown, long enough not to spin under steady-state load.
		const keepAliveRecheck = 30 * time.Second
		if holdingOpen && timeout > keepAliveRecheck {
			timeout = keepAliveRecheck
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case resp, ok := <-t.frameQ:
			if !ok {
				// Channel closed — relay exited. Treat as EOF; the
				// streamErr path may have already populated an error.
				return nil, io.EOF
			}
			t.lastFrameAt = time.Now()
			return resp, nil
		case err, ok := <-t.streamErr:
			// Server closed the stream. Clean close (Err()==nil) becomes
			// io.EOF so the loop exits cleanly; a real error propagates so
			// the session is marked failed.
			if !ok || err == nil {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("connect_transport: stream err: %w", err)
		case <-time.After(timeout):
			if holdingOpen {
				// Predicate was true at the top of this loop; re-check
				// it on the next iteration before letting idleGrace fire.
				continue
			}
			// Idle grace elapsed. Return io.EOF; Loop.Run's defer Shutdown
			// will fire MarkIdle and the relay's stream gets cancelled.
			return nil, io.EOF
		}
	}
}

// SetKeepAlive installs a predicate the transport consults each time
// it would otherwise let the 5-minute idle grace fire. While the
// predicate returns true the grace is suppressed and the stream
// subscription stays open; once it flips back to false the grace
// resumes from "now". Wired by NewProvider to async.HasRunningJobs > 0
// so an in-flight sleep timer / background bash task keeps the wake
// alive until completion notifications can be delivered.
//
// Safe to call exactly once before the first StreamFrame; not
// goroutine-safe afterwards (the loop is the only StreamFrame caller).
func (t *connectTransport) SetKeepAlive(fn func() bool) {
	t.keepAlive = fn
}

// ensureStream opens the StreamInputs subscription on first use. Safe
// to call multiple times — only the first opens; subsequent calls are
// fast no-ops under the mutex.
func (t *connectTransport) ensureStream() error {
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	if t.stream != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := t.client.StreamInputs(ctx, connect.NewRequest(&agentv1.StreamInputsRequest{
		SessionId: t.sessionID,
	}))
	if err != nil {
		cancel()
		return fmt.Errorf("connect_transport: open stream: %w", err)
	}
	t.stream = stream
	t.streamCtx = ctx
	t.streamCancel = cancel
	// Buffer at 16 absorbs short bursts without blocking the wire
	// reader; in steady state the loop drains as fast as the server
	// pushes so the buffer rarely fills.
	t.frameQ = make(chan *agentv1.StreamInputsResponse, 16)
	t.streamErr = make(chan error, 1)
	go t.relay()
	return nil
}

// relay copies frames off the wire onto frameQ until Receive returns
// false (clean close or transport error). The final stream.Err() lands
// on streamErr so StreamFrame can distinguish "wake done cleanly"
// from "real failure".
func (t *connectTransport) relay() {
	defer close(t.frameQ)
	defer close(t.streamErr)
	for t.stream.Receive() {
		select {
		case t.frameQ <- t.stream.Msg():
		case <-t.streamCtx.Done():
			return
		}
	}
	if err := t.stream.Err(); err != nil {
		t.streamErr <- err
	}
}

// ---- frameSink ----

// appendOutbound is the single seam every typed convenience method
// funnels through. Wraps the proto envelope in a unary AppendMessage
// RPC with a 30s timeout.
func (t *connectTransport) appendOutbound(frame *agentv1.Outbound) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := t.client.AppendMessage(ctx, connect.NewRequest(&agentv1.AppendMessageRequest{
		SessionId: t.sessionID,
		Frame:     frame,
	}))
	if err != nil {
		return fmt.Errorf("connect_transport: append message: %w", err)
	}
	return nil
}

func (t *connectTransport) Status(phase string) error {
	return t.appendOutbound(&agentv1.Outbound{Body: &agentv1.Outbound_Status{
		Status: &agentv1.StatusFrame{Phase: phase},
	}})
}

func (t *connectTransport) Log(level, msg string) error {
	return t.appendOutbound(&agentv1.Outbound{Body: &agentv1.Outbound_Log{
		Log: &agentv1.LogFrame{Level: level, Message: msg},
	}})
}

func (t *connectTransport) Message(role, content string, calls []*agentv1.ToolCall) error {
	return t.appendOutbound(&agentv1.Outbound{Body: &agentv1.Outbound_Message{
		Message: &agentv1.MessageFrame{Role: role, Content: content, ToolCalls: calls},
	}})
}

func (t *connectTransport) ToolCall(id, name string, args, result json.RawMessage) error {
	return t.appendOutbound(&agentv1.Outbound{Body: &agentv1.Outbound_ToolCall{
		ToolCall: &agentv1.ToolCallFrame{
			Id:     id,
			Name:   name,
			Args:   []byte(args),
			Result: []byte(result),
		},
	}})
}

func (t *connectTransport) Done(turnID string) error {
	return t.appendOutbound(&agentv1.Outbound{Body: &agentv1.Outbound_Done{
		Done: &agentv1.DoneFrame{TurnId: turnID},
	}})
}

// Idle is a runtime-internal per-turn signal. Under the Connect
// transport it intentionally does NOT round-trip to the server —
// per-turn idle would flip the session row's status mid-wake and let
// the spawner spin up a second container for an event that the
// already-live agent could absorb. The per-wake exit signal is
// Shutdown, which calls MarkIdle exactly once.
func (t *connectTransport) Idle(runningJobs int) error { return nil }

func (t *connectTransport) Suspended(expectedExitAt string) error {
	return t.appendOutbound(&agentv1.Outbound{Body: &agentv1.Outbound_Status{
		Status: &agentv1.StatusFrame{Phase: "suspended", ExpectedExitAt: expectedExitAt},
	}})
}

// Shutdown POSTs MarkIdle once and cancels the stream context. Called
// from Loop.Run's defer, after which the agent process exits.
func (t *connectTransport) Shutdown() error {
	if t.streamCancel != nil {
		t.streamCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := t.client.MarkIdle(ctx, connect.NewRequest(&agentv1.MarkIdleRequest{
		SessionId: t.sessionID,
	}))
	if err != nil {
		return fmt.Errorf("connect_transport: mark idle: %w", err)
	}
	return nil
}
