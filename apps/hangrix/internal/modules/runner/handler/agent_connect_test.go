package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	agentv1 "github.com/hangrix/hangrix/gen/go/hangrix/agent/v1"
	"github.com/hangrix/hangrix/gen/go/hangrix/agent/v1/agentv1connect"
)

// stubValidator returns a fixed session for a fixed token and errors
// otherwise. Just enough to drive the interceptor's auth branches.
type stubValidator struct {
	expectedToken string
	session       *domain.AgentSession
	err           error
}

func (v *stubValidator) ValidateSessionToken(_ context.Context, plaintext string) (*domain.AgentSession, error) {
	if v.err != nil {
		return nil, v.err
	}
	if plaintext != v.expectedToken {
		return nil, domain.ErrInvalidSessionToken
	}
	return v.session, nil
}

// connectStubRepo implements the narrow agentConnectRepo interface
// (defined locally in agent_connect.go) — four methods, all the Connect
// handler touches. Tests use the package-private `connectHandlerForTest`
// constructor below to inject this stub without dragging in the full
// domain.Repo.
type connectStubRepo struct {
	appendCalls       []*domain.Message
	listMessagesArgs  []int64
	listMessagesReply []*domain.Message
	claimQueue        []*domain.SessionInput
	markIdleSessionID int64
	markIdleExit      *int32
	markIdleErr       error
}

func (r *connectStubRepo) ListMessages(_ context.Context, sessionID int64) ([]*domain.Message, error) {
	r.listMessagesArgs = append(r.listMessagesArgs, sessionID)
	return r.listMessagesReply, nil
}

func (r *connectStubRepo) AppendMessage(_ context.Context, m *domain.Message) (*domain.Message, error) {
	r.appendCalls = append(r.appendCalls, m)
	return m, nil
}

func (r *connectStubRepo) ClaimPendingInputs(_ context.Context, _ int64, _ int) ([]*domain.SessionInput, error) {
	if len(r.claimQueue) == 0 {
		return nil, nil
	}
	out := r.claimQueue
	r.claimQueue = nil
	return out, nil
}

func (r *connectStubRepo) MarkSessionIdle(_ context.Context, id int64, exit *int32) error {
	r.markIdleSessionID = id
	r.markIdleExit = exit
	return r.markIdleErr
}

// newConnectTestServer wires AgentConnectHandler against the supplied
// stubs and returns a Connect client pointed at it. Token is the
// caller's bearer; pass empty to test the missing-header path.
func newConnectTestServer(t *testing.T, repo *connectStubRepo, validator domain.SessionTokenValidator, token string) (agentv1connect.AgentServiceClient, func()) {
	t.Helper()
	h := newAgentConnectHandlerForTest(repo, validator)
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	httpClient := srv.Client()
	opts := []connect.ClientOption{}
	if token != "" {
		opts = append(opts, connect.WithInterceptors(bearerClientInterceptor(token)))
	}
	client := agentv1connect.NewAgentServiceClient(httpClient, srv.URL, opts...)
	return client, srv.Close
}

// bearerClientInterceptor attaches Authorization: Bearer <token> to
// every outgoing request the test client makes, on both unary and
// streaming calls. Writing it as a full Interceptor (not the
// Unary-only convenience type) is what lets StreamInputs through —
// streaming clients have a separate code path that the Unary form
// misses entirely.
func bearerClientInterceptor(token string) connect.Interceptor {
	return &bearerClient{token: token}
}

type bearerClient struct{ token string }

func (b *bearerClient) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+b.token)
		return next(ctx, req)
	}
}

func (b *bearerClient) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+b.token)
		return conn
	}
}

func (b *bearerClient) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func TestAgentConnect_AppendMessage_PersistsLogFrame(t *testing.T) {
	const sid = int64(42)
	repo := &connectStubRepo{}
	validator := &stubValidator{
		expectedToken: "hgxs_test_token",
		session:       &domain.AgentSession{ID: sid},
	}
	client, cleanup := newConnectTestServer(t, repo, validator, "hgxs_test_token")
	defer cleanup()

	frame := &agentv1.Outbound{Body: &agentv1.Outbound_Log{
		Log: &agentv1.LogFrame{Level: "info", Message: "hello"},
	}}
	_, err := client.AppendMessage(context.Background(), connect.NewRequest(&agentv1.AppendMessageRequest{
		SessionId: sid,
		Frame:     frame,
	}))
	if err != nil {
		t.Fatalf("AppendMessage err: %v", err)
	}
	if len(repo.appendCalls) != 1 {
		t.Fatalf("AppendMessage calls = %d, want 1", len(repo.appendCalls))
	}
	got := repo.appendCalls[0]
	if got.SessionID != sid {
		t.Errorf("session id = %d, want %d", got.SessionID, sid)
	}
	if got.Kind != domain.MessageKindLog {
		t.Errorf("kind = %q, want %q", got.Kind, domain.MessageKindLog)
	}
}

func TestAgentConnect_MissingBearer_Returns401(t *testing.T) {
	repo := &connectStubRepo{}
	validator := &stubValidator{}
	client, cleanup := newConnectTestServer(t, repo, validator, "")
	defer cleanup()

	_, err := client.MarkIdle(context.Background(), connect.NewRequest(&agentv1.MarkIdleRequest{SessionId: 1}))
	if err == nil {
		t.Fatal("expected unauthenticated, got nil")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected *connect.Error, got %T: %v", err, err)
	}
	if connectErr.Code() != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want Unauthenticated", connectErr.Code())
	}
}

func TestAgentConnect_BadToken_Returns403(t *testing.T) {
	repo := &connectStubRepo{}
	validator := &stubValidator{expectedToken: "hgxs_right_token", session: &domain.AgentSession{ID: 1}}
	client, cleanup := newConnectTestServer(t, repo, validator, "hgxs_wrong_token")
	defer cleanup()

	_, err := client.MarkIdle(context.Background(), connect.NewRequest(&agentv1.MarkIdleRequest{SessionId: 1}))
	if err == nil {
		t.Fatal("expected permission denied, got nil")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected *connect.Error, got %T: %v", err, err)
	}
	if connectErr.Code() != connect.CodePermissionDenied {
		t.Errorf("code = %v, want PermissionDenied", connectErr.Code())
	}
}

func TestAgentConnect_SessionIDMismatch_Returns404(t *testing.T) {
	repo := &connectStubRepo{}
	validator := &stubValidator{
		expectedToken: "hgxs_test_token",
		session:       &domain.AgentSession{ID: 42}, // token-bound session
	}
	client, cleanup := newConnectTestServer(t, repo, validator, "hgxs_test_token")
	defer cleanup()

	// Request session_id 99 with a token bound to session 42 — caller
	// must not see other sessions' data even if the URL says so.
	_, err := client.MarkIdle(context.Background(), connect.NewRequest(&agentv1.MarkIdleRequest{SessionId: 99}))
	if err == nil {
		t.Fatal("expected not-found, got nil")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected *connect.Error, got %T: %v", err, err)
	}
	if connectErr.Code() != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", connectErr.Code())
	}
}

func TestAgentConnect_MarkIdle_FlipsSession(t *testing.T) {
	const sid = int64(7)
	repo := &connectStubRepo{}
	validator := &stubValidator{expectedToken: "hgxs_test_token", session: &domain.AgentSession{ID: sid}}
	client, cleanup := newConnectTestServer(t, repo, validator, "hgxs_test_token")
	defer cleanup()

	_, err := client.MarkIdle(context.Background(), connect.NewRequest(&agentv1.MarkIdleRequest{SessionId: sid, RunningJobs: 0}))
	if err != nil {
		t.Fatalf("MarkIdle err: %v", err)
	}
	if repo.markIdleSessionID != sid {
		t.Errorf("MarkSessionIdle target = %d, want %d", repo.markIdleSessionID, sid)
	}
}

// TestAgentConnect_FetchHistory_TrimsTrailingEventItems pins the dedup
// for the just-enqueued cause event: the agent's first-wake context
// must NOT include the event row at the tail of history, because the
// stream is about to deliver the same frame via StreamInputs and
// applyInboxItem will append it as a user-role message — leaving it
// in history too would surface the same event twice to the LLM.
//
// Past events (already responded to, i.e. followed by an assistant
// message) survive the trim because the trailing-only loop stops at
// the first non-event item.
func TestAgentConnect_FetchHistory_TrimsTrailingEventItems(t *testing.T) {
	const sid = int64(11)
	repo := &connectStubRepo{
		listMessagesReply: []*domain.Message{
			// Settled past turn — survives the trim.
			{SessionID: sid, Kind: domain.MessageKindEvent, EventName: "issue.opened", Payload: []byte(`{"payload":{}}`)},
			{SessionID: sid, Kind: domain.MessageKindMessage, Role: "assistant", Content: "ack 1"},
			// Just-enqueued cause for the current wake — must be trimmed.
			{SessionID: sid, Kind: domain.MessageKindEvent, EventName: "issue.comment", Payload: []byte(`{"payload":{}}`)},
		},
	}
	validator := &stubValidator{expectedToken: "hgxs_test_token", session: &domain.AgentSession{ID: sid}}
	client, cleanup := newConnectTestServer(t, repo, validator, "hgxs_test_token")
	defer cleanup()

	resp, err := client.FetchHistory(context.Background(), connect.NewRequest(&agentv1.FetchHistoryRequest{SessionId: sid}))
	if err != nil {
		t.Fatalf("FetchHistory err: %v", err)
	}
	got := resp.Msg.GetMessages()
	if len(got) != 2 {
		t.Fatalf("returned %d items, want 2 (trim should have dropped the trailing event)", len(got))
	}
	if got[0].GetKind() != "event" {
		t.Errorf("items[0].kind = %q, want event (settled past trigger)", got[0].GetKind())
	}
	if got[1].GetRole() != "assistant" || got[1].GetContent() != "ack 1" {
		t.Errorf("items[1] = %+v, want the assistant ack", got[1])
	}
}

// TestAgentConnect_FetchHistory_TrimsAllTrailingEvents covers the
// pathological case where a previous wake crashed mid-turn and the
// spawner enqueued another cause on the next trigger: multiple
// unsettled events pile up at the tail. The whole run must be trimmed
// — the inputs queue will redeliver each.
func TestAgentConnect_FetchHistory_TrimsAllTrailingEvents(t *testing.T) {
	const sid = int64(13)
	repo := &connectStubRepo{
		listMessagesReply: []*domain.Message{
			{SessionID: sid, Kind: domain.MessageKindMessage, Role: "assistant", Content: "older ack"},
			{SessionID: sid, Kind: domain.MessageKindEvent, EventName: "issue.comment", Payload: []byte(`{"payload":{}}`)},
			{SessionID: sid, Kind: domain.MessageKindEvent, EventName: "issue.comment", Payload: []byte(`{"payload":{}}`)},
		},
	}
	validator := &stubValidator{expectedToken: "hgxs_test_token", session: &domain.AgentSession{ID: sid}}
	client, cleanup := newConnectTestServer(t, repo, validator, "hgxs_test_token")
	defer cleanup()

	resp, err := client.FetchHistory(context.Background(), connect.NewRequest(&agentv1.FetchHistoryRequest{SessionId: sid}))
	if err != nil {
		t.Fatalf("FetchHistory err: %v", err)
	}
	got := resp.Msg.GetMessages()
	if len(got) != 1 {
		t.Fatalf("returned %d items, want 1 (both trailing events should be trimmed)", len(got))
	}
}

func TestAgentConnect_FetchHistory_ReordersMidTurnEventAfterToolResults(t *testing.T) {
	const sid = int64(17)
	repo := &connectStubRepo{
		listMessagesReply: []*domain.Message{
			{SessionID: sid, Kind: domain.MessageKindMessage, Role: "assistant", Content: "working", Payload: []byte(`{"tool_calls":[{"id":"tc_1","name":"issue_comment","arguments":"{}"}]}`)},
			{SessionID: sid, Kind: domain.MessageKindEvent, EventName: "issue.comment", Payload: []byte(`{"payload":{}}`)},
			{SessionID: sid, Kind: domain.MessageKindToolCall, ToolCallID: "tc_1", ToolName: "issue_comment", Payload: []byte(`{"result":{"ok":true}}`)},
		},
	}
	validator := &stubValidator{expectedToken: "hgxs_test_token", session: &domain.AgentSession{ID: sid}}
	client, cleanup := newConnectTestServer(t, repo, validator, "hgxs_test_token")
	defer cleanup()

	resp, err := client.FetchHistory(context.Background(), connect.NewRequest(&agentv1.FetchHistoryRequest{SessionId: sid}))
	if err != nil {
		t.Fatalf("FetchHistory err: %v", err)
	}
	got := resp.Msg.GetMessages()
	if len(got) != 3 {
		t.Fatalf("returned %d items, want 3", len(got))
	}
	if got[0].GetRole() != "assistant" || len(got[0].GetToolCalls()) != 1 || got[0].GetToolCalls()[0].GetId() != "tc_1" {
		t.Fatalf("items[0] = %+v, want assistant with tool call tc_1", got[0])
	}
	if got[1].GetRole() != "tool" || got[1].GetToolCallId() != "tc_1" {
		t.Fatalf("items[1] = %+v, want tool result for tc_1 immediately after assistant", got[1])
	}
	if got[2].GetKind() != historyEventKind {
		t.Fatalf("items[2] = %+v, want deferred event after tool result", got[2])
	}
}

func TestNormalizeInterruptedToolResultChains_DropsBufferedEventWhenChainIncomplete(t *testing.T) {
	got := normalizeInterruptedToolResultChains([]*agentv1.HistoryItem{
		{Role: "assistant", Content: "working", ToolCalls: []*agentv1.ToolCall{{Id: "tc_1", Name: "issue_comment"}}},
		{Role: "user", Kind: historyEventKind, Content: "event"},
	})
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1; got=%+v", len(got), got)
	}
	if got[0].GetRole() != "assistant" || len(got[0].GetToolCalls()) != 1 {
		t.Fatalf("got[0] = %+v, want original assistant tool-call item", got[0])
	}
}

func TestAgentConnect_FetchHistory_CompactSessionReplaysToolResultAndSummary(t *testing.T) {
	const sid = int64(19)
	repo := &connectStubRepo{
		listMessagesReply: []*domain.Message{
			{SessionID: sid, Kind: domain.MessageKindMessage, Role: "assistant", Content: "compacting", Payload: []byte(`{"tool_calls":[{"id":"cs_1","name":"compact_session","arguments":"{\"summary\":\"summary here\"}"}]}`)},
			{SessionID: sid, Kind: domain.MessageKindToolCall, ToolCallID: "cs_1", ToolName: "compact_session", Payload: []byte(`{"args":{"summary":"summary here"},"result":{"ok":true}}`)},
		},
	}
	validator := &stubValidator{expectedToken: "hgxs_test_token", session: &domain.AgentSession{ID: sid}}
	client, cleanup := newConnectTestServer(t, repo, validator, "hgxs_test_token")
	defer cleanup()

	resp, err := client.FetchHistory(context.Background(), connect.NewRequest(&agentv1.FetchHistoryRequest{SessionId: sid}))
	if err != nil {
		t.Fatalf("FetchHistory err: %v", err)
	}
	got := resp.Msg.GetMessages()
	if len(got) != 3 {
		t.Fatalf("returned %d items, want 3 (assistant + tool result + summary)", len(got))
	}
	if got[0].GetRole() != "assistant" || len(got[0].GetToolCalls()) != 1 || got[0].GetToolCalls()[0].GetId() != "cs_1" {
		t.Fatalf("items[0] = %+v, want compact_session assistant with tool call", got[0])
	}
	if got[1].GetRole() != "tool" || got[1].GetToolCallId() != "cs_1" {
		t.Fatalf("items[1] = %+v, want tool result for cs_1", got[1])
	}
	if got[2].GetKind() != "summary" || got[2].GetContent() != "summary here" {
		t.Fatalf("items[2] = %+v, want summary marker", got[2])
	}
}

func TestAgentConnect_StreamInputs_ServesQueuedEvent(t *testing.T) {
	const sid = int64(3)
	repo := &connectStubRepo{
		claimQueue: []*domain.SessionInput{
			{Payload: []byte(`{"kind":"event","event":"issue.comment","payload":{"foo":"bar"}}`)},
		},
	}
	validator := &stubValidator{expectedToken: "hgxs_test_token", session: &domain.AgentSession{ID: sid}}

	// The streaming interceptor and bearer header attach via the same
	// interceptor model on Connect's streaming client; reuse the helper.
	httpClient := http.DefaultClient
	h := newAgentConnectHandlerForTest(repo, validator)
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	defer srv.Close()
	client := agentv1connect.NewAgentServiceClient(httpClient, srv.URL,
		connect.WithInterceptors(bearerClientInterceptor("hgxs_test_token")))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := client.StreamInputs(ctx, connect.NewRequest(&agentv1.StreamInputsRequest{SessionId: sid}))
	if err != nil {
		t.Fatalf("StreamInputs open: %v", err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatalf("Receive failed: %v", stream.Err())
	}
	msg := stream.Msg()
	got, ok := msg.GetBody().(*agentv1.StreamInputsResponse_Event)
	if !ok {
		t.Fatalf("expected event body, got %T", msg.GetBody())
	}
	if got.Event.GetEvent() != "issue.comment" {
		t.Errorf("event name = %q, want issue.comment", got.Event.GetEvent())
	}
}

// TestTrimTrailingDanglingToolCallsProto covers the server-side guard that
// drops trailing assistant HistoryItems carrying tool_calls with no
// corresponding tool items after them.
func TestTrimTrailingDanglingToolCallsProto(t *testing.T) {
	t.Parallel()

	tcs := []struct {
		name string
		in   []*agentv1.HistoryItem
		want int
	}{
		{
			name: "empty",
			in:   nil,
			want: 0,
		},
		{
			name: "clean assistant no tool calls",
			in: []*agentv1.HistoryItem{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi there"},
			},
			want: 2,
		},
		{
			name: "clean assistant with tool calls + tool results",
			in: []*agentv1.HistoryItem{
				{Role: "user", Content: "check file"},
				{Role: "assistant", Content: "checking", ToolCalls: []*agentv1.ToolCall{{Id: "tc_1", Name: "read"}}},
				{Role: "tool", ToolCallId: "tc_1", Content: "contents"},
				{Role: "assistant", Content: "done"},
			},
			want: 4,
		},
		{
			name: "dangling tool_use at end",
			in: []*agentv1.HistoryItem{
				{Role: "user", Content: "check file"},
				{Role: "assistant", Content: "checking", ToolCalls: []*agentv1.ToolCall{{Id: "tc_1", Name: "read"}}},
			},
			want: 1,
		},
		{
			name: "dangling tool_use with preceding event",
			in: []*agentv1.HistoryItem{
				{Role: "user", Content: "event 1"},
				{Role: "assistant", Content: "done 1"},
				{Role: "user", Content: "event 2"},
				{Role: "assistant", Content: "working", ToolCalls: []*agentv1.ToolCall{{Id: "tc_broken", Name: "read"}}},
			},
			want: 3,
		},
		{
			name: "multiple dangling tool_uses at end",
			in: []*agentv1.HistoryItem{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "first", ToolCalls: []*agentv1.ToolCall{{Id: "a", Name: "read"}}},
				{Role: "tool", ToolCallId: "a", Content: "ok"},
				{Role: "assistant", Content: "second", ToolCalls: []*agentv1.ToolCall{{Id: "b", Name: "glob"}}},
				{Role: "assistant", Content: "third", ToolCalls: []*agentv1.ToolCall{{Id: "c", Name: "grep"}}},
			},
			want: 3,
		},
		{
			name: "summary marker at end does not hide dangling assistant",
			in: []*agentv1.HistoryItem{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "compacting", ToolCalls: []*agentv1.ToolCall{{Id: "cs", Name: "compact_session"}}},
				{Role: "user", Kind: "summary", Content: "summary here"},
			},
			want: 1,
		},
	}

	for _, tc := range tcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := trimTrailingDanglingToolCallsProto(tc.in)
			if len(got) != tc.want {
				t.Errorf("len=%d, want %d", len(got), tc.want)
			}
		})
	}
}
