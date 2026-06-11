// agent_connect.go mounts the typed hangrix.agent.v1.AgentService at
// `/hangrix.agent.v1.AgentService/*`, replacing the JSON-over-HTTP
// `/api/agent/sessions/{id}/*` surface (agent_direct.go) one wake at a
// time. Both can coexist while the agent client cuts over; once that
// lands the old routes go away.
//
// Auth model is identical to agent_direct: every RPC carries an
// `Authorization: Bearer hgxs_<...>` session token, validated via
// runnerdomain.SessionTokenValidator. The Connect interceptor stashes
// the resolved AgentSession on the context; each RPC then enforces
// request.session_id == ctx-session.id so a leaked token can't address
// a different session.
//
// StreamInputs implementation: server-streaming over a tight loop that
// claims rows from agent_session_inputs and sends them as
// StreamInputsResponse frames. When the queue is empty we sleep
// pollTick (the existing 500ms cadence) and try again — keeping the
// stream open through idle windows so a freshly enqueued frame lands
// on the agent within a tick. The agent's 5-minute idle-grace timer
// (now a client-side stream close, not an HTTP loop) bounds wake life.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	agentv1 "github.com/hangrix/hangrix/gen/go/hangrix/agent/v1"
	"github.com/hangrix/hangrix/gen/go/hangrix/agent/v1/agentv1connect"
)

// agentConnectRepo is the narrow read/write surface AgentConnectHandler
// touches. Declared here (not in domain) because no other package needs
// this slice — keeping it local lets tests stub four methods instead
// of forty. *infra.PostgresRepo satisfies it via the wider domain.Repo
// it already implements.
type agentConnectRepo interface {
	ListMessages(ctx context.Context, sessionID int64) ([]*domain.Message, error)
	AppendMessage(ctx context.Context, m *domain.Message) (*domain.Message, error)
	ClaimPendingInputs(ctx context.Context, sessionID int64, limit int) ([]*domain.SessionInput, error)
	MarkSessionIdle(ctx context.Context, id int64, exitCode *int32) error
}

// AgentConnectHandler is the Connect-Go implementation of AgentService.
// One handler per server process; goroutine-safe by virtue of holding
// only stateless dependencies (repo + validator).
type AgentConnectHandler struct {
	repo      agentConnectRepo
	validator domain.SessionTokenValidator
}

type AgentConnectHandlerDeps struct {
	Repo      domain.Repo
	Validator domain.SessionTokenValidator
}

func NewAgentConnectHandler(deps *AgentConnectHandlerDeps) *AgentConnectHandler {
	return &AgentConnectHandler{repo: deps.Repo, validator: deps.Validator}
}

// newAgentConnectHandlerForTest is a package-private constructor that
// accepts the narrow agentConnectRepo interface directly. Lets tests
// inject a tiny stub instead of having to implement the wide
// domain.Repo surface. Production code uses NewAgentConnectHandler.
func newAgentConnectHandlerForTest(repo agentConnectRepo, validator domain.SessionTokenValidator) *AgentConnectHandler {
	return &AgentConnectHandler{repo: repo, validator: validator}
}

// RegisterRoutes mounts the Connect service under the standard Connect
// path. chi.Mount strips the prefix before dispatching, so the inner
// handler sees the bare `/<Service>/<Method>` paths it expects.
func (h *AgentConnectHandler) RegisterRoutes(r chi.Router) {
	path, conn := agentv1connect.NewAgentServiceHandler(
		h,
		connect.WithInterceptors(newSessionTokenInterceptor(h.validator)),
	)
	r.Mount(path, conn)
}

// ---- interceptor + context plumbing ----

type connectSessionCtxKey struct{}

// sessionFromConnectContext is the typed accessor each RPC handler
// uses to recover the validated session. nil result is an internal
// error (interceptor misconfigured).
func sessionFromConnectContext(ctx context.Context) *domain.AgentSession {
	v, _ := ctx.Value(connectSessionCtxKey{}).(*domain.AgentSession)
	return v
}

// newSessionTokenInterceptor returns a Connect interceptor that
// resolves the bearer header into an AgentSession and stores it on the
// request context for the wrapped RPC. The same validation logic ran
// in agent_direct.go's chi middleware; lifting it into a Connect
// interceptor lets us keep one auth implementation while running both
// transports during the migration window.
func newSessionTokenInterceptor(v domain.SessionTokenValidator) connect.Interceptor {
	return &sessionTokenInterceptor{validator: v}
}

type sessionTokenInterceptor struct {
	validator domain.SessionTokenValidator
}

func (i *sessionTokenInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := i.authorize(ctx, req.Header())
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	})
}

func (i *sessionTokenInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	// Clients don't need server-side authorization plumbing — leave the
	// downstream Connect machinery to add Authorization headers itself
	// via Request.Header().
	return next
}

func (i *sessionTokenInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return connect.StreamingHandlerFunc(func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := i.authorize(ctx, conn.RequestHeader())
		if err != nil {
			return err
		}
		return next(ctx, conn)
	})
}

// authorize is the shared bearer-token resolution used by both unary
// and streaming entry points. Returns a context decorated with the
// resolved AgentSession on success; a Connect error otherwise. Error
// codes mirror agent_direct's HTTP status mapping:
//   - 401 Unauthenticated → missing/malformed Authorization header
//   - 403 PermissionDenied → token doesn't validate (bad or revoked)
//   - 13 Internal → validator implementation error
func (i *sessionTokenInterceptor) authorize(ctx context.Context, header http.Header) (context.Context, error) {
	const prefix = "Bearer "
	raw := header.Get("Authorization")
	if !strings.HasPrefix(raw, prefix) {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
	}
	token := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
	if token == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
	}
	sess, err := i.validator.ValidateSessionToken(ctx, token)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidSessionToken):
			return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("invalid session token"))
		case errors.Is(err, domain.ErrSessionTokenInactive):
			return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("session token revoked or session terminated"))
		default:
			return ctx, connect.NewError(connect.CodeInternal, err)
		}
	}
	return context.WithValue(ctx, connectSessionCtxKey{}, sess), nil
}

// resolveSession pulls the validated session off the context and
// enforces the request's session_id matches. 404 on mismatch keeps
// callers from probing for other sessions' existence.
func (h *AgentConnectHandler) resolveSession(ctx context.Context, requestedID int64) (*domain.AgentSession, error) {
	sess := sessionFromConnectContext(ctx)
	if sess == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no session in context"))
	}
	if sess.ID != requestedID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	return sess, nil
}

// ---- RPC implementations ----

// streamPollTick is the wait between empty-queue polls inside
// StreamInputs. Matches the legacy /api/agent/sessions/{id}/inputs
// long-poll cadence so latency under burst stays bounded.
const streamPollTick = 500 * time.Millisecond

// historyEventKind is the Kind value FetchHistory stamps on event-kind
// HistoryItems. Used by the agent to recognise — and drop — the trailing
// unsettled cause event that the inputs queue is about to deliver via
// applyInboxItem, so the LLM doesn't see the same event twice.
const historyEventKind = "event"

func (h *AgentConnectHandler) FetchHistory(
	ctx context.Context,
	req *connect.Request[agentv1.FetchHistoryRequest],
) (*connect.Response[agentv1.FetchHistoryResponse], error) {
	sess, err := h.resolveSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	rows, err := h.repo.ListMessages(ctx, sess.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items := make([]*agentv1.HistoryItem, 0, len(rows))
	for _, m := range rows {
		items = append(items, messageToHistoryItemsProto(m)...)
	}
	// Trim trailing event-kind items: they are the just-enqueued cause
	// frames for the wake that's about to start. The agent will receive
	// each as a stream frame via StreamInputs and fold it into the LLM
	// context through applyInboxItem; leaving them on history too would
	// land them twice and the LLM would see the same event duplicated
	// on its first turn. Past events (already responded to) are
	// followed by an assistant message in the log, so they survive the
	// trim.
	items = trimTrailingEventItems(items)
	items = normalizeInterruptedToolResultChains(items)
	items = trimTrailingDanglingToolCallsProto(items)
	return connect.NewResponse(&agentv1.FetchHistoryResponse{Messages: items}), nil
}

// trimTrailingDanglingToolCallsProto is the server-side counterpart of the
// agent's trimTrailingDanglingToolCalls. It drops trailing assistant HistoryItems
// that carry tool_calls with no corresponding tool items after them — the
// classic "container crashed mid-turn in a previous wake" scenario. Dropping
// at the server means even older agent versions that lack the agent-side guard
// won't trip the upstream 400 on rewake.
func trimTrailingDanglingToolCallsProto(items []*agentv1.HistoryItem) []*agentv1.HistoryItem {
	if len(items) == 0 {
		return items
	}
	// Build the set of tool-call IDs referenced by tool-role items.
	seenToolIDs := make(map[string]bool)
	for _, it := range items {
		if it.GetRole() == "tool" && it.GetToolCallId() != "" {
			seenToolIDs[it.GetToolCallId()] = true
		}
	}
	cut := len(items)
	for scan := len(items) - 1; scan >= 0; scan-- {
		it := items[scan]
		if it.GetKind() == "summary" {
			continue
		}
		if it.GetRole() != "assistant" || len(it.GetToolCalls()) == 0 {
			break
		}
		dangling := false
		for _, tc := range it.GetToolCalls() {
			if !seenToolIDs[tc.GetId()] {
				dangling = true
				break
			}
		}
		if !dangling {
			break
		}
		cut = scan
	}
	return items[:cut]
}

// trimTrailingEventItems drops any contiguous run of event-kind items
// at the end of the history slice. Multiple unsettled events can pile
// up when a previous wake crashed mid-turn or when the spawner
// enqueued several causes before the agent had a chance to respond —
// every one of them will be redelivered via the inputs queue.
func trimTrailingEventItems(items []*agentv1.HistoryItem) []*agentv1.HistoryItem {
	end := len(items)
	for end > 0 && items[end-1].GetKind() == historyEventKind {
		end--
	}
	return items[:end]
}

// normalizeInterruptedToolResultChains preserves the LLM API invariant that
// tool results must immediately follow the assistant(tool_calls=...) that
// produced them. Mid-turn events are mirrored into agent_session_messages for
// audit while a session is live, so on rewake the raw log can contain:
//
//	assistant(tool_calls=...) -> event -> tool_result
//
// That ordering is legal for audit, but Anthropic/OpenAI reject it on replay.
// We therefore buffer event-kind items that land inside an open tool-result
// chain and flush them immediately after the final matching tool result.
//
// If the chain never completes (crash mid-turn), buffered events are dropped
// from history so the trailing dangling assistant stays visible to the later
// trim pass; unsettled events are still redelivered from the inputs queue.
func normalizeInterruptedToolResultChains(items []*agentv1.HistoryItem) []*agentv1.HistoryItem {
	if len(items) == 0 {
		return items
	}
	out := make([]*agentv1.HistoryItem, 0, len(items))
	var bufferedEvents []*agentv1.HistoryItem
	pending := map[string]struct{}(nil)
	startPending := func(it *agentv1.HistoryItem) {
		if it.GetRole() != "assistant" || len(it.GetToolCalls()) == 0 {
			pending = nil
			return
		}
		pending = make(map[string]struct{}, len(it.GetToolCalls()))
		for _, tc := range it.GetToolCalls() {
			pending[tc.GetId()] = struct{}{}
		}
	}
	flushBuffered := func() {
		if len(bufferedEvents) == 0 {
			return
		}
		out = append(out, bufferedEvents...)
		bufferedEvents = nil
	}
	for _, it := range items {
		if len(pending) == 0 {
			out = append(out, it)
			startPending(it)
			continue
		}
		if it.GetKind() == historyEventKind {
			bufferedEvents = append(bufferedEvents, it)
			continue
		}
		if it.GetRole() == "tool" && it.GetToolCallId() != "" {
			if _, ok := pending[it.GetToolCallId()]; ok {
				out = append(out, it)
				delete(pending, it.GetToolCallId())
				if len(pending) == 0 {
					flushBuffered()
					pending = nil
				}
				continue
			}
		}
		flushBuffered()
		pending = nil
		out = append(out, it)
		startPending(it)
	}
	return out
}

func (h *AgentConnectHandler) StreamInputs(
	ctx context.Context,
	req *connect.Request[agentv1.StreamInputsRequest],
	stream *connect.ServerStream[agentv1.StreamInputsResponse],
) error {
	sess, err := h.resolveSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			// Client closed (5-min idle grace timer fired, or shutdown).
			// Returning nil keeps the stream's terminal status at OK so
			// the agent doesn't treat clean exits as failures.
			return nil
		}
		rows, err := h.repo.ClaimPendingInputs(ctx, sess.ID, 50)
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		for _, in := range rows {
			resp, perr := parseStoredInputFrame(in.Payload)
			if perr != nil {
				// One malformed frame would orphan an inputs row that
				// the agent will never see; drop it on the floor and
				// keep the stream going. The audit log records the
				// claimed input so an operator can replay manually if
				// needed.
				continue
			}
			if resp == nil {
				// Frame was kind="history" or some other shape that
				// shouldn't appear on the inputs queue. Skip silently.
				continue
			}
			if err := stream.Send(resp); err != nil {
				return err
			}
		}
		if len(rows) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(streamPollTick):
			}
		}
	}
}

func (h *AgentConnectHandler) AppendMessage(
	ctx context.Context,
	req *connect.Request[agentv1.AppendMessageRequest],
) (*connect.Response[agentv1.AppendMessageResponse], error) {
	sess, err := h.resolveSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	msg, err := outboundProtoToDomain(sess.ID, req.Msg.GetFrame())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if msg == nil {
		// Variant we deliberately ignore (e.g. an unset oneof). Return
		// success so the agent doesn't retry — there's nothing to fix.
		return connect.NewResponse(&agentv1.AppendMessageResponse{}), nil
	}
	if _, err := h.repo.AppendMessage(ctx, msg); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.AppendMessageResponse{}), nil
}

func (h *AgentConnectHandler) MarkIdle(
	ctx context.Context,
	req *connect.Request[agentv1.MarkIdleRequest],
) (*connect.Response[agentv1.MarkIdleResponse], error) {
	sess, err := h.resolveSession(ctx, req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	// MarkSessionIdle takes *int32 for exit code; the proto carries no
	// exit-code field (the workflow_job_run wrapper records the docker
	// exit code separately), so we always pass nil here.
	if err := h.repo.MarkSessionIdle(ctx, sess.ID, nil); err != nil {
		if errors.Is(err, domain.ErrSessionStateInvalid) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("session not in a state that accepts idle"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.MarkIdleResponse{}), nil
}

// ---- proto <-> domain translation ----

// parseStoredInputFrame decodes one row of agent_session_inputs into
// the StreamInputsResponse shape. Today's stored frames are JSON in
// the legacy ipc.Inbound shape (kind=event|control|history); we map
// each to the appropriate oneof variant. Returns (nil, nil) for shapes
// that don't belong on the stream (history — fetched via FetchHistory
// instead) so the caller can drop them quietly.
func parseStoredInputFrame(payload []byte) (*agentv1.StreamInputsResponse, error) {
	if len(payload) == 0 {
		return nil, errors.New("empty input frame")
	}
	var stored struct {
		Kind           string          `json:"kind"`
		Event          string          `json:"event"`
		Payload        json.RawMessage `json:"payload"`
		Op             string          `json:"op"`
		Reason         string          `json:"reason"`
		ExpectedExitAt string          `json:"expected_exit_at"`
	}
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, err
	}
	switch stored.Kind {
	case "event":
		return &agentv1.StreamInputsResponse{
			Body: &agentv1.StreamInputsResponse_Event{
				Event: &agentv1.EventFrame{
					Event:   stored.Event,
					Payload: []byte(stored.Payload),
				},
			},
		}, nil
	case "control":
		return &agentv1.StreamInputsResponse{
			Body: &agentv1.StreamInputsResponse_Control{
				Control: &agentv1.ControlFrame{
					Op:             controlOpFromString(stored.Op),
					Reason:         stored.Reason,
					ExpectedExitAt: stored.ExpectedExitAt,
				},
			},
		}, nil
	case "history":
		return nil, nil
	default:
		return nil, nil
	}
}

func controlOpFromString(s string) agentv1.ControlFrame_Op {
	switch s {
	case "shutdown":
		return agentv1.ControlFrame_OP_SHUTDOWN
	case "suspend":
		return agentv1.ControlFrame_OP_SUSPEND
	case "resume":
		return agentv1.ControlFrame_OP_RESUME
	default:
		return agentv1.ControlFrame_OP_UNSPECIFIED
	}
}

// messageToHistoryItemsProto is the proto twin of the JSON-history replay
// path. Most stored rows map to one HistoryItem; compact_session is the lone
// exception: on replay we must preserve BOTH the tool result (to satisfy the
// preceding assistant(tool_calls=...)) and the synthetic summary marker that
// Snapshot anchors on after rewake.
func messageToHistoryItemsProto(m *domain.Message) []*agentv1.HistoryItem {
	switch m.Kind {
	case domain.MessageKindEvent:
		content := renderHistoryEvent(m.EventName, m.Payload)
		// Kind=event is the trim marker FetchHistory uses to drop the
		// just-enqueued cause from the tail so the agent doesn't see
		// it twice (once from history, once from the inputs stream).
		return []*agentv1.HistoryItem{{Role: "user", Kind: historyEventKind, Content: content}}
	case domain.MessageKindMessage:
		role := m.Role
		if role == "" {
			role = "assistant"
		}
		return []*agentv1.HistoryItem{{
			Role:      role,
			Content:   m.Content,
			ToolCalls: extractToolCallsProto(m.Payload),
		}}
	case domain.MessageKindToolCall:
		toolItem := &agentv1.HistoryItem{
			Role:       "tool",
			Content:    extractToolResult(m.Payload),
			ToolCallId: m.ToolCallID,
		}
		if m.ToolName == "compact_session" {
			if summary := extractCompactSummary(m.Payload); summary != "" {
				return []*agentv1.HistoryItem{
					toolItem,
					{Role: "user", Kind: "summary", Content: summary},
				}
			}
		}
		return []*agentv1.HistoryItem{toolItem}
	default:
		return nil
	}
}

func extractToolCallsProto(payload []byte) []*agentv1.ToolCall {
	if len(payload) == 0 {
		return nil
	}
	var p struct {
		ToolCalls []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil
	}
	if len(p.ToolCalls) == 0 {
		return nil
	}
	out := make([]*agentv1.ToolCall, len(p.ToolCalls))
	for i, c := range p.ToolCalls {
		out[i] = &agentv1.ToolCall{Id: c.ID, Name: c.Name, Arguments: c.Arguments}
	}
	return out
}

// outboundProtoToDomain converts one AppendMessage request's Outbound
// proto into the persistence row shape. Returns (nil, nil) when the
// oneof body is unset or carries a variant we don't persist (today:
// every variant maps to a domain MessageKind so this is mostly a
// defensive nil check).
func outboundProtoToDomain(sessionID int64, frame *agentv1.Outbound) (*domain.Message, error) {
	if frame == nil {
		return nil, nil
	}
	switch body := frame.GetBody().(type) {
	case *agentv1.Outbound_Status:
		return &domain.Message{
			SessionID: sessionID,
			Kind:      domain.MessageKindStatus,
			Payload:   marshalJSON(map[string]any{"phase": body.Status.GetPhase()}),
		}, nil
	case *agentv1.Outbound_Log:
		return &domain.Message{
			SessionID: sessionID,
			Kind:      domain.MessageKindLog,
			Payload:   marshalJSON(map[string]any{"level": body.Log.GetLevel(), "msg": body.Log.GetMessage()}),
		}, nil
	case *agentv1.Outbound_Message:
		payload := map[string]any{}
		if calls := body.Message.GetToolCalls(); len(calls) > 0 {
			out := make([]map[string]any, len(calls))
			for i, c := range calls {
				out[i] = map[string]any{
					"id":        c.GetId(),
					"name":      c.GetName(),
					"arguments": c.GetArguments(),
				}
			}
			payload["tool_calls"] = out
		}
		var raw []byte
		if len(payload) > 0 {
			raw = marshalJSON(payload)
		}
		return &domain.Message{
			SessionID: sessionID,
			Kind:      domain.MessageKindMessage,
			Role:      body.Message.GetRole(),
			Content:   body.Message.GetContent(),
			Payload:   raw,
		}, nil
	case *agentv1.Outbound_ToolCall:
		payload := map[string]any{}
		if args := body.ToolCall.GetArgs(); len(args) > 0 {
			payload["args"] = json.RawMessage(args)
		}
		if result := body.ToolCall.GetResult(); len(result) > 0 {
			payload["result"] = json.RawMessage(result)
		}
		var raw []byte
		if len(payload) > 0 {
			raw = marshalJSON(payload)
		}
		return &domain.Message{
			SessionID:  sessionID,
			Kind:       domain.MessageKindToolCall,
			ToolCallID: body.ToolCall.GetId(),
			ToolName:   body.ToolCall.GetName(),
			Payload:    raw,
		}, nil
	case *agentv1.Outbound_Done:
		return &domain.Message{
			SessionID: sessionID,
			Kind:      domain.MessageKindDone,
			Payload:   marshalJSON(map[string]any{"turn_id": body.Done.GetTurnId()}),
		}, nil
	default:
		return nil, nil
	}
}

// marshalJSON wraps json.Marshal for the small payload maps above.
// Error suppression is deliberate — the maps only contain
// json.Marshal-safe types (string / int / json.RawMessage). A non-nil
// error here would imply a Go runtime bug.
func marshalJSON(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}
