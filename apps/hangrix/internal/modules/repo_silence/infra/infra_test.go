package infra

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
)

// testPool connects to the local Postgres (already running in the
// agent container). See .hangrix/knowledge/sqlc-and-migrations.md
// for credentials. DSN_TEST_PG overrides the default for environments
// where Postgres is reachable on a host other than localhost (e.g. the
// dev container's `postgres` service alias).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DSN_TEST_PG")
	if dsn == "" {
		dsn = "postgres://hangrix:hangrix@localhost:5432/hangrix"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	// Clean any leftovers from previous runs, including the goose
	// version table so migrations re-apply fresh.
	pool.Exec(ctx, `DROP TABLE IF EXISTS repo_silence_overrides`)
	pool.Exec(ctx, `DROP TABLE IF EXISTS repo_silence_audit`)
	pool.Exec(ctx, `DROP TABLE IF EXISTS repo_silence_state`)
	pool.Exec(ctx, `DROP TABLE IF EXISTS goose_repo_silence`)
	return pool
}

func TestStore_UpsertAndGetState(t *testing.T) {
	pool := testPool(t)
	store := NewStore(&StoreDeps{Pool: pool})
	ctx := context.Background()

	// Initially no state.
	st, err := store.GetState(ctx, 1)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if st != nil {
		t.Fatal("expected nil state for new repo")
	}

	// Upsert.
	entered := time.Now().Truncate(time.Microsecond)
	expectedExit := entered.Add(1 * time.Hour)
	err = store.UpsertState(ctx, 1, true, "manual", "alice", &entered, &expectedExit, "testing")
	if err != nil {
		t.Fatalf("UpsertState: %v", err)
	}

	st, err = store.GetState(ctx, 1)
	if err != nil {
		t.Fatalf("GetState after upsert: %v", err)
	}
	if st == nil {
		t.Fatal("expected non-nil state")
	}
	if !st.Active {
		t.Fatal("expected active=true")
	}
	if st.Source != "manual" {
		t.Fatalf("source = %q, want manual", st.Source)
	}
	if st.SourceRef != "alice" {
		t.Fatalf("source_ref = %q, want alice", st.SourceRef)
	}
}

func TestStore_CASUpdate(t *testing.T) {
	pool := testPool(t)
	store := NewStore(&StoreDeps{Pool: pool})
	ctx := context.Background()

	entered := time.Now().Truncate(time.Microsecond)
	err := store.UpsertState(ctx, 2, true, "manual", "bob", &entered, nil, "")
	if err != nil {
		t.Fatalf("UpsertState: %v", err)
	}

	st, err := store.GetState(ctx, 2)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}

	// Successful CAS.
	next := &domain.State{
		RepoID:    2,
		Active:    true,
		Source:    "schedule",
		SourceRef: "nightly",
		Reason:    "updated",
	}
	err = store.UpdateState(ctx, 2, next, st.UpdatedAt)
	if err != nil {
		t.Fatalf("UpdateState CAS: %v", err)
	}

	// Stale CAS — use the OLD updated_at witness.
	staleErr := store.UpdateState(ctx, 2, next, st.UpdatedAt)
	if staleErr == nil {
		t.Fatal("expected stale state error")
	}
	var ss *domain.ErrStaleState
	if !errors.As(staleErr, &ss) {
		t.Fatalf("expected *ErrStaleState, got %T: %v", staleErr, staleErr)
	}
}

func TestStore_Audit(t *testing.T) {
	pool := testPool(t)
	store := NewStore(&StoreDeps{Pool: pool})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		err := store.AppendAudit(ctx, &domain.AuditEntry{
			RepoID: 3,
			Event:  "entered",
			Source: "manual",
		})
		if err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}

	entries, err := store.ListAudit(ctx, 3, 2)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (limit)", len(entries))
	}
}

func TestStore_Overrides(t *testing.T) {
	pool := testPool(t)
	store := NewStore(&StoreDeps{Pool: pool})
	ctx := context.Background()

	// Grant.
	ov := &domain.Override{
		SessionID: 100,
		RepoID:    4,
		GrantedBy: 1,
		Reason:    "critical",
	}
	err := store.GrantOverride(ctx, ov)
	if err != nil {
		t.Fatalf("GrantOverride: %v", err)
	}

	// Check active.
	got, found, err := store.ActiveOverride(ctx, 100)
	if err != nil {
		t.Fatalf("ActiveOverride: %v", err)
	}
	if !found || got == nil {
		t.Fatal("expected active override")
	}
	if got.Reason != "critical" {
		t.Fatalf("reason = %q", got.Reason)
	}

	// Revoke.
	err = store.RevokeOverride(ctx, 100, 1)
	if err != nil {
		t.Fatalf("RevokeOverride: %v", err)
	}

	// Now not found.
	_, found, err = store.ActiveOverride(ctx, 100)
	if err != nil {
		t.Fatalf("ActiveOverride after revoke: %v", err)
	}
	if found {
		t.Fatal("expected override not found after revoke")
	}

	// List overrides for repo.
	ov2 := &domain.Override{
		SessionID: 101,
		RepoID:    4,
		GrantedBy: 1,
		Reason:    "urgent",
	}
	_ = store.GrantOverride(ctx, ov2)

	list, err := store.ListOverrides(ctx, 4)
	if err != nil {
		t.Fatalf("ListOverrides: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d overrides, want 1", len(list))
	}
}
