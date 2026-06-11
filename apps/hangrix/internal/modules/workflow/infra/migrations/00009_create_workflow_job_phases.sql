-- +goose Up
CREATE TABLE IF NOT EXISTS workflow_job_phases (
    id                  BIGSERIAL PRIMARY KEY,
    workflow_job_run_id BIGINT NOT NULL REFERENCES workflow_job_runs(id) ON DELETE CASCADE,
    phase               TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending',
    sequence_index      INT NOT NULL DEFAULT 0,
    started_at          TIMESTAMPTZ,
    finished_at         TIMESTAMPTZ,
    exit_code           INT,
    error_message       TEXT NOT NULL DEFAULT '',
    image_ref           TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workflow_job_run_id, phase)
);

CREATE INDEX IF NOT EXISTS idx_workflow_job_phases_job_seq
    ON workflow_job_phases (workflow_job_run_id, sequence_index);

-- +goose Down
DROP TABLE IF EXISTS workflow_job_phases;
