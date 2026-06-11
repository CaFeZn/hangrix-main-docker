package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	automationdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/automation/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/server"
)

// scannerIntervalDefault is the fallback scan interval. Every 60 s is
// fine for hour-aligned schedule windows; repos that define sub-minute
// cron expressions will still round to the nearest scan tick.
const scannerIntervalDefault = 60 * time.Second

// cronKey builds a "repoID:scheduleName" key for the lastCronFire map.
func cronKey(repoID int64, scheduleName string) string {
	return fmt.Sprintf("%d:%s", repoID, scheduleName)
}

// Scheduler is a BackgroundJob that scans every repo on a ticker, reads
// .hangrix/agents.yml from the repo's default branch, parses the
// silence.schedules block, and enters/exits silence via the Controller
// when the cron-driven windows open and close.
//
// Each schedule defines a 5-field cron expression (robfig/cron), a
// duration, and an optional IANA timezone. The scheduler computes the
// next cron trigger time and calls Controller.Enter when it has elapsed
// since the last recorded fire. It monitors the current silence state
// and calls Controller.Exit when a schedule's expected_exit_at has
// passed and no manual / API override is active.
type Scheduler struct {
	controller domain.Controller
	store      domain.Store
	lister     automationdomain.RepoLister
	pathRes    repodomain.PathResolver
	interval   time.Duration

	mu           sync.Mutex
	lastCronFire map[string]time.Time
}

// SchedulerDeps wires the Scheduler's dependencies through ioc.
type SchedulerDeps struct {
	Controller domain.Controller
	Store      domain.Store
	Lister     automationdomain.RepoLister
	PathRes    repodomain.PathResolver
}

// NewScheduler returns a ready-to-use background Scheduler.
func NewScheduler(deps *SchedulerDeps) *Scheduler {
	return &Scheduler{
		controller:   deps.Controller,
		store:        deps.Store,
		lister:       deps.Lister,
		pathRes:      deps.PathRes,
		interval:     scannerIntervalDefault,
		lastCronFire: make(map[string]time.Time),
	}
}

// Start runs the scan loop on a ticker. It does one immediate scan on
// startup so a restart doesn't introduce a full-tick delay.
func (s *Scheduler) Start(ctx context.Context) {
	s.sync(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sync(ctx)
		}
	}
}

// sync lists all repos and processes each one for silence schedules.
func (s *Scheduler) sync(ctx context.Context) {
	repos, err := s.lister.ListAll(ctx)
	if err != nil {
		log.Printf("repo_silence scheduler: list repos: %v", err)
		return
	}
	for _, repo := range repos {
		s.processRepo(ctx, repo)
	}
}

// processRepo reads a single repo's agents.yml, extracts silence
// schedules, and enters/exits silence as appropriate.
func (s *Scheduler) processRepo(ctx context.Context, repo automationdomain.RepoRef) {
	fsPath, err := s.pathRes.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		return
	}

	// Read .hangrix/agents.yml from the repo's default branch.
	raw, ok := readBlob(ctx, fsPath, repo.DefaultBranch, ".hangrix/agents.yml")
	if !ok {
		return
	}

	cfg, err := agentsconfig.ParseHostConfig(raw)
	if err != nil {
		log.Printf("repo_silence scheduler: repo %d parse agents.yml: %v", repo.ID, err)
		return
	}
	if cfg.Silence == nil || len(cfg.Silence.Schedules) == 0 {
		return
	}

	// Get current silence state from the store.
	state, err := s.store.GetState(ctx, repo.ID)
	if err != nil {
		log.Printf("repo_silence scheduler: repo %d get state: %v", repo.ID, err)
		return
	}

	now := time.Now()

	// Process each schedule: check if the cron should have fired.
	for _, sch := range cfg.Silence.Schedules {
		s.processEnter(ctx, repo.ID, sch, now)
	}

	// Check if we should exit a schedule-based silence.
	s.processExit(ctx, repo.ID, state, now)
}

// processEnter checks whether a single schedule's cron window has
// opened since the last recorded fire. If so, it calls Controller.Enter.
func (s *Scheduler) processEnter(ctx context.Context, repoID int64, sch agentsconfig.SilenceSchedule, now time.Time) {
	// Parse the cron expression.
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(sch.Cron)
	if err != nil {
		log.Printf("repo_silence scheduler: repo %d schedule %q bad cron %q: %v", repoID, sch.Name, sch.Cron, err)
		return
	}

	// Apply the schedule's timezone by setting Location on the parsed
	// SpecSchedule. robfig/cron v3 always returns *SpecSchedule from
	// the standard parser.
	loc, err := time.LoadLocation(sch.Timezone)
	if err != nil {
		log.Printf("repo_silence scheduler: repo %d schedule %q bad timezone %q: %v", repoID, sch.Name, sch.Timezone, err)
		return
	}
	if spec, ok := sched.(*cron.SpecSchedule); ok {
		spec.Location = loc
	}

	// Determine the reference time for "next cron trigger".
	key := cronKey(repoID, sch.Name)
	s.mu.Lock()
	refTime, seen := s.lastCronFire[key]
	if !seen {
		// First time seeing this schedule — set the reference to
		// now so it won't fire retroactively and will instead fire
		// on its next natural cron tick.
		s.lastCronFire[key] = now
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Compute the next scheduled time after the reference time.
	nextTime := sched.Next(refTime)

	// If the next scheduled time hasn't happened yet, skip.
	if nextTime.After(now) {
		return
	}

	// Parse the duration string to compute expected exit.
	dur, err := time.ParseDuration(sch.Duration)
	if err != nil {
		log.Printf("repo_silence scheduler: repo %d schedule %q bad duration %q: %v", repoID, sch.Name, sch.Duration, err)
		return
	}

	expectedExit := now.Add(dur)
	in := domain.EnterInput{
		Source:         domain.SourceSchedule,
		SourceRef:      sch.Name,
		Reason:         fmt.Sprintf("schedule %q triggered by cron %s", sch.Name, sch.Cron),
		ExpectedExitAt: &expectedExit,
	}

	if err := s.controller.Enter(ctx, repoID, in); err != nil {
		log.Printf("repo_silence scheduler: repo %d schedule %q enter: %v", repoID, sch.Name, err)
		return
	}

	log.Printf("repo_silence scheduler: entered silence for repo %d (schedule=%q, duration=%s, exit≈%s)",
		repoID, sch.Name, sch.Duration, expectedExit.Format(time.RFC3339))

	// Record the fire time so we don't re-fire on the next scan.
	s.mu.Lock()
	s.lastCronFire[key] = now
	s.mu.Unlock()
}

// processExit checks whether the current schedule-based silence should
// be exited (expected_exit_at has passed). It does NOT exit manual or
// API-driven silences — those are managed by the operator.
func (s *Scheduler) processExit(ctx context.Context, repoID int64, state *domain.State, now time.Time) {
	if state == nil || !state.Active {
		return
	}
	// Only auto-exit schedule-driven silences.
	if state.Source != domain.SourceSchedule {
		return
	}
	// No expected exit means the schedule didn't set one — skip.
	if state.ExpectedExitAt == nil {
		return
	}
	// Not yet time to exit.
	if state.ExpectedExitAt.After(now) {
		return
	}

	in := domain.ExitInput{
		Source: domain.SourceSchedule,
		Reason: fmt.Sprintf("schedule %q duration expired at %s", state.SourceRef, state.ExpectedExitAt.Format(time.RFC3339)),
	}

	if err := s.controller.Exit(ctx, repoID, in); err != nil {
		log.Printf("repo_silence scheduler: repo %d exit: %v", repoID, err)
		return
	}

	log.Printf("repo_silence scheduler: exited silence for repo %d (schedule=%q)", repoID, state.SourceRef)
}

// readBlob reads a file at ref:path from a bare repo. Returns (content, true)
// on success, (nil, false) when the file doesn't exist or can't be read.
func readBlob(ctx context.Context, repoFsPath, ref, path string) ([]byte, bool) {
	cmd := exec.CommandContext(ctx,
		"git",
		"--git-dir="+repoFsPath,
		"cat-file",
		"-p",
		ref+":"+path,
	)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

// compile-time check
var _ server.BackgroundJob = (*Scheduler)(nil)
