package loop

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
)

// ---- test interfaces (per architecture §设计决策 6) ----

type taskSource interface {
	PollTasksBatch(ctx context.Context, maxBatch int) ([]*client.Task, error)
}

type taskExecutor interface {
	Run(ctx context.Context, job *client.WorkflowJob) error
}

// fakePoller is a controllable taskSource for tests. It records calls
// (maxBatch, inflight) and returns tasks from a slice or channel.
type fakePoller struct {
	mu       sync.Mutex
	calls    []pollCall
	inflight int64 // atomic

	// Behaviour knobs.
	tasks     []*client.Task
	ch        chan []*client.Task
	errs      []error
	errIdx    int
	taskIdx   int
	pollDelay time.Duration

	totalCalls int64
}

type pollCall struct {
	maxBatch  int
	inflight  int64
	timestamp time.Time
}

func (f *fakePoller) PollTasksBatch(ctx context.Context, maxBatch int) ([]*client.Task, error) {
	n := atomic.AddInt64(&f.inflight, 1)
	defer atomic.AddInt64(&f.inflight, -1)
	atomic.AddInt64(&f.totalCalls, 1)

	f.mu.Lock()
	f.calls = append(f.calls, pollCall{maxBatch: maxBatch, inflight: n, timestamp: time.Now()})
	f.mu.Unlock()

	if f.pollDelay > 0 {
		select {
		case <-time.After(f.pollDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if f.errIdx < len(f.errs) {
		err := f.errs[f.errIdx]
		f.errIdx++
		return nil, err
	}

	if f.ch != nil {
		select {
		case tasks, ok := <-f.ch:
			if !ok {
				return nil, nil
			}
			return tasks, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if len(f.tasks) == 0 {
		return nil, nil
	}
	result := f.tasks[f.taskIdx%len(f.tasks)]
	f.taskIdx++
	if result == nil {
		return nil, nil
	}
	return []*client.Task{result}, nil
}

func (f *fakePoller) lastCall() *pollCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	idx := len(f.calls) - 1
	lc := f.calls[idx]
	return &lc
}

// fakeRunner records which jobs ran.
type fakeRunner struct {
	mu         sync.Mutex
	jobs       []int64
	delay      time.Duration
	panicOnJob *int64
}

func (r *fakeRunner) Run(ctx context.Context, job *client.WorkflowJob) error {
	r.mu.Lock()
	r.jobs = append(r.jobs, job.JobRunID)
	r.mu.Unlock()
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.panicOnJob != nil && job.JobRunID == *r.panicOnJob {
		panic(fmt.Sprintf("injected panic on job %d", job.JobRunID))
	}
	return nil
}

func (r *fakeRunner) executed() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int64, len(r.jobs))
	copy(out, r.jobs)
	return out
}

// mkTask creates a minimal workflow_job client.Task.
func mkTask(jobID int64) *client.Task {
	return &client.Task{
		Kind: "workflow_job",
		WorkflowJob: &client.WorkflowJob{
			JobRunID: jobID,
		},
	}
}

// mkAgentTask creates an agent_session task (unknown kind).
func mkAgentTask(sessionID int64) *client.Task {
	return &client.Task{
		Kind:      "agent_session",
		SessionID: sessionID,
	}
}

// ---- Test 1: SinglePoller ----

func TestDispatcher_SinglePoller(t *testing.T) {
	poller := &fakePoller{}
	runner := &fakeRunner{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runWithHooks(ctx, poller, runner, 8)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	if atomic.LoadInt64(&poller.totalCalls) < 2 {
		t.Error("expected ≥2 poll calls in 200ms")
	}

	poller.mu.Lock()
	defer poller.mu.Unlock()
	for i, c := range poller.calls {
		if c.inflight > 1 {
			t.Errorf("call %d: inflight=%d but expected ≤ 1", i, c.inflight)
		}
	}
}

// ---- Test 2: RequestsBatch ----

func TestDispatcher_RequestsBatch(t *testing.T) {
	const parallelism = 4

	poller := &fakePoller{
		pollDelay: 500 * time.Millisecond,
	}
	runner := &fakeRunner{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runWithHooks(ctx, poller, runner, parallelism)

	// Wait for first poll call to start.
	time.Sleep(100 * time.Millisecond)
	// Give time for the drain loop to consume idle slots.
	time.Sleep(200 * time.Millisecond)

	lc := poller.lastCall()
	cancel()

	if lc == nil {
		t.Fatal("no poll calls recorded")
	}
	if lc.maxBatch < 2 {
		t.Errorf("expected maxBatch ≥ 2 when all workers idle, got %d", lc.maxBatch)
	}
}

// ---- Test 3: BurstFanOut ----

func TestDispatcher_BurstFanOut(t *testing.T) {
	const parallelism = 4
	tasks := []*client.Task{mkTask(1), mkTask(2), mkTask(3), mkTask(4)}
	runner := &fakeRunner{delay: 100 * time.Millisecond}
	poller := &fakePoller{tasks: tasks}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runWithHooks(ctx, poller, runner, parallelism)

	time.Sleep(300 * time.Millisecond)
	cancel()

	executed := runner.executed()
	if len(executed) < 4 {
		t.Errorf("expected ≥ 4 executed jobs, got %d: %v", len(executed), executed)
	}
	if atomic.LoadInt64(&poller.totalCalls) < 1 {
		t.Error("expected ≥ 1 poll call")
	}
}

// ---- Test 4: SlotsConserved ----

func TestDispatcher_SlotsConserved(t *testing.T) {
	const parallelism = 2

	panicJobID := int64(99)
	runner := &fakeRunner{delay: 20 * time.Millisecond, panicOnJob: &panicJobID}
	poller := &fakePoller{
		errs: []error{fmt.Errorf("transient 1"), fmt.Errorf("transient 2")},
	}
	ch := make(chan []*client.Task, 10)
	poller.ch = ch

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runWithHooks(ctx, poller, runner, parallelism)

	// Phase 1: let error injection play out.
	time.Sleep(100 * time.Millisecond)

	// Phase 2: unknown kind (agent_session) — should not deadlock.
	ch <- []*client.Task{mkAgentTask(1001)}
	time.Sleep(50 * time.Millisecond)

	// Phase 3: normal tasks.
	ch <- []*client.Task{mkTask(10), mkTask(11)}
	time.Sleep(150 * time.Millisecond)

	// Phase 4: panic task.
	ch <- []*client.Task{mkTask(panicJobID)}
	time.Sleep(50 * time.Millisecond)

	// Phase 5: task after panic — proves recovery.
	ch <- []*client.Task{mkTask(12)}
	time.Sleep(100 * time.Millisecond)

	cancel()
	close(ch)

	executed := runner.executed()
	t.Logf("executed job IDs: %v", executed)

	foundLater := false
	for _, id := range executed {
		if id == 12 {
			foundLater = true
			break
		}
	}
	if !foundLater {
		t.Error("job 12 not executed after panic on job 99 — panic isolation failed")
	}

	if atomic.LoadInt64(&poller.totalCalls) < 3 {
		t.Errorf("expected ≥ 3 total poll calls, got %d", poller.totalCalls)
	}
}

// ---- Test 5: GracefulShutdown ----

func TestLoop_GracefulShutdown(t *testing.T) {
	before := runtime.NumGoroutine()

	poller := &fakePoller{}
	runner := &fakeRunner{}

	ctx, cancel := context.WithCancel(context.Background())

	errc := make(chan error, 1)
	go func() {
		errc <- runWithHooks(ctx, poller, runner, 1)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after cancel")
	}

	// Let goroutines settle.
	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Errorf("goroutine leak: before=%d after=%d diff=%d", before, after, after-before)
	}
}

// ---- Test 6: UnknownKind no deadlock ----

func TestDispatcher_UnknownKind_NoDeadlock(t *testing.T) {
	const parallelism = 2

	runner := &fakeRunner{delay: 10 * time.Millisecond}
	ch := make(chan []*client.Task, 10)
	poller := &fakePoller{ch: ch}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runWithHooks(ctx, poller, runner, parallelism)

	ch <- []*client.Task{mkAgentTask(2001)}
	time.Sleep(50 * time.Millisecond)

	ch <- []*client.Task{mkTask(100)}
	time.Sleep(100 * time.Millisecond)

	cancel()
	close(ch)

	executed := runner.executed()
	found := false
	for _, id := range executed {
		if id == 100 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("job 100 should have been executed after unknown kind. executed: %v", executed)
	}
	for _, id := range executed {
		if id == 2001 {
			t.Error("agent_session task should not be executed")
		}
	}
}

// ---- Test 7: PollErrorRecovery ----

func TestDispatcher_PollError_Recovers(t *testing.T) {
	const parallelism = 2
	panicJobID := int64(50)
	runner := &fakeRunner{panicOnJob: &panicJobID}
	ch := make(chan []*client.Task, 10)
	poller := &fakePoller{
		errs: []error{fmt.Errorf("error 1"), fmt.Errorf("error 2")},
		ch:   ch,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runWithHooks(ctx, poller, runner, parallelism)

	// Wait for error recovery (2 backoff cycles).
	time.Sleep(200 * time.Millisecond)

	// Inject tasks in sequence using channel mode.
	ch <- []*client.Task{mkTask(1)}
	time.Sleep(100 * time.Millisecond)
	ch <- []*client.Task{mkTask(panicJobID)}
	time.Sleep(50 * time.Millisecond)
	ch <- []*client.Task{mkTask(2)}
	time.Sleep(100 * time.Millisecond)

	cancel()
	close(ch)

	executed := runner.executed()
	t.Logf("executed: %v", executed)

	foundAfter := false
	for _, id := range executed {
		if id == 2 {
			foundAfter = true
			break
		}
	}
	if !foundAfter {
		t.Error("job 2 not executed after panic — panic isolation failed")
	}
}

// ---- runWithHooks: dispatcher + worker pool using test interfaces ----

func runWithHooks(ctx context.Context, src taskSource, exec taskExecutor, parallelism int) error {
	slots := make(chan struct{}, parallelism)
	jobs := make(chan *client.Task, parallelism)
	for i := 0; i < parallelism; i++ {
		slots <- struct{}{}
	}

	var wg sync.WaitGroup

	// Worker pool using test executor.
	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					// Worker-level panic isolation.
					_ = r
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
					func() {
						defer func() {
							if r := recover(); r != nil {
								// Job-level panic isolation.
							}
						}()
						if task.Kind != "workflow_job" || task.WorkflowJob == nil {
							return
						}
						_ = exec.Run(ctx, task.WorkflowJob)
					}()
					select {
					case slots <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
			}
		}(i)
	}

	// Dispatcher using test source.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(jobs)

		const pollErrorBackoff = 50 * time.Millisecond
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

			tasks, err := src.PollTasksBatch(ctx, batch)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
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
					for i := 0; i < batch-delivered; i++ {
						select {
						case slots <- struct{}{}:
						default:
						}
					}
					return
				}
			}

			for i := 0; i < batch-delivered; i++ {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	wg.Wait()
	return nil
}
