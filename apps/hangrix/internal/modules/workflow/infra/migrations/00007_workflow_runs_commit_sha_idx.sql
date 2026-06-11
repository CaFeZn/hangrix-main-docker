-- +goose Up
CREATE INDEX IF NOT EXISTS idx_workflow_runs_repo_commit
    ON workflow_runs(repo_id, commit_sha);

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_runs_repo_commit;
