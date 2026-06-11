package service

import (
	"context"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
)

// stubSchedulerController implements domain.Controller with call recording.
type stubSchedulerController struct {
	enterCalls []struct {
		repoID int64
		in     domain.EnterInput
	}
	exitCalls []struct {
		repoID int64
		in     domain.ExitInput
	}
}

func (c *stubSchedulerController) Enter(_ context.Context, repoID int64, in domain.EnterInput) error {
	c.enterCalls = append(c.enterCalls, struct {
		repoID int64
		in     domain.EnterInput
	}{repoID, in})
	return nil
}

func (c *stubSchedulerController) Exit(_ context.Context, repoID int64, in domain.ExitInput) error {
	c.exitCalls = append(c.exitCalls, struct {
		repoID int64
		in     domain.ExitInput
	}{repoID, in})
	return nil
}

func (c *stubSchedulerController) GrantOverride(_ context.Context, _ int64, _ domain.OverrideInput) error {
	return nil
}
func (c *stubSchedulerController) RevokeOverride(_ context.Context, _ int64, _ int64) error {
	return nil
}

func TestCronKey(t *testing.T) {
	tests := []struct {
		repoID int64
		name   string
		want   string
	}{
		{1, "nightly", "1:nightly"},
		{42, "weekend-maintenance", "42:weekend-maintenance"},
		{0, "", "0:"},
	}
	for _, tt := range tests {
		got := cronKey(tt.repoID, tt.name)
		if got != tt.want {
			t.Errorf("cronKey(%d, %q) = %q, want %q", tt.repoID, tt.name, got, tt.want)
		}
	}
}

func TestScheduler_processEnter_firstSeen(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{
		controller:   ctrl,
		lastCronFire: make(map[string]time.Time),
	}

	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	sch := agentsconfig.SilenceSchedule{
		Name:     "nightly",
		Cron:     "0 22 * * *", // runs at 22:00 — already past today
		Duration: "8h",
		Timezone: "UTC",
	}

	// First call: schedule never seen → should NOT fire (first-seen suppression).
	s.processEnter(context.Background(), 1, sch, now)

	if len(ctrl.enterCalls) != 0 {
		t.Fatalf("expected 0 enter calls (first-seen suppression), got %d: %+v",
			len(ctrl.enterCalls), ctrl.enterCalls)
	}

	// Verify the fire time was recorded.
	key := cronKey(1, "nightly")
	s.mu.Lock()
	fireTime, seen := s.lastCronFire[key]
	s.mu.Unlock()
	if !seen {
		t.Fatal("expected lastCronFire entry to exist after first-seen")
	}
	if !fireTime.Equal(now) {
		t.Fatalf("lastCronFire[%q] = %v, want %v", key, fireTime, now)
	}
}

func TestScheduler_processEnter_notYetFired(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{
		controller:   ctrl,
		lastCronFire: make(map[string]time.Time),
	}

	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	sch := agentsconfig.SilenceSchedule{
		Name:     "nightly",
		Cron:     "0 22 * * *", // runs at 22:00 — in the future
		Duration: "8h",
		Timezone: "UTC",
	}

	key := cronKey(1, "nightly")
	s.mu.Lock()
	s.lastCronFire[key] = time.Date(2026, 5, 28, 22, 0, 0, 0, time.UTC) // last fire was yesterday
	s.mu.Unlock()

	// The next fire after reference (yesterday 22:00) is today 22:00.
	// Today is 10:00, so the next fire hasn't happened yet.
	s.processEnter(context.Background(), 1, sch, now)

	if len(ctrl.enterCalls) != 0 {
		t.Fatalf("expected 0 enter calls (next fire hasn't happened yet), got %d: %+v",
			len(ctrl.enterCalls), ctrl.enterCalls)
	}
}

func TestScheduler_processEnter_shouldFire(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{
		controller:   ctrl,
		lastCronFire: make(map[string]time.Time),
	}

	// We use a cron that fires every minute; reference was 2 minutes ago, so
	// it should have fired since then.
	now := time.Date(2026, 5, 29, 10, 5, 0, 0, time.UTC)
	sch := agentsconfig.SilenceSchedule{
		Name:     "frequent",
		Cron:     "* * * * *", // every minute
		Duration: "30m",
		Timezone: "UTC",
	}

	key := cronKey(1, "frequent")
	s.mu.Lock()
	s.lastCronFire[key] = time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC) // 5 min ago
	s.mu.Unlock()

	s.processEnter(context.Background(), 1, sch, now)

	if len(ctrl.enterCalls) != 1 {
		t.Fatalf("expected 1 enter call, got %d: %+v", len(ctrl.enterCalls), ctrl.enterCalls)
	}

	call := ctrl.enterCalls[0]
	if call.repoID != 1 {
		t.Errorf("enter repoID = %d, want 1", call.repoID)
	}
	if call.in.Source != domain.SourceSchedule {
		t.Errorf("enter Source = %q, want %q", call.in.Source, domain.SourceSchedule)
	}
	if call.in.SourceRef != "frequent" {
		t.Errorf("enter SourceRef = %q, want %q", call.in.SourceRef, "frequent")
	}
	if call.in.ExpectedExitAt == nil {
		t.Fatal("ExpectedExitAt is nil, want non-nil")
	}
	expectedExit := now.Add(30 * time.Minute)
	if !call.in.ExpectedExitAt.Equal(expectedExit) {
		t.Errorf("ExpectedExitAt = %v, want %v", *call.in.ExpectedExitAt, expectedExit)
	}

	// Verify that lastCronFire was updated.
	s.mu.Lock()
	fireTime, seen := s.lastCronFire[key]
	s.mu.Unlock()
	if !seen || !fireTime.Equal(now) {
		t.Fatalf("lastCronFire[%q] = %v (seen=%v), want %v", key, fireTime, seen, now)
	}
}

func TestScheduler_processEnter_badCron(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{
		controller:   ctrl,
		lastCronFire: make(map[string]time.Time),
	}

	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	sch := agentsconfig.SilenceSchedule{
		Name:     "broken",
		Cron:     "invalid-cron",
		Duration: "1h",
		Timezone: "UTC",
	}

	// Set a prior fire so we don't hit first-seen.
	key := cronKey(1, "broken")
	s.mu.Lock()
	s.lastCronFire[key] = time.Date(2026, 5, 29, 9, 0, 0, 0, time.UTC)
	s.mu.Unlock()

	s.processEnter(context.Background(), 1, sch, now)

	// Bad cron → no enter call, just log.
	if len(ctrl.enterCalls) != 0 {
		t.Fatalf("expected 0 enter calls (bad cron), got %d", len(ctrl.enterCalls))
	}
}

func TestScheduler_processEnter_badTimezone(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{
		controller:   ctrl,
		lastCronFire: make(map[string]time.Time),
	}

	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	sch := agentsconfig.SilenceSchedule{
		Name:     "tz-broken",
		Cron:     "* * * * *",
		Duration: "1h",
		Timezone: "Mars/Midnight", // doesn't exist
	}

	key := cronKey(1, "tz-broken")
	s.mu.Lock()
	s.lastCronFire[key] = time.Date(2026, 5, 29, 9, 0, 0, 0, time.UTC)
	s.mu.Unlock()

	s.processEnter(context.Background(), 1, sch, now)

	if len(ctrl.enterCalls) != 0 {
		t.Fatalf("expected 0 enter calls (bad timezone), got %d", len(ctrl.enterCalls))
	}
}

func TestScheduler_processEnter_badDuration(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{
		controller:   ctrl,
		lastCronFire: make(map[string]time.Time),
	}

	now := time.Date(2026, 5, 29, 10, 5, 0, 0, time.UTC)
	sch := agentsconfig.SilenceSchedule{
		Name:     "bad-dur",
		Cron:     "* * * * *",
		Duration: "not-a-duration",
		Timezone: "UTC",
	}

	key := cronKey(1, "bad-dur")
	s.mu.Lock()
	s.lastCronFire[key] = time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	s.mu.Unlock()

	s.processEnter(context.Background(), 1, sch, now)

	if len(ctrl.enterCalls) != 0 {
		t.Fatalf("expected 0 enter calls (bad duration), got %d", len(ctrl.enterCalls))
	}
}

func TestScheduler_processExit_noState(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{controller: ctrl}
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)

	// nil state → no exit.
	s.processExit(context.Background(), 1, nil, now)
	if len(ctrl.exitCalls) != 0 {
		t.Fatalf("expected 0 exit calls (nil state), got %d", len(ctrl.exitCalls))
	}
}

func TestScheduler_processExit_inactive(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{controller: ctrl}
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)

	state := &domain.State{RepoID: 1, Active: false}
	s.processExit(context.Background(), 1, state, now)
	if len(ctrl.exitCalls) != 0 {
		t.Fatalf("expected 0 exit calls (inactive), got %d", len(ctrl.exitCalls))
	}
}

func TestScheduler_processExit_nonScheduleSource(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{controller: ctrl}
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)

	// Manual silence → scheduler should NOT auto-exit.
	exitAt := now.Add(1 * time.Hour)
	state := &domain.State{
		RepoID:         1,
		Active:         true,
		Source:         domain.SourceManual,
		ExpectedExitAt: &exitAt,
	}
	s.processExit(context.Background(), 1, state, now)
	if len(ctrl.exitCalls) != 0 {
		t.Fatalf("expected 0 exit calls (manual source), got %d", len(ctrl.exitCalls))
	}
}

func TestScheduler_processExit_notYetExpired(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{controller: ctrl}

	// Expected exit is 1 hour in the future → not yet time.
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	exitAt := now.Add(1 * time.Hour)
	state := &domain.State{
		RepoID:         1,
		Active:         true,
		Source:         domain.SourceSchedule,
		SourceRef:      "nightly",
		ExpectedExitAt: &exitAt,
	}
	s.processExit(context.Background(), 1, state, now)
	if len(ctrl.exitCalls) != 0 {
		t.Fatalf("expected 0 exit calls (not expired yet), got %d", len(ctrl.exitCalls))
	}
}

func TestScheduler_processExit_shouldExit(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{controller: ctrl}

	// Expected exit was 5 minutes ago → should exit.
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	exitAt := time.Date(2026, 5, 29, 9, 55, 0, 0, time.UTC)
	state := &domain.State{
		RepoID:         1,
		Active:         true,
		Source:         domain.SourceSchedule,
		SourceRef:      "nightly",
		ExpectedExitAt: &exitAt,
	}
	s.processExit(context.Background(), 1, state, now)

	if len(ctrl.exitCalls) != 1 {
		t.Fatalf("expected 1 exit call, got %d: %+v", len(ctrl.exitCalls), ctrl.exitCalls)
	}
	call := ctrl.exitCalls[0]
	if call.repoID != 1 {
		t.Errorf("exit repoID = %d, want 1", call.repoID)
	}
	if call.in.Source != domain.SourceSchedule {
		t.Errorf("exit Source = %q, want %q", call.in.Source, domain.SourceSchedule)
	}
}

func TestScheduler_processExit_nilExpectedExit(t *testing.T) {
	ctrl := &stubSchedulerController{}
	s := &Scheduler{controller: ctrl}
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)

	// Schedule-driven silence with nil ExpectedExitAt → skip (shouldn't
	// happen in practice, but guard against it).
	state := &domain.State{
		RepoID:         1,
		Active:         true,
		Source:         domain.SourceSchedule,
		SourceRef:      "adhoc",
		ExpectedExitAt: nil,
	}
	s.processExit(context.Background(), 1, state, now)
	if len(ctrl.exitCalls) != 0 {
		t.Fatalf("expected 0 exit calls (nil ExpectedExitAt), got %d", len(ctrl.exitCalls))
	}
}
