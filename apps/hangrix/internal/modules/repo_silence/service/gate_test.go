package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
)

// stubSilenceStore implements domain.Store with in-memory maps for tests.
type stubSilenceStore struct {
	states    map[int64]*domain.State
	overrides map[int64]*domain.Override
}

func newStubSilenceStore() *stubSilenceStore {
	return &stubSilenceStore{
		states:    map[int64]*domain.State{},
		overrides: map[int64]*domain.Override{},
	}
}

func (s *stubSilenceStore) GetState(_ context.Context, repoID int64) (*domain.State, error) {
	st := s.states[repoID]
	return st, nil
}

func (s *stubSilenceStore) UpdateState(_ context.Context, repoID int64, next *domain.State, _ time.Time) error {
	st := s.states[repoID]
	if st == nil {
		return &domain.ErrStaleState{RepoID: repoID}
	}
	*st = *next
	return nil
}

func (s *stubSilenceStore) UpsertState(_ context.Context, repoID int64, active bool, source, sourceRef string, enteredAt, expectedExitAt *time.Time, reason string) error {
	s.states[repoID] = &domain.State{
		RepoID:         repoID,
		Active:         active,
		Source:         source,
		SourceRef:      sourceRef,
		EnteredAt:      enteredAt,
		ExpectedExitAt: expectedExitAt,
		Reason:         reason,
	}
	return nil
}

func (s *stubSilenceStore) AppendAudit(_ context.Context, entry *domain.AuditEntry) error { return nil }
func (s *stubSilenceStore) ListAudit(_ context.Context, repoID int64, limit int) ([]*domain.AuditEntry, error) {
	return nil, nil
}
func (s *stubSilenceStore) GrantOverride(_ context.Context, o *domain.Override) error {
	s.overrides[o.SessionID] = o
	return nil
}
func (s *stubSilenceStore) RevokeOverride(_ context.Context, sessionID int64, by int64) error {
	delete(s.overrides, sessionID)
	return nil
}
func (s *stubSilenceStore) ActiveOverride(_ context.Context, sessionID int64) (*domain.Override, bool, error) {
	ov, ok := s.overrides[sessionID]
	if !ok || ov == nil {
		return nil, false, nil
	}
	return ov, true, nil
}
func (s *stubSilenceStore) ListOverrides(_ context.Context, repoID int64) ([]*domain.Override, error) {
	return nil, nil
}

func TestGate_CheckSession_WithOverride(t *testing.T) {
	store := newStubSilenceStore()
	gate := NewGate(&GateDeps{Store: store})

	// Set repo as silenced.
	now := time.Now()
	exitAt := now.Add(1 * time.Hour)
	store.states[1] = &domain.State{
		RepoID:         1,
		Active:         true,
		Source:         "manual",
		EnteredAt:      &now,
		ExpectedExitAt: &exitAt,
	}

	// Grant an override for session 100.
	store.overrides[100] = &domain.Override{SessionID: 100, RepoID: 1}

	// Session with override should be allowed.
	err := gate.CheckSession(context.Background(), 100, 1)
	if err != nil {
		t.Fatalf("expected nil (override), got: %v", err)
	}
}

func TestGate_CheckSession_NoOverride_Silenced(t *testing.T) {
	store := newStubSilenceStore()
	gate := NewGate(&GateDeps{Store: store})

	now := time.Now()
	exitAt := now.Add(1 * time.Hour)
	store.states[1] = &domain.State{
		RepoID:         1,
		Active:         true,
		Source:         "manual",
		EnteredAt:      &now,
		ExpectedExitAt: &exitAt,
	}

	// Session without override → should be rejected.
	err := gate.CheckSession(context.Background(), 200, 1)
	if err == nil {
		t.Fatal("expected ErrRepoSilenced, got nil")
	}
	var silenced *domain.ErrRepoSilenced
	if !errors.As(err, &silenced) {
		t.Fatalf("expected *ErrRepoSilenced, got %T: %v", err, err)
	}
	if silenced.RepoID != 1 {
		t.Errorf("RepoSilenced.RepoID = %d, want 1", silenced.RepoID)
	}
	if silenced.ExpectedExitAt == nil || !silenced.ExpectedExitAt.Equal(exitAt) {
		t.Errorf("ExpectedExitAt = %v, want %v", silenced.ExpectedExitAt, exitAt)
	}
}

func TestGate_CheckSession_RepoNotSilenced(t *testing.T) {
	store := newStubSilenceStore()
	gate := NewGate(&GateDeps{Store: store})

	// Repo has no state row → not silenced.
	err := gate.CheckSession(context.Background(), 300, 2)
	if err != nil {
		t.Fatalf("expected nil (no state), got: %v", err)
	}
}

func TestGate_CheckSession_RepoInactive(t *testing.T) {
	store := newStubSilenceStore()
	gate := NewGate(&GateDeps{Store: store})

	// Repo has state but inactive.
	store.states[3] = &domain.State{
		RepoID: 3,
		Active: false,
	}

	err := gate.CheckSession(context.Background(), 400, 3)
	if err != nil {
		t.Fatalf("expected nil (inactive), got: %v", err)
	}
}

func TestGate_CheckRepo_NoState(t *testing.T) {
	store := newStubSilenceStore()
	gate := NewGate(&GateDeps{Store: store})

	err := gate.CheckRepo(context.Background(), 99)
	if err != nil {
		t.Fatalf("expected nil (no state), got: %v", err)
	}
}

func TestGate_CheckRepo_Silenced(t *testing.T) {
	store := newStubSilenceStore()
	gate := NewGate(&GateDeps{Store: store})

	now := time.Now()
	exitAt := now.Add(1 * time.Hour)
	store.states[5] = &domain.State{
		RepoID:         5,
		Active:         true,
		Source:         "schedule",
		SourceRef:      "nightly",
		EnteredAt:      &now,
		ExpectedExitAt: &exitAt,
	}

	err := gate.CheckRepo(context.Background(), 5)
	if err == nil {
		t.Fatal("expected ErrRepoSilenced, got nil")
	}
	var silenced *domain.ErrRepoSilenced
	if !errors.As(err, &silenced) {
		t.Fatalf("expected *ErrRepoSilenced, got %T: %v", err, err)
	}
	if silenced.Source != "schedule" {
		t.Errorf("source = %q, want schedule", silenced.Source)
	}
}
