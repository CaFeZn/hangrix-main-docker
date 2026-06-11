package loop

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

// Loop is the outer "what the runner does forever" object. It owns the
// heartbeat ticker and the task-poll workers; per-job work is delegated
// to WorkflowJobDriver. One Loop per process.
//
// Agent sessions used to run through a separate SessionDriver that
// piped stdin/stdout JSONL to the container. That path is gone — the
// spawner now dispatches every wake as a hidden `_agent.wake` workflow
// run, so agent containers come up through the same workflow_job code
// path as user CI jobs. The agent talks to /api/agent/sessions/{id}/*
// directly; the runner just starts and stops the container.
type Loop struct {
	Client       *client.Client
	Orchestrator orchestrator.Orchestrator

	// AgentBinaryPath is the full host-side path to the extracted
	// hangrix-agent binary. WorkflowJobDriver uses filepath.Dir of it
	// as the source for the reserved `hangrix-agent` bind-mount volume.
	AgentBinaryPath string
	WorkspaceRoot   string

	BaseURL string

	HeartbeatEvery time.Duration

	// Parallelism is the max number of workflow jobs this runner will
	// drive concurrently. <=0 falls back to 1 — defensive only; the CLI
	// config layer defaults to 16 long before reaching here. Each unit
	// runs an independent /tasks long-poller + job driver. The DB
	// claim is FOR UPDATE SKIP LOCKED so workers never collide on the
	// same row.
	Parallelism int
}

// Run blocks until ctx is cancelled. Internally:
//
//   - one heartbeat goroutine (period = HeartbeatEvery).
//   - one cleanup-sweeper goroutine.
//   - one dispatcher goroutine that single-polls /tasks with batch-drain
//     logic, feeding jobs into a channel.
//   - N worker goroutines (Parallelism), each fed from the jobs channel
//     and returning an idle token (slot) when done.
//
// The invariant: len(slots) + active workers + undelivered tasks == Parallelism.
func (l *Loop) Run(ctx context.Context) error {
	if l.HeartbeatEvery <= 0 {
		l.HeartbeatEvery = 20 * time.Second
	}
	p := l.Parallelism
	if p < 1 {
		p = 1
	}

	// Heartbeat goroutine.
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		l.heartbeatLoop(ctx, p)
	}()

	// Cleanup-sweeper goroutine.
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		sw := &CleanupSweeper{Client: l.Client, Orchestrator: l.Orchestrator}
		if err := sw.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("cleanup sweeper exited: %v", err)
		}
	}()

	// Channels.
	slots := make(chan struct{}, p)
	jobs := make(chan *client.Task, p)
	for i := 0; i < p; i++ {
		slots <- struct{}{}
	}

	var wg sync.WaitGroup

	// Worker pool.
	agentBinDir := filepath.Dir(l.AgentBinaryPath)
	for i := 0; i < p; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("worker %d panicked: %v", workerID, r)
				}
			}()
			for {
				select {
				case <-ctx.Done():
					return
				case task, ok := <-jobs:
					if !ok {
						return
					}
					l.runJob(ctx, workerID, task, agentBinDir)
					select {
					case slots <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
			}
		}(i)
	}

	// Dispatcher goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(jobs)
		l.dispatcherLoop(ctx, slots, jobs, p)
	}()

	wg.Wait()
	<-hbDone
	<-cleanupDone

	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return ctx.Err()
}

// runJob drives a single workflow job, recovering from panics.
func (l *Loop) runJob(ctx context.Context, workerID int, task *client.Task, agentBinDir string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("worker %d: panicked on job %d: %v", workerID, task.WorkflowJob.JobRunID, r)
		}
	}()
	if task.Kind != "workflow_job" || task.WorkflowJob == nil {
		log.Printf("worker %d: unexpected task kind=%q — skipping", workerID, task.Kind)
		return
	}
	log.Printf("worker %d: claimed workflow job %d (workflow=%s job=%s)",
		workerID, task.WorkflowJob.JobRunID, task.WorkflowJob.WorkflowName, task.WorkflowJob.JobKey)
	drv := &WorkflowJobDriver{
		Client:         l.Client,
		Orchestrator:   l.Orchestrator,
		WorkspaceRoot:  l.WorkspaceRoot,
		BaseURL:        l.BaseURL,
		AgentBinaryDir: agentBinDir,
	}
	if err := drv.Run(ctx, task.WorkflowJob); err != nil {
		log.Printf("worker %d: workflow job %d failed: %v", workerID, task.WorkflowJob.JobRunID, err)
	} else {
		log.Printf("worker %d: workflow job %d finished", workerID, task.WorkflowJob.JobRunID)
	}
}

// dispatcherLoop is the single long-poller. It drains idle slots,
// calls PollTasksBatch with the accumulated batch size, and feeds
// jobs into the jobs channel. Unused slots are returned.
func (l *Loop) dispatcherLoop(ctx context.Context, slots chan struct{}, jobs chan<- *client.Task, parallelism int) {
	const pollErrorBackoff = 2 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-slots:
		}

		batch := 1
	drain:
		for batch < parallelism {
			select {
			case <-slots:
				batch++
			default:
				break drain
			}
		}

		tasks, err := l.Client.PollTasksBatch(ctx, batch)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("dispatcher: poll tasks: %v", err)
			for i := 0; i < batch; i++ {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollErrorBackoff):
			}
			continue
		}

		delivered := 0
		for _, t := range tasks {
			if t == nil || t.Kind != "workflow_job" || t.WorkflowJob == nil {
				continue
			}
			select {
			case jobs <- t:
				delivered++
			case <-ctx.Done():
				// Return remaining slots.
				for i := 0; i < batch-delivered; i++ {
					select {
					case slots <- struct{}{}:
					default:
					}
				}
				return
			}
		}

		// Return any unused slots.
		for i := 0; i < batch-delivered; i++ {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (l *Loop) heartbeatLoop(ctx context.Context, parallelism int) {
	tick := time.NewTicker(l.HeartbeatEvery)
	defer tick.Stop()
	// Send an immediate heartbeat so the platform sees the runner live
	// straight after enroll, without waiting one tick.
	l.sendHeartbeat(ctx, parallelism)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			l.sendHeartbeat(ctx, parallelism)
		}
	}
}

func (l *Loop) sendHeartbeat(ctx context.Context, parallelism int) {
	caps := map[string]any{
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"go":          runtime.Version(),
		"parallelism": parallelism,
	}
	body, _ := json.Marshal(caps)
	if err := l.Client.Heartbeat(ctx, client.HeartbeatRequest{Capabilities: body}); err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Printf("heartbeat: %v", err)
		}
	}
}
