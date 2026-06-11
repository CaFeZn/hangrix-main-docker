package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	runnerv1 "github.com/hangrix/hangrix/gen/go/hangrix/runner/v1"
	"github.com/hangrix/hangrix/gen/go/hangrix/runner/v1/runnerv1connect"
	"github.com/hangrix/hangrix/pkg/cryptobox"
)

// testEncryptionKey is a deterministic 32-byte AES key encoded as
// base64. Cryptobox refuses an empty key; tests don't actually exercise
// session-token decryption (those paths only run inside Tasks, which
// these tests skip), so any well-formed key works.
const testEncryptionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 bytes of 0x00, base64'd

// ---- stubs ----

// stubRunnerRepo is a minimal in-memory implementation of
// runnerConnectRepo. Only the methods each test actually exercises do
// anything; the rest return zero values or sentinel "not pending"
// errors so callers can drive long-poll paths quickly.
type stubRunnerRepo struct {
	heartbeatCalls   []heartbeatCall
	heartbeatErr     error
	cleanupRows      []domain.ContainerCleanupTask
	cleanupListErr   error
	clearCalls       []clearCall
	clearErr         error
	stopRows         []domain.ContainerStopTask
	stopListErr      error
	ackStopCalls     []clearCall
	ackStopErr       error
	claimSessionErr  error
	claimSessionRows *domain.AgentSession
	markTerminalErr  error
}

type heartbeatCall struct {
	ID  int64
	Cap []byte
}

type clearCall struct {
	SessionID int64
	RunnerID  int64
}

func (r *stubRunnerRepo) UpdateRunnerHeartbeat(_ context.Context, id int64, caps []byte) error {
	r.heartbeatCalls = append(r.heartbeatCalls, heartbeatCall{ID: id, Cap: caps})
	return r.heartbeatErr
}

func (r *stubRunnerRepo) ClaimNextSession(_ context.Context, _ int64) (*domain.AgentSession, error) {
	if r.claimSessionErr != nil {
		return nil, r.claimSessionErr
	}
	if r.claimSessionRows != nil {
		return r.claimSessionRows, nil
	}
	return nil, domain.ErrNoPendingSession
}

func (r *stubRunnerRepo) MarkSessionTerminal(_ context.Context, _ int64, _ domain.SessionStatus, _ *int32, _ string) error {
	return r.markTerminalErr
}

func (r *stubRunnerRepo) ListPendingContainerCleanups(_ context.Context, _ int64, _ int) ([]domain.ContainerCleanupTask, error) {
	if r.cleanupListErr != nil {
		return nil, r.cleanupListErr
	}
	return r.cleanupRows, nil
}

func (r *stubRunnerRepo) ClearSessionContainer(_ context.Context, sessionID, runnerID int64) error {
	r.clearCalls = append(r.clearCalls, clearCall{SessionID: sessionID, RunnerID: runnerID})
	return r.clearErr
}

func (r *stubRunnerRepo) ListPendingContainerStops(_ context.Context, _ int64, _ int) ([]domain.ContainerStopTask, error) {
	if r.stopListErr != nil {
		return nil, r.stopListErr
	}
	return r.stopRows, nil
}

func (r *stubRunnerRepo) AckContainerStop(_ context.Context, sessionID, runnerID int64) error {
	r.ackStopCalls = append(r.ackStopCalls, clearCall{SessionID: sessionID, RunnerID: runnerID})
	return r.ackStopErr
}

// stubAgentValidator validates Bearer tokens. Returns runner for any
// token equal to `expectedToken`; everything else surfaces the
// canonical "invalid" / "inactive" errors so the interceptor's
// 401 / 403 / 500 mapping can be exercised.
type stubAgentValidator struct {
	expectedToken string
	runner        *domain.Runner
	err           error
}

func (v *stubAgentValidator) ValidateAgentToken(_ context.Context, token string) (*domain.Runner, error) {
	if v.err != nil {
		return nil, v.err
	}
	if token != v.expectedToken {
		return nil, domain.ErrInvalidToken
	}
	return v.runner, nil
}

// stubEnrollValidator is the redemption stub. Returns redeemResult on
// any token equal to `expectedToken`; an empty result + nil error is
// not allowed by the contract so we always populate the runner.
type stubEnrollValidator struct {
	expectedToken string
	result        *domain.RedeemEnrollResult
	err           error
}

func (v *stubEnrollValidator) RedeemEnrollment(_ context.Context, token string, _ []byte) (*domain.RedeemEnrollResult, error) {
	if v.err != nil {
		return nil, v.err
	}
	if token != v.expectedToken {
		return nil, domain.ErrInvalidToken
	}
	return v.result, nil
}

// stubWorkflow is the no-op runnerConnectWorkflow stub. Tasks tests
// always return ErrNoPendingJob; workflow-callback tests are deferred
// to Phase 3 (Connect client end-to-end).
// Set jobs / run fields to simulate a real claim.
type stubWorkflow struct {
	jobs []*workflowdomain.WorkflowJobRun
	err  error
	run  *workflowdomain.WorkflowRun
}

func (s stubWorkflow) ClaimNextJob(_ context.Context, _ int64) (*workflowdomain.WorkflowJobRun, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(s.jobs) == 0 {
		return nil, workflowdomain.ErrNoPendingJob
	}
	return s.jobs[0], nil
}
func (s stubWorkflow) ClaimNextJobs(_ context.Context, _ int64, _ int) ([]*workflowdomain.WorkflowJobRun, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(s.jobs) == 0 {
		return nil, nil
	}
	return s.jobs, nil
}
func (s stubWorkflow) GetRunForJob(_ context.Context, _ int64) (*workflowdomain.WorkflowRun, error) {
	if s.run != nil {
		return s.run, nil
	}
	return nil, errors.New("not implemented in stub")
}
func (stubWorkflow) MarkJobRunning(_ context.Context, _, _ int64) error { return nil }
func (stubWorkflow) AppendLog(_ context.Context, _ int64, _ workflowdomain.LogStream, _ string, _ *string) error {
	return nil
}
func (stubWorkflow) SetStepOutputs(_ context.Context, _ int64, _ string, _ map[string]workflowdomain.StepOutputValue) error {
	return nil
}
func (stubWorkflow) MarkJobTerminal(_ context.Context, _ int64, _ workflowdomain.JobStatus, _ *int32, _ string) error {
	return nil
}
func (stubWorkflow) ResolveJobOutputs(_ context.Context, _ int64) error { return nil }
func (stubWorkflow) GetJobRun(_ context.Context, _ int64) (*workflowdomain.WorkflowJobRun, error) {
	return nil, errors.New("not implemented in stub")
}
func (stubWorkflow) AdvanceRun(_ context.Context, _ int64) error { return nil }
func (stubWorkflow) RegisterPhase(_ context.Context, _ int64, _ workflowdomain.PhaseKind, _ int32, _ string) (*workflowdomain.JobPhase, error) {
	return nil, errors.New("not implemented in stub")
}
func (stubWorkflow) MarkPhaseRunning(_ context.Context, _ int64, _ workflowdomain.PhaseKind) error {
	return nil
}
func (stubWorkflow) MarkPhaseTerminal(_ context.Context, _ int64, _ workflowdomain.PhaseKind, _ workflowdomain.PhaseStatus, _ *int32, _ string) error {
	return nil
}

// ---- harness ----

// newRunnerConnectTestServer spins up a httptest.Server hosting the
// RunnerConnectHandler with the supplied stubs and returns a Connect
// client pointed at it. Token is the bearer header to attach to every
// call; pass empty to test the missing-header path.
func newRunnerConnectTestServer(
	t *testing.T,
	repo runnerConnectRepo,
	agentValidator domain.AgentValidator,
	enrollValidator domain.EnrollValidator,
	workflow runnerConnectWorkflow,
	token string,
) (runnerv1connect.RunnerServiceClient, func()) {
	t.Helper()
	cfg := &config.Config{}
	cfg.LLM.EncryptionKey = testEncryptionKey
	cfg.Server.URL = "https://platform.test"
	box, err := cryptobox.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("cryptobox: %v", err)
	}
	h := newRunnerConnectHandlerForTest(repo, agentValidator, enrollValidator, box, cfg, nil, nil, workflow)
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	httpClient := srv.Client()
	opts := []connect.ClientOption{}
	if token != "" {
		opts = append(opts, connect.WithInterceptors(bearerClientInterceptor(token)))
	}
	client := runnerv1connect.NewRunnerServiceClient(httpClient, srv.URL, opts...)
	return client, srv.Close
}

// ---- Enroll ----

func TestRunnerConnect_Enroll_Success(t *testing.T) {
	enroll := &stubEnrollValidator{
		expectedToken: "hgxe_test",
		result: &domain.RedeemEnrollResult{
			Runner:              &domain.Runner{ID: 7, Name: "rn-1"},
			AgentTokenPlaintext: "hgxr_minted",
		},
	}
	// Auth validator unused on Enroll (interceptor exempts it), but we
	// still pass a stub so the handler constructor doesn't see nil.
	client, cleanup := newRunnerConnectTestServer(t, &stubRunnerRepo{}, &stubAgentValidator{}, enroll, stubWorkflow{}, "")
	defer cleanup()

	resp, err := client.Enroll(context.Background(), connect.NewRequest(&runnerv1.EnrollRequest{
		EnrollToken: "hgxe_test",
	}))
	if err != nil {
		t.Fatalf("Enroll err: %v", err)
	}
	if resp.Msg.GetRunnerId() != 7 {
		t.Errorf("runner_id = %d, want 7", resp.Msg.GetRunnerId())
	}
	if resp.Msg.GetRunnerName() != "rn-1" {
		t.Errorf("runner_name = %q, want rn-1", resp.Msg.GetRunnerName())
	}
	if resp.Msg.GetAgentToken() != "hgxr_minted" {
		t.Errorf("agent_token = %q, want hgxr_minted", resp.Msg.GetAgentToken())
	}
	bp := resp.Msg.GetBootstrap()
	if bp == nil {
		t.Fatal("Bootstrap payload not populated")
	}
	if bp.GetBaseUrl() != "https://platform.test" {
		t.Errorf("base_url = %q, want https://platform.test", bp.GetBaseUrl())
	}
	if bp.GetPollWaitSec() <= 0 {
		t.Errorf("poll_wait_sec = %d, want > 0", bp.GetPollWaitSec())
	}
}

func TestRunnerConnect_Enroll_InvalidToken_Returns401(t *testing.T) {
	enroll := &stubEnrollValidator{expectedToken: "hgxe_correct"}
	client, cleanup := newRunnerConnectTestServer(t, &stubRunnerRepo{}, &stubAgentValidator{}, enroll, stubWorkflow{}, "")
	defer cleanup()

	_, err := client.Enroll(context.Background(), connect.NewRequest(&runnerv1.EnrollRequest{
		EnrollToken: "hgxe_wrong",
	}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want Unauthenticated", code)
	}
}

func TestRunnerConnect_Enroll_AlreadyUsed_ReturnsConflict(t *testing.T) {
	enroll := &stubEnrollValidator{err: domain.ErrEnrollUsed}
	client, cleanup := newRunnerConnectTestServer(t, &stubRunnerRepo{}, &stubAgentValidator{}, enroll, stubWorkflow{}, "")
	defer cleanup()

	_, err := client.Enroll(context.Background(), connect.NewRequest(&runnerv1.EnrollRequest{
		EnrollToken: "hgxe_anything",
	}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeAlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", code)
	}
}

// ---- Auth interceptor (non-Enroll RPCs) ----
//
// Heartbeat stands in for the whole "behind agentTokenInterceptor"
// surface — these tests pin the interceptor's behaviour, not the
// specific RPC. (Bootstrap used to live here too; it moved off
// Connect to plain HTTP — see binary.go — so its tests now live
// alongside that handler.)

func TestRunnerConnect_AuthInterceptor_MissingBearer_Returns401(t *testing.T) {
	client, cleanup := newRunnerConnectTestServer(t, &stubRunnerRepo{}, &stubAgentValidator{}, &stubEnrollValidator{}, stubWorkflow{}, "")
	defer cleanup()

	_, err := client.Heartbeat(context.Background(), connect.NewRequest(&runnerv1.HeartbeatRequest{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want Unauthenticated", code)
	}
}

func TestRunnerConnect_AuthInterceptor_BadToken_Returns401(t *testing.T) {
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 1}}
	client, cleanup := newRunnerConnectTestServer(t, &stubRunnerRepo{}, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_wrong")
	defer cleanup()

	_, err := client.Heartbeat(context.Background(), connect.NewRequest(&runnerv1.HeartbeatRequest{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want Unauthenticated", code)
	}
}

func TestRunnerConnect_AuthInterceptor_InactiveToken_Returns403(t *testing.T) {
	auth := &stubAgentValidator{err: domain.ErrTokenInactive}
	client, cleanup := newRunnerConnectTestServer(t, &stubRunnerRepo{}, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_dead")
	defer cleanup()

	_, err := client.Heartbeat(context.Background(), connect.NewRequest(&runnerv1.HeartbeatRequest{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Errorf("code = %v, want PermissionDenied", code)
	}
}

// TestRunnerConnect_Enroll_FallsBackToRequestHost pins the
// devcontainer / `go run` happy path on the surviving Connect path
// that still embeds a BootstrapPayload: when cfg.Server.URL is empty,
// the Enroll response must derive BaseURL from the inbound request
// rather than hard-coding "http://localhost:8080". capturePublicBase
// stashes scheme://Host onto ctx; this test exercises that.
//
// Regression for the dev workflow: without this, every agent
// container the runner spawns would receive
// HANGRIX_PLATFORM_BASE_URL=http://localhost:8080 and fail to reach
// the platform from inside the container network.
func TestRunnerConnect_Enroll_FallsBackToRequestHost(t *testing.T) {
	enroll := &stubEnrollValidator{
		expectedToken: "hgxe_test",
		result: &domain.RedeemEnrollResult{
			Runner:              &domain.Runner{ID: 1, Name: "rn-1"},
			AgentTokenPlaintext: "hgxr_minted",
		},
	}
	// Spin up a custom server with cfg.Server.URL empty so the
	// fallback path is the one under test.
	cfg := &config.Config{}
	cfg.LLM.EncryptionKey = testEncryptionKey
	// Server.URL deliberately left blank.
	box, err := cryptobox.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("cryptobox: %v", err)
	}
	h := newRunnerConnectHandlerForTest(&stubRunnerRepo{}, &stubAgentValidator{}, enroll, box, cfg, nil, nil, stubWorkflow{})
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	client := runnerv1connect.NewRunnerServiceClient(srv.Client(), srv.URL)

	resp, err := client.Enroll(context.Background(), connect.NewRequest(&runnerv1.EnrollRequest{
		EnrollToken: "hgxe_test",
	}))
	if err != nil {
		t.Fatalf("Enroll err: %v", err)
	}
	got := resp.Msg.GetBootstrap().GetBaseUrl()
	// The httptest.Server's URL is what we passed as the dial target;
	// the handler should echo back scheme://Host derived from the
	// inbound request, which equals srv.URL.
	if got != srv.URL {
		t.Errorf("BaseURL = %q, want %q (derived from request Host)", got, srv.URL)
	}
}

// ---- Heartbeat ----

func TestRunnerConnect_Heartbeat_OK(t *testing.T) {
	repo := &stubRunnerRepo{}
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 42}}
	client, cleanup := newRunnerConnectTestServer(t, repo, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_correct")
	defer cleanup()

	_, err := client.Heartbeat(context.Background(), connect.NewRequest(&runnerv1.HeartbeatRequest{
		Capabilities: []byte(`{"docker":"27.0"}`),
	}))
	if err != nil {
		t.Fatalf("Heartbeat err: %v", err)
	}
	if len(repo.heartbeatCalls) != 1 {
		t.Fatalf("heartbeat calls = %d, want 1", len(repo.heartbeatCalls))
	}
	if repo.heartbeatCalls[0].ID != 42 {
		t.Errorf("runner id = %d, want 42", repo.heartbeatCalls[0].ID)
	}
	if string(repo.heartbeatCalls[0].Cap) != `{"docker":"27.0"}` {
		t.Errorf("capabilities = %q, want %q", repo.heartbeatCalls[0].Cap, `{"docker":"27.0"}`)
	}
}

// ---- Cleanup tasks ----

func TestRunnerConnect_ListCleanupTasks_ReturnsRows(t *testing.T) {
	repo := &stubRunnerRepo{
		cleanupRows: []domain.ContainerCleanupTask{
			{SessionID: 1, ContainerID: "c1"},
			{SessionID: 2, ContainerID: "c2"},
		},
	}
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 1}}
	client, cleanup := newRunnerConnectTestServer(t, repo, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_correct")
	defer cleanup()

	resp, err := client.ListCleanupTasks(context.Background(), connect.NewRequest(&runnerv1.ListCleanupTasksRequest{}))
	if err != nil {
		t.Fatalf("ListCleanupTasks err: %v", err)
	}
	got := resp.Msg.GetTasks()
	if len(got) != 2 {
		t.Fatalf("tasks = %d, want 2", len(got))
	}
	if got[0].GetSessionId() != 1 || got[0].GetContainerId() != "c1" {
		t.Errorf("tasks[0] = (%d, %q), want (1, \"c1\")", got[0].GetSessionId(), got[0].GetContainerId())
	}
}

func TestRunnerConnect_ListCleanupTasks_EmptyReturnsEmptyList(t *testing.T) {
	repo := &stubRunnerRepo{}
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 1}}
	client, cleanup := newRunnerConnectTestServer(t, repo, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_correct")
	defer cleanup()

	resp, err := client.ListCleanupTasks(context.Background(), connect.NewRequest(&runnerv1.ListCleanupTasksRequest{}))
	if err != nil {
		t.Fatalf("ListCleanupTasks err: %v", err)
	}
	if len(resp.Msg.GetTasks()) != 0 {
		t.Errorf("tasks = %d, want 0", len(resp.Msg.GetTasks()))
	}
}

func TestRunnerConnect_MarkCleanupDone_OK(t *testing.T) {
	repo := &stubRunnerRepo{}
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 7}}
	client, cleanup := newRunnerConnectTestServer(t, repo, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_correct")
	defer cleanup()

	_, err := client.MarkCleanupDone(context.Background(), connect.NewRequest(&runnerv1.MarkCleanupDoneRequest{
		SessionId: 99,
	}))
	if err != nil {
		t.Fatalf("MarkCleanupDone err: %v", err)
	}
	if len(repo.clearCalls) != 1 {
		t.Fatalf("clear calls = %d, want 1", len(repo.clearCalls))
	}
	if repo.clearCalls[0].SessionID != 99 || repo.clearCalls[0].RunnerID != 7 {
		t.Errorf("cleared (%d, %d), want (99, 7)", repo.clearCalls[0].SessionID, repo.clearCalls[0].RunnerID)
	}
}

// MarkCleanupDone on a deleted session must return success — the
// session row may have been pruned between list + ack. Legacy handler
// translated this to 204; Connect-side should be a clean success too.
func TestRunnerConnect_MarkCleanupDone_NotFound_TreatedAsSuccess(t *testing.T) {
	repo := &stubRunnerRepo{clearErr: domain.ErrSessionNotFound}
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 7}}
	client, cleanup := newRunnerConnectTestServer(t, repo, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_correct")
	defer cleanup()

	_, err := client.MarkCleanupDone(context.Background(), connect.NewRequest(&runnerv1.MarkCleanupDoneRequest{
		SessionId: 99,
	}))
	if err != nil {
		t.Fatalf("MarkCleanupDone (notFound) should succeed, got: %v", err)
	}
}

// ---- Stop tasks ----

func TestRunnerConnect_ListStopTasks_ReturnsRows(t *testing.T) {
	repo := &stubRunnerRepo{
		stopRows: []domain.ContainerStopTask{
			{SessionID: 5, ContainerID: "cstop"},
		},
	}
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 1}}
	client, cleanup := newRunnerConnectTestServer(t, repo, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_correct")
	defer cleanup()

	resp, err := client.ListStopTasks(context.Background(), connect.NewRequest(&runnerv1.ListStopTasksRequest{}))
	if err != nil {
		t.Fatalf("ListStopTasks err: %v", err)
	}
	got := resp.Msg.GetTasks()
	if len(got) != 1 {
		t.Fatalf("tasks = %d, want 1", len(got))
	}
	if got[0].GetSessionId() != 5 || got[0].GetContainerId() != "cstop" {
		t.Errorf("tasks[0] = (%d, %q), want (5, \"cstop\")", got[0].GetSessionId(), got[0].GetContainerId())
	}
}

func TestRunnerConnect_MarkStopDone_OK(t *testing.T) {
	repo := &stubRunnerRepo{}
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 7}}
	client, cleanup := newRunnerConnectTestServer(t, repo, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_correct")
	defer cleanup()

	_, err := client.MarkStopDone(context.Background(), connect.NewRequest(&runnerv1.MarkStopDoneRequest{
		SessionId: 88,
	}))
	if err != nil {
		t.Fatalf("MarkStopDone err: %v", err)
	}
	if len(repo.ackStopCalls) != 1 {
		t.Fatalf("ack calls = %d, want 1", len(repo.ackStopCalls))
	}
}

// ---- Tasks (no-work path only — full claim paths covered in Phase 3) ----

// When neither a session nor a workflow job is pending, Tasks returns
// an empty TasksResponse (oneof unset). The wait period is bounded so
// the test runs fast: we shrink the deadline by cancelling the
// request context immediately after the call kicks off.
func TestRunnerConnect_Tasks_NoWork_ReturnsEmpty(t *testing.T) {
	repo := &stubRunnerRepo{} // ClaimNextSession → ErrNoPendingSession
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 1}}
	client, cleanup := newRunnerConnectTestServer(t, repo, auth, &stubEnrollValidator{}, stubWorkflow{}, "hgxr_correct")
	defer cleanup()

	// Bound the wait: cancel the request context after a short delay
	// so the handler's select on ctx.Done fires before the pollWait
	// deadline. Empty TasksResponse on either path.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	resp, err := client.Tasks(ctx, connect.NewRequest(&runnerv1.TasksRequest{}))
	if err != nil {
		// CodeCanceled / CodeDeadlineExceeded are acceptable — the
		// Connect runtime may surface the timeout as a transport error
		// rather than letting the handler's empty response through.
		switch connect.CodeOf(err) {
		case connect.CodeCanceled, connect.CodeDeadlineExceeded:
			return
		}
		t.Fatalf("Tasks err: %v", err)
	}
	if resp.Msg.GetTask() != nil {
		t.Errorf("Task should be unset when no work pending, got %+v", resp.Msg.GetTask())
	}
}

// TestRunnerConnect_Tasks_LegacyClient_DualFill verifies that when
// max_batch is unset (0), the server populates both .Task and .Tasks[0]
// for backward compatibility with old runners.
func TestRunnerConnect_Tasks_LegacyClient_DualFill(t *testing.T) {
	repoStore := &stubRepoForTasks{}

	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 1}}
	wf := stubWorkflow{
		jobs: []*workflowdomain.WorkflowJobRun{
			{ID: 100, WorkflowRunID: 200, JobKey: "build", Status: workflowdomain.JobStatusRunning, SequenceIndex: 0},
		},
		run: &workflowdomain.WorkflowRun{
			ID: 200, RepoID: 1, WorkflowName: "ci", Status: workflowdomain.RunStatusRunning,
			EventName: workflowdomain.EventRepoPush, Ref: "refs/heads/main", CommitSHA: "abc123",
		},
	}

	cfg := &config.Config{}
	cfg.LLM.EncryptionKey = testEncryptionKey
	box, err := cryptobox.New(testEncryptionKey)
	if err != nil {
		t.Fatalf("cryptobox: %v", err)
	}

	h := newRunnerConnectHandlerForTest(&stubRunnerRepo{}, auth, &stubEnrollValidator{}, box, cfg, stubVariableStore{}, repoStore, wf)
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	defer srv.Close()
	httpClient := srv.Client()
	client := runnerv1connect.NewRunnerServiceClient(httpClient, srv.URL, connect.WithInterceptors(bearerClientInterceptor("hgxr_correct")))

	resp, err := client.Tasks(context.Background(), connect.NewRequest(&runnerv1.TasksRequest{MaxBatch: 0}))
	if err != nil {
		t.Fatalf("Tasks err: %v", err)
	}

	// Old client reads .Task — must be set.
	if resp.Msg.GetTask() == nil {
		t.Error("Task should be set for legacy client (max_batch=0)")
	}

	// New client reads .Tasks — must also be set.
	if len(resp.Msg.GetTasks()) == 0 {
		t.Error("Tasks should be set even when max_batch=0")
	}

	// Both should reference the same workflow job.
	gotTask := resp.Msg.GetTask()
	gotTasks := resp.Msg.GetTasks()
	if gotTask == nil {
		t.Fatal("Task is nil")
	}
	if len(gotTasks) == 0 || gotTasks[0] == nil {
		t.Fatal("Tasks[0] is nil")
	}
	// Both must represent the same workflow job.
	if gotTask.GetWorkflowJob().GetJobRunId() != gotTasks[0].GetWorkflowJob().GetJobRunId() {
		t.Error("Task and Tasks[0] should have the same workflow job")
	}
}

// stubRepoForTasks is a minimal repodomain.Store that satisfies GetByID.
type stubRepoForTasks struct{}

func (s *stubRepoForTasks) Create(context.Context, repodomain.OwnerKind, int64, string, string, string, repodomain.Visibility) (*repodomain.Repo, error) {
	return nil, errors.New("not stubbed")
}
func (s *stubRepoForTasks) GetByID(_ context.Context, id int64) (*repodomain.Repo, error) {
	return &repodomain.Repo{ID: id, OwnerName: "testowner", Name: "testrepo"}, nil
}
func (s *stubRepoForTasks) GetByOwnerAndName(context.Context, repodomain.OwnerKind, int64, string) (*repodomain.Repo, error) {
	return nil, errors.New("not stubbed")
}
func (s *stubRepoForTasks) ListByOwner(context.Context, repodomain.OwnerKind, int64, bool, int32, int32) ([]*repodomain.Repo, int64, error) {
	return nil, 0, errors.New("not stubbed")
}
func (s *stubRepoForTasks) Delete(context.Context, int64) error { return errors.New("not stubbed") }
func (s *stubRepoForTasks) UpdateMeta(context.Context, int64, string, string, repodomain.Visibility) (*repodomain.Repo, error) {
	return nil, errors.New("not stubbed")
}
func (s *stubRepoForTasks) Transfer(context.Context, int64, repodomain.OwnerKind, int64) (*repodomain.Repo, error) {
	return nil, errors.New("not stubbed")
}

// stubVariableStore is a minimal repodomain.VariableStore.
type stubVariableStore struct{}

func (stubVariableStore) List(context.Context, int64) ([]*repodomain.RepoVariable, error) { return nil, nil }
func (stubVariableStore) Get(context.Context, int64, int64) (*repodomain.RepoVariable, error) {
	return nil, errors.New("not stubbed")
}
func (stubVariableStore) Create(context.Context, int64, string, string, repodomain.VariableKind) (*repodomain.RepoVariable, error) {
	return nil, errors.New("not stubbed")
}
func (stubVariableStore) Update(context.Context, int64, int64, string, string, repodomain.VariableKind) (*repodomain.RepoVariable, error) {
	return nil, errors.New("not stubbed")
}
func (stubVariableStore) Delete(context.Context, int64, int64) error { return errors.New("not stubbed") }

// ---- Workflow callback delegation tests ----
//
// These cover the seven RPCs in runner_connect_workflow.go. The
// pattern is: drive the RPC, then assert the workflow-service stub
// saw the right call with the right arguments. Status enum
// validation (where applicable) is covered with negative tests
// returning InvalidArgument before reaching the service. Service
// errors are propagated as Internal (or InvalidArgument, for
// RegisterPhase which gates on user-supplied phase strings).
//
// recordingWorkflow is a more capable runnerConnectWorkflow stub
// than stubWorkflow: every method records its (sanitised) arguments
// into a slice the test can introspect afterwards.

type recordingWorkflow struct {
	markRunning   []markRunningCall
	appendLog     []appendLogCall
	setStepOuts   []setStepOutsCall
	markTerminal  []markTerminalCall
	resolveOuts   []int64
	advanceRun    []int64
	registerPhase []registerPhaseCall
	markPhaseRun  []markPhaseCall
	markPhaseTerm []markPhaseTermCall

	// Optional error overrides — set per-test to drive failure paths
	// without rebuilding the stub.
	registerPhaseErr error
}

type markRunningCall struct {
	JobID, RunnerID int64
}
type appendLogCall struct {
	JobRunID int64
	Stream   workflowdomain.LogStream
	Line     string
	StepID   *string
}
type setStepOutsCall struct {
	JobRunID int64
	StepID   string
	Outputs  map[string]workflowdomain.StepOutputValue
}
type markTerminalCall struct {
	JobID    int64
	Status   workflowdomain.JobStatus
	ExitCode *int32
	Message  string
}
type registerPhaseCall struct {
	JobRunID      int64
	Phase         workflowdomain.PhaseKind
	SequenceIndex int32
	ImageRef      string
}
type markPhaseCall struct {
	JobRunID int64
	Phase    workflowdomain.PhaseKind
}
type markPhaseTermCall struct {
	JobRunID int64
	Phase    workflowdomain.PhaseKind
	Status   workflowdomain.PhaseStatus
	ExitCode *int32
	Message  string
}

func (r *recordingWorkflow) ClaimNextJob(context.Context, int64) (*workflowdomain.WorkflowJobRun, error) {
	return nil, workflowdomain.ErrNoPendingJob
}
func (r *recordingWorkflow) ClaimNextJobs(_ context.Context, _ int64, _ int) ([]*workflowdomain.WorkflowJobRun, error) {
	return nil, nil
}
func (r *recordingWorkflow) GetRunForJob(context.Context, int64) (*workflowdomain.WorkflowRun, error) {
	return nil, errors.New("not stubbed")
}
func (r *recordingWorkflow) MarkJobRunning(_ context.Context, jobID, runnerID int64) error {
	r.markRunning = append(r.markRunning, markRunningCall{JobID: jobID, RunnerID: runnerID})
	return nil
}
func (r *recordingWorkflow) AppendLog(_ context.Context, jobRunID int64, stream workflowdomain.LogStream, line string, stepID *string) error {
	r.appendLog = append(r.appendLog, appendLogCall{JobRunID: jobRunID, Stream: stream, Line: line, StepID: stepID})
	return nil
}
func (r *recordingWorkflow) SetStepOutputs(_ context.Context, jobRunID int64, stepID string, outs map[string]workflowdomain.StepOutputValue) error {
	r.setStepOuts = append(r.setStepOuts, setStepOutsCall{JobRunID: jobRunID, StepID: stepID, Outputs: outs})
	return nil
}
func (r *recordingWorkflow) MarkJobTerminal(_ context.Context, jobID int64, status workflowdomain.JobStatus, exitCode *int32, message string) error {
	r.markTerminal = append(r.markTerminal, markTerminalCall{JobID: jobID, Status: status, ExitCode: exitCode, Message: message})
	return nil
}
func (r *recordingWorkflow) ResolveJobOutputs(_ context.Context, jobID int64) error {
	r.resolveOuts = append(r.resolveOuts, jobID)
	return nil
}
func (r *recordingWorkflow) GetJobRun(_ context.Context, id int64) (*workflowdomain.WorkflowJobRun, error) {
	// TerminateWorkflowJob's best-effort AdvanceRun branch needs a
	// resolved WorkflowRunID. Return a synthetic row tying jobID to
	// runID 9000+jobID so tests can assert which run was advanced.
	return &workflowdomain.WorkflowJobRun{ID: id, WorkflowRunID: 9000 + id}, nil
}
func (r *recordingWorkflow) AdvanceRun(_ context.Context, runID int64) error {
	r.advanceRun = append(r.advanceRun, runID)
	return nil
}
func (r *recordingWorkflow) RegisterPhase(_ context.Context, jobRunID int64, phase workflowdomain.PhaseKind, sequenceIndex int32, imageRef string) (*workflowdomain.JobPhase, error) {
	r.registerPhase = append(r.registerPhase, registerPhaseCall{JobRunID: jobRunID, Phase: phase, SequenceIndex: sequenceIndex, ImageRef: imageRef})
	if r.registerPhaseErr != nil {
		return nil, r.registerPhaseErr
	}
	return &workflowdomain.JobPhase{Phase: phase, SequenceIndex: sequenceIndex, ImageRef: imageRef}, nil
}
func (r *recordingWorkflow) MarkPhaseRunning(_ context.Context, jobRunID int64, phase workflowdomain.PhaseKind) error {
	r.markPhaseRun = append(r.markPhaseRun, markPhaseCall{JobRunID: jobRunID, Phase: phase})
	return nil
}
func (r *recordingWorkflow) MarkPhaseTerminal(_ context.Context, jobRunID int64, phase workflowdomain.PhaseKind, status workflowdomain.PhaseStatus, exitCode *int32, message string) error {
	r.markPhaseTerm = append(r.markPhaseTerm, markPhaseTermCall{JobRunID: jobRunID, Phase: phase, Status: status, ExitCode: exitCode, Message: message})
	return nil
}

// newWorkflowTestClient is a thin wrapper around newRunnerConnectTestServer
// that always uses the recording workflow + a valid agent token. Cuts
// boilerplate in the dozen tests below.
func newWorkflowTestClient(t *testing.T, wf *recordingWorkflow) (runnerv1connect.RunnerServiceClient, func()) {
	t.Helper()
	auth := &stubAgentValidator{expectedToken: "hgxr_correct", runner: &domain.Runner{ID: 5}}
	return newRunnerConnectTestServer(t, &stubRunnerRepo{}, auth, &stubEnrollValidator{}, wf, "hgxr_correct")
}

func TestRunnerConnect_MarkWorkflowJobRunning_Delegates(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.MarkWorkflowJobRunning(context.Background(), connect.NewRequest(&runnerv1.MarkWorkflowJobRunningRequest{
		JobRunId: 123,
	}))
	if err != nil {
		t.Fatalf("MarkWorkflowJobRunning err: %v", err)
	}
	if len(wf.markRunning) != 1 {
		t.Fatalf("markRunning calls = %d, want 1", len(wf.markRunning))
	}
	got := wf.markRunning[0]
	if got.JobID != 123 || got.RunnerID != 5 {
		t.Errorf("got %+v, want (jobID=123, runnerID=5)", got)
	}
}

func TestRunnerConnect_AppendWorkflowJobLog_HappyPath(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	cases := []struct {
		stream     string
		wantStream workflowdomain.LogStream
		stepID     string
		wantStepID *string
	}{
		{"stdout", workflowdomain.LogStreamStdout, "step-a", strPtr("step-a")},
		{"stderr", workflowdomain.LogStreamStderr, "", nil},
		{"system", workflowdomain.LogStreamSystem, "", nil},
	}
	for _, c := range cases {
		_, err := client.AppendWorkflowJobLog(context.Background(), connect.NewRequest(&runnerv1.AppendWorkflowJobLogRequest{
			JobRunId: 77,
			Stream:   c.stream,
			Line:     "hello " + c.stream,
			StepId:   c.stepID,
		}))
		if err != nil {
			t.Fatalf("AppendWorkflowJobLog(%s) err: %v", c.stream, err)
		}
	}
	if len(wf.appendLog) != len(cases) {
		t.Fatalf("appendLog calls = %d, want %d", len(wf.appendLog), len(cases))
	}
	for i, c := range cases {
		got := wf.appendLog[i]
		if got.JobRunID != 77 || got.Stream != c.wantStream || got.Line != "hello "+c.stream {
			t.Errorf("call %d: got %+v, want stream=%s line=%q", i, got, c.wantStream, "hello "+c.stream)
		}
		if !stepIDEqual(got.StepID, c.wantStepID) {
			t.Errorf("call %d: got stepID=%v, want %v", i, derefStr(got.StepID), derefStr(c.wantStepID))
		}
	}
}

func TestRunnerConnect_AppendWorkflowJobLog_BadStream_ReturnsInvalidArgument(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.AppendWorkflowJobLog(context.Background(), connect.NewRequest(&runnerv1.AppendWorkflowJobLogRequest{
		JobRunId: 1, Stream: "wrong", Line: "x",
	}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", code)
	}
	if len(wf.appendLog) != 0 {
		t.Errorf("AppendLog should not be reached on bad stream; got %d calls", len(wf.appendLog))
	}
}

func TestRunnerConnect_ReportWorkflowStepResult_MergesMaskedFlag(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.ReportWorkflowStepResult(context.Background(), connect.NewRequest(&runnerv1.ReportWorkflowStepResultRequest{
		JobRunId:  42,
		StepIndex: 3,
		StepId:    "release-step",
		ExitCode:  0,
		Outputs:   map[string]string{"version": "1.2.3", "secret_token": "hidden"},
		Masked:    []string{"secret_token"},
	}))
	if err != nil {
		t.Fatalf("ReportWorkflowStepResult err: %v", err)
	}
	if len(wf.setStepOuts) != 1 {
		t.Fatalf("setStepOuts calls = %d, want 1", len(wf.setStepOuts))
	}
	call := wf.setStepOuts[0]
	if call.JobRunID != 42 || call.StepID != "release-step" {
		t.Errorf("got (jobRunID=%d, stepID=%q), want (42, release-step)", call.JobRunID, call.StepID)
	}
	if v := call.Outputs["version"]; v.Value != "1.2.3" || v.Masked {
		t.Errorf("version output = %+v, want {Value:1.2.3 Masked:false}", v)
	}
	if v := call.Outputs["secret_token"]; v.Value != "hidden" || !v.Masked {
		t.Errorf("secret_token output = %+v, want {Value:hidden Masked:true}", v)
	}
}

func TestRunnerConnect_ReportWorkflowStepResult_EmptyStepID_ReturnsInvalidArgument(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.ReportWorkflowStepResult(context.Background(), connect.NewRequest(&runnerv1.ReportWorkflowStepResultRequest{
		JobRunId: 1, StepId: "",
	}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", code)
	}
}

// TerminateWorkflowJob: on success status, the handler must invoke
// MarkJobTerminal AND ResolveJobOutputs AND AdvanceRun (the latter
// two best-effort). On non-success status it skips ResolveJobOutputs
// but still advances the run.
func TestRunnerConnect_TerminateWorkflowJob_Success_ResolvesAndAdvances(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.TerminateWorkflowJob(context.Background(), connect.NewRequest(&runnerv1.TerminateWorkflowJobRequest{
		JobRunId: 11, Status: "success", ExitCode: 0,
	}))
	if err != nil {
		t.Fatalf("TerminateWorkflowJob err: %v", err)
	}
	if len(wf.markTerminal) != 1 || wf.markTerminal[0].Status != workflowdomain.JobStatusSuccess {
		t.Errorf("markTerminal = %+v, want one success call", wf.markTerminal)
	}
	if len(wf.resolveOuts) != 1 || wf.resolveOuts[0] != 11 {
		t.Errorf("resolveOuts = %v, want [11]", wf.resolveOuts)
	}
	// AdvanceRun was called with the workflowRunID GetJobRun returned,
	// which the stub maps to 9000 + jobID.
	if len(wf.advanceRun) != 1 || wf.advanceRun[0] != 9011 {
		t.Errorf("advanceRun = %v, want [9011]", wf.advanceRun)
	}
}

func TestRunnerConnect_TerminateWorkflowJob_Failed_SkipsResolve(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.TerminateWorkflowJob(context.Background(), connect.NewRequest(&runnerv1.TerminateWorkflowJobRequest{
		JobRunId: 22, Status: "failed", ExitCode: 1, Message: "build broke",
	}))
	if err != nil {
		t.Fatalf("TerminateWorkflowJob err: %v", err)
	}
	if len(wf.markTerminal) != 1 || wf.markTerminal[0].Status != workflowdomain.JobStatusFailed {
		t.Errorf("markTerminal = %+v, want one failed call", wf.markTerminal)
	}
	if wf.markTerminal[0].Message != "build broke" {
		t.Errorf("message = %q, want %q", wf.markTerminal[0].Message, "build broke")
	}
	if len(wf.resolveOuts) != 0 {
		t.Errorf("resolveOuts = %v, want empty on failure", wf.resolveOuts)
	}
	if len(wf.advanceRun) != 1 {
		t.Errorf("advanceRun should still fire on failure; got %v", wf.advanceRun)
	}
}

func TestRunnerConnect_TerminateWorkflowJob_BadStatus_ReturnsInvalidArgument(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.TerminateWorkflowJob(context.Background(), connect.NewRequest(&runnerv1.TerminateWorkflowJobRequest{
		JobRunId: 1, Status: "exploded",
	}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", code)
	}
	if len(wf.markTerminal) != 0 {
		t.Errorf("markTerminal should not run on bad status; got %d calls", len(wf.markTerminal))
	}
}

func TestRunnerConnect_RegisterWorkflowJobPhase_HappyPath(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.RegisterWorkflowJobPhase(context.Background(), connect.NewRequest(&runnerv1.RegisterWorkflowJobPhaseRequest{
		JobRunId:      33,
		Phase:         "build",
		SequenceIndex: 2,
		ImageRef:      "node:20",
	}))
	if err != nil {
		t.Fatalf("RegisterWorkflowJobPhase err: %v", err)
	}
	if len(wf.registerPhase) != 1 {
		t.Fatalf("registerPhase calls = %d, want 1", len(wf.registerPhase))
	}
	got := wf.registerPhase[0]
	if got.JobRunID != 33 || got.Phase != workflowdomain.PhaseKind("build") || got.SequenceIndex != 2 || got.ImageRef != "node:20" {
		t.Errorf("got %+v, want (jobRunID=33 phase=build seq=2 image=node:20)", got)
	}
}

// RegisterPhase maps service errors to InvalidArgument because the
// legacy handler did — the validation errors there (bad phase kind,
// duplicate sequence) are user-supplied input issues.
func TestRunnerConnect_RegisterWorkflowJobPhase_ServiceError_MapsToInvalidArgument(t *testing.T) {
	wf := &recordingWorkflow{registerPhaseErr: errors.New("phase already exists")}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.RegisterWorkflowJobPhase(context.Background(), connect.NewRequest(&runnerv1.RegisterWorkflowJobPhaseRequest{
		JobRunId: 1, Phase: "build", SequenceIndex: 1,
	}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", code)
	}
}

func TestRunnerConnect_MarkWorkflowJobPhaseRunning_HappyPath(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.MarkWorkflowJobPhaseRunning(context.Background(), connect.NewRequest(&runnerv1.MarkWorkflowJobPhaseRunningRequest{
		JobRunId: 44, Phase: "deploy",
	}))
	if err != nil {
		t.Fatalf("MarkWorkflowJobPhaseRunning err: %v", err)
	}
	if len(wf.markPhaseRun) != 1 || wf.markPhaseRun[0].JobRunID != 44 || wf.markPhaseRun[0].Phase != workflowdomain.PhaseKind("deploy") {
		t.Errorf("markPhaseRun = %+v, want one (jobRunID=44, phase=deploy)", wf.markPhaseRun)
	}
}

// TerminateWorkflowJobPhase's has_exit_code field distinguishes "0"
// (succeeded with exit code 0) from "no exit code" (phase was skipped
// before running). The legacy JSON path used a nullable pointer; the
// proto carries a separate bool because proto3 int32 cannot represent
// "absent".
func TestRunnerConnect_TerminateWorkflowJobPhase_WithExitCode(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.TerminateWorkflowJobPhase(context.Background(), connect.NewRequest(&runnerv1.TerminateWorkflowJobPhaseRequest{
		JobRunId:    55,
		Phase:       "build",
		Status:      "success",
		ExitCode:    0,
		HasExitCode: true,
	}))
	if err != nil {
		t.Fatalf("TerminateWorkflowJobPhase err: %v", err)
	}
	if len(wf.markPhaseTerm) != 1 {
		t.Fatalf("markPhaseTerm calls = %d, want 1", len(wf.markPhaseTerm))
	}
	call := wf.markPhaseTerm[0]
	if call.ExitCode == nil || *call.ExitCode != 0 {
		t.Errorf("ExitCode = %v, want pointer to 0", derefInt32(call.ExitCode))
	}
}

func TestRunnerConnect_TerminateWorkflowJobPhase_WithoutExitCode(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.TerminateWorkflowJobPhase(context.Background(), connect.NewRequest(&runnerv1.TerminateWorkflowJobPhaseRequest{
		JobRunId:    66,
		Phase:       "deploy",
		Status:      "skipped",
		HasExitCode: false,
	}))
	if err != nil {
		t.Fatalf("TerminateWorkflowJobPhase err: %v", err)
	}
	call := wf.markPhaseTerm[0]
	if call.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil (HasExitCode=false)", *call.ExitCode)
	}
}

func TestRunnerConnect_TerminateWorkflowJobPhase_BadStatus_ReturnsInvalidArgument(t *testing.T) {
	wf := &recordingWorkflow{}
	client, cleanup := newWorkflowTestClient(t, wf)
	defer cleanup()

	_, err := client.TerminateWorkflowJobPhase(context.Background(), connect.NewRequest(&runnerv1.TerminateWorkflowJobPhaseRequest{
		JobRunId: 1, Phase: "build", Status: "exploded",
	}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", code)
	}
}

// ---- tiny helpers ----

func strPtr(s string) *string { return &s }

func stepIDEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func derefInt32(p *int32) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *p)
}

// ---- shared helpers ----

// Compile-time assertion: testEncryptionKey is well-formed.
var _ = func() int {
	if _, err := base64.StdEncoding.DecodeString(testEncryptionKey); err != nil {
		panic("testEncryptionKey: " + err.Error())
	}
	return 0
}()
