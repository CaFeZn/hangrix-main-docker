// Package client wraps the runner-facing surface of the Hangrix
// server. One Client per process; methods are safe for concurrent use
// because the underlying *http.Client + Connect client are.
//
// Transport split. RunnerService RPCs (enroll / heartbeat / tasks /
// cleanup / stop / workflow-job callbacks) ride hangrix.runner.v1
// over Connect-Go. Plain-byte / self-rescue routes stay on a vanilla
// *http.Client:
//
//   - binary downloads from /api/runner/binaries/{name}
//   - the curl|sh install script
//   - multipart release-asset uploads
//   - GET /api/runner/bootstrap — deliberately NOT Connect so that
//     `serve --auto-update` on a runner whose Connect wire is too
//     stale to talk to the current server can still fetch the binary
//     catalogue and swap itself out
//
// The public DTOs (Task, WorkflowJob, etc.) preserve their pre-Connect
// JSON shape so callers in the runner loop don't have to know which
// transport carried their data.
//
// Auth. All Connect RPCs except Enroll attach `Authorization: Bearer
// <agent token>` via a Connect interceptor. The plain-HTTP paths add
// the same header by hand (Bootstrap, DownloadBinary); release calls
// use the caller-supplied workflow token instead.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	runnerv1 "github.com/hangrix/hangrix/gen/go/hangrix/runner/v1"
	"github.com/hangrix/hangrix/gen/go/hangrix/runner/v1/runnerv1connect"
)

type Client struct {
	base       string
	agentToken string
	http       *http.Client
	rpc        runnerv1connect.RunnerServiceClient
}

// New makes a Client without an agent token (used for enrollment); set
// the token afterwards with WithAgentToken once you have one. The
// Connect client carries the token via a per-Client interceptor that
// reads c.agentToken at call time, so updating it post-construction
// (the `enroll → set token → bootstrap` flow) works without rebuilding.
func New(base string) *Client {
	base = strings.TrimRight(base, "/")
	c := &Client{
		base: base,
		http: &http.Client{
			// Long-poll Tasks needs > 20s to outlive the server-side
			// poll window; a 60s ceiling matches the legacy client's
			// budget and the platform's idleness expectations.
			Timeout: 60 * time.Second,
		},
	}
	c.rpc = runnerv1connect.NewRunnerServiceClient(
		c.http,
		base,
		connect.WithInterceptors(newBearerInterceptor(c)),
	)
	return c
}

func (c *Client) WithAgentToken(tok string) *Client {
	c.agentToken = tok
	return c
}

// newBearerInterceptor attaches Authorization: Bearer <agent token>
// to every Connect call. The interceptor reads c.agentToken at call
// time, so WithAgentToken-after-New takes effect on subsequent RPCs
// without needing to rebuild the client. Empty token = no header
// (Enroll runs that way; the server-side interceptor exempts it).
func newBearerInterceptor(c *Client) connect.Interceptor {
	return &bearerInterceptor{c: c}
}

type bearerInterceptor struct{ c *Client }

func (b *bearerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if tok := b.c.agentToken; tok != "" {
			req.Header().Set("Authorization", "Bearer "+tok)
		}
		return next(ctx, req)
	}
}

func (b *bearerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if tok := b.c.agentToken; tok != "" {
			conn.RequestHeader().Set("Authorization", "Bearer "+tok)
		}
		return conn
	}
}

func (b *bearerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// ---- enroll ----

type EnrollRequest struct {
	EnrollToken  string          `json:"enroll_token"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

type EnrollResponse struct {
	RunnerID   int64            `json:"runner_id"`
	RunnerName string           `json:"runner_name"`
	AgentToken string           `json:"agent_token"`
	Bootstrap  BootstrapPayload `json:"bootstrap"`
}

// BootstrapPayload is the side of the enroll/bootstrap responses that
// tells the runner everything it needs to run with no extra flags:
// endpoints to inject into the agent, the embedded runner-binary
// catalogue (for future self-update), and the cadence parameters
// server and runner must agree on.
type BootstrapPayload struct {
	Binaries          map[string]BinaryInfo `json:"binaries"`
	BaseURL           string                `json:"base_url"`
	DefaultAgentImage string                `json:"default_agent_image,omitempty"`
	PollWaitSec       int                   `json:"poll_wait_sec"`
	HeartbeatSec      int                   `json:"heartbeat_sec"`
}

// BinaryInfo is one entry in BootstrapPayload.Binaries. URL is
// server-relative; the runner prepends the same base URL it uses for
// every other call.
type BinaryInfo struct {
	URL    string `json:"url"`
	Name   string `json:"name"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func (c *Client) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResponse, error) {
	resp, err := c.rpc.Enroll(ctx, connect.NewRequest(&runnerv1.EnrollRequest{
		EnrollToken:  req.EnrollToken,
		Capabilities: []byte(req.Capabilities),
	}))
	if err != nil {
		return nil, err
	}
	return &EnrollResponse{
		RunnerID:   resp.Msg.GetRunnerId(),
		RunnerName: resp.Msg.GetRunnerName(),
		AgentToken: resp.Msg.GetAgentToken(),
		Bootstrap:  bootstrapFromProto(resp.Msg.GetBootstrap()),
	}, nil
}

// Bootstrap re-fetches the bootstrap payload using the long-term agent
// token. Called by `serve` at startup and on every `serve --auto-update`
// tick to pick up endpoint / agent-binary changes the platform made
// since enroll.
//
// Stays on plain HTTP-JSON on purpose — auto-update is the only flow
// that can rescue a runner whose binary is too stale to speak the
// current Connect wire. Riding Connect here would brick that path on
// every breaking proto change.
func (c *Client) Bootstrap(ctx context.Context) (*BootstrapPayload, error) {
	if c.agentToken == "" {
		return nil, errors.New("agent token not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/runner/bootstrap", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.agentToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read bootstrap: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET /api/runner/bootstrap: %d %s", resp.StatusCode, snippet(body))
	}
	var bp BootstrapPayload
	if err := json.Unmarshal(body, &bp); err != nil {
		return nil, fmt.Errorf("decode bootstrap: %w", err)
	}
	return &bp, nil
}

func bootstrapFromProto(in *runnerv1.BootstrapPayload) BootstrapPayload {
	if in == nil {
		return BootstrapPayload{}
	}
	out := BootstrapPayload{
		BaseURL:           in.GetBaseUrl(),
		DefaultAgentImage: in.GetDefaultAgentImage(),
		PollWaitSec:       int(in.GetPollWaitSec()),
		HeartbeatSec:      int(in.GetHeartbeatSec()),
	}
	if bins := in.GetBinaries(); len(bins) > 0 {
		out.Binaries = make(map[string]BinaryInfo, len(bins))
		for k, b := range bins {
			out.Binaries[k] = BinaryInfo{
				URL:    b.GetUrl(),
				Name:   b.GetName(),
				GOOS:   b.GetGoos(),
				GOARCH: b.GetGoarch(),
				SHA256: b.GetSha256(),
				Size:   b.GetSize(),
			}
		}
	}
	return out
}

// ---- heartbeat ----

type HeartbeatRequest struct {
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
}

func (c *Client) Heartbeat(ctx context.Context, req HeartbeatRequest) error {
	_, err := c.rpc.Heartbeat(ctx, connect.NewRequest(&runnerv1.HeartbeatRequest{
		Capabilities: []byte(req.Capabilities),
	}))
	return err
}

// ---- tasks ----

type Task struct {
	// Kind discriminates the task payload: "agent_session" (default when empty
	// for backward compatibility) or "workflow_job". The runner's worker loop
	// routes to SessionDriver or WorkflowJobDriver accordingly.
	Kind string `json:"kind,omitempty"`

	// ---- workflow_job fields (present when Kind == "workflow_job") ----
	WorkflowJob *WorkflowJob `json:"workflow_job,omitempty"`

	SessionID           int64             `json:"session_id"`
	HostRepoID          int64             `json:"host_repo_id,omitempty"`
	AgentImage          string            `json:"agent_image"`
	AgentEntrypoint     []string          `json:"agent_entrypoint,omitempty"`
	AgentBuild          *BuildSpec        `json:"agent_build,omitempty"`
	Role                string            `json:"role"`
	Model               string            `json:"model"`
	LLMReasoningEffort  string            `json:"llm_reasoning_effort,omitempty"`
	LLMThinking         string            `json:"llm_thinking,omitempty"`
	IssueNumber         int32             `json:"issue_number,omitempty"`
	WorkingBranch       string            `json:"working_branch"`
	BaseBranch          string            `json:"base_branch"`
	HostAddendum        string            `json:"host_addendum"`
	Env                 map[string]string `json:"env"`
	SessionToken        string            `json:"session_token"`
	ContainerID         string            `json:"container_id,omitempty"`
	RepoVariables       map[string]string `json:"repo_variables"`
	Volumes             []Volume          `json:"volumes,omitempty"`
	McpServers          []string          `json:"mcp_servers,omitempty"`
}

// Volume mirrors agentsconfig.Volume on the wire.
type Volume struct {
	Name  string `json:"name"`
	Mount string `json:"mount"`
}

// BuildSpec mirrors agentsconfig.Build on the wire. Paths are
// repo-relative; the orchestrator resolves them against HostWorkdir.
type BuildSpec struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// ---- workflow job types ----

type WorkflowJob struct {
	JobRunID                int64             `json:"job_run_id"`
	WorkflowRunID           int64             `json:"workflow_run_id"`
	RepoID                  int64             `json:"repo_id"`
	Owner                   string            `json:"owner"`
	Name                    string            `json:"name"`
	WorkflowName            string            `json:"workflow_name"`
	JobKey                  string            `json:"job_key"`
	CheckoutRef             string            `json:"checkout_ref"`
	CommitSHA               string            `json:"commit_sha"`
	Tag                     string            `json:"tag,omitempty"`
	EventName               string            `json:"event_name,omitempty"`
	EventCauseID            string            `json:"event_cause_id,omitempty"`
	Container               WorkflowContainer `json:"container"`
	WorkingDir              string            `json:"working_directory"`
	Steps                   []WorkflowStep    `json:"steps"`
	TimeoutMinutes          int               `json:"timeout_minutes"`
	RepoVariables           map[string]string `json:"repo_variables"`
	Inputs                  map[string]string `json:"inputs,omitempty"`
	WorkflowToken           string            `json:"workflow_token,omitempty"`
	TriggerActorKind        string            `json:"trigger_actor_kind,omitempty"`
	TriggerActorID          string            `json:"trigger_actor_id,omitempty"`
	TriggerActorDisplayName string            `json:"trigger_actor_display_name,omitempty"`
}

type WorkflowContainer struct {
	Image      string            `json:"image"`
	Build      *BuildSpec        `json:"build,omitempty"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	Env        map[string]string `json:"env"`
	Volumes    []Volume          `json:"volumes"`
}

type WorkflowStep struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	// Shell-step fields (type "" / "run").
	Run string            `json:"run,omitempty"`
	Env map[string]string `json:"env,omitempty"`
	Dir string            `json:"dir,omitempty"`
	// Script-step source (type "script").
	Script string `json:"script,omitempty"`
	// With carries typed-step params (e.g. release tag/notes/assets).
	With map[string]any `json:"with,omitempty"`
}

// WorkflowStepAsset describes a single file to attach to a release.
// Kept as part of the public surface for the workflow runner's release
// step, even though it isn't carried over the wire by name (it ends
// up inside WorkflowStep.With).
type WorkflowStepAsset struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

// PollTasks returns (task, true, nil) on a real assignment, (nil, false, nil)
// when the server's long-poll window elapsed with no work, or (nil, false, err)
// on transport / server error. Callers loop on `false` after a small backoff.
// DEPRECATED: thin wrapper around PollTasksBatch(ctx, 1) for backward compat.
// PollTasksBatch already maps Canceled/DeadlineExceeded to (nil, nil), so
// the only error paths here are genuine transport/server failures.
func (c *Client) PollTasks(ctx context.Context) (*Task, bool, error) {
	tasks, err := c.PollTasksBatch(ctx, 1)
	if err != nil {
		return nil, false, err
	}
	if len(tasks) == 0 {
		return nil, false, nil
	}
	return tasks[0], true, nil
}

// PollTasksBatch long-polls for up to maxBatch tasks. Returns 0..K tasks;
// empty slice + nil error means no work was available. MaxBatch <= 0
// defaults to 1. The request's MaxBatch field is clamped server-side
// to maxTasksPerPoll (16).
func (c *Client) PollTasksBatch(ctx context.Context, maxBatch int) ([]*Task, error) {
	if maxBatch < 1 {
		maxBatch = 1
	}
	resp, err := c.rpc.Tasks(ctx, connect.NewRequest(&runnerv1.TasksRequest{
		MaxBatch: int32(maxBatch),
	}))
	if err != nil {
		switch connect.CodeOf(err) {
		case connect.CodeCanceled, connect.CodeDeadlineExceeded:
			return nil, nil
		}
		return nil, err
	}

	// Prefer the batch field (new servers); fall back to single .Task
	// for backward compat with old servers that don't know about .Tasks.
	tasks := tasksFromProto(resp.Msg)
	return tasks, nil
}

// tasksFromProto extracts a []*Task from a TasksResponse, preferring
// the .Tasks field (new protocol) and falling back to .Task for old
// servers that only populate the deprecated single field.
func tasksFromProto(msg *runnerv1.TasksResponse) []*Task {
	protos := msg.GetTasks()
	if len(protos) > 0 {
		tasks := make([]*Task, 0, len(protos))
		for _, t := range protos {
			if t == nil {
				continue
			}
			ct := taskFromProto(t)
			if ct != nil {
				tasks = append(tasks, ct)
			}
		}
		return tasks
	}

	// Fallback: old server only populated .Task
	t := msg.GetTask()
	if t == nil {
		return nil
	}
	ct := taskFromProto(t)
	if ct == nil {
		return nil
	}
	return []*Task{ct}
}

// taskFromProto converts a single proto Task to a client Task.
func taskFromProto(t *runnerv1.Task) *Task {
	switch body := t.GetBody().(type) {
	case *runnerv1.Task_AgentSession:
		return taskFromAgentSessionProto(body.AgentSession)
	case *runnerv1.Task_WorkflowJob:
		return &Task{
			Kind:        "workflow_job",
			HostRepoID:  body.WorkflowJob.GetRepoId(),
			WorkflowJob: workflowJobFromProto(body.WorkflowJob),
		}
	default:
		return nil
	}
}

func taskFromAgentSessionProto(in *runnerv1.AgentSessionTask) *Task {
	if in == nil {
		return nil
	}
	return &Task{
		Kind:                "", // agent_session is the default (empty string)
		SessionID:           in.GetSessionId(),
		HostRepoID:          in.GetHostRepoId(),
		AgentImage:          in.GetAgentImage(),
		AgentEntrypoint:     in.GetAgentEntrypoint(),
		AgentBuild:          buildSpecFromProto(in.GetAgentBuild()),
		Role:                in.GetRole(),
		Model:               in.GetModel(),
		LLMReasoningEffort:  in.GetLlmReasoningEffort(),
		LLMThinking:         in.GetLlmThinking(),
		IssueNumber:         in.GetIssueNumber(),
		WorkingBranch:       in.GetWorkingBranch(),
		BaseBranch:          in.GetBaseBranch(),
		HostAddendum:        in.GetHostAddendum(),
		Env:                 in.GetEnv(),
		SessionToken:        in.GetSessionToken(),
		ContainerID:         in.GetContainerId(),
		RepoVariables:       in.GetRepoVariables(),
		Volumes:             volumesFromProto(in.GetVolumes()),
		McpServers:          in.GetMcpServers(),
	}
}

func workflowJobFromProto(in *runnerv1.WorkflowJobTask) *WorkflowJob {
	if in == nil {
		return nil
	}
	out := &WorkflowJob{
		JobRunID:                in.GetJobRunId(),
		WorkflowRunID:           in.GetWorkflowRunId(),
		RepoID:                  in.GetRepoId(),
		Owner:                   in.GetOwner(),
		Name:                    in.GetName(),
		WorkflowName:            in.GetWorkflowName(),
		JobKey:                  in.GetJobKey(),
		CheckoutRef:             in.GetCheckoutRef(),
		CommitSHA:               in.GetCommitSha(),
		Tag:                     in.GetTag(),
		EventName:               in.GetEventName(),
		EventCauseID:            in.GetEventCauseId(),
		WorkingDir:              in.GetWorkingDirectory(),
		TimeoutMinutes:          int(in.GetTimeoutMinutes()),
		RepoVariables:           in.GetRepoVariables(),
		Inputs:                  in.GetInputs(),
		WorkflowToken:           in.GetWorkflowToken(),
		TriggerActorKind:        in.GetTriggerActorKind(),
		TriggerActorID:          in.GetTriggerActorId(),
		TriggerActorDisplayName: in.GetTriggerActorDisplayName(),
	}
	if c := in.GetContainer(); c != nil {
		out.Container = WorkflowContainer{
			Image:      c.GetImage(),
			Build:      buildSpecFromProto(c.GetBuild()),
			Entrypoint: c.GetEntrypoint(),
			Env:        c.GetEnv(),
			Volumes:    volumesFromProto(c.GetVolumes()),
		}
	}
	if steps := in.GetSteps(); len(steps) > 0 {
		out.Steps = make([]WorkflowStep, len(steps))
		for i, s := range steps {
			out.Steps[i] = WorkflowStep{
				ID:     s.GetId(),
				Name:   s.GetName(),
				Type:   s.GetType(),
				Run:    s.GetRun(),
				Env:    s.GetEnv(),
				Dir:    s.GetDir(),
				Script: s.GetScript(),
			}
			// `with` arrives as canonical JSON bytes — decode into
			// map[string]any so the runner's per-step interpreter sees
			// the same shape it always did.
			if raw := s.GetWith(); len(raw) > 0 {
				var w map[string]any
				if err := json.Unmarshal(raw, &w); err == nil {
					out.Steps[i].With = w
				}
			}
		}
	}
	return out
}

func buildSpecFromProto(in *runnerv1.BuildSpec) *BuildSpec {
	if in == nil {
		return nil
	}
	return &BuildSpec{
		Dockerfile: in.GetDockerfile(),
		Context:    in.GetContext(),
		Args:       in.GetArgs(),
	}
}

func volumesFromProto(in []*runnerv1.Volume) []Volume {
	if len(in) == 0 {
		return nil
	}
	out := make([]Volume, len(in))
	for i, v := range in {
		out[i] = Volume{Name: v.GetName(), Mount: v.GetMount()}
	}
	return out
}

// ---- container cleanup ----

type CleanupTask struct {
	SessionID   int64  `json:"session_id"`
	ContainerID string `json:"container_id"`
}

type CleanupTasksResponse struct {
	Tasks []CleanupTask `json:"tasks"`
}

// ListCleanupTasks polls the platform for containers this runner should
// remove. Empty result is `{Tasks: []}` (non-nil slice), so a polling
// client can treat the call as always-successful.
func (c *Client) ListCleanupTasks(ctx context.Context) (*CleanupTasksResponse, error) {
	resp, err := c.rpc.ListCleanupTasks(ctx, connect.NewRequest(&runnerv1.ListCleanupTasksRequest{}))
	if err != nil {
		return nil, err
	}
	out := &CleanupTasksResponse{Tasks: make([]CleanupTask, 0, len(resp.Msg.GetTasks()))}
	for _, t := range resp.Msg.GetTasks() {
		out.Tasks = append(out.Tasks, CleanupTask{
			SessionID:   t.GetSessionId(),
			ContainerID: t.GetContainerId(),
		})
	}
	return out, nil
}

// MarkCleanupDone reports that `docker rm` of the session's container
// succeeded (or that the container was already gone — the server-side
// handler treats both as success).
func (c *Client) MarkCleanupDone(ctx context.Context, sessionID int64) error {
	_, err := c.rpc.MarkCleanupDone(ctx, connect.NewRequest(&runnerv1.MarkCleanupDoneRequest{
		SessionId: sessionID,
	}))
	return err
}

// ---- workflow job callbacks ----

// MarkWorkflowJobRunning signals the platform that the runner has claimed
// this workflow job and is starting execution.
func (c *Client) MarkWorkflowJobRunning(ctx context.Context, jobRunID int64) error {
	_, err := c.rpc.MarkWorkflowJobRunning(ctx, connect.NewRequest(&runnerv1.MarkWorkflowJobRunningRequest{
		JobRunId: jobRunID,
	}))
	return err
}

// AppendWorkflowJobLog sends a single log line to the platform. Stream
// must be "stdout", "stderr", or "system". stepID identifies the
// currently executing step; empty for system-level log lines emitted
// between steps.
func (c *Client) AppendWorkflowJobLog(ctx context.Context, jobRunID int64, stream, line, stepID string) error {
	_, err := c.rpc.AppendWorkflowJobLog(ctx, connect.NewRequest(&runnerv1.AppendWorkflowJobLogRequest{
		JobRunId: jobRunID,
		Stream:   stream,
		Line:     line,
		StepId:   stepID,
	}))
	return err
}

// WorkflowJobTerminateRequest reports a workflow job's terminal state.
type WorkflowJobTerminateRequest struct {
	Status   string `json:"status"`
	ExitCode int32  `json:"exit_code"`
	Message  string `json:"message,omitempty"`
}

// TerminateWorkflowJob reports the final status of a workflow job back
// to the platform. Status must be "success", "failed", or "cancelled".
func (c *Client) TerminateWorkflowJob(ctx context.Context, jobRunID int64, req WorkflowJobTerminateRequest) error {
	_, err := c.rpc.TerminateWorkflowJob(ctx, connect.NewRequest(&runnerv1.TerminateWorkflowJobRequest{
		JobRunId: jobRunID,
		Status:   req.Status,
		ExitCode: req.ExitCode,
		Message:  req.Message,
	}))
	return err
}

// ---- workflow phase callbacks ----

type RegisterPhaseRequest struct {
	Phase         string `json:"phase"`
	SequenceIndex int32  `json:"sequence_index"`
	ImageRef      string `json:"image_ref"`
}

// PhaseResponse is the server's response to a phase registration.
// Retained as a public type for callers that already constructed it
// against the legacy JSON path; the new Connect RPC returns no body so
// every field comes back zero-valued.
type PhaseResponse struct {
	ID            int64   `json:"id"`
	Phase         string  `json:"phase"`
	Status        string  `json:"status"`
	SequenceIndex int32   `json:"sequence_index"`
	ImageRef      string  `json:"image_ref"`
	StartedAt     *string `json:"started_at"`
	FinishedAt    *string `json:"finished_at"`
	ExitCode      *int32  `json:"exit_code"`
	ErrorMessage  string  `json:"error_message"`
}

// RegisterWorkflowJobPhase creates or retrieves a phase row for a workflow job.
func (c *Client) RegisterWorkflowJobPhase(ctx context.Context, jobRunID int64, req RegisterPhaseRequest) error {
	_, err := c.rpc.RegisterWorkflowJobPhase(ctx, connect.NewRequest(&runnerv1.RegisterWorkflowJobPhaseRequest{
		JobRunId:      jobRunID,
		Phase:         req.Phase,
		SequenceIndex: req.SequenceIndex,
		ImageRef:      req.ImageRef,
	}))
	return err
}

// MarkWorkflowJobPhaseRunning signals the platform that a phase has started.
func (c *Client) MarkWorkflowJobPhaseRunning(ctx context.Context, jobRunID int64, phase string) error {
	_, err := c.rpc.MarkWorkflowJobPhaseRunning(ctx, connect.NewRequest(&runnerv1.MarkWorkflowJobPhaseRunningRequest{
		JobRunId: jobRunID,
		Phase:    phase,
	}))
	return err
}

// TerminatePhaseRequest reports the terminal state of a workflow job phase.
type TerminatePhaseRequest struct {
	Status   string `json:"status"`
	ExitCode *int32 `json:"exit_code,omitempty"`
	Message  string `json:"message,omitempty"`
}

// TerminateWorkflowJobPhase reports the final status of a phase.
func (c *Client) TerminateWorkflowJobPhase(ctx context.Context, jobRunID int64, phase string, req TerminatePhaseRequest) error {
	wireReq := &runnerv1.TerminateWorkflowJobPhaseRequest{
		JobRunId: jobRunID,
		Phase:    phase,
		Status:   req.Status,
		Message:  req.Message,
	}
	if req.ExitCode != nil {
		wireReq.ExitCode = *req.ExitCode
		wireReq.HasExitCode = true
	}
	_, err := c.rpc.TerminateWorkflowJobPhase(ctx, connect.NewRequest(wireReq))
	return err
}

// ---- workflow step result reporting ----

type WorkflowStepResultRequest struct {
	StepIndex int               `json:"step_index"`
	StepID    string            `json:"step_id,omitempty"`
	ExitCode  int32             `json:"exit_code"`
	Outputs   map[string]string `json:"outputs,omitempty"`
	Masked    []string          `json:"masked,omitempty"`
}

// ReportWorkflowStepResult reports a single step's outcome and captured
// outputs to the platform. Called after each step completes.
func (c *Client) ReportWorkflowStepResult(ctx context.Context, jobRunID int64, req WorkflowStepResultRequest) error {
	_, err := c.rpc.ReportWorkflowStepResult(ctx, connect.NewRequest(&runnerv1.ReportWorkflowStepResultRequest{
		JobRunId:  jobRunID,
		StepIndex: int32(req.StepIndex),
		StepId:    req.StepID,
		ExitCode:  req.ExitCode,
		Outputs:   req.Outputs,
		Masked:    req.Masked,
	}))
	return err
}

// ---- release API (called with workflow token, not agent token) ----
//
// Release endpoints live under /api/repos/{owner}/{name}/releases on
// the platform's general REST surface, not the runner Connect service.
// They use the workflow-scoped HANGRIX_WORKFLOW_TOKEN from the job
// payload rather than the runner's long-lived agent token, so they
// stay on plain JSON-over-HTTP.

type releaseResponse struct {
	ID      int64  `json:"id"`
	TagName string `json:"tag_name"`
	IsDraft bool   `json:"is_draft"`
}

// CreateReleaseRequest is the JSON body for POST /api/repos/{owner}/{name}/releases.
type CreateReleaseRequest struct {
	TagName string  `json:"tag_name"`
	Notes   *string `json:"notes,omitempty"`
}

// CreateRelease creates a draft release for the given tag. Returns the
// minimal release metadata the runner needs to produce step outputs.
// The workflowToken is the HANGRIX_WORKFLOW_TOKEN value from the job payload.
func (c *Client) CreateRelease(ctx context.Context, baseURL, owner, name, workflowToken string, req CreateReleaseRequest) (*releaseResponse, error) {
	path := fmt.Sprintf("/api/repos/%s/%s/releases", owner, name)
	var out releaseResponse
	if err := c.doWithToken(ctx, baseURL, http.MethodPost, path, req, &out, workflowToken); err != nil {
		return nil, err
	}
	return &out, nil
}

// UploadReleaseAsset uploads a single file as a release asset via
// multipart/form-data. contentType defaults to application/octet-stream.
func (c *Client) UploadReleaseAsset(ctx context.Context, baseURL, owner, name, workflowToken string, releaseID int64, assetName, contentType string, body io.Reader) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	path := fmt.Sprintf("/api/repos/%s/%s/releases/%d/assets", owner, name, releaseID)
	return c.uploadMultipart(ctx, baseURL, path, workflowToken, assetName, contentType, body)
}

// PublishRelease publishes a draft release. Returns the updated
// release metadata.
func (c *Client) PublishRelease(ctx context.Context, baseURL, owner, name, workflowToken string, releaseID int64) (*releaseResponse, error) {
	path := fmt.Sprintf("/api/repos/%s/%s/releases/%d/publish", owner, name, releaseID)
	var out releaseResponse
	if err := c.doWithToken(ctx, baseURL, http.MethodPost, path, nil, &out, workflowToken); err != nil {
		return nil, err
	}
	return &out, nil
}

// doWithToken is the plain-HTTP helper for release calls. Carries the
// caller-supplied bearer (not c.agentToken) and prepends baseURL to
// path (so a job can target a different platform than the runner's).
func (c *Client) doWithToken(ctx context.Context, baseURL, method, path string, in, out any, token string) error {
	url := strings.TrimRight(baseURL, "/") + path
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode body: %w", err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, snippet(respBody))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return nil
}

func (c *Client) uploadMultipart(ctx context.Context, baseURL, path, token, assetName, contentType string, fileReader io.Reader) error {
	url := strings.TrimRight(baseURL, "/") + path

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	if err := writer.WriteField("name", assetName); err != nil {
		return fmt.Errorf("write name field: %w", err)
	}
	part, err := writer.CreateFormFile("file", assetName)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, fileReader); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s: %d %s", path, resp.StatusCode, snippet(respBody))
	}
	return nil
}

// ---- binary downloads ----
//
// Binary downloads stay on plain HTTP because the body is raw bytes
// (a binary executable) — wrapping it in proto would force base64 on
// every byte for zero benefit. Path is the server-relative URL from
// BootstrapPayload.Binaries[*].URL.

func (c *Client) DownloadBinary(ctx context.Context, path string) ([]byte, string, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if c.agentToken == "" {
		return nil, "", errors.New("agent token not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.agentToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("GET %s: %d %s", path, resp.StatusCode, snippet(body))
	}
	return body, resp.Header.Get("X-Hangrix-SHA256"), nil
}

func snippet(b []byte) string {
	if len(b) > 256 {
		return string(b[:256]) + "…"
	}
	return string(b)
}
