-- +goose Up
-- Add source_issue_id to issue_comments to structurally record which issue a
-- cross-posted comment originated from. Only set for cross-issue comments;
-- NULL for regular comments on the current issue.
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS source_issue_id INTEGER;

-- +goose Down
ALTER TABLE issue_comments DROP COLUMN IF EXISTS source_issue_id;
