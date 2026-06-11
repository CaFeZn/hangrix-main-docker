-- +goose Up
-- The workflow_runs CHECK constraint (last updated in 00008) predates the
-- hidden internal `_agent.wake` event used by the agent-session spawner's
-- workflow cutover. Replace the constraint with one that includes it.
-- Same fix-forward rationale as 00004 and 00008: editing earlier
-- migrations in place doesn't re-run on databases already past them.
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_event_name_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_event_name_check
    CHECK (event_name IN ('repo.push', 'repo.push_tag', 'issue.opened', 'issue.comment', 'workflow.dispatch', 'contribution.push', 'issue.push', '_agent.wake'));

-- +goose Down
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_event_name_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_event_name_check
    CHECK (event_name IN ('repo.push', 'repo.push_tag', 'issue.opened', 'issue.comment', 'workflow.dispatch', 'contribution.push', 'issue.push'));
