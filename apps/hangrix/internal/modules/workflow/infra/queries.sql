-- Workflow module queries.
-- Naming convention: sqlc.arg('name') for named parameters.

-- ---- workflow_runs ----

-- name: CreateWorkflowRun :one
INSERT INTO workflow_runs (
    repo_id, workflow_name, source_file, status, event_name,
    cause_id, ref, commit_sha, container_snapshot_json, trigger_payload_json,
    workflow_token,
    trigger_actor_kind, trigger_actor_user_id, trigger_actor_role_key,
    trigger_actor_workflow_run_id, trigger_actor_display_name,
    run_actor_kind, run_actor_user_id, run_actor_role_key,
    run_actor_workflow_run_id, run_actor_display_name
) VALUES (
    sqlc.arg('repo_id'), sqlc.arg('workflow_name'), sqlc.arg('source_file'),
    'pending', sqlc.arg('event_name'),
    sqlc.narg('cause_id'), sqlc.arg('ref'), sqlc.arg('commit_sha'),
    sqlc.narg('container_snapshot_json'), sqlc.narg('trigger_payload_json'),
    sqlc.arg('workflow_token'),
    sqlc.arg('trigger_actor_kind'), sqlc.narg('trigger_actor_user_id'), sqlc.arg('trigger_actor_role_key'),
    sqlc.narg('trigger_actor_workflow_run_id'), sqlc.arg('trigger_actor_display_name'),
    sqlc.arg('run_actor_kind'), sqlc.narg('run_actor_user_id'), sqlc.arg('run_actor_role_key'),
    sqlc.narg('run_actor_workflow_run_id'), sqlc.arg('run_actor_display_name')
) RETURNING *;

-- name: GetWorkflowRunByToken :one
SELECT id, repo_id, workflow_name, status FROM workflow_runs
WHERE workflow_token = sqlc.arg('token') AND workflow_token <> '';

-- name: GetWorkflowRun :one
SELECT * FROM workflow_runs WHERE id = sqlc.arg('id');

-- name: ListWorkflowRunsByRepo :many
-- User-facing list: excludes the hidden `_agent` workflow (agent-session
-- spawner runs). Callers that need internal rows use a dedicated query.
SELECT *, COUNT(*) OVER() AS total_count
FROM workflow_runs
WHERE repo_id = sqlc.arg('repo_id')
  AND workflow_name <> '_agent'
  AND (sqlc.arg('workflow_name') = '' OR workflow_name = sqlc.arg('workflow_name'))
  AND (sqlc.arg('status') = '' OR status = sqlc.arg('status'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: ListWorkflowRunsByRepoAndCommitSHA :many
-- User-facing CI status check: same internal filter as ListWorkflowRunsByRepo.
SELECT * FROM workflow_runs
WHERE repo_id = sqlc.arg('repo_id') AND commit_sha = sqlc.arg('commit_sha')
  AND workflow_name <> '_agent'
ORDER BY created_at DESC;

-- name: ListAgentWorkflowRunsByRepo :many
-- Internal list: only the hidden `_agent` workflow (agent-session spawner runs).
SELECT *, COUNT(*) OVER() AS total_count
FROM workflow_runs
WHERE repo_id = sqlc.arg('repo_id')
  AND workflow_name = '_agent'
  AND (sqlc.arg('status') = '' OR status = sqlc.arg('status'))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: MarkWorkflowRunStarted :exec
UPDATE workflow_runs
SET status = 'running', started_at = NOW()
WHERE id = sqlc.arg('id') AND status = 'pending';

-- name: MarkWorkflowRunTerminal :exec
UPDATE workflow_runs
SET status = sqlc.arg('status'), finished_at = NOW()
WHERE id = sqlc.arg('id') AND status IN ('pending', 'running');

-- name: SetWorkflowRunActor :exec
UPDATE workflow_runs
SET run_actor_kind = sqlc.arg('run_actor_kind'),
    run_actor_user_id = sqlc.narg('run_actor_user_id'),
    run_actor_role_key = sqlc.arg('run_actor_role_key'),
    run_actor_workflow_run_id = sqlc.narg('run_actor_workflow_run_id'),
    run_actor_display_name = sqlc.arg('run_actor_display_name')
WHERE id = sqlc.arg('id');

-- ---- workflow_job_runs ----

-- name: CreateWorkflowJobRun :one
INSERT INTO workflow_job_runs (
    workflow_run_id, job_key, display_name, status, sequence_index,
    working_directory, timeout_minutes, env_json, steps_json,
    job_outputs_raw_json
) VALUES (
    sqlc.arg('workflow_run_id'), sqlc.arg('job_key'), sqlc.arg('display_name'),
    'pending', sqlc.arg('sequence_index'),
    sqlc.arg('working_directory'), sqlc.arg('timeout_minutes'),
    sqlc.narg('env_json'), sqlc.narg('steps_json'),
    sqlc.narg('job_outputs_raw_json')
) RETURNING *;

-- name: GetWorkflowJobRun :one
SELECT * FROM workflow_job_runs WHERE id = sqlc.arg('id');

-- name: ListWorkflowJobRunsByRun :many
SELECT * FROM workflow_job_runs
WHERE workflow_run_id = sqlc.arg('workflow_run_id')
ORDER BY sequence_index ASC;

-- name: ClaimNextWorkflowJob :one
-- Only claim a job if no earlier-sequence job in the same run is still
-- pending or running. This preserves the sequential execution guarantee:
-- jobs within a workflow run execute one at a time in sequence order.
-- DEPRECATED: kept for backward-compat; new code uses ClaimNextWorkflowJobs.
UPDATE workflow_job_runs
SET status = 'running', runner_id = sqlc.arg('runner_id'), started_at = NOW()
WHERE id = (
    SELECT j.id FROM workflow_job_runs j
    WHERE j.status = 'pending'
      AND NOT EXISTS (
        SELECT 1 FROM workflow_job_runs j2
        WHERE j2.workflow_run_id = j.workflow_run_id
          AND j2.sequence_index < j.sequence_index
          AND j2.status NOT IN ('success', 'skipped', 'failed', 'cancelled')
      )
    ORDER BY j.sequence_index ASC, j.created_at ASC, j.id ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ClaimNextWorkflowJobs :many
-- Claims up to sqlc.arg('limit') pending jobs for a runner, preserving
-- the sequential-execution guarantee: jobs within the same workflow_run
-- execute one at a time in sequence order. The NOT EXISTS subquery
-- ensures no earlier-sequence job in the same run is still pending or
-- running, so multi-job batches naturally span different workflow_runs.
-- FOR UPDATE SKIP LOCKED keeps concurrent runners from colliding.
UPDATE workflow_job_runs
SET status = 'running', runner_id = sqlc.arg('runner_id'), started_at = NOW()
WHERE id = ANY(ARRAY(
    SELECT j.id FROM workflow_job_runs j
    WHERE j.status = 'pending'
      AND NOT EXISTS (
        SELECT 1 FROM workflow_job_runs j2
        WHERE j2.workflow_run_id = j.workflow_run_id
          AND j2.sequence_index < j.sequence_index
          AND j2.status NOT IN ('success', 'skipped', 'failed', 'cancelled')
      )
    ORDER BY j.sequence_index ASC, j.created_at ASC, j.id ASC
    LIMIT sqlc.arg('limit')
    FOR UPDATE SKIP LOCKED
))
RETURNING *;

-- name: MarkWorkflowJobRunning :exec
UPDATE workflow_job_runs
SET status = 'running', runner_id = sqlc.arg('runner_id'), started_at = NOW()
WHERE id = sqlc.arg('id');

-- name: MarkWorkflowJobTerminal :exec
UPDATE workflow_job_runs
SET status = sqlc.arg('status'),
    exit_code = sqlc.narg('exit_code'),
    error_message = sqlc.arg('error_message'),
    finished_at = NOW()
WHERE id = sqlc.arg('id');

-- name: SkipRemainingWorkflowJobs :exec
UPDATE workflow_job_runs
SET status = 'skipped', finished_at = NOW()
WHERE workflow_run_id = sqlc.arg('workflow_run_id')
  AND status = 'pending'
  AND sequence_index > sqlc.arg('after_sequence_index');

-- name: CancelRunningWorkflowJobs :exec
UPDATE workflow_job_runs
SET status = 'cancelled', finished_at = NOW()
WHERE workflow_run_id = sqlc.arg('workflow_run_id')
  AND status = 'running';

-- name: SetWorkflowJobContainer :exec
UPDATE workflow_job_runs
SET container_id = sqlc.arg('container_id')
WHERE id = sqlc.arg('id');

-- ---- workflow_job_logs ----

-- name: AppendWorkflowJobLog :exec
INSERT INTO workflow_job_logs (workflow_job_run_id, stream, line, step_id)
VALUES (sqlc.arg('workflow_job_run_id'), sqlc.arg('stream'), sqlc.arg('line'), sqlc.narg('step_id'));

-- name: ListWorkflowJobLogs :many
SELECT *, COUNT(*) OVER() AS total_count
FROM workflow_job_logs
WHERE workflow_job_run_id = sqlc.arg('workflow_job_run_id')
  AND (sqlc.narg('step_id')::TEXT IS NULL OR step_id = sqlc.narg('step_id'))
  AND (sqlc.narg('since_id')::INT8 IS NULL OR id > sqlc.narg('since_id'))
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- ---- step and job outputs ----

-- name: SetWorkflowJobStepOutputs :exec
-- Merge a step's outputs into the job's step_outputs_json column.
-- Uses jsonb_set with concatenation to merge the step-level map.
-- step_id is cast to text because jsonb_build_object takes "any"-typed
-- arguments, so a bare parameter leaves Postgres unable to infer the type
-- (SQLSTATE 42P18: could not determine data type of parameter $1).
UPDATE workflow_job_runs
SET step_outputs_json =
    COALESCE(step_outputs_json, '{}'::jsonb) ||
    jsonb_build_object(sqlc.arg('step_id')::text, sqlc.arg('outputs_json')::jsonb)
WHERE id = sqlc.arg('id');

-- name: SetWorkflowJobOutputs :exec
-- Write resolved job outputs after job completion.
UPDATE workflow_job_runs
SET job_outputs_json = sqlc.arg('outputs_json')::jsonb
WHERE id = sqlc.arg('id');

-- ---- workflow_job_phases ----

-- name: CreateWorkflowJobPhase :one
-- Insert a phase row; ON CONFLICT DO NOTHING makes it idempotent.
-- Returns the row (either newly inserted or existing).
INSERT INTO workflow_job_phases (
    workflow_job_run_id, phase, status, sequence_index, image_ref
) VALUES (
    sqlc.arg('workflow_job_run_id'), sqlc.arg('phase'),
    'pending', sqlc.arg('sequence_index'), sqlc.arg('image_ref')
)
ON CONFLICT (workflow_job_run_id, phase) DO NOTHING
RETURNING *;

-- name: GetWorkflowJobPhase :one
SELECT * FROM workflow_job_phases
WHERE workflow_job_run_id = sqlc.arg('workflow_job_run_id')
  AND phase = sqlc.arg('phase');

-- name: MarkWorkflowJobPhaseRunning :exec
UPDATE workflow_job_phases
SET status = 'running', started_at = NOW()
WHERE workflow_job_run_id = sqlc.arg('workflow_job_run_id')
  AND phase = sqlc.arg('phase')
  AND status = 'pending';

-- name: MarkWorkflowJobPhaseTerminal :exec
UPDATE workflow_job_phases
SET status = sqlc.arg('status'),
    exit_code = sqlc.narg('exit_code'),
    error_message = sqlc.arg('error_message'),
    finished_at = NOW()
WHERE workflow_job_run_id = sqlc.arg('workflow_job_run_id')
  AND phase = sqlc.arg('phase');

-- name: ListWorkflowJobPhases :many
SELECT * FROM workflow_job_phases
WHERE workflow_job_run_id = sqlc.arg('workflow_job_run_id')
ORDER BY sequence_index ASC;
