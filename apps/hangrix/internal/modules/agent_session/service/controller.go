package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// Controller is the user-facing session-lifecycle service: Stop,
// Resume, Delete, Reset. Separate from Spawner because the call sites
// are different — Spawner reacts to upstream events, Controller reacts
// to UI buttons — but the actual rewake mechanics (token preservation,
// cause-frame enqueue, workflow_run dispatch) live in Spawner and
// Controller delegates to it through OnTrigger.
type Controller struct {
	runner  runnerdomain.Repo
	spawner domain.Spawner
}

type ControllerDeps struct {
	Runner  runnerdomain.Repo
	Spawner domain.Spawner
}

func NewController(deps *ControllerDeps) *Controller {
	return &Controller{runner: deps.Runner, spawner: deps.Spawner}
}

// Stop satisfies domain.Controller.
//
// Flow:
//  1. Look up the session — 404 if missing.
//  2. If already terminal/archived, return nil (idempotent: UI may
//     click stop on a session that just exited on its own).
//  3. Enqueue a control:shutdown frame so a running container exits
//     cleanly when it next polls /inputs. Failure is logged on the
//     enqueue path but doesn't block the mark — worst case the
//     container keeps running until it hits an idle gap.
//  4. Mark the session 'failed' with an explanation message so the
//     audit row records who asked for the stop.
func (c *Controller) Stop(ctx context.Context, sessionID int64, reason string) error {
	sess, err := c.runner.GetSessionByID(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.Status.Terminal() || sess.Status == runnerdomain.SessionStatusArchived {
		return nil
	}
	frame, _ := json.Marshal(map[string]any{
		"kind": "control",
		"op":   "shutdown",
	})
	if _, err := c.runner.EnqueueInput(ctx, sessionID, frame); err != nil {
		// Non-fatal: the container will eventually be killed when the
		// orchestrator notices the session is failed (a later
		// milestone). For now we keep going so the session shows up
		// as failed in the UI even if enqueue raced.
	}
	msg := reason
	if msg == "" {
		msg = "stopped by user"
	}
	if err := c.runner.MarkSessionTerminal(ctx, sessionID, runnerdomain.SessionStatusFailed, nil, msg); err != nil {
		if errors.Is(err, runnerdomain.ErrSessionStateInvalid) {
			// Race with the runner's own terminate: session went
			// terminal between the GetSessionByID check and our
			// mark. Treat as success — caller's intent is met.
			return nil
		}
		return err
	}
	// Record the stop event on the message log so the audit timeline
	// reflects the manual cancellation.
	_, _ = c.runner.AppendMessage(ctx, &runnerdomain.Message{
		SessionID: sessionID,
		Kind:      runnerdomain.MessageKindSystem,
		Content:   msg,
	})
	return nil
}

// Resume satisfies domain.Controller. Routes a "resume this session"
// click through the same path an upstream event would take —
// spawner.OnTrigger, which finds the (repo, issue, role) row and runs
// rewakeRole. rewakeRole flips the row out of its non-live status,
// preserves the session token identity when possible, enqueues the
// cause frame, and dispatches the _agent workflow_run that boots a
// fresh container. Before this delegation, Resume just flipped the
// row to 'pending' and stopped — leaving the agent never restarted
// post-cutover (the runner no longer claims agent sessions; a
// workflow_run is what actually starts the container).
//
// Used by the web UI resume button.
func (c *Controller) Resume(ctx context.Context, sessionID int64) error {
	return c.resume(ctx, sessionID, fmt.Sprintf("resume:%d", sessionID), "resumed by user")
}

// Recover satisfies domain.Controller. Same path as Resume; the
// `recover:<actor>:<session>` cause_id distinguishes agent-initiated
// recovery from a UI resume in the audit timeline.
func (c *Controller) Recover(ctx context.Context, sessionID int64, recoveredBy string) error {
	return c.resume(ctx, sessionID, fmt.Sprintf("recover:%s:%d", recoveredBy, sessionID), "")
}

// resume is the shared implementation for Resume and Recover.
func (c *Controller) resume(ctx context.Context, sessionID int64, causeID, msg string) error {
	sess, err := c.runner.GetSessionByID(ctx, sessionID)
	if err != nil {
		return err
	}
	// Archived rows can never come back — the parent issue archived
	// them, a new issue is required to start fresh. Live rows (pending /
	// claimed / running) are surfaced as ErrNotResumable too: the agent
	// is already in flight, so "resume" is a no-op the UI should turn
	// into a 409 rather than silently dropping the click.
	switch sess.Status {
	case runnerdomain.SessionStatusArchived,
		runnerdomain.SessionStatusPending,
		runnerdomain.SessionStatusClaimed,
		runnerdomain.SessionStatusRunning:
		return domain.ErrNotResumable
	}
	// Defensive — sessions outside the admin smoke path always carry
	// repo_id + issue_number + role_key. A corrupted row would otherwise
	// trigger a confusing OnTrigger no-op far from the resume call site.
	if sess.RepoID == nil || sess.IssueNumber == nil || sess.RoleKey == "" {
		return fmt.Errorf("resume: session %d missing repo_id / issue_number / role_key", sessionID)
	}

	if msg != "" {
		_, _ = c.runner.AppendMessage(ctx, &runnerdomain.Message{
			SessionID: sessionID,
			Kind:      runnerdomain.MessageKindSystem,
			Content:   msg,
		})
	}

	spawned, err := c.spawner.OnTrigger(ctx, domain.TriggerInput{
		Trigger:     agentsconfig.TriggerManual,
		CauseKind:   domain.CauseKindManual,
		CauseID:     causeID,
		RepoID:      *sess.RepoID,
		IssueNumber: *sess.IssueNumber,
		RoleKey:     sess.RoleKey,
		ActorID:     sess.CreatedBy,
	})
	if err != nil {
		return fmt.Errorf("resume dispatch: %w", err)
	}
	// OnTrigger returned no spawns: the host yaml may have dropped the
	// role between the original spawn and now, or the trigger map
	// excluded TriggerManual for this role. Surface a clear error
	// instead of a silent "click did nothing".
	if len(spawned) == 0 {
		return fmt.Errorf("resume: session %d not eligible — role %q may be missing from host yaml or its trigger map excludes manual", sessionID, sess.RoleKey)
	}
	return nil
}

// Delete satisfies domain.Controller. Refuses live sessions to keep
// runner-side state coherent: a runner that just claimed the row would
// 500 on its next AppendMessage if we deleted from under it.
//
// Container-aware: when the session owns a long-lived container (see
// migration 00004), hard-DELETE would strand the container — the
// runner's cleanup poll keys off agent_sessions.runner_id, and a deleted
// row has nothing to match. We instead archive the row (so the user
// sees it leave their active list) and flag the container for cleanup;
// the runner's sweeper picks it up on its next poll and `docker rm`s.
// A future commit can add a "purge archived" sweeper to hard-DELETE
// these rows once the container is gone — for now they stay archived,
// which is cheap (zero non-row state) and audit-friendly.
func (c *Controller) Delete(ctx context.Context, sessionID int64) error {
	sess, err := c.runner.GetSessionByID(ctx, sessionID)
	if err != nil {
		return err
	}
	switch sess.Status {
	case runnerdomain.SessionStatusPending,
		runnerdomain.SessionStatusClaimed,
		runnerdomain.SessionStatusRunning:
		return domain.ErrSessionLive
	}
	if sess.ContainerID != "" {
		return c.runner.ArchiveSessionByID(ctx, sessionID)
	}
	return c.runner.DeleteSession(ctx, sessionID)
}

// StopContainerNow satisfies domain.Controller. Flags the session's
// container for an immediate docker stop by the owning runner.
func (c *Controller) StopContainerNow(ctx context.Context, sessionID int64) error {
	return c.runner.FlagSessionContainerStop(ctx, sessionID)
}

// RemoveContainerNow satisfies domain.Controller. Flags the session's
// container for an immediate docker rm by the owning runner.
func (c *Controller) RemoveContainerNow(ctx context.Context, sessionID int64) error {
	return c.runner.FlagSessionContainerCleanup(ctx, sessionID)
}

// Reset satisfies domain.Controller. Archives the session and spawns a
// brand-new session row for the same (repo, issue, role) tuple.
func (c *Controller) Reset(ctx context.Context, sessionID int64) (int64, error) {
	sess, err := c.runner.GetSessionByID(ctx, sessionID)
	if err != nil {
		return 0, err
	}

	// 1. Status gate.
	switch sess.Status {
	case runnerdomain.SessionStatusArchived:
		return 0, domain.ErrNotResettable
	case runnerdomain.SessionStatusPending, runnerdomain.SessionStatusClaimed:
		return 0, domain.ErrSessionLive
	}

	// 2. If running — shut down cleanly first, same sequence as Stop.
	if sess.Status == runnerdomain.SessionStatusRunning {
		frame, _ := json.Marshal(map[string]any{
			"kind": "control",
			"op":   "shutdown",
		})
		// Best-effort enqueue; failure doesn't block the reset.
		_, _ = c.runner.EnqueueInput(ctx, sessionID, frame)

		reason := "reset by user"
		if err := c.runner.MarkSessionTerminal(ctx, sessionID, runnerdomain.SessionStatusFailed, nil, reason); err != nil &&
			!errors.Is(err, runnerdomain.ErrSessionStateInvalid) {
			return 0, err
		}
		_, _ = c.runner.AppendMessage(ctx, &runnerdomain.Message{
			SessionID: sessionID,
			Kind:      runnerdomain.MessageKindSystem,
			Content:   reason,
		})
	}

	// 3. Archive the old row.
	if err := c.runner.ArchiveSessionByID(ctx, sessionID); err != nil {
		return 0, err
	}

	// 4. Spawn a fresh row via the spawner — direct-invoke path.
	// Defensive: RepoID / IssueNumber are non-nil for real sessions but a
	// corrupted row would panic on deref; surface a clear error instead.
	if sess.RepoID == nil || sess.IssueNumber == nil {
		return 0, fmt.Errorf("reset: session %d is missing repo_id or issue_number", sessionID)
	}
	spawned, err := c.spawner.OnTrigger(ctx, domain.TriggerInput{
		Trigger:     agentsconfig.TriggerManual,
		CauseKind:   domain.CauseKindManual,
		CauseID:     fmt.Sprintf("reset:%d", sessionID),
		RepoID:      *sess.RepoID,
		IssueNumber: *sess.IssueNumber,
		RoleKey:     sess.RoleKey,
		ActorID:     sess.CreatedBy,
		Payload:     []byte(`{"reason":"user_reset"}`),
	})
	if err != nil {
		return 0, err
	}
	if len(spawned) != 1 {
		return 0, fmt.Errorf("reset: expected 1 spawned session, got %d", len(spawned))
	}

	// 5. Record a system message on the new session linking back to the old.
	_, _ = c.runner.AppendMessage(ctx, &runnerdomain.Message{
		SessionID: spawned[0].SessionID,
		Kind:      runnerdomain.MessageKindSystem,
		Content:   fmt.Sprintf("session reset from #%d by user", sessionID),
	})

	return spawned[0].SessionID, nil
}

var _ domain.Controller = (*Controller)(nil)
