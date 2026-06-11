-- +goose Up
-- Add step_id column to workflow_job_logs so the frontend can group log lines
-- by step. The runner sends the current step_id with each log append call.
ALTER TABLE workflow_job_logs ADD COLUMN step_id VARCHAR(255);

-- Index for efficient per-step log queries (used by the ?step_id= filter).
CREATE INDEX idx_workflow_job_logs_step ON workflow_job_logs (workflow_job_run_id, step_id);

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_job_logs_step;
ALTER TABLE workflow_job_logs DROP COLUMN IF EXISTS step_id;
