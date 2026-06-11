package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
)

// Controller implements domain.Controller using the Store for
// persistence and a Gate for read-side checks.
type Controller struct {
	store domain.Store
}

// ControllerDeps is the ioc dependency struct for Controller.
type ControllerDeps struct {
	Store domain.Store
}

// NewController creates a Controller.
func NewController(deps *ControllerDeps) *Controller {
	return &Controller{store: deps.Store}
}

// Enter puts the repo into silence mode. Idempotent: if already active,
// it refreshes source/expected_exit_at and appends an audit entry
// without re-broadcasting suspend.
func (c *Controller) Enter(ctx context.Context, repoID int64, in domain.EnterInput) error {
	now := time.Now()

	state, err := c.store.GetState(ctx, repoID)
	if err != nil {
		return fmt.Errorf("repo_silence enter: %w", err)
	}

	enteredAt := now
	if state != nil && state.Active && state.EnteredAt != nil {
		// Already active — keep the original entered_at so the
		// UI can show "silent since HH:MM".
		enteredAt = *state.EnteredAt
	}

	// Build the next state.
	next := &domain.State{
		RepoID:         repoID,
		Active:         true,
		Source:         in.Source,
		SourceRef:      in.SourceRef,
		EnteredAt:      &enteredAt,
		ExpectedExitAt: in.ExpectedExitAt,
		Reason:         in.Reason,
	}

	if state != nil {
		// CAS update to avoid stomping a concurrent change.
		err = c.store.UpdateState(ctx, repoID, next, state.UpdatedAt)
		if err != nil {
			return fmt.Errorf("repo_silence enter: %w", err)
		}
	} else {
		err = c.store.UpsertState(ctx, repoID, true, in.Source, in.SourceRef,
			&enteredAt, in.ExpectedExitAt, in.Reason)
		if err != nil {
			return fmt.Errorf("repo_silence enter: %w", err)
		}
	}

	// Append audit entry.
	audit := &domain.AuditEntry{
		RepoID:    repoID,
		Event:     domain.AuditEventEntered,
		Source:    in.Source,
		ActorID:   in.ActorID,
		CreatedAt: now,
	}
	if err := c.store.AppendAudit(ctx, audit); err != nil {
		return fmt.Errorf("repo_silence enter audit: %w", err)
	}

	return nil
}

// Exit takes the repo out of silence mode. Idempotent: if already
// inactive, returns nil.
func (c *Controller) Exit(ctx context.Context, repoID int64, in domain.ExitInput) error {
	state, err := c.store.GetState(ctx, repoID)
	if err != nil {
		return fmt.Errorf("repo_silence exit: %w", err)
	}
	if state == nil || !state.Active {
		return nil // already inactive — idempotent
	}

	next := &domain.State{
		RepoID: repoID,
		Active: false,
		Source: in.Source,
		Reason: in.Reason,
	}

	if err := c.store.UpdateState(ctx, repoID, next, state.UpdatedAt); err != nil {
		return fmt.Errorf("repo_silence exit: %w", err)
	}

	audit := &domain.AuditEntry{
		RepoID:    repoID,
		Event:     domain.AuditEventExited,
		Source:    in.Source,
		ActorID:   in.ActorID,
		CreatedAt: time.Now(),
	}
	if err := c.store.AppendAudit(ctx, audit); err != nil {
		return fmt.Errorf("repo_silence exit audit: %w", err)
	}

	return nil
}

// GrantOverride exempts a session from silence.
func (c *Controller) GrantOverride(ctx context.Context, sessionID int64, in domain.OverrideInput) error {
	ov := &domain.Override{
		SessionID: sessionID,
		RepoID:    in.RepoID,
		GrantedBy: in.GrantedBy,
		Reason:    in.Reason,
		ExpiresAt: in.ExpiresAt,
		GrantedAt: time.Now(),
	}

	if err := c.store.GrantOverride(ctx, ov); err != nil {
		return fmt.Errorf("repo_silence grant_override: %w", err)
	}

	audit := &domain.AuditEntry{
		RepoID:    in.RepoID,
		Event:     domain.AuditEventOverrideGranted,
		Source:    domain.SourceManual,
		ActorID:   &in.GrantedBy,
		SessionID: &sessionID,
		CreatedAt: time.Now(),
	}
	if err := c.store.AppendAudit(ctx, audit); err != nil {
		return fmt.Errorf("repo_silence grant_override audit: %w", err)
	}

	return nil
}

// RevokeOverride removes a session's silence exemption.
func (c *Controller) RevokeOverride(ctx context.Context, sessionID int64, by int64) error {
	ov, found, err := c.store.ActiveOverride(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("repo_silence revoke_override: %w", err)
	}
	if !found || ov == nil {
		return nil // already revoked — idempotent
	}

	if err := c.store.RevokeOverride(ctx, sessionID, by); err != nil {
		return fmt.Errorf("repo_silence revoke_override: %w", err)
	}

	audit := &domain.AuditEntry{
		RepoID:    ov.RepoID,
		Event:     domain.AuditEventOverrideRevoked,
		Source:    domain.SourceManual,
		ActorID:   &by,
		SessionID: &sessionID,
		CreatedAt: time.Now(),
	}
	if err := c.store.AppendAudit(ctx, audit); err != nil {
		return fmt.Errorf("repo_silence revoke_override audit: %w", err)
	}

	return nil
}
