// Package service implements the repo_silence domain interfaces:
// SilenceGate and Controller.
package service

import (
	"context"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
)

// Gate implements domain.SilenceGate by consulting the Store.
type Gate struct {
	store domain.Store
}

// GateDeps is the ioc dependency struct for Gate.
type GateDeps struct {
	Store domain.Store
}

// NewGate creates a Gate.
func NewGate(deps *GateDeps) *Gate {
	return &Gate{store: deps.Store}
}

// CheckRepo returns domain.ErrRepoSilenced when the repo is in an
// active silence window. nil means the repo is not silenced and
// normal agent operations are allowed.
func (g *Gate) CheckRepo(ctx context.Context, repoID int64) error {
	state, err := g.store.GetState(ctx, repoID)
	if err != nil {
		return err
	}
	if state == nil || !state.Active {
		return nil
	}
	return &domain.ErrRepoSilenced{
		RepoID:         repoID,
		Source:         state.Source,
		EnteredAt:      ptrTime(state.EnteredAt),
		ExpectedExitAt: state.ExpectedExitAt,
	}
}

// CheckSession returns domain.ErrRepoSilenced when the repo is silent
// and the session has no active override. nil means the session is
// allowed to operate. The caller provides both sessionID and repoID
// — repoID is always available because the caller already resolved the
// session token and holds the *runnerdomain.AgentSession.
func (g *Gate) CheckSession(ctx context.Context, sessionID, repoID int64) error {
	// Look up the active override first — if one exists, allow.
	ov, found, err := g.store.ActiveOverride(ctx, sessionID)
	if err != nil {
		return err
	}
	if found && ov != nil {
		return nil // override active — allow
	}

	// No override — check repo state.
	return g.CheckRepo(ctx, repoID)
}

func ptrTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
