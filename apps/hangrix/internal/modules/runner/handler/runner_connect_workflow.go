// runner_connect_workflow.go holds the seven workflow-job lifecycle
// RPCs that originally lived under `/api/runner/workflow-jobs/{id}/*`
// in the workflow module's HTTP handler. Methods belong to the same
// RunnerConnectHandler struct as runner_connect.go (Go allows
// multi-file type definitions); the split is purely organisational so
// the two halves of the runner protocol are each digestible.
//
// Every method delegates to *workflowservice.Service for the SQL +
// orchestration; this file owns only the proto translation and the
// HTTP-status-code → connect.Code mapping. Status validation (the
// "must be success/failed/cancelled" gates from the legacy handler)
// happens here so the service layer doesn't see invalid enum strings.
package handler

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	runnerv1 "github.com/hangrix/hangrix/gen/go/hangrix/runner/v1"
)

// ---- Job lifecycle ----

func (h *RunnerConnectHandler) MarkWorkflowJobRunning(
	ctx context.Context,
	req *connect.Request[runnerv1.MarkWorkflowJobRunningRequest],
) (*connect.Response[runnerv1.MarkWorkflowJobRunningResponse], error) {
	runner := runnerFromConnectContext(ctx)
	if runner == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no runner in context"))
	}
	if err := h.workflow.MarkJobRunning(ctx, req.Msg.GetJobRunId(), runner.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&runnerv1.MarkWorkflowJobRunningResponse{}), nil
}

func (h *RunnerConnectHandler) AppendWorkflowJobLog(
	ctx context.Context,
	req *connect.Request[runnerv1.AppendWorkflowJobLogRequest],
) (*connect.Response[runnerv1.AppendWorkflowJobLogResponse], error) {
	streamStr := strings.TrimSpace(req.Msg.GetStream())
	stream := workflowdomain.LogStream(streamStr)
	switch stream {
	case workflowdomain.LogStreamStdout, workflowdomain.LogStreamStderr, workflowdomain.LogStreamSystem:
		// valid
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("stream must be stdout, stderr, or system"))
	}
	// AppendLog takes *string for step_id — empty string means "between
	// steps", which the service stores as NULL.
	var stepID *string
	if s := req.Msg.GetStepId(); s != "" {
		stepID = &s
	}
	if err := h.workflow.AppendLog(ctx, req.Msg.GetJobRunId(), stream, req.Msg.GetLine(), stepID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&runnerv1.AppendWorkflowJobLogResponse{}), nil
}

func (h *RunnerConnectHandler) ReportWorkflowStepResult(
	ctx context.Context,
	req *connect.Request[runnerv1.ReportWorkflowStepResultRequest],
) (*connect.Response[runnerv1.ReportWorkflowStepResultResponse], error) {
	stepID := strings.TrimSpace(req.Msg.GetStepId())
	if stepID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("step_id is required"))
	}
	maskedSet := make(map[string]bool, len(req.Msg.GetMasked()))
	for _, k := range req.Msg.GetMasked() {
		maskedSet[k] = true
	}
	outputs := make(map[string]workflowdomain.StepOutputValue, len(req.Msg.GetOutputs()))
	for k, v := range req.Msg.GetOutputs() {
		outputs[k] = workflowdomain.StepOutputValue{Value: v, Masked: maskedSet[k]}
	}
	if err := h.workflow.SetStepOutputs(ctx, req.Msg.GetJobRunId(), stepID, outputs); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&runnerv1.ReportWorkflowStepResultResponse{}), nil
}

func (h *RunnerConnectHandler) TerminateWorkflowJob(
	ctx context.Context,
	req *connect.Request[runnerv1.TerminateWorkflowJobRequest],
) (*connect.Response[runnerv1.TerminateWorkflowJobResponse], error) {
	status := workflowdomain.JobStatus(strings.TrimSpace(req.Msg.GetStatus()))
	switch status {
	case workflowdomain.JobStatusSuccess, workflowdomain.JobStatusFailed, workflowdomain.JobStatusCancelled:
		// valid
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("status must be success, failed, or cancelled"))
	}
	// MarkJobTerminal takes *int32; we always have an exit code on the
	// wire (proto3 int32 defaults to 0), so pass a pointer through.
	// Distinguishing "0" from "missing" in the legacy JSON API used
	// nullable; the runner client always populated it, so this is
	// behaviourally equivalent.
	exitCode := req.Msg.GetExitCode()
	jobID := req.Msg.GetJobRunId()
	if err := h.workflow.MarkJobTerminal(ctx, jobID, status, &exitCode, req.Msg.GetMessage()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Best-effort: resolve job outputs on success. Failure here doesn't
	// fail the RPC — the job is already terminal; unresolved outputs
	// stay empty in the audit log.
	if status == workflowdomain.JobStatusSuccess {
		_ = h.workflow.ResolveJobOutputs(ctx, jobID)
	}

	// Best-effort: advance the run. Same rationale.
	job, err := h.workflow.GetJobRun(ctx, jobID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	_ = h.workflow.AdvanceRun(ctx, job.WorkflowRunID)

	return connect.NewResponse(&runnerv1.TerminateWorkflowJobResponse{}), nil
}

// ---- Phase lifecycle ----

func (h *RunnerConnectHandler) RegisterWorkflowJobPhase(
	ctx context.Context,
	req *connect.Request[runnerv1.RegisterWorkflowJobPhaseRequest],
) (*connect.Response[runnerv1.RegisterWorkflowJobPhaseResponse], error) {
	phase := workflowdomain.PhaseKind(strings.TrimSpace(req.Msg.GetPhase()))
	imageRef := strings.TrimSpace(req.Msg.GetImageRef())
	if _, err := h.workflow.RegisterPhase(ctx, req.Msg.GetJobRunId(), phase, req.Msg.GetSequenceIndex(), imageRef); err != nil {
		// RegisterPhase's contract maps validation errors (bad phase
		// kind, duplicate sequence) to plain errors; the legacy handler
		// returned 400. Mirror that here as CodeInvalidArgument.
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&runnerv1.RegisterWorkflowJobPhaseResponse{}), nil
}

func (h *RunnerConnectHandler) MarkWorkflowJobPhaseRunning(
	ctx context.Context,
	req *connect.Request[runnerv1.MarkWorkflowJobPhaseRunningRequest],
) (*connect.Response[runnerv1.MarkWorkflowJobPhaseRunningResponse], error) {
	phase := workflowdomain.PhaseKind(strings.TrimSpace(req.Msg.GetPhase()))
	if err := h.workflow.MarkPhaseRunning(ctx, req.Msg.GetJobRunId(), phase); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&runnerv1.MarkWorkflowJobPhaseRunningResponse{}), nil
}

func (h *RunnerConnectHandler) TerminateWorkflowJobPhase(
	ctx context.Context,
	req *connect.Request[runnerv1.TerminateWorkflowJobPhaseRequest],
) (*connect.Response[runnerv1.TerminateWorkflowJobPhaseResponse], error) {
	phase := workflowdomain.PhaseKind(strings.TrimSpace(req.Msg.GetPhase()))
	status := workflowdomain.PhaseStatus(strings.TrimSpace(req.Msg.GetStatus()))
	switch status {
	case workflowdomain.PhaseStatusSuccess, workflowdomain.PhaseStatusFailed, workflowdomain.PhaseStatusSkipped:
		// valid
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("status must be success, failed, or skipped"))
	}
	// `has_exit_code` distinguishes 0 ("succeeded with exit code 0")
	// from "no exit code" ("phase was skipped before running"); the
	// legacy JSON path used a nullable pointer.
	var exitCode *int32
	if req.Msg.GetHasExitCode() {
		v := req.Msg.GetExitCode()
		exitCode = &v
	}
	if err := h.workflow.MarkPhaseTerminal(ctx, req.Msg.GetJobRunId(), phase, status, exitCode, req.Msg.GetMessage()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&runnerv1.TerminateWorkflowJobPhaseResponse{}), nil
}
