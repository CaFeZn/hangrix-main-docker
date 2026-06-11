// Package infra holds the Postgres-backed implementation of the
// repo_silence domain. SQL lives in queries.sql; sqlc generates
// the typed accessors under reposilencedb/.
package infra

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/database"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/infra/reposilencedb"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store implements domain.Store using sqlc-generated queries.
type Store struct {
	q *reposilencedb.Queries
}

// StoreDeps is the ioc dependency struct for Store.
type StoreDeps struct {
	Pool *pgxpool.Pool
}

// NewStore creates a Store and applies migrations.
func NewStore(deps *StoreDeps) *Store {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Errorf("repo_silence migrations sub-fs: %w", err))
	}
	if err := database.Migrate(deps.Pool, sub, "goose_repo_silence", "."); err != nil {
		panic(fmt.Errorf("apply repo_silence migrations: %w", err))
	}
	return &Store{
		q: reposilencedb.New(deps.Pool),
	}
}

// ---- helpers ----

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func tsToPg(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func tsToPgVal(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func i8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func i8ToPg(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

// ---- domain.Store methods ----

// GetState returns the current state row, or (nil, nil) if none exists.
func (s *Store) GetState(ctx context.Context, repoID int64) (*domain.State, error) {
	row, err := s.q.GetState(ctx, repoID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("repo_silence get_state: %w", err)
	}
	return &domain.State{
		RepoID:         row.RepoID,
		Active:         row.Active,
		Source:         row.Source,
		SourceRef:      row.SourceRef,
		EnteredAt:      tsPtr(row.EnteredAt),
		ExpectedExitAt: tsPtr(row.ExpectedExitAt),
		Reason:         row.Reason,
		UpdatedAt:      row.UpdatedAt.Time,
	}, nil
}

// UpdateState performs a CAS update: the row's updated_at must equal
// updatedAtWitness, or the call returns domain.ErrStaleState.
func (s *Store) UpdateState(ctx context.Context, repoID int64, next *domain.State, updatedAtWitness time.Time) error {
	rows, err := s.q.UpdateState(ctx, reposilencedb.UpdateStateParams{
		Active:           next.Active,
		Source:           next.Source,
		SourceRef:        next.SourceRef,
		EnteredAt:        tsToPg(next.EnteredAt),
		ExpectedExitAt:   tsToPg(next.ExpectedExitAt),
		Reason:           next.Reason,
		RepoID:           repoID,
		UpdatedAtWitness: tsToPgVal(updatedAtWitness),
	})
	if err != nil {
		return fmt.Errorf("repo_silence update_state: %w", err)
	}
	if rows == 0 {
		return &domain.ErrStaleState{RepoID: repoID}
	}
	return nil
}

// UpsertState inserts or updates the state row (non-CAS).
func (s *Store) UpsertState(ctx context.Context, repoID int64, active bool, source, sourceRef string, enteredAt, expectedExitAt *time.Time, reason string) error {
	err := s.q.UpsertState(ctx, reposilencedb.UpsertStateParams{
		RepoID:         repoID,
		Active:         active,
		Source:         source,
		SourceRef:      sourceRef,
		EnteredAt:      tsToPg(enteredAt),
		ExpectedExitAt: tsToPg(expectedExitAt),
		Reason:         reason,
	})
	if err != nil {
		return fmt.Errorf("repo_silence upsert_state: %w", err)
	}
	return nil
}

// AppendAudit appends one audit entry.
func (s *Store) AppendAudit(ctx context.Context, entry *domain.AuditEntry) error {
	payload := []byte(entry.Payload)
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	err := s.q.AppendAudit(ctx, reposilencedb.AppendAuditParams{
		RepoID:    entry.RepoID,
		Event:     entry.Event,
		Source:    entry.Source,
		ActorID:   i8ToPg(entry.ActorID),
		SessionID: i8ToPg(entry.SessionID),
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("repo_silence append_audit: %w", err)
	}
	return nil
}

// ListAudit returns the most recent audit entries for a repo.
func (s *Store) ListAudit(ctx context.Context, repoID int64, limit int) ([]*domain.AuditEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.q.ListAudit(ctx, reposilencedb.ListAuditParams{
		RepoID: repoID,
		Limit:  int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("repo_silence list_audit: %w", err)
	}
	out := make([]*domain.AuditEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, &domain.AuditEntry{
			ID:        row.ID,
			RepoID:    row.RepoID,
			Event:     row.Event,
			Source:    row.Source,
			ActorID:   i8Ptr(row.ActorID),
			SessionID: i8Ptr(row.SessionID),
			Payload:   string(row.Payload),
			CreatedAt: row.CreatedAt.Time,
		})
	}
	return out, nil
}

// GrantOverride creates an active override for a session.
func (s *Store) GrantOverride(ctx context.Context, o *domain.Override) error {
	err := s.q.GrantOverride(ctx, reposilencedb.GrantOverrideParams{
		SessionID: o.SessionID,
		RepoID:    o.RepoID,
		GrantedBy: o.GrantedBy,
		Reason:    o.Reason,
		ExpiresAt: tsToPg(o.ExpiresAt),
	})
	if err != nil {
		return fmt.Errorf("repo_silence grant_override: %w", err)
	}
	return nil
}

// RevokeOverride marks an override as revoked.
func (s *Store) RevokeOverride(ctx context.Context, sessionID int64, by int64) error {
	err := s.q.RevokeOverride(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("repo_silence revoke_override: %w", err)
	}
	return nil
}

// ActiveOverride returns the active override for a session.
func (s *Store) ActiveOverride(ctx context.Context, sessionID int64) (*domain.Override, bool, error) {
	row, err := s.q.ActiveOverride(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("repo_silence active_override: %w", err)
	}
	return &domain.Override{
		SessionID: row.SessionID,
		RepoID:    row.RepoID,
		GrantedBy: row.GrantedBy,
		Reason:    row.Reason,
		ExpiresAt: tsPtr(row.ExpiresAt),
		GrantedAt: row.GrantedAt.Time,
		RevokedAt: tsPtr(row.RevokedAt),
	}, true, nil
}

// ListOverrides returns all non-revoked overrides for a repo.
func (s *Store) ListOverrides(ctx context.Context, repoID int64) ([]*domain.Override, error) {
	rows, err := s.q.ListOverrides(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("repo_silence list_overrides: %w", err)
	}
	out := make([]*domain.Override, 0, len(rows))
	for _, row := range rows {
		out = append(out, &domain.Override{
			SessionID: row.SessionID,
			RepoID:    row.RepoID,
			GrantedBy: row.GrantedBy,
			Reason:    row.Reason,
			ExpiresAt: tsPtr(row.ExpiresAt),
			GrantedAt: row.GrantedAt.Time,
			RevokedAt: tsPtr(row.RevokedAt),
		})
	}
	return out, nil
}
