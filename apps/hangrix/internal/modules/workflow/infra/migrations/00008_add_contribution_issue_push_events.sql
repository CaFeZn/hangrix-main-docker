-- +goose Up
-- The workflow_runs CHECK constraint (last updated in 00004) predates the
-- contribution.push and issue.push events and never listed them, so every
-- workflow run triggered by these events fails with a 23514 check violation.
-- Replace the constraint with one that includes both new event names.
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_event_name_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_event_name_check
    CHECK (event_name IN ('repo.push', 'repo.push_tag', 'issue.opened', 'issue.comment', 'workflow.dispatch', 'contribution.push', 'issue.push'));

-- +goose Down
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_event_name_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_event_name_check
    CHECK (event_name IN ('repo.push', 'repo.push_tag', 'issue.opened', 'issue.comment', 'workflow.dispatch'));
