// Package domain declares the repository silence system types:
// the SilenceGate cross-cutting seam, the Store persistence
// interface, and the Controller state-machine interface.
//
// The SilenceGate is consumed by every agent-facing surface
// (spawner, LLM proxy, platform API, git push) to check whether
// a repo or session is currently silenced.
package domain

import (
	"context"
	"fmt"
	"time"
)

// SilenceGate is the cross-module seam for the repo-silence check.
// Every agent-facing surface consumes this interface; the
// repo_silence module's service implements it.
type SilenceGate interface {
	// CheckRepo returns ErrRepoSilenced when the repo is silenced.
	// The spawner calls this at OnTrigger entry to decide whether
	// to suppress new spawns/rewakes. Enqueue onto an existing
	// live session is still allowed through a separate path.
	CheckRepo(ctx context.Context, repoID int64) error

	// CheckSession returns ErrRepoSilenced when the repo is silent
	// AND the session has no active override. LLM proxy / platform
	// API / git push handlers call this to enforce silence on
	// session-level calls (returning 423 + Retry-After).
	//
	// sessionID + repoID are both required: the caller extracts
	// repoID from the session it already holds (via token validate),
	// avoiding a cross-module dependency on the runner store.
	CheckSession(ctx context.Context, sessionID, repoID int64) error
}

// ErrRepoSilenced is returned by SilenceGate when the repo is in a
// silence window and the target has no override. It carries structured
// context so consumers can render a consistent 423 / suppressed-action
// envelope.
type ErrRepoSilenced struct {
	RepoID         int64
	Source         string
	EnteredAt      time.Time
	ExpectedExitAt *time.Time // nil = manual silence (no expected exit)
}

func (e *ErrRepoSilenced) Error() string {
	if e.ExpectedExitAt != nil {
		return fmt.Sprintf(
			"Repository %d is in silence mode (source=%s) until %s. Agent actions are paused.",
			e.RepoID, e.Source, e.ExpectedExitAt.Format(time.RFC3339),
		)
	}
	return fmt.Sprintf(
		"Repository %d is in silence mode (source=%s, manual — no expected exit). Agent actions are paused.",
		e.RepoID, e.Source,
	)
}

// State is the current silence state for a repository (one row per repo).
type State struct {
	RepoID         int64
	Active         bool
	Source         string // "manual" / "schedule" / "api"
	SourceRef      string // schedule name or operator username
	EnteredAt      *time.Time
	ExpectedExitAt *time.Time
	Reason         string
	UpdatedAt      time.Time // CAS optimistic-lock column
}

// Source values for State.Source.
const (
	SourceManual   = "manual"
	SourceSchedule = "schedule"
	SourceAPI      = "api"
)

// AuditEntry is one row in the append-only repo_silence_audit table.
type AuditEntry struct {
	ID        int64
	RepoID    int64
	Event     string // "entered" / "exited" / "override_granted" / "override_revoked" / "suspended" / "resumed"
	Source    string
	ActorID   *int64  // users.id soft-ref; nil for schedule-triggered
	SessionID *int64  // session this event relates to
	Payload   string  // JSONB, stored as raw JSON string
	CreatedAt time.Time
}

// Audit event constants.
const (
	AuditEventEntered          = "entered"
	AuditEventExited           = "exited"
	AuditEventOverrideGranted  = "override_granted"
	AuditEventOverrideRevoked  = "override_revoked"
	AuditEventSuspended        = "suspended"
	AuditEventResumed          = "resumed"
)

// Override is a per-session silence exemption.
type Override struct {
	SessionID int64
	RepoID    int64
	GrantedBy int64
	Reason    string
	ExpiresAt *time.Time // nil = until revoked or session ends
	GrantedAt time.Time
	RevokedAt *time.Time // nil = still active
}

// Store is the persistence abstraction for repo_silence tables.
type Store interface {
	// GetState returns the current state for a repo. When no row
	// exists yet, returns (nil, nil) — the caller treats that as
	// "not silenced".
	GetState(ctx context.Context, repoID int64) (*State, error)

	// UpdateState performs a CAS update: the row's updated_at must
	// equal updatedAtWitness, or the call returns ErrStaleState.
	// Use this to prevent manual ↔ schedule mutual stomping.
	UpdateState(ctx context.Context, repoID int64, next *State, updatedAtWitness time.Time) error

	// UpsertState inserts or updates the state row (non-CAS).
	// Used when no concurrent guard is needed (e.g., initial creation).
	UpsertState(ctx context.Context, repoID int64, active bool, source, sourceRef string, enteredAt, expectedExitAt *time.Time, reason string) error

	// AppendAudit appends one audit entry.
	AppendAudit(ctx context.Context, entry *AuditEntry) error

	// ListAudit returns the most recent audit entries for a repo,
	// newest first.
	ListAudit(ctx context.Context, repoID int64, limit int) ([]*AuditEntry, error)

	// GrantOverride creates an active override for a session.
	// Fails if one already exists and is not revoked.
	GrantOverride(ctx context.Context, o *Override) error

	// RevokeOverride marks an override as revoked.
	RevokeOverride(ctx context.Context, sessionID int64, by int64) error

	// ActiveOverride returns the active (non-revoked, non-expired)
	// override for a session. Returns (nil, false, nil) when none.
	ActiveOverride(ctx context.Context, sessionID int64) (*Override, bool, error)

	// ListOverrides returns all non-revoked overrides for a repo.
	ListOverrides(ctx context.Context, repoID int64) ([]*Override, error)
}

// Controller is the state-machine entry point shared by the Web UI
// handler, the scheduler, and any future API consumers.
type Controller interface {
	// Enter puts the repo into silence mode. Idempotent: if already
	// active, it refreshes source/expected_exit_at and appends an
	// audit entry without re-broadcasting suspend.
	Enter(ctx context.Context, repoID int64, in EnterInput) error

	// Exit takes the repo out of silence mode. Idempotent: if already
	// inactive, returns nil.
	Exit(ctx context.Context, repoID int64, in ExitInput) error

	// GrantOverride exempts a session from silence.
	GrantOverride(ctx context.Context, sessionID int64, in OverrideInput) error

	// RevokeOverride removes a session's silence exemption.
	RevokeOverride(ctx context.Context, sessionID int64, by int64) error
}

// EnterInput carries the parameters for Controller.Enter.
type EnterInput struct {
	Source         string    // "manual" / "schedule" / "api"
	SourceRef      string    // schedule name or operator username
	Reason         string    // optional free-text
	ExpectedExitAt *time.Time // nil for manual
	ActorID        *int64     // nil for schedule-triggered
}

// ExitInput carries the parameters for Controller.Exit.
type ExitInput struct {
	Source  string
	Reason  string
	ActorID *int64
}

// OverrideInput carries the parameters for Controller.GrantOverride.
type OverrideInput struct {
	RepoID    int64
	GrantedBy int64
	Reason    string
	ExpiresAt *time.Time
}

// ErrStaleState is returned by Store.UpdateState when the CAS
// updated_at witness does not match the current row.
type ErrStaleState struct {
	RepoID int64
}

func (e *ErrStaleState) Error() string {
	return fmt.Sprintf("stale silence state for repo %d: retry", e.RepoID)
}
