package agentsconfig

import (
	"testing"
)

func TestIsValidTrigger(t *testing.T) {
	t.Parallel()

	valid := []string{
		"issue.opened",
		"issue.closed",
		"issue.comment",
		"commit.pushed",
		"review_vote.posted",
		"ci.status_changed",
		"sub_issue.merged",
		"sub_issue.closed",
	}
	for _, s := range valid {
		if !IsValidTrigger(s) {
			t.Fatalf("expected %q valid", s)
		}
	}

	invalid := []string{
		"",
		"issue",
		"Issue.Opened",
		"unknown.event",
		"issue.comment.any",       // removed — replaced by issue.comment + filter
		"issue.comment.mentioned", // removed — replaced by mentioned_only filter
		"sub_issue.merged.foo",    // typo of valid trigger
		"sub_issue.closed.bar",    // typo of valid trigger
	}
	for _, s := range invalid {
		if IsValidTrigger(s) {
			t.Fatalf("expected %q invalid", s)
		}
	}
}
