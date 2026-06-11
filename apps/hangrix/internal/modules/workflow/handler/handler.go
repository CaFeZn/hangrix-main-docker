// Package handler exposes the HTTP surface for workflow management,
// viewing, and dispatch. The runner-callback endpoints (workflow job
// lifecycle + phase lifecycle) moved to hangrix.runner.v1 Connect-Go;
// see modules/runner/handler/runner_connect_workflow.go.
package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/service"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// Handler implements server.RouteProvider for the workflow module.
type Handler struct {
	svc         *service.Service
	middleware  authdomain.Middleware
	repoStore   repodomain.Store
	orgResolver orgdomain.Resolver
	issueStore  issuedomain.Store
}

// HandlerDeps wires the handler's dependencies through ioc.
//
// The agent-token validator is no longer needed here: every runner
// callback (workflow job start/log/step-result/terminate, phase
// register/run/terminate) moved to runner_connect.go's
// hangrix.runner.v1.RunnerService and uses that handler's
// agentTokenInterceptor for auth.
type HandlerDeps struct {
	Service     *service.Service
	Middleware  authdomain.Middleware
	RepoStore   repodomain.Store
	OrgResolver orgdomain.Resolver
	IssueStore  issuedomain.Store
}

// NewHandler creates a ready-to-use workflow Handler.
func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		svc:         deps.Service,
		middleware:  deps.Middleware,
		repoStore:   deps.RepoStore,
		orgResolver: deps.OrgResolver,
		issueStore:  deps.IssueStore,
	}
}

// RegisterRoutes implements server.RouteProvider.
func (h *Handler) RegisterRoutes(r chi.Router) {
	// User-facing / agent-facing: commit-level checks endpoint.
	r.Route("/api/repos/{owner}/{name}/commits/{sha}/checks", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.listCommitChecks)
	})

	// User-facing API: requires auth. Mounted under
	// /api/repos/{owner}/{name}/workflow-runs (not /api/repos/{owner}/{name})
	// so chi doesn't steal exact-path GET /api/repos/{owner}/{name} from the
	// repo handler — chi gives longer prefix mounts priority.
	r.Route("/api/repos/{owner}/{name}/workflow-runs", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.listRuns)
		r.Get("/{runID}", h.getRun)
		r.Post("/", h.dispatch)
		r.Get("/{runID}/jobs/{jobID}/logs", h.getLogs)
		r.Post("/{runID}/cancel", h.cancelRun)
	})

	// Issue-level CI checks endpoint.
	r.Route("/api/repos/{owner}/{name}/issues/{issueNumber}/checks", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.listIssueChecks)
	})

	// /api/runner/workflow-jobs/{jobRunID}/* was the legacy runner
	// callback surface. It moved to hangrix.runner.v1.RunnerService —
	// see modules/runner/handler/runner_connect_workflow.go.
}

// ---- request/response DTOs ----

type runDTO struct {
	ID           int64   `json:"id"`
	RepoID       int64   `json:"repo_id"`
	WorkflowName string  `json:"workflow_name"`
	SourceFile   string  `json:"source_file"`
	Status       string  `json:"status"`
	EventName    string  `json:"event_name"`
	CauseID      *int64  `json:"cause_id"`
	Ref          string  `json:"ref"`
	CommitSHA    string  `json:"commit_sha"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	CreatedAt    string  `json:"created_at"`
}

type jobRunDTO struct {
	ID               int64                                        `json:"id"`
	WorkflowRunID    int64                                        `json:"workflow_run_id"`
	JobKey           string                                       `json:"job_key"`
	DisplayName      string                                       `json:"display_name"`
	Status           string                                       `json:"status"`
	SequenceIndex    int32                                        `json:"sequence_index"`
	WorkingDirectory string                                       `json:"working_directory"`
	TimeoutMinutes   int32                                        `json:"timeout_minutes"`
	RunnerID         *int64                                       `json:"runner_id"`
	ContainerID      *string                                      `json:"container_id"`
	Steps            []workflowStepDTO                            `json:"steps,omitempty"`
	StepOutputs      map[string]map[string]domain.StepOutputValue `json:"step_outputs"`
	JobOutputs       map[string]domain.StepOutputValue            `json:"job_outputs"`
	StartedAt        *string                                      `json:"started_at"`
	FinishedAt       *string                                      `json:"finished_at"`
	ExitCode         *int32                                       `json:"exit_code"`
	ErrorMessage     string                                       `json:"error_message"`
	CreatedAt        string                                       `json:"created_at"`
	Phases           []jobPhaseDTO                                `json:"phases"`
}


// workflowStepDTO mirrors the runner's client.WorkflowStep — the shape
// stored in domain.WorkflowJobRun.StepsJSON.
type workflowStepDTO struct {
	ID   string            `json:"id,omitempty"`
	Name string            `json:"name,omitempty"`
	Type string            `json:"type,omitempty"`
	Run  string            `json:"run,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
	Dir  string            `json:"dir,omitempty"`
	With map[string]any    `json:"with,omitempty"`
}

type logLineDTO struct {
	ID               int64   `json:"id"`
	WorkflowJobRunID int64   `json:"workflow_job_run_id"`
	StepID           *string `json:"step_id"`
	Stream           string  `json:"stream"`
	Line             string  `json:"line"`
	CreatedAt        string  `json:"created_at"`
}

type runListResp struct {
	Items []runDTO `json:"items"`
	Total int64    `json:"total"`
}

type runDetailResp struct {
	Run  runDTO      `json:"run"`
	Jobs []jobRunDTO `json:"jobs"`
}

type logsResp struct {
	Lines []logLineDTO `json:"lines"`
	Total int64        `json:"total"`
}

type dispatchReq struct {
	WorkflowName string             `json:"workflow_name"`
	Ref          string             `json:"ref"`
	Inputs       []dispatchInputDTO `json:"inputs"`
}

type dispatchInputDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ---- mappers ----

func toRunDTO(r *domain.WorkflowRun) runDTO {
	dto := runDTO{
		ID:           r.ID,
		RepoID:       r.RepoID,
		WorkflowName: r.WorkflowName,
		SourceFile:   r.SourceFile,
		Status:       string(r.Status),
		EventName:    string(r.EventName),
		CauseID:      r.CauseID,
		Ref:          r.Ref,
		CommitSHA:    r.CommitSHA,
		CreatedAt:    r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if r.StartedAt != nil {
		s := r.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.StartedAt = &s
	}
	if r.FinishedAt != nil {
		s := r.FinishedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.FinishedAt = &s
	}
	return dto
}

func toJobRunDTO(j *domain.WorkflowJobRun) jobRunDTO {
	dto := jobRunDTO{
		ID:               j.ID,
		WorkflowRunID:    j.WorkflowRunID,
		JobKey:           j.JobKey,
		DisplayName:      j.DisplayName,
		Status:           string(j.Status),
		SequenceIndex:    j.SequenceIndex,
		WorkingDirectory: j.WorkingDirectory,
		TimeoutMinutes:   j.TimeoutMinutes,
		RunnerID:         j.RunnerID,
		ContainerID:      j.ContainerID,
		ExitCode:         j.ExitCode,
		ErrorMessage:     j.ErrorMessage,
		CreatedAt:        j.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}

	// Deserialise frozen step definitions from StepsJSON.
	if len(j.StepsJSON) > 0 {
		var steps []workflowStepDTO
		if err := json.Unmarshal(j.StepsJSON, &steps); err == nil {
			dto.Steps = steps
		}
	}
	if j.StartedAt != nil {
		s := j.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.StartedAt = &s
	}
	if j.FinishedAt != nil {
		s := j.FinishedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.FinishedAt = &s
	}
	return dto
}

func toLogLineDTO(l *domain.WorkflowJobLogLine) logLineDTO {
	return logLineDTO{
		ID:               l.ID,
		WorkflowJobRunID: l.WorkflowJobRunID,
		StepID:           l.StepID,
		Stream:           string(l.Stream),
		Line:             l.Line,
		CreatedAt:        l.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// ---- helpers ----

// resolveRepo parses owner/name from the request URL, resolves the owner
// through orgdomain.Resolver, and fetches the repo from repo.Store.
func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) (*repodomain.Repo, bool) {
	ownerName := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "name")

	owner, err := h.orgResolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		if errors.Is(err, orgdomain.ErrOwnerNotFound) || errors.Is(err, orgdomain.ErrOrgReserved) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}

	repo, err := h.repoStore.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(owner.Kind), owner.ID, repoName)
	if err != nil {
		if errors.Is(err, repodomain.ErrRepoNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}

	return repo, true
}

// ---- user-facing endpoints ----

// GET /api/repos/{owner}/{name}/workflow-runs
func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}

	workflowName := r.URL.Query().Get("workflow")
	status := r.URL.Query().Get("status")
	kind := r.URL.Query().Get("kind") // "agent" selects _agent workflow runs

	// When commit_sha is provided, return runs attached to that commit.
	if commitSHA := r.URL.Query().Get("commit_sha"); commitSHA != "" {
		runs, err := h.svc.ListRunsByCommitSHA(r.Context(), repo.ID, commitSHA)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items := make([]runDTO, len(runs))
		for i, run := range runs {
			items[i] = toRunDTO(run)
		}
		httpx.WriteJSON(w, http.StatusOK, runListResp{Items: items, Total: int64(len(items))})
		return
	}

	offset := int32(0)
	limit := int32(50)
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.ParseInt(o, 10, 32); err == nil {
			offset = int32(v)
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.ParseInt(l, 10, 32); err == nil && v > 0 && v <= 200 {
			limit = int32(v)
		}
	}

	var runs []*domain.WorkflowRun
	var total int64
	var err error

	if kind == "agent" {
		// Return only the internal _agent workflow runs.
		runs, total, err = h.svc.ListAgentRunsByRepo(r.Context(), repo.ID, status, offset, limit)
	} else {
		runs, total, err = h.svc.ListRunsByRepo(r.Context(), repo.ID, workflowName, status, offset, limit)
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]runDTO, len(runs))
	for i, run := range runs {
		items[i] = toRunDTO(run)
	}

	httpx.WriteJSON(w, http.StatusOK, runListResp{Items: items, Total: total})
}

// GET /api/repos/{owner}/{name}/workflow-runs/{runID}
func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	runID, ok := httpx.ParseID(w, chi.URLParam(r, "runID"))
	if !ok {
		return
	}

	run, err := h.svc.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, domain.ErrRunNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "run not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jobs, err := h.svc.ListJobRuns(r.Context(), runID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jobDTOs := make([]jobRunDTO, len(jobs))
	for i, j := range jobs {
		dto := toJobRunDTO(j)
		phases, err := h.svc.ListJobPhases(r.Context(), j.ID)
		if err != nil {
			// Non-fatal: phases are best-effort enrichment.
			log.Printf("workflow: getRun list phases job=%d: %v", j.ID, err)
		} else {
			dto.Phases = make([]jobPhaseDTO, len(phases))
			for pi, p := range phases {
				dto.Phases[pi] = toPhaseDTO(p)
			}
		}
		jobDTOs[i] = dto
	}

	httpx.WriteJSON(w, http.StatusOK, runDetailResp{
		Run:  toRunDTO(run),
		Jobs: jobDTOs,
	})
}

// POST /api/repos/{owner}/{name}/workflow-runs
func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}

	var req dispatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if req.WorkflowName == "" {
		httpx.WriteError(w, http.StatusBadRequest, "workflow_name is required")
		return
	}

	inputs := make([]service.DispatchInput, len(req.Inputs))
	for i, in := range req.Inputs {
		inputs[i] = service.DispatchInput{Name: in.Name, Value: in.Value}
	}

	repoRef := service.Ref{
		ID:            repo.ID,
		Name:          repo.Name,
		DefaultBranch: repo.DefaultBranch,
		OwnerName:     repo.OwnerName,
	}

	run, jobs, err := h.svc.Dispatch(r.Context(), repoRef, req.WorkflowName, inputs, req.Ref)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jobDTOs := make([]jobRunDTO, len(jobs))
	for i, j := range jobs {
		jobDTOs[i] = toJobRunDTO(j)
	}

	httpx.WriteJSON(w, http.StatusCreated, runDetailResp{
		Run:  toRunDTO(run),
		Jobs: jobDTOs,
	})
}

// GET /api/repos/{owner}/{name}/workflow-runs/{runID}/jobs/{jobID}/logs
func (h *Handler) getLogs(w http.ResponseWriter, r *http.Request) {
	jobID, ok := httpx.ParseID(w, chi.URLParam(r, "jobID"))
	if !ok {
		return
	}

	var stepID *string
	if s := r.URL.Query().Get("step_id"); s != "" {
		stepID = &s
	}

	var sinceID int64
	if s := r.URL.Query().Get("since"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
			sinceID = v
		}
	}

	offset := int32(0)
	limit := int32(100)
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.ParseInt(o, 10, 32); err == nil {
			offset = int32(v)
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.ParseInt(l, 10, 32); err == nil && v > 0 && v <= 1000 {
			limit = int32(v)
		}
	}

	lines, total, err := h.svc.ListLogs(r.Context(), jobID, stepID, sinceID, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	dto := make([]logLineDTO, len(lines))
	for i, l := range lines {
		dto[i] = toLogLineDTO(l)
	}

	httpx.WriteJSON(w, http.StatusOK, logsResp{Lines: dto, Total: total})
}

// POST /api/repos/{owner}/{name}/workflow-runs/{runID}/cancel
func (h *Handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	runID, ok := httpx.ParseID(w, chi.URLParam(r, "runID"))
	if !ok {
		return
	}

	if err := h.svc.CancelRun(r.Context(), runID); err != nil {
		if errors.Is(err, domain.ErrRunNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "run not found")
			return
		}
		if errors.Is(err, domain.ErrInvalidStatus) {
			httpx.WriteError(w, http.StatusBadRequest, "run is already in a terminal state")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
}

// ---- issue-level checks endpoint ----

type issueChecksResp struct {
	Items []domain.CheckItem `json:"items"`
}

// GET /api/repos/{owner}/{name}/issues/{issueNumber}/checks
func (h *Handler) listIssueChecks(w http.ResponseWriter, r *http.Request) {
	issueNumberStr := chi.URLParam(r, "issueNumber")
	issueNumber, err := strconv.ParseInt(issueNumberStr, 10, 64)
	if err != nil || issueNumber <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid issue number")
		return
	}

	repo, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}

	issue, err := h.issueStore.GetByNumber(r.Context(), repo.ID, issueNumber)
	if err != nil {
		if errors.Is(err, issuedomain.ErrIssueNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "issue not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if issue.HeadSHA == "" {
		httpx.WriteJSON(w, http.StatusOK, issueChecksResp{Items: []domain.CheckItem{}})
		return
	}

	items, err := h.svc.ListChecksByCommit(r.Context(), repo.ID, issue.HeadSHA)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []domain.CheckItem{}
	}

	httpx.WriteJSON(w, http.StatusOK, issueChecksResp{Items: items})
}

// ---- commit-level checks endpoint ----

// GET /api/repos/{owner}/{name}/commits/{sha}/checks
func (h *Handler) listCommitChecks(w http.ResponseWriter, r *http.Request) {
	sha := chi.URLParam(r, "sha")
	if sha == "" {
		httpx.WriteError(w, http.StatusBadRequest, "commit sha is required")
		return
	}

	repo, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}

	items, err := h.svc.ListChecksByCommit(r.Context(), repo.ID, sha)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []domain.CheckItem{}
	}

	httpx.WriteJSON(w, http.StatusOK, issueChecksResp{Items: items})
}

// ---- phase DTOs ----
//
// jobPhaseDTO + toPhaseDTO survive because the user-facing run-detail
// API embeds them in jobRunDTO.Phases. The runner-callback handlers
// that used to live below moved to runner_connect_workflow.go.

type jobPhaseDTO struct {
	Phase         string  `json:"phase"`
	Status        string  `json:"status"`
	SequenceIndex int32   `json:"sequence_index"`
	ImageRef      string  `json:"image_ref"`
	StartedAt     *string `json:"started_at"`
	FinishedAt    *string `json:"finished_at"`
	ExitCode      *int32  `json:"exit_code"`
	ErrorMessage  string  `json:"error_message"`
}

func toPhaseDTO(p *domain.JobPhase) jobPhaseDTO {
	dto := jobPhaseDTO{
		Phase:         string(p.Phase),
		Status:        string(p.Status),
		SequenceIndex: p.SequenceIndex,
		ImageRef:      p.ImageRef,
		ErrorMessage:  p.ErrorMessage,
	}
	if p.StartedAt != nil {
		s := p.StartedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.StartedAt = &s
	}
	if p.FinishedAt != nil {
		s := p.FinishedAt.UTC().Format("2006-01-02T15:04:05Z")
		dto.FinishedAt = &s
	}
	if p.ExitCode != nil {
		dto.ExitCode = p.ExitCode
	}
	return dto
}

// ---- compile-time check ----
var _ server.RouteProvider = (*Handler)(nil)
