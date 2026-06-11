package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/agent_session/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// stubSpawner implements domain.Spawner for controller tests.
// Only OnTrigger is exercised; the rest panic.
type stubSpawner struct {
	// spawnResult is the session the spawner returns on the next
	// OnTrigger call.
	spawnResult *domain.SpawnedSession
	spawnErr    error
	// lastInput captures the TriggerInput from the last OnTrigger call.
	lastInput domain.TriggerInput
}

func (s *stubSpawner) OnTrigger(_ context.Context, in domain.TriggerInput) ([]domain.SpawnedSession, error) {
	s.lastInput = in
	if s.spawnErr != nil {
		return nil, s.spawnErr
	}
	if s.spawnResult == nil {
		return nil, nil
	}
	return []domain.SpawnedSession{*s.spawnResult}, nil
}

func (s *stubSpawner) LoadHostConfig(context.Context, int64) (*agentsconfig.HostConfig, error) {
	panic("LoadHostConfig not stubbed")
}

var _ domain.Spawner = (*stubSpawner)(nil)

// newTestController wires a Controller with a stub runner repo + stub
// spawner. Resume/Recover delegate to the spawner; tests assert on the
// captured TriggerInput rather than on session-row mutations (which
// only happen inside the real spawner.rewakeRole).
func newTestController(t *testing.T) (*Controller, *stubRunnerRepo, *stubSpawner) {
	t.Helper()
	runner := newStubRunnerRepo()
	sp := &stubSpawner{spawnResult: &domain.SpawnedSession{SessionID: 9000, RoleKey: "server", Action: domain.SpawnActionRewoken}}
	ctrl := NewController(&ControllerDeps{Runner: runner, Spawner: sp})
	return ctrl, runner, sp
}

// seedSession is a helper that inserts one session row into the stub repo
// with the given status and sealed value.
func seedSession(r *stubRunnerRepo, status runnerdomain.SessionStatus, sealed string) *runnerdomain.AgentSession {
	sess := &runnerdomain.AgentSession{
		ID:                 r.nextID,
		RunnerID:           nil,
		RepoID:             intPtr(1),
		IssueNumber:        int32Ptr(42),
		Status:             status,
		Role:               "server",
		RoleKey:            "server",
		Model:              "claude-sonnet-4-6",
		SessionTokenPrefix: "hgxs_aaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SessionTokenHash:   "$2a$10$...existing...",
		SessionTokenSealed: sealed,
	}
	r.nextID++
	r.sessions = append(r.sessions, sess)
	return sess
}

func intPtr(v int64) *int64   { return &v }
func int32Ptr(v int32) *int32 { return &v }

// ---- tests ----

// TestControllerResumeDelegatesToSpawner verifies Resume hands off the
// rewake work to spawner.OnTrigger with a TriggerInput pointing at the
// session's own (repo, issue, role). The actual session-row mutations
// (token preservation, status flip, cause-frame enqueue) live inside
// the real spawner.rewakeRole; the Controller's responsibility is
// limited to validating preconditions and dispatching.
func TestControllerResumeDelegatesToSpawner(t *testing.T) {
	ctrl, runner, sp := newTestController(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusFailed, "enc:old-sealed-plaintext")

	if err := ctrl.Resume(ctx, sess.ID); err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	in := sp.lastInput
	if in.Trigger != agentsconfig.TriggerManual {
		t.Errorf("trigger = %q, want manual", in.Trigger)
	}
	if in.CauseKind != domain.CauseKindManual {
		t.Errorf("cause_kind = %q, want manual", in.CauseKind)
	}
	if in.RepoID != *sess.RepoID {
		t.Errorf("repo_id = %d, want %d", in.RepoID, *sess.RepoID)
	}
	if in.IssueNumber != *sess.IssueNumber {
		t.Errorf("issue_number = %d, want %d", in.IssueNumber, *sess.IssueNumber)
	}
	if in.RoleKey != sess.RoleKey {
		t.Errorf("role_key = %q, want %q", in.RoleKey, sess.RoleKey)
	}
	if in.CauseID == "" {
		t.Errorf("cause_id should be non-empty so OnTrigger's dedup map can't false-positive")
	}
}

// TestControllerResumeOnArchivedReturnsError asserts archived sessions
// cannot be resumed.
func TestControllerResumeOnArchivedReturnsError(t *testing.T) {
	ctrl, runner, _ := newTestController(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusArchived, "enc:sealed")

	err := ctrl.Resume(ctx, sess.ID)
	if err != domain.ErrNotResumable {
		t.Fatalf("expected ErrNotResumable, got %v", err)
	}
}

// TestControllerResumeOnLiveReturnsError asserts live (pending/claimed/
// running) sessions cannot be resumed.
func TestControllerResumeOnLiveReturnsError(t *testing.T) {
	ctx := context.Background()

	for _, status := range []runnerdomain.SessionStatus{
		runnerdomain.SessionStatusPending,
		runnerdomain.SessionStatusClaimed,
		runnerdomain.SessionStatusRunning,
	} {
		// fresh repo for each to avoid duplicate id panics
		ctrl2, runner2, _ := newTestController(t)
		sess := seedSession(runner2, status, "enc:sealed")
		err := ctrl2.Resume(ctx, sess.ID)
		if err != domain.ErrNotResumable {
			t.Errorf("status %q: expected ErrNotResumable, got %v", status, err)
		}
	}
}

// TestControllerRecoverDelegatesToSpawner mirrors the Resume test; the
// only observable difference is the cause_id prefix ("recover:" vs
// "resume:") so an audit can tell which entry point fired.
func TestControllerRecoverDelegatesToSpawner(t *testing.T) {
	ctrl, runner, sp := newTestController(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusFailed, "enc:sealed-recover")

	if err := ctrl.Recover(ctx, sess.ID, "server"); err != nil {
		t.Fatalf("Recover returned error: %v", err)
	}
	in := sp.lastInput
	if in.Trigger != agentsconfig.TriggerManual {
		t.Errorf("trigger = %q, want manual", in.Trigger)
	}
	if in.RoleKey != sess.RoleKey {
		t.Errorf("role_key = %q, want %q", in.RoleKey, sess.RoleKey)
	}
	if !strings.HasPrefix(in.CauseID, "recover:") {
		t.Errorf("cause_id = %q, want a recover:* prefix", in.CauseID)
	}
}

// newTestControllerPair is like newTestController but also returns the
// stubSpawner so tests can inspect spawn-call parameters.
func newTestControllerPair(t *testing.T) (*Controller, *stubRunnerRepo, *stubSpawner) {
	t.Helper()
	runner := newStubRunnerRepo()
	sp := &stubSpawner{spawnResult: &domain.SpawnedSession{SessionID: 9000, RoleKey: "server", Action: domain.SpawnActionSpawned}}
	ctrl := NewController(&ControllerDeps{Runner: runner, Spawner: sp})
	return ctrl, runner, sp
}

// ---- Reset tests ----

// TestControllerResetOnArchived returns ErrNotResettable.
func TestControllerResetOnArchived(t *testing.T) {
	ctrl, runner, _ := newTestControllerPair(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusArchived, "enc:sealed")

	_, err := ctrl.Reset(ctx, sess.ID)
	if !errors.Is(err, domain.ErrNotResettable) {
		t.Fatalf("expected ErrNotResettable, got %v", err)
	}
}

// TestControllerResetOnLive returns ErrSessionLive for pending / claimed.
func TestControllerResetOnLive(t *testing.T) {
	ctx := context.Background()

	for _, status := range []runnerdomain.SessionStatus{
		runnerdomain.SessionStatusPending,
		runnerdomain.SessionStatusClaimed,
	} {
		ctrl, runner, _ := newTestControllerPair(t)
		sess := seedSession(runner, status, "enc:sealed")
		_, err := ctrl.Reset(ctx, sess.ID)
		if !errors.Is(err, domain.ErrSessionLive) {
			t.Errorf("status %q: expected ErrSessionLive, got %v", status, err)
		}
	}
}

// TestControllerResetOnTerminal archives the old row and spawns a new one.
// Covers idle, succeeded, cancelled, failed.
func TestControllerResetOnTerminal(t *testing.T) {
	ctx := context.Background()

	for _, status := range []runnerdomain.SessionStatus{
		runnerdomain.SessionStatusIdle,
		runnerdomain.SessionStatusSucceeded,
		runnerdomain.SessionStatusCancelled,
		runnerdomain.SessionStatusFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			ctrl, runner, sp := newTestControllerPair(t)
			sess := seedSession(runner, status, "enc:sealed")
			// Give the session a container so we assert cleanup_pending after archive.
			sess.ContainerID = "container-abc"

			newID, err := ctrl.Reset(ctx, sess.ID)
			if err != nil {
				t.Fatalf("Reset returned error: %v", err)
			}
			if newID != 9000 {
				t.Errorf("new session id = %d, want 9000", newID)
			}

			// Old row is archived.
			old := runner.sessions[0]
			if old.Status != runnerdomain.SessionStatusArchived {
				t.Errorf("old row status = %q, want archived", old.Status)
			}
			if !old.ContainerCleanupPending {
				t.Error("old row container_cleanup_pending should be true")
			}

			// Spawner was called with correct params.
			in := sp.lastInput
			if in.RoleKey != "server" {
				t.Errorf("spawner RoleKey = %q, want server", in.RoleKey)
			}
			if in.CauseKind != domain.CauseKindManual {
				t.Errorf("spawner CauseKind = %q, want manual", in.CauseKind)
			}
			if in.CauseID != fmt.Sprintf("reset:%d", sess.ID) {
				t.Errorf("spawner CauseID = %q, want reset:%d", in.CauseID, sess.ID)
			}
			if in.RepoID != 1 {
				t.Errorf("spawner RepoID = %d, want 1", in.RepoID)
			}

			// New row got a system message.
			found := false
			for _, m := range runner.messages {
				if m.SessionID == 9000 && m.Kind == runnerdomain.MessageKindSystem {
					found = true
					break
				}
			}
			if !found {
				t.Error("no system message on new session")
			}
		})
	}
}

// TestControllerResetOnRunning stops the container first, then archives,
// then spawns.
func TestControllerResetOnRunning(t *testing.T) {
	ctrl, runner, sp := newTestControllerPair(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusRunning, "enc:sealed")
	sess.ContainerID = "container-running"

	newID, err := ctrl.Reset(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}
	if newID != 9000 {
		t.Errorf("new session id = %d, want 9000", newID)
	}

	// Old row was first marked failed, then archived.
	// check it's now archived.
	old := runner.sessions[0]
	if old.Status != runnerdomain.SessionStatusArchived {
		t.Errorf("old row status = %q, want archived", old.Status)
	}
	if old.ErrorMessage != "reset by user" {
		t.Errorf("old row error_message = %q, want 'reset by user'", old.ErrorMessage)
	}

	// A shutdown frame was enqueued.
	foundShutdown := false
	for _, in := range runner.inputs {
		if string(in.Payload) == `{"kind":"control","op":"shutdown"}` {
			foundShutdown = true
			break
		}
	}
	if !foundShutdown {
		t.Error("no control:shutdown frame enqueued for running session")
	}

	// Spawner was called.
	if sp.lastInput.RoleKey != "server" {
		t.Errorf("spawner RoleKey = %q, want server", sp.lastInput.RoleKey)
	}
}

// TestControllerResetIdentityIsolation verifies the new session is
// fully independent of the old one.
func TestControllerResetIdentityIsolation(t *testing.T) {
	ctrl, runner, _ := newTestControllerPair(t)
	ctx := context.Background()
	sess := seedSession(runner, runnerdomain.SessionStatusFailed, "enc:sealed")
	sess.ContainerID = "container-old"

	// Inject a message on the old session to verify it survives archive.
	runner.messages = append(runner.messages, &runnerdomain.Message{
		ID:        1,
		SessionID: sess.ID,
		Seq:       1,
		Kind:      runnerdomain.MessageKindMessage,
		Content:   "old session message",
	})

	newID, err := ctrl.Reset(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	// newID != oldID.
	if newID == sess.ID {
		t.Errorf("new session id %d == old session id %d", newID, sess.ID)
	}

	// Old row has container_cleanup_pending = true.
	old := runner.sessions[0]
	if !old.ContainerCleanupPending {
		t.Error("old row container_cleanup_pending should be true")
	}

	// Old messages are preserved.
	oldMsgs := 0
	newMsgs := 0
	for _, m := range runner.messages {
		if m.SessionID == sess.ID {
			oldMsgs++
		}
		if m.SessionID == newID {
			newMsgs++
		}
	}
	if oldMsgs != 1 {
		t.Errorf("old session messages = %d, want 1 (preserved)", oldMsgs)
	}
	// New session has exactly 1 message: the "session reset from #X" system message.
	if newMsgs != 1 {
		t.Errorf("new session messages = %d, want 1 (system message only)", newMsgs)
	}
}
