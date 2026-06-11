// runner_connect.go mounts the typed hangrix.runner.v1.RunnerService at
// `/hangrix.runner.v1.RunnerService/*`, replacing the JSON-over-HTTP
// `/api/runner/*` (this file's AgentHandler) and `/api/runner/
// workflow-jobs/{id}/*` (workflow handler) surface. Both coexist while
// the standalone hangrix-runner cuts over; once the runner client
// stops calling the legacy routes they go away.
//
// Auth model. Every RPC except Enroll carries `Authorization: Bearer
// hgxr_<...>` and validates via runnerdomain.AgentValidator. Enroll is
// exempt — it consumes the one-shot `hgxe_` enroll token inside the
// request body, redeemed via runnerdomain.EnrollValidator. The
// interceptor's procedure-name check exempts Enroll alone; everything
// else falls through to the bearer flow.
//
// Tasks long-poll. The unary Tasks RPC blocks up to pollWait (matched
// to the legacy handler's 20s ceiling). Inside the loop we first try
// to claim an agent session, then a workflow job; the first hit wins.
// Empty TasksResponse (no inner Task body) is the 204 equivalent — the
// runner re-calls in a loop with a small backoff. We deliberately did
// NOT make Tasks server-streaming: the runner processes one task at a
// time, so streaming would either need an explicit ready-for-next ACK
// (extra complexity) or buffer tasks on the runner (extra failure
// modes). Long-poll unary is the natural fit.
//
// Workflow callbacks. The seven workflow-lifecycle RPCs delegate
// straight to *workflowservice.Service, the same service the legacy
// `/api/runner/workflow-jobs/{id}/*` handlers call. The Connect
// handler owns the proto translation; the service owns the SQL.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/binaries"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	workflowservice "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/service"
	"github.com/hangrix/hangrix/pkg/cryptobox"
	runnerv1 "github.com/hangrix/hangrix/gen/go/hangrix/runner/v1"
	"github.com/hangrix/hangrix/gen/go/hangrix/runner/v1/runnerv1connect"
)

// runnerConnectRepo is the narrow persistence surface RunnerConnectHandler
// touches. Declared locally so tests can inject a tiny stub rather than
// implementing the full domain.Repo (~40 methods). *infra.PostgresRepo
// satisfies this via the wider domain.Repo it already implements.
type runnerConnectRepo interface {
	UpdateRunnerHeartbeat(ctx context.Context, id int64, capabilities []byte) error
	ListPendingContainerCleanups(ctx context.Context, runnerID int64, limit int) ([]domain.ContainerCleanupTask, error)
	ClearSessionContainer(ctx context.Context, sessionID, ownerRunnerID int64) error
	ListPendingContainerStops(ctx context.Context, runnerID int64, limit int) ([]domain.ContainerStopTask, error)
	AckContainerStop(ctx context.Context, sessionID, ownerRunnerID int64) error
}

// runnerConnectWorkflow is the narrow surface RunnerConnectHandler
// uses against the workflow service. Production binds the concrete
// *workflowservice.Service; tests inject a stub.
type runnerConnectWorkflow interface {
	ClaimNextJob(ctx context.Context, runnerID int64) (*workflowdomain.WorkflowJobRun, error)
	ClaimNextJobs(ctx context.Context, runnerID int64, limit int) ([]*workflowdomain.WorkflowJobRun, error)
	GetRunForJob(ctx context.Context, jobRunID int64) (*workflowdomain.WorkflowRun, error)
	MarkJobRunning(ctx context.Context, jobID, runnerID int64) error
	AppendLog(ctx context.Context, jobRunID int64, stream workflowdomain.LogStream, line string, stepID *string) error
	SetStepOutputs(ctx context.Context, id int64, stepID string, outputs map[string]workflowdomain.StepOutputValue) error
	MarkJobTerminal(ctx context.Context, jobID int64, status workflowdomain.JobStatus, exitCode *int32, errMsg string) error
	ResolveJobOutputs(ctx context.Context, jobID int64) error
	GetJobRun(ctx context.Context, id int64) (*workflowdomain.WorkflowJobRun, error)
	AdvanceRun(ctx context.Context, runID int64) error
	RegisterPhase(ctx context.Context, jobRunID int64, phase workflowdomain.PhaseKind, sequenceIndex int32, imageRef string) (*workflowdomain.JobPhase, error)
	MarkPhaseRunning(ctx context.Context, jobRunID int64, phase workflowdomain.PhaseKind) error
	MarkPhaseTerminal(ctx context.Context, jobRunID int64, phase workflowdomain.PhaseKind, status workflowdomain.PhaseStatus, exitCode *int32, errMsg string) error
}

// RunnerConnectHandler is the Connect-Go implementation of
// RunnerService. One handler per server process; goroutine-safe
// because every dependency it holds is goroutine-safe.
type RunnerConnectHandler struct {
	repo            runnerConnectRepo
	agentValidator  domain.AgentValidator
	enrollValidator domain.EnrollValidator
	box             *cryptobox.Box
	cfg             *config.Config
	variables       repodomain.VariableStore
	repoStore       repodomain.Store
	workflow        runnerConnectWorkflow
}

type RunnerConnectHandlerDeps struct {
	Repo            domain.Repo
	AgentValidator  domain.AgentValidator
	EnrollValidator domain.EnrollValidator
	Config          *config.Config
	Variables       repodomain.VariableStore
	RepoStore       repodomain.Store
	Workflow        *workflowservice.Service
}

func NewRunnerConnectHandler(deps *RunnerConnectHandlerDeps) *RunnerConnectHandler {
	box, err := cryptobox.New(deps.Config.LLM.EncryptionKey)
	if err != nil {
		panic(err)
	}
	return &RunnerConnectHandler{
		repo:            deps.Repo,
		agentValidator:  deps.AgentValidator,
		enrollValidator: deps.EnrollValidator,
		box:             box,
		cfg:             deps.Config,
		variables:       deps.Variables,
		repoStore:       deps.RepoStore,
		workflow:        deps.Workflow,
	}
}

// newRunnerConnectHandlerForTest is the package-private constructor
// tests use to inject the narrow runnerConnectRepo + runnerConnectWorkflow
// stubs without dragging in the full domain.Repo / *workflowservice.Service.
// Production code uses NewRunnerConnectHandler.
func newRunnerConnectHandlerForTest(
	repo runnerConnectRepo,
	agentValidator domain.AgentValidator,
	enrollValidator domain.EnrollValidator,
	box *cryptobox.Box,
	cfg *config.Config,
	variables repodomain.VariableStore,
	repoStore repodomain.Store,
	workflow runnerConnectWorkflow,
) *RunnerConnectHandler {
	return &RunnerConnectHandler{
		repo:            repo,
		agentValidator:  agentValidator,
		enrollValidator: enrollValidator,
		box:             box,
		cfg:             cfg,
		variables:       variables,
		repoStore:       repoStore,
		workflow:        workflow,
	}
}

// RegisterRoutes mounts the Connect service under the standard Connect
// path. chi.Mount strips the prefix before dispatching, so the inner
// handler sees `/<Service>/<Method>`.
//
// The Connect handler is wrapped in capturePublicBase so Bootstrap's
// payload assembly can fall back to the inbound request's Host header
// when cfg.Server.URL is empty (the devcontainer / `go run` happy
// path). Without this, the BootstrapPayload.BaseURL the runner
// receives — and forwards into every agent container as
// HANGRIX_PLATFORM_BASE_URL — would hard-code "http://localhost:8080"
// in dev and the agents couldn't reach the platform.
func (h *RunnerConnectHandler) RegisterRoutes(r chi.Router) {
	path, conn := runnerv1connect.NewRunnerServiceHandler(
		h,
		connect.WithInterceptors(newAgentTokenInterceptor(h.agentValidator)),
	)
	r.Mount(path, capturePublicBase(conn))
}

// publicBaseCtxKey carries the inbound-request-derived base URL
// (scheme://host) onto the per-RPC ctx so unary handlers can fall
// back to it when cfg.Server.URL isn't set. Connect doesn't expose
// the *http.Request to handlers; this is the established workaround.
type publicBaseCtxKey struct{}

// capturePublicBase wraps the Connect handler with a thin layer that
// stamps scheme+Host onto the ctx before the call lands. It does NOT
// override anything when the inbound Host is empty (test transports
// commonly leave it blank — the handler will then fall back to its
// hardcoded "http://localhost:8080" default).
func capturePublicBase(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			ctx := context.WithValue(r.Context(), publicBaseCtxKey{}, scheme+"://"+r.Host)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// publicBaseFromContext returns the scheme://Host the inbound
// request carried, or "" when capturePublicBase didn't stamp one
// (e.g. a hand-rolled test client).
func publicBaseFromContext(ctx context.Context) string {
	s, _ := ctx.Value(publicBaseCtxKey{}).(string)
	return s
}

// ---- interceptor + context plumbing ----

type connectRunnerCtxKey struct{}

func runnerFromConnectContext(ctx context.Context) *domain.Runner {
	v, _ := ctx.Value(connectRunnerCtxKey{}).(*domain.Runner)
	return v
}

// enrollProcedure is the fully-qualified Connect procedure name for
// RunnerService.Enroll. The interceptor exempts this method from the
// bearer-token check — Enroll consumes an `hgxe_` token in its body,
// not the long-lived `hgxr_`. Hardcoded rather than reflected so a
// future RPC named ".*Enroll" doesn't accidentally skip auth.
const enrollProcedure = runnerv1connect.RunnerServiceEnrollProcedure

func newAgentTokenInterceptor(v domain.AgentValidator) connect.Interceptor {
	return &agentTokenInterceptor{validator: v}
}

type agentTokenInterceptor struct {
	validator domain.AgentValidator
}

func (i *agentTokenInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().Procedure == enrollProcedure {
			return next(ctx, req)
		}
		ctx, err := i.authorize(ctx, req.Header())
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	})
}

func (i *agentTokenInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *agentTokenInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return connect.StreamingHandlerFunc(func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if conn.Spec().Procedure == enrollProcedure {
			return next(ctx, conn)
		}
		ctx, err := i.authorize(ctx, conn.RequestHeader())
		if err != nil {
			return err
		}
		return next(ctx, conn)
	})
}

// authorize resolves the bearer header into a Runner. Error codes
// mirror the legacy `requireAgentToken` middleware's HTTP status
// mapping:
//   - 401 Unauthenticated → missing / malformed Authorization
//   - 403 PermissionDenied → invalid or inactive token
//   - 13 Internal → validator implementation error
func (i *agentTokenInterceptor) authorize(ctx context.Context, header http.Header) (context.Context, error) {
	const prefix = "Bearer "
	raw := header.Get("Authorization")
	if !strings.HasPrefix(raw, prefix) {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
	}
	token := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
	if token == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
	}
	runner, err := i.validator.ValidateAgentToken(ctx, token)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidToken):
			return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid token"))
		case errors.Is(err, domain.ErrTokenInactive):
			return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("token inactive"))
		default:
			return ctx, connect.NewError(connect.CodeInternal, err)
		}
	}
	return context.WithValue(ctx, connectRunnerCtxKey{}, runner), nil
}

// ---- Enroll / Heartbeat ----
//
// Bootstrap deliberately isn't here — it lives on `GET /api/runner/
// bootstrap` (plain JSON, see binary.go). Auto-update is the only
// flow that can rescue a stale runner across protocol changes, so
// the runner must be able to call Bootstrap without speaking the
// current Connect wire — which it does over fixed-shape JSON instead.

func (h *RunnerConnectHandler) Enroll(
	ctx context.Context,
	req *connect.Request[runnerv1.EnrollRequest],
) (*connect.Response[runnerv1.EnrollResponse], error) {
	token := strings.TrimSpace(req.Msg.GetEnrollToken())
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("enroll_token required"))
	}
	caps := req.Msg.GetCapabilities()
	if len(caps) == 0 {
		// Empty caps normalise to `{}` so the column is never null.
		caps = []byte("{}")
	}
	out, err := h.enrollValidator.RedeemEnrollment(ctx, token, caps)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidToken):
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid enroll token"))
		case errors.Is(err, domain.ErrEnrollUsed):
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("enrollment already redeemed"))
		case errors.Is(err, domain.ErrRunnerDisabled):
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("runner disabled"))
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&runnerv1.EnrollResponse{
		RunnerId:   out.Runner.ID,
		RunnerName: out.Runner.Name,
		AgentToken: out.AgentTokenPlaintext,
		Bootstrap:  h.buildBootstrapProto(ctx),
	}), nil
}

// buildBootstrapProto centralises the bootstrap payload assembly.
// Reused by Enroll (Connect RPC) and by the plain-JSON GET
// /api/runner/bootstrap handler in binary.go.
//
// BaseURL preference order (matches the legacy chi handler):
//
//  1. config.Server.URL when set explicitly (production deploys).
//  2. Reconstructed from the inbound request's Host header
//     (devcontainer / `go run` happy path — see capturePublicBase).
//  3. Hardcoded "http://localhost:8080" as a last resort. Reached
//     only when both of the above are missing (e.g. a test transport
//     that doesn't stamp a Host header).
func (h *RunnerConnectHandler) buildBootstrapProto(ctx context.Context) *runnerv1.BootstrapPayload {
	return &runnerv1.BootstrapPayload{
		Binaries:          h.binariesInfoProto(),
		BaseUrl:           h.publicBaseFromConfig(ctx),
		DefaultAgentImage: h.cfg.Runner.DefaultAgentImage,
		PollWaitSec:       int32(pollWait / time.Second),
		HeartbeatSec:      20,
	}
}

func (h *RunnerConnectHandler) publicBaseFromConfig(ctx context.Context) string {
	if b := strings.TrimSpace(h.cfg.Server.URL); b != "" {
		return strings.TrimRight(b, "/")
	}
	if b := publicBaseFromContext(ctx); b != "" {
		return b
	}
	return "http://localhost:8080"
}

func (h *RunnerConnectHandler) binariesInfoProto() map[string]*runnerv1.BinaryInfo {
	out := map[string]*runnerv1.BinaryInfo{}
	for _, b := range binaries.All() {
		out[b.AssetName] = &runnerv1.BinaryInfo{
			Url:    "/api/runner/binaries/" + b.AssetName,
			Name:   b.Name,
			Goos:   b.GOOS,
			Goarch: b.GOARCH,
			Sha256: b.SHA256,
			Size:   b.Size,
		}
	}
	return out
}

func (h *RunnerConnectHandler) Heartbeat(
	ctx context.Context,
	req *connect.Request[runnerv1.HeartbeatRequest],
) (*connect.Response[runnerv1.HeartbeatResponse], error) {
	runner := runnerFromConnectContext(ctx)
	if runner == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no runner in context"))
	}
	caps := req.Msg.GetCapabilities()
	if err := h.repo.UpdateRunnerHeartbeat(ctx, runner.ID, caps); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&runnerv1.HeartbeatResponse{}), nil
}

// ---- Tasks (long-poll) ----

// pollWait is the Tasks long-poll ceiling. The runner's HTTP client
// times its Tasks request out at ~25s, so 20s here gives the server
// room to return an empty response cleanly before the client gives
// up.
const pollWait = 20 * time.Second

// connectTasksTick is the wait between empty claim attempts inside
// Tasks. Matches the legacy 500ms cadence so latency under burst
// stays bounded.
const connectTasksTick = 500 * time.Millisecond

// maxTasksPerPoll is the server-side clamp on the number of tasks a
// single Tasks call may return, regardless of the client's max_batch.
// Matches the runner's default Parallelism (16).
const maxTasksPerPoll = 16

// Tasks returns 0..K workflow_jobs for this runner, or empty after
// pollWait. Agent sessions are NOT surfaced as tasks: the
// agent-as-workflow cutover (commit e422121) deleted the runner-side
// SessionDriver, so agent runs ride entirely on workflow_jobs created
// by spawner.dispatchAgentRun. The agent_sessions row exists only as
// an identity anchor (session token + per-issue ACL) — the runner
// never claims it. The spawner advances it to 'running' inline before
// returning, so MarkSessionIdle (called by the agent on /idle) finds
// the row in an acceptable state.
func (h *RunnerConnectHandler) Tasks(
	ctx context.Context,
	req *connect.Request[runnerv1.TasksRequest],
) (*connect.Response[runnerv1.TasksResponse], error) {
	runner := runnerFromConnectContext(ctx)
	if runner == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no runner in context"))
	}
	if h.workflow == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no workflow dispatcher wired"))
	}

	requested := int(req.Msg.GetMaxBatch())
	batch := requested
	if batch < 1 {
		batch = 1
	}
	if batch > maxTasksPerPoll {
		batch = maxTasksPerPoll
	}

	deadline := time.Now().Add(pollWait)
	for {
		jobs, err := h.workflow.ClaimNextJobs(ctx, runner.ID, batch)
		if err == nil && len(jobs) > 0 {
			return h.buildBatchResponse(ctx, jobs, requested <= 1)
		}
		if err != nil && !errors.Is(err, workflowdomain.ErrNoPendingJob) {
			return nil, connect.NewError(connect.CodeInternal, err)
		}

		if time.Now().After(deadline) {
			return connect.NewResponse(&runnerv1.TasksResponse{}), nil
		}
		select {
		case <-ctx.Done():
			return connect.NewResponse(&runnerv1.TasksResponse{}), nil
		case <-time.After(connectTasksTick):
		}
	}
}

// buildBatchResponse builds a batch TasksResponse from claimed jobs.
// When fillLegacyTask is true (old client, max_batch <= 1), the single
// .Task field is filled in addition to .Tasks[0] for backward compat.
func (h *RunnerConnectHandler) buildBatchResponse(
	ctx context.Context,
	jobs []*workflowdomain.WorkflowJobRun,
	fillLegacyTask bool,
) (*connect.Response[runnerv1.TasksResponse], error) {
	tasks := make([]*runnerv1.Task, len(jobs))
	for i, job := range jobs {
		protoJob, err := h.buildWorkflowJobTaskProto(ctx, job)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		tasks[i] = &runnerv1.Task{
			Body: &runnerv1.Task_WorkflowJob{WorkflowJob: protoJob},
		}
	}

	resp := &runnerv1.TasksResponse{Tasks: tasks}
	if fillLegacyTask && len(tasks) == 1 {
		resp.Task = tasks[0]
	}
	return connect.NewResponse(resp), nil
}

// buildWorkflowJobTaskProto is the proto twin of AgentHandler.
// buildWorkflowJobDTO. Mirrors the same translation (frozen JSON →
// container/steps/inputs) but emits the proto WorkflowJobTask shape.
func (h *RunnerConnectHandler) buildWorkflowJobTaskProto(
	ctx context.Context,
	job *workflowdomain.WorkflowJobRun,
) (*runnerv1.WorkflowJobTask, error) {
	run, err := h.workflow.GetRunForJob(ctx, job.ID)
	if err != nil {
		return nil, fmt.Errorf("get run for job %d: %w", job.ID, err)
	}
	repo, err := h.repoStore.GetByID(ctx, run.RepoID)
	if err != nil {
		return nil, fmt.Errorf("get repo %d: %w", run.RepoID, err)
	}

	var env map[string]string
	if len(job.EnvJSON) > 0 {
		if err := json.Unmarshal(job.EnvJSON, &env); err != nil {
			return nil, fmt.Errorf("deserialise job env: %w", err)
		}
	}
	if env == nil {
		env = make(map[string]string)
	}

	// Steps come off the frozen JSON in the workflowStepDTO shape; we
	// translate to proto WorkflowStep here.
	var legacySteps []workflowStepDTO
	if len(job.StepsJSON) > 0 {
		if err := json.Unmarshal(job.StepsJSON, &legacySteps); err != nil {
			return nil, fmt.Errorf("deserialise job steps: %w", err)
		}
	}
	steps := stepsLegacyToProto(legacySteps)

	var snap workflowdomain.ContainerSnapshot
	if len(run.ContainerSnapshotJSON) > 0 {
		if err := json.Unmarshal(run.ContainerSnapshotJSON, &snap); err != nil {
			return nil, fmt.Errorf("deserialise container snapshot: %w", err)
		}
	}
	container := &runnerv1.WorkflowContainer{
		Image:      snap.Image,
		Entrypoint: snap.Entrypoint,
		Env:        env,
		Volumes:    make([]*runnerv1.Volume, 0, len(snap.Volumes)),
	}
	if snap.Build != nil {
		container.Build = &runnerv1.BuildSpec{
			Dockerfile: snap.Build.Dockerfile,
			Context:    snap.Build.Context,
			Args:       snap.Build.Args,
		}
	}
	for _, vol := range snap.Volumes {
		container.Volumes = append(container.Volumes, &runnerv1.Volume{
			Name:  vol.Name,
			Mount: vol.Mount,
		})
	}

	var repoVars map[string]string
	vars, err := h.variables.List(ctx, run.RepoID)
	if err == nil && vars != nil {
		repoVars = make(map[string]string, len(vars))
		for _, v := range vars {
			if v.DecryptionFailed {
				continue
			}
			repoVars[v.Name] = v.Value
		}
	}

	var inputs map[string]string
	if len(run.TriggerPayloadJSON) > 0 {
		var trigger struct {
			DispatchInputs map[string]string `json:"dispatch_inputs"`
		}
		if err := json.Unmarshal(run.TriggerPayloadJSON, &trigger); err == nil {
			inputs = trigger.DispatchInputs
		}
	}

	var causeID string
	if run.CauseID != nil {
		causeID = strconv.FormatInt(*run.CauseID, 10)
	}

	var tag string
	if run.EventName == workflowdomain.EventRepoPushTag && strings.HasPrefix(run.Ref, "refs/tags/") {
		tag = strings.TrimPrefix(run.Ref, "refs/tags/")
	}

	triggerKind, triggerID, triggerDisplay := triggerActorFromRun(run)

	return &runnerv1.WorkflowJobTask{
		JobRunId:                job.ID,
		WorkflowRunId:           run.ID,
		RepoId:                  run.RepoID,
		Owner:                   repo.OwnerName,
		Name:                    repo.Name,
		WorkflowName:            run.WorkflowName,
		JobKey:                  job.JobKey,
		CheckoutRef:             run.Ref,
		CommitSha:               run.CommitSHA,
		Tag:                     tag,
		EventName:               string(run.EventName),
		EventCauseId:            causeID,
		Container:               container,
		WorkingDirectory:        job.WorkingDirectory,
		Steps:                   steps,
		TimeoutMinutes:          int32(job.TimeoutMinutes),
		RepoVariables:           repoVars,
		Inputs:                  inputs,
		WorkflowToken:           run.WorkflowToken,
		TriggerActorKind:        triggerKind,
		TriggerActorId:          triggerID,
		TriggerActorDisplayName: triggerDisplay,
	}, nil
}

// ---- CleanupTask / StopTask ----

func (h *RunnerConnectHandler) ListCleanupTasks(
	ctx context.Context,
	req *connect.Request[runnerv1.ListCleanupTasksRequest],
) (*connect.Response[runnerv1.ListCleanupTasksResponse], error) {
	runner := runnerFromConnectContext(ctx)
	if runner == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no runner in context"))
	}
	tasks, err := h.repo.ListPendingContainerCleanups(ctx, runner.ID, 50)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*runnerv1.CleanupTask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, &runnerv1.CleanupTask{
			SessionId:   t.SessionID,
			ContainerId: t.ContainerID,
		})
	}
	return connect.NewResponse(&runnerv1.ListCleanupTasksResponse{Tasks: out}), nil
}

func (h *RunnerConnectHandler) MarkCleanupDone(
	ctx context.Context,
	req *connect.Request[runnerv1.MarkCleanupDoneRequest],
) (*connect.Response[runnerv1.MarkCleanupDoneResponse], error) {
	runner := runnerFromConnectContext(ctx)
	if runner == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no runner in context"))
	}
	if err := h.repo.ClearSessionContainer(ctx, req.Msg.GetSessionId(), runner.ID); err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			// Session may have been deleted between list + ack; treat as success.
			return connect.NewResponse(&runnerv1.MarkCleanupDoneResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&runnerv1.MarkCleanupDoneResponse{}), nil
}

func (h *RunnerConnectHandler) ListStopTasks(
	ctx context.Context,
	req *connect.Request[runnerv1.ListStopTasksRequest],
) (*connect.Response[runnerv1.ListStopTasksResponse], error) {
	runner := runnerFromConnectContext(ctx)
	if runner == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no runner in context"))
	}
	tasks, err := h.repo.ListPendingContainerStops(ctx, runner.ID, 50)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*runnerv1.StopTask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, &runnerv1.StopTask{
			SessionId:   t.SessionID,
			ContainerId: t.ContainerID,
		})
	}
	return connect.NewResponse(&runnerv1.ListStopTasksResponse{Tasks: out}), nil
}

func (h *RunnerConnectHandler) MarkStopDone(
	ctx context.Context,
	req *connect.Request[runnerv1.MarkStopDoneRequest],
) (*connect.Response[runnerv1.MarkStopDoneResponse], error) {
	runner := runnerFromConnectContext(ctx)
	if runner == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no runner in context"))
	}
	if err := h.repo.AckContainerStop(ctx, req.Msg.GetSessionId(), runner.ID); err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return connect.NewResponse(&runnerv1.MarkStopDoneResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&runnerv1.MarkStopDoneResponse{}), nil
}

// ---- proto translation helpers (legacy DTO → proto) ----
//
// buildSpecLegacyToProto + volumesLegacyToProto used to live here for
// the AgentSession claim path (RoleConfig-frozen build / volumes).
// They're gone with that path: see Tasks above for why agent runs ride
// workflow_jobs now.

// stepsLegacyToProto converts the frozen workflowStepDTO slice into
// proto WorkflowStep slice. `with` is re-encoded back to bytes — it
// arrived from the DB as JSON and the runner re-decodes it per
// step-type, so we ride it through verbatim.
func stepsLegacyToProto(in []workflowStepDTO) []*runnerv1.WorkflowStep {
	if len(in) == 0 {
		return nil
	}
	out := make([]*runnerv1.WorkflowStep, len(in))
	for i, s := range in {
		out[i] = &runnerv1.WorkflowStep{
			Id:     s.ID,
			Name:   s.Name,
			Type:   s.Type,
			Run:    s.Run,
			Env:    s.Env,
			Dir:    s.Dir,
			Script: "", // legacy DTO does not carry script source today
		}
		if len(s.With) > 0 {
			// Re-marshal the map[string]any back to canonical JSON for
			// the wire. json.Marshal sorts keys, so two semantically
			// equal `with` blocks produce identical bytes.
			if encoded, err := json.Marshal(s.With); err == nil {
				out[i].With = encoded
			}
		}
	}
	return out
}

// ---- compile-time check ----
var _ runnerv1connect.RunnerServiceHandler = (*RunnerConnectHandler)(nil)
