// Package domain declares the workflow module's types and interfaces.
// Other modules depend only on this package; the Postgres implementation
// and HTTP handler live in sibling packages.
package domain

import (
	"context"
	"errors"
	"time"

	"github.com/hangrix/hangrix/pkg/actor"
)

// ---- status enums ----

// RunStatus is the lifecycle state of a workflow run.
type RunStatus string

const (
	RunStatusPending   RunStatus = "pending"
	RunStatusRunning   RunStatus = "running"
	RunStatusSuccess   RunStatus = "success"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
)

// Terminal returns true when the status represents a final state.
func (s RunStatus) Terminal() bool {
	return s == RunStatusSuccess || s == RunStatusFailed || s == RunStatusCancelled
}

// JobStatus is the lifecycle state of a workflow job run.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusSuccess   JobStatus = "success"
	JobStatusFailed    JobStatus = "failed"
	JobStatusSkipped   JobStatus = "skipped"
	JobStatusCancelled JobStatus = "cancelled"
)

// Terminal returns true when the job has reached a final state.
func (s JobStatus) Terminal() bool {
	return s == JobStatusSuccess || s == JobStatusFailed ||
		s == JobStatusSkipped || s == JobStatusCancelled
}

// ---- event name constants (mirror workflowsconfig for purity) ----

// EventName identifies the trigger event for a workflow run.
type EventName string

const (
	EventRepoPush         EventName = "repo.push"
	EventRepoPushTag      EventName = "repo.push_tag"
	EventIssueOpened      EventName = "issue.opened"
	EventIssueComment     EventName = "issue.comment"
	EventWorkflowDispatch EventName = "workflow.dispatch"
	EventContributionPush EventName = "contribution.push"
	EventIssuePush        EventName = "issue.push"
	// EventAgentWake is the trigger for the internal `_agent` workflow.
	// Each agent-session wake (fresh trigger or rewake of an idle session)
	// spawns one run of the hidden workflow; the run hosts exactly one
	// container that execs the hangrix-agent binary and exits when the
	// agent does. Not selectable from user-authored .hangrix/workflows.
	EventAgentWake EventName = "_agent.wake"
)

// InternalAgentWorkflowName is the reserved workflow_name for the hidden
// agent workflow that the spawner creates on every wake. The leading
// underscore is unreachable from user yaml (parser rejects names not
// matching `[a-z][a-z0-9-]*`), so this name is a safe discriminator: any
// workflow_runs row whose workflow_name equals it is a spawner-driven
// agent run, not a user workflow. User-facing list/get queries filter it
// out by name to keep the workflow UI clean.
const InternalAgentWorkflowName = "_agent"

// ---- domain models ----

// WorkflowRun is a single execution of a workflow.
type WorkflowRun struct {
	ID           int64
	RepoID       int64
	WorkflowName string
	SourceFile   string
	Status       RunStatus
	EventName    EventName
	CauseID      *int64 // push event ID, comment ID, or nil for dispatch
	Ref          string
	CommitSHA    string
	// ContainerSnapshotJSON caches the resolved container info at run creation
	// time (image, build, entrypoint, volumes, env keys) for audit/retry.
	ContainerSnapshotJSON []byte
	// TriggerPayloadJSON stores event-specific metadata for audit.
	TriggerPayloadJSON []byte
	// WorkflowToken is a short-term hangrix_wf_ token generated at run
	// creation time. Workflow steps use it to authenticate against
	// repo-scoped write endpoints (e.g. releases).
	WorkflowToken string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	CreatedAt     time.Time
	// TriggerActor is the actor who triggered this workflow run (user, agent, workflow, system).
	TriggerActor *actor.Ref
	// RunActor is the workflow run itself as an actor for provenance tracking
	// of downstream side effects (e.g. releases created by this workflow).
	RunActor *actor.Ref
}

// WorkflowJobRun is a single job execution within a workflow run.
type WorkflowJobRun struct {
	ID               int64
	WorkflowRunID    int64
	JobKey           string
	DisplayName      string
	Status           JobStatus
	SequenceIndex    int32
	WorkingDirectory string
	TimeoutMinutes   int32
	RunnerID         *int64
	ContainerID      *string
	// EnvJSON stores the merged env map for this job (container ← workflow ← job)
	// as a JSON blob. Secrets are not included; only non-secret values.
	EnvJSON []byte
	// StepsJSON stores the resolved step list for this job.
	StepsJSON []byte
	// StepOutputsJSON stores per-step outputs captured during job execution.
	// Map of step_id -> {key: StepOutputValue}. Written incrementally as steps complete.
	StepOutputsJSON []byte
	// JobOutputsJSON stores resolved job outputs computed after job completion.
	// Map of output_key -> StepOutputValue. Populated from ${{ }} resolution in the
	// job's declared outputs.
	JobOutputsJSON []byte
	// JobOutputsRawJSON stores the raw output templates at run creation time.
	// Map of output_key -> expression string (may contain ${{ }} references).
	// The service resolves these against runtime context at job completion.
	JobOutputsRawJSON []byte
	StartedAt         *time.Time
	FinishedAt        *time.Time
	ExitCode          *int32
	ErrorMessage      string
	CreatedAt         time.Time
}

// LogStream identifies the output stream for a log line.
type LogStream string

const (
	LogStreamStdout LogStream = "stdout"
	LogStreamStderr LogStream = "stderr"
	LogStreamSystem LogStream = "system"
)

// WorkflowJobLogLine is a single line of output from a workflow job.
// StepID is non-nil when the line was emitted inside a known step.
type WorkflowJobLogLine struct {
	ID               int64
	WorkflowJobRunID int64
	StepID           *string
	Stream           LogStream
	Line             string
	CreatedAt        time.Time
}

// ---- container snapshot (for audit) ----

// ContainerSnapshot captures the resolved container definition at run creation
// time, frozen so subsequent config changes don't affect in-flight runs.
type ContainerSnapshot struct {
	Image      string           `json:"image"`
	Build      *BuildSpec       `json:"build,omitempty"`
	Entrypoint []string         `json:"entrypoint,omitempty"`
	EnvKeys    []string         `json:"env_keys"` // env key names only, no values
	Volumes    []VolumeSnapshot `json:"volumes,omitempty"`
}

// BuildSpec mirrors agentsconfig.Build for snapshotting.
type BuildSpec struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// VolumeSnapshot captures a named volume mount at snapshot time.
type VolumeSnapshot struct {
	Name  string `json:"name"`
	Mount string `json:"mount"`
}

// ---- input params ----

// CreateRunParams is the input bag for creating a new workflow run and its
// initial pending jobs.
type CreateRunParams struct {
	RepoID       int64
	WorkflowName string
	SourceFile   string
	EventName    EventName
	CauseID      *int64
	Ref          string
	CommitSHA    string
	// ContainerEnv is the merged container.image env from agents.yml
	ContainerEnv map[string]string
	// ContainerImage is the resolved image (or empty if build is used)
	ContainerImage string
	// ContainerBuild is the build spec from agents.yml (nil if using image)
	ContainerBuild *BuildSpec
	// ContainerEntrypoint from agents.yml
	ContainerEntrypoint []string
	// ContainerVolumes from agents.yml
	ContainerVolumes []VolumeSnapshot
	// JobDefs carries the parsed job definitions for this workflow
	JobDefs []JobDefInput
	// DispatchInputs carries the user-provided inputs for workflow.dispatch.
	// Keys are already transformed to WORKFLOW_INPUT_UPPER_SNAKE.
	DispatchInputs map[string]string
	// TriggerPayloadJSON, when non-nil, is stored verbatim in the
	// trigger_payload_json column. When nil, the infra auto-generates
	// a payload from EventName + DispatchInputs.
	TriggerPayloadJSON []byte
	// WorkflowToken is the pre-generated hangrix_wf_ token for this run.
	WorkflowToken string
	// TriggerActor is the actor who triggered this workflow run (user, agent, etc.).
	TriggerActor actor.Ref
	// RunActor is the workflow run itself as an actor for downstream side effects.
	RunActor actor.Ref
}

// JobDefInput is the input bag for a single job within a new workflow run.
type JobDefInput struct {
	JobKey           string
	DisplayName      string
	Env              map[string]string
	TimeoutMinutes   int32
	WorkingDirectory string
	Steps            []StepInput
	// Outputs carries the raw output templates from the job definition.
	// Map of output_key -> expression string (may contain ${{ }} references).
	Outputs map[string]string
}

// StepInput is a single step within a job definition.
type StepInput struct {
	Id   *string // optional step id for ${{ steps.<id>.outputs.<key> }} references
	Name string
	// Type discriminates between step kinds. "" and "run" are shell steps;
	// other values name a built-in typed step (e.g. "release", "script").
	Type string `json:"type,omitempty"`
	// Run is the shell command (only for type=run / type omitted).
	Run string `json:"run,omitempty"`
	// Script is the inline script body for type=script steps.
	Script string `json:"script,omitempty"`
	// Env is a per-step env map merged over the job/container env at
	// execution time (only for type=run / type omitted).
	Env map[string]string `json:"env,omitempty"`
	// Dir overrides the job working directory for this step. Relative
	// paths resolve against the job working directory.
	Dir string `json:"dir,omitempty"`
	// With carries the parameters for built-in typed steps (e.g. release's
	// tag/notes/draft/assets), mirroring GitHub Actions' `with:`. It is
	// stored verbatim and interpreted per step type by the runner.
	With map[string]any `json:"with,omitempty"`
}

// ---- Phase types ----

// PhaseKind identifies a workflow job system phase.
type PhaseKind string

const (
	PhaseImagePull      PhaseKind = "image_pull"
	PhaseImageBuild     PhaseKind = "image_build"
	PhaseContainerStart PhaseKind = "container_start"
)

// PhaseStatus is the lifecycle state of a single phase.
type PhaseStatus string

const (
	PhaseStatusPending PhaseStatus = "pending"
	PhaseStatusRunning PhaseStatus = "running"
	PhaseStatusSuccess PhaseStatus = "success"
	PhaseStatusFailed  PhaseStatus = "failed"
	PhaseStatusSkipped PhaseStatus = "skipped"
)

// JobPhase records the structured state of one system phase
// (image_pull / image_build / container_start) within a job run.
type JobPhase struct {
	ID               int64
	WorkflowJobRunID int64
	Phase            PhaseKind
	Status           PhaseStatus
	SequenceIndex    int32
	ImageRef         string
	StartedAt        *time.Time
	FinishedAt       *time.Time
	ExitCode         *int32
	ErrorMessage     string
	CreatedAt        time.Time
}

// PhaseLogStepID maps a PhaseKind to the reserved step_id used in
// workflow_job_logs to attribute log lines to that phase.
func PhaseLogStepID(p PhaseKind) string { return "phase:" + string(p) }

// ---- outputs ----

// StepOutputValue is a single output value with masking metadata.
// The runner reports which output keys contain secret values (masked=true),
// and the UI uses this to render secrets as "***".
type StepOutputValue struct {
	Value  string `json:"value"`
	Masked bool   `json:"masked"`
}

// ---- interfaces ----

// Store is the persistence abstraction for workflow runs, jobs, and logs.
type Store interface {
	// ---- workflow runs ----

	// CreateRun inserts a new workflow_run row in 'pending' status and
	// all associated workflow_job_run rows in 'pending' status.
	CreateRun(ctx context.Context, params CreateRunParams) (*WorkflowRun, []*WorkflowJobRun, error)

	// GetRun returns a single workflow run by ID.
	GetRun(ctx context.Context, id int64) (*WorkflowRun, error)

	// GetRunByToken returns id, repo_id, workflow_name, and status for a
	// workflow run identified by its workflow_token.
	GetRunByToken(ctx context.Context, token string) (repoID int64, runID int64, workflowName string, status RunStatus, err error)

	// ListRunsByRepo returns workflow runs for a repo, ordered by created_at DESC.
	// workflowName filters to a specific workflow (empty = all).
	// status filters by run status (empty = all).
	ListRunsByRepo(ctx context.Context, repoID int64, workflowName, status string, offset, limit int32) ([]*WorkflowRun, int64, error)

	// ListAgentRunsByRepo returns only the hidden _agent workflow runs for a repo.
	// status filters by run status (empty = all).
	ListAgentRunsByRepo(ctx context.Context, repoID int64, status string, offset, limit int32) ([]*WorkflowRun, int64, error)

	// ListRunsByRepoAndCommitSHA returns workflow runs for a repo matching
	// the given commit SHA, ordered by created_at DESC.
	ListRunsByRepoAndCommitSHA(ctx context.Context, repoID int64, commitSHA string) ([]*WorkflowRun, error)

	// MarkRunStarted transitions a run from pending to running.
	MarkRunStarted(ctx context.Context, id int64) error

	// MarkRunTerminal transitions a run to a terminal status.
	MarkRunTerminal(ctx context.Context, id int64, status RunStatus) error

	// CancelRunningJobs sets all running jobs in a run to cancelled.
	CancelRunningJobs(ctx context.Context, runID int64) error

	// ---- workflow job runs ----

	// GetJobRun returns a single job run by ID.
	GetJobRun(ctx context.Context, id int64) (*WorkflowJobRun, error)

	// ListJobRunsByRun returns all job runs for a workflow run, ordered by sequence_index.
	ListJobRunsByRun(ctx context.Context, workflowRunID int64) ([]*WorkflowJobRun, error)

	// ClaimNextJob claims the next pending workflow job (oldest first) for a runner.
	// Uses SELECT ... FOR UPDATE SKIP LOCKED for race-safe claiming.
	// Returns ErrNoPendingJob when no jobs are available.
	// DEPRECATED: kept for backward-compat; new code uses ClaimNextJobs.
	ClaimNextJob(ctx context.Context, runnerID int64) (*WorkflowJobRun, error)

	// ClaimNextJobs claims up to limit pending workflow jobs for a runner.
	// Returns 0..limit jobs; nil error + empty slice means no pending jobs.
	// Preserves sequential-execution invariant (max 1 per workflow_run).
	ClaimNextJobs(ctx context.Context, runnerID int64, limit int) ([]*WorkflowJobRun, error)

	// MarkJobRunning transitions a job from pending/claimed to running.
	MarkJobRunning(ctx context.Context, id int64, runnerID int64) error

	// MarkJobTerminal transitions a job to a terminal status with exit code and message.
	MarkJobTerminal(ctx context.Context, id int64, status JobStatus, exitCode *int32, errMsg string) error

	// SkipRemainingJobs marks all remaining pending jobs in a run as skipped.
	SkipRemainingJobs(ctx context.Context, workflowRunID int64, afterSequenceIndex int32) error

	// SetJobContainer records the container ID for a running job.
	SetJobContainer(ctx context.Context, id int64, containerID string) error

	// SetStepOutputs merges a step's outputs into the job's step_outputs_json.
	// stepID identifies the step within the job (must match a declared step id).
	// outputs is the map of key -> StepOutputValue captured from the step's stdout.
	SetStepOutputs(ctx context.Context, id int64, stepID string, outputs map[string]StepOutputValue) error

	// SetJobOutputs writes resolved job outputs after job completion.
	SetJobOutputs(ctx context.Context, id int64, outputs map[string]StepOutputValue) error

	// ---- workflow job phases ----

	// CreatePhase inserts a single phase row in 'pending' status. Idempotent
	// via UNIQUE(job_run_id, phase): subsequent calls return the existing row.
	CreatePhase(ctx context.Context, jobRunID int64, phase PhaseKind, sequenceIndex int32, imageRef string) (*JobPhase, error)

	// MarkPhaseRunning transitions a phase to 'running' and stamps started_at.
	MarkPhaseRunning(ctx context.Context, jobRunID int64, phase PhaseKind) error

	// MarkPhaseTerminal transitions a phase to a terminal status with
	// optional exit code and error message; stamps finished_at.
	MarkPhaseTerminal(ctx context.Context, jobRunID int64, phase PhaseKind, status PhaseStatus, exitCode *int32, errMsg string) error

	// ListPhasesByJob returns all phases for a job, ordered by sequence_index ASC.
	ListPhasesByJob(ctx context.Context, jobRunID int64) ([]*JobPhase, error)

	// ---- workflow job logs ----

	// AppendLog appends a single log line to a job run. stepID is the
	// currently-executing step key, or nil when between steps.
	AppendLog(ctx context.Context, jobRunID int64, stream LogStream, line string, stepID *string) error

	// ListLogs returns log lines for a job run, ordered by created_at ASC.
	// stepID filters to a specific step (nil = all steps).
	// sinceID returns only lines with id > sinceID (0 = no filter).
	ListLogs(ctx context.Context, jobRunID int64, stepID *string, sinceID int64, offset, limit int32) ([]*WorkflowJobLogLine, int64, error)
}

// Dispatcher is the cross-module interface that the runner module uses to
// claim workflow jobs during task polling. It exposes the minimal surface
// needed for the runner to discover and claim workflow work.
type Dispatcher interface {
	// ClaimNextJob claims the next pending workflow job for the given runner.
	// Returns ErrNoPendingJob when no jobs are available.
	ClaimNextJob(ctx context.Context, runnerID int64) (*WorkflowJobRun, error)

	// ClaimNextJobs claims up to limit pending workflow jobs for the given runner.
	// Returns 0..limit jobs; nil error + empty slice means no pending jobs.
	ClaimNextJobs(ctx context.Context, runnerID int64, limit int) ([]*WorkflowJobRun, error)

	// GetRunForJob returns the workflow run that owns the given job.
	GetRunForJob(ctx context.Context, jobRunID int64) (*WorkflowRun, error)
}

// TagEventTrigger is the cross-module interface for triggering workflow runs
// in response to tag creation or push events. Modules that produce tag events
// (repo REST API, git push observers) depend on this interface rather than
// the concrete service.
type TagEventTrigger interface {
	TriggerTagEvent(ctx context.Context, repoID int64, ownerName, repoName, defaultBranch, tagName, commitSHA string) error
}

// ---- sentinel errors ----

// WorkflowTokenValidator is the cross-module interface that allows other
// modules (e.g. release) to validate a hangrix_wf_ token and get the repo
// ID it is scoped to. The workflow module's Service implements it.
type WorkflowTokenValidator interface {
	// ValidateWorkflowToken returns the repo ID for a valid, non-terminal token.
	ValidateWorkflowToken(ctx context.Context, token string) (repoID int64, err error)

	// ValidateWorkflowTokenWithActor returns repo ID + the workflow actor for
	// provenance tracking. Callers that record side effects (e.g. release writes)
	// should use this to attribute the action to the correct workflow actor.
	ValidateWorkflowTokenWithActor(ctx context.Context, token string) (repoID int64, actor actor.Ref, err error)
}


// RunStatusObserver is the cross-module interface for consumers that
// need to react to workflow run status transitions (pending → running,
// running → success/failed/cancelled). Implementations are called
// synchronously after the transition is persisted; they must not block
// or error (best-effort side effect only).
type RunStatusObserver interface {
	// OnRunStatusChanged is called after a workflow run's status has
	// changed. oldStatus is the previous status; run carries the
	// updated state (including the new status).
	OnRunStatusChanged(ctx context.Context, oldStatus RunStatus, run *WorkflowRun) error
}

// CheckItem is a single job-level CI check status entry returned to agent-facing
// tools. Each job within a workflow run produces its own CheckItem — so a
// workflow with two jobs produces two items (e.g. "ci / lint" and "ci / test").
type CheckItem struct {
	Name         string  `json:"name"`         // "<workflow_name> / <job display_name>"
	Status       string  `json:"status"`       // pending|running|completed
	Conclusion  string  `json:"conclusion"`   // success|failure|cancelled|skipped; empty when status != completed
	JobRunID    int64   `json:"job_run_id"`   // for workflow_download_job_log
	RunID       int64   `json:"run_id"`       // for jump to run detail
	WorkflowName string `json:"workflow_name"`
	JobKey      string  `json:"job_key"`
	EventName   string  `json:"event_name"`   // contribution.push / issue.push / ...
	URL         string  `json:"url,omitempty"`
	StartedAt   *string `json:"started_at,omitempty"`
	FinishedAt  *string `json:"finished_at,omitempty"`
}

// CheckReader is the cross-module interface that allows the
// platform_api module to list CI checks for an issue's commit.
type CheckReader interface {
	// ListChecksByCommit returns job-level CI checks for the given repo
	// and commit SHA, ordered by run created_at DESC and within each run
	// by job sequence_index ASC.
	ListChecksByCommit(ctx context.Context, repoID int64, commitSHA string) ([]CheckItem, error)
}

// PushEventDispatcher is the cross-module interface for triggering workflow
// runs in response to contribution.push and issue.push events.
type PushEventDispatcher interface {
	DispatchContributionPush(ctx context.Context, repo Ref, in ContributionPushInput)
	DispatchIssuePush(ctx context.Context, repo Ref, in IssuePushInput)
}

// Ref is a lightweight reference to a repo, used across domain interfaces.
type Ref struct {
	ID            int64
	Name          string
	DefaultBranch string
	OwnerName     string
}

// ContributionPushInput carries the data needed to dispatch a contribution.push
// workflow event.
type ContributionPushInput struct {
	IssueNumber  int64
	AgentRole    string
	RefName      string // refs/heads/issue-<n>/<role>/<slug>
	CommitSHA    string
	ChangedPaths []string
	TriggerActor actor.Ref
}

// IssuePushInput carries the data needed to dispatch an issue.push workflow
// event.
type IssuePushInput struct {
	IssueNumber  int64
	BranchName   string // refs/heads/issue/<n>
	CommitSHA    string // new head sha
	OldCommitSHA string // head sha before the push
	ChangedPaths []string
	Cause        string // "contribution_apply" / "child_issue_merged" / "manual_sync"
	TriggerActor actor.Ref
}

var (
	ErrNoPendingJob         = errors.New("no pending workflow job")
	ErrJobNotFound          = errors.New("workflow job not found")
	ErrRunNotFound          = errors.New("workflow run not found")
	ErrInvalidStatus        = errors.New("invalid status transition")
	ErrInvalidWorkflowToken = errors.New("invalid workflow token")
)
