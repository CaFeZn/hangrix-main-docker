package runtime_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

// TestLoopEmitsIdleAfterEvent pins the long-lived-agent contract:
// after the assistant finishes a turn (no more tool calls), the loop
// must emit an `idle` outbound signal so downstream knows the
// container is reusable for the next queued event. Regress this and
// the spawner can never tell when it's safe to retire a container.
func TestLoopEmitsIdleAfterEvent(t *testing.T) {
	t.Parallel()

	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id": "resp_ok",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ack"}}},
			},
			"usage": map[string]any{},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("x", json.RawMessage(`{}`))
	time.Sleep(200 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	// The sequence we care about: done(event) -> idle(running_jobs=0).
	// We don't pin the absolute position of idle (status frames bracket
	// the work), just that:
	//   * exactly one done frame appears for the event
	//   * an idle frame appears AFTER it
	//   * the idle frame reports 0 running jobs (no bash work fired)
	var idleIdx, doneIdx int = -1, -1
	for i, f := range frames {
		switch f.Kind {
		case "done":
			if doneIdx != -1 {
				t.Errorf("expected exactly one done frame; saw two")
			}
			doneIdx = i
		case "idle":
			if idleIdx != -1 {
				t.Errorf("expected exactly one idle frame; saw two")
			}
			idleIdx = i
			if f.RunningJobs != 0 {
				t.Errorf("idle.running_jobs = %d, want 0", f.RunningJobs)
			}
		}
	}
	if doneIdx == -1 {
		t.Fatal("no done frame seen")
	}
	if idleIdx == -1 {
		t.Fatal("no idle frame seen; spawner has no signal to retire the container")
	}
	if idleIdx <= doneIdx {
		t.Errorf("idle (idx=%d) should come after done (idx=%d)", idleIdx, doneIdx)
	}
}

// TestLoopProcessesMultipleEvents pins the headline lifecycle:
// one container can drain multiple events before exiting.
func TestLoopProcessesMultipleEvents(t *testing.T) {
	t.Parallel()

	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ok"}}},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("first", json.RawMessage(`{}`))
	// Wait long enough for the first event's done/idle to flush so the
	// second event genuinely arrives "after idle", not piggy-backed
	// inside the first event's pendingItems.
	time.Sleep(300 * time.Millisecond)
	h.fake.pushEvent("second", json.RawMessage(`{}`))
	time.Sleep(300 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	var doneCount, idleCount int
	for _, f := range frames {
		switch f.Kind {
		case "done":
			doneCount++
		case "idle":
			idleCount++
		}
	}
	if doneCount != 2 {
		t.Errorf("expected 2 done frames (one per event); got %d", doneCount)
	}
	if idleCount != 2 {
		t.Errorf("expected 2 idle frames (one after each event); got %d", idleCount)
	}
	if got := llmCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls; got %d", got)
	}
}

// TestLoopShutdownInvokesBashCleanup pins the cleanup hook. When the
// server sends control:shutdown and there are still-running background
// bash tasks, the loop MUST call AsyncLifecycle.Cleanup with a bounded
// context before returning.
func TestLoopShutdownInvokesBashCleanup(t *testing.T) {
	t.Parallel()

	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ok"}}},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	fake := newFakeAsync()
	fake.running.Store(2)

	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, fake)
	h.fake.pushEvent("e", json.RawMessage(`{}`))
	time.Sleep(200 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	sawIdleWithJobs := false
	for _, f := range frames {
		if f.Kind == "idle" && f.RunningJobs == 2 {
			sawIdleWithJobs = true
		}
	}
	if !sawIdleWithJobs {
		t.Error("expected an idle frame with running_jobs=2 (the fake's reported count)")
	}
	if got := fake.cleanupCount.Load(); got != 1 {
		t.Errorf("Cleanup should have been called exactly once on shutdown; got %d", got)
	}
}

// TestLoopNotificationDuringEvent pins the headline inbox contract:
// when a background-bash notification arrives WHILE the LLM is
// processing an event, the loop appends it to the conversation so the
// LLM sees it on the next round.
func TestLoopNotificationDuringEvent(t *testing.T) {
	t.Parallel()

	fake := newFakeAsync()

	// Scripted LLM:
	//   call #1: takes ~300ms (mocked via Sleep), returns one tool call
	//            (so a second round happens). During this 300ms we
	//            inject a fake bash notification.
	//   call #2: returns final message. The test checks that the
	//            request body for call #2 includes the notification.
	var (
		call2Body atomic.Value // string
		callCount atomic.Int32
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			go func() {
				time.Sleep(80 * time.Millisecond)
				fake.notifications <- `<hangrix-event kind="notification.bash.finished" id="task_xyz" status="done"><outcome exit_code="0" timed_out="false" elapsed_seconds="0"/><command>echo test</command><output_tail>test</output_tail></hangrix-event>`
			}()
			time.Sleep(250 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r1",
				"output": []map[string]any{
					{"type": "function_call", "call_id": "tc_1", "name": "glob",
						"arguments": `{"pattern":"*.nonexistent"}`},
				},
				"usage": map[string]any{},
			})
		default:
			body, _ := io.ReadAll(r.Body)
			call2Body.Store(string(body))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r2",
				"output": []map[string]any{
					{"type": "message", "role": "assistant",
						"content": []map[string]any{{"type": "output_text", "text": "done"}}},
				},
				"usage": map[string]any{},
			})
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, fake)
	h.fake.pushEvent("e", json.RawMessage(`{}`))
	time.Sleep(600 * time.Millisecond)
	if _, err := h.shutdown(t); err != nil {
		t.Fatalf("loop: %v", err)
	}

	body, _ := call2Body.Load().(string)
	if body == "" {
		t.Fatal("LLM call #2 never happened; tool-call round didn't run")
	}
	if !strings.Contains(body, `notification.bash.finished`) || !strings.Contains(body, `task_xyz`) {
		t.Errorf("notification fired mid-call should appear in the next LLM request body; body=%s", body)
	}
}

// TestLoopEventDuringTurnFoldsIn pins the mid-turn-event contract:
// when a new `event` frame arrives WHILE the LLM is processing an
// in-flight turn, the agent must fold the event into the current
// conversation so the LLM sees it on the very next round.
func TestLoopEventDuringTurnFoldsIn(t *testing.T) {
	t.Parallel()

	fake := newFakeAsync()

	var (
		call2Body atomic.Value // string
		callCount atomic.Int32
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			time.Sleep(250 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r1",
				"output": []map[string]any{
					{"type": "function_call", "call_id": "tc_1", "name": "glob",
						"arguments": `{"pattern":"*.nonexistent"}`},
				},
				"usage": map[string]any{},
			})
		default:
			body, _ := io.ReadAll(r.Body)
			call2Body.Store(string(body))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r2",
				"output": []map[string]any{
					{"type": "message", "role": "assistant",
						"content": []map[string]any{{"type": "output_text", "text": "done"}}},
				},
				"usage": map[string]any{},
			})
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, fake)
	h.fake.pushEvent("first", json.RawMessage(`{"marker":"first_event_body"}`))
	// Wait until LLM call #1 is in flight, then push the second event
	// so it lands mid-call.
	time.Sleep(80 * time.Millisecond)
	h.fake.pushEvent("second", json.RawMessage(`{"marker":"second_event_body"}`))
	time.Sleep(600 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	body, _ := call2Body.Load().(string)
	if body == "" {
		t.Fatal("LLM call #2 never happened; the tool-call round didn't run")
	}
	if !strings.Contains(body, "second_event_body") {
		t.Errorf("mid-turn event should appear in the next LLM request body; body=%s", body)
	}

	var doneCount, idleCount int
	for _, f := range frames {
		switch f.Kind {
		case "done":
			doneCount++
		case "idle":
			idleCount++
		}
	}
	if doneCount != 1 {
		t.Errorf("mid-turn event should be folded into the current turn (1 done); got %d", doneCount)
	}
	if idleCount != 1 {
		t.Errorf("expected exactly 1 idle frame (one turn); got %d", idleCount)
	}
	if got := callCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (round 1 had tool calls, round 2 finalised); got %d", got)
	}
}

// TestLoopNotificationDrivesIdleTurn pins the second half of the inbox
// contract: when a notification arrives WHILE the loop is idle, it
// kicks off a brand-new LLM turn.
func TestLoopNotificationDrivesIdleTurn(t *testing.T) {
	t.Parallel()

	fake := newFakeAsync()

	var callCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ack"}}},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, fake)
	h.fake.pushEvent("first", json.RawMessage(`{}`))
	// Wait for the first event to finish + idle, then push a
	// notification from "outside". The loop should pick it up and
	// drive a second round.
	time.Sleep(300 * time.Millisecond)
	fake.notifications <- `<hangrix-event kind="notification.bash.finished" id="task_late" status="done"><outcome exit_code="0" timed_out="false" elapsed_seconds="0"/></hangrix-event>`
	time.Sleep(300 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	var done, idle int
	for _, f := range frames {
		switch f.Kind {
		case "done":
			done++
		case "idle":
			idle++
		}
	}
	if done != 2 {
		t.Errorf("expected 2 done frames (event + notification-driven turn); got %d", done)
	}
	if idle != 2 {
		t.Errorf("expected 2 idle frames; got %d", idle)
	}
	if got := callCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (one per round); got %d", got)
	}
}

// TestLoopSleepNotificationBufferedAtIdleStillDrivesWake closes the race
// reported in issue #372: if a sleep completion is already buffered right
// after the loop returns to idle, the wake must still stay alive long
// enough for that notification to drive a second turn.
func TestLoopSleepNotificationBufferedAtIdleStillDrivesWake(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ack"}}},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("first", json.RawMessage(`{}`))
	// Wait for the first event to finish + idle.
	time.Sleep(300 * time.Millisecond)

	// Schedule a short async notification and let it fire while the loop is
	// idle. This exercises the exact path where the notification is buffered
	// first and only then observed by the loop.
	bundle.Async.Schedule(1*time.Second, `<hangrix-event kind="notification.sleep.finished" id="sleep_test" status="done"><sleep seconds="1" reason="idle-buffered"/></hangrix-event>`)

	time.Sleep(1500 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	var done, idle int
	for _, f := range frames {
		switch f.Kind {
		case "done":
			done++
		case "idle":
			idle++
		}
	}
	if done != 2 {
		t.Errorf("expected 2 done frames (event + sleep-notification-driven turn); got %d", done)
	}
	if idle != 2 {
		t.Errorf("expected 2 idle frames; got %d", idle)
	}
	if got := callCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (one per round); got %d", got)
	}
}

// TestLoopEventArrivesAfterReady is the regression for the rewake
// scenario: the agent boots and emits ready, and THEN the event frame
// arrives. The event must still be processed.
func TestLoopEventArrivesAfterReady(t *testing.T) {
	t.Parallel()

	var llmCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		llmCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "r",
			"output": []map[string]any{
				{"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": "ack"}}},
			},
			"usage": map[string]any{},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	// Wait for ready before shipping the rewake event — this is the key
	// property: event arrives AFTER the agent parked on the inbox.
	h.waitForReady(t, 2*time.Second)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"comment_id":1344}`))
	time.Sleep(300 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	var doneCount, idleCount int
	for _, f := range frames {
		switch f.Kind {
		case "done":
			doneCount++
		case "idle":
			idleCount++
		}
	}
	if doneCount != 1 {
		t.Errorf("expected exactly 1 done frame; got %d", doneCount)
	}
	if idleCount != 1 {
		t.Errorf("expected exactly 1 idle frame; got %d", idleCount)
	}
	if got := llmCount.Load(); got != 1 {
		t.Errorf("expected 1 LLM call; got %d", got)
	}
}

// TestLoopMidCallEventPreventsDone pins the no-tool-call + new-input
// contract: when the LLM returns no tool calls but the loop absorbed
// a fresh event mid-call, the loop must NOT emit `done` — it must
// give the LLM another round so it can react.
func TestLoopMidCallEventPreventsDone(t *testing.T) {
	t.Parallel()

	var (
		callCount atomic.Int32
		call2Body atomic.Value // string
	)

	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			time.Sleep(200 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r1",
				"output": []map[string]any{
					{"type": "message", "role": "assistant",
						"content": []map[string]any{{"type": "output_text", "text": "first round done"}}},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13},
			})
		case 2:
			body, _ := io.ReadAll(r.Body)
			call2Body.Store(string(body))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "r2",
				"output": []map[string]any{
					{"type": "message", "role": "assistant",
						"content": []map[string]any{{"type": "output_text", "text": "ack event 2"}}},
				},
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 3, "total_tokens": 11},
			})
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("first", json.RawMessage(`{"marker":"first_event"}`))
	// Wait until LLM call #1 is in flight (handler sleeps 200ms), then
	// push a second event so it lands mid-call.
	time.Sleep(80 * time.Millisecond)
	h.fake.pushEvent("second", json.RawMessage(`{"marker":"second_event"}`))
	time.Sleep(500 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	body, _ := call2Body.Load().(string)
	if body == "" {
		t.Fatal("LLM call #2 never happened; loop emitted `done` on call #1 despite new mid-call input")
	}
	if !strings.Contains(body, "second_event") {
		t.Errorf("mid-call event should appear in the second LLM request body; body=%s", body)
	}

	var doneCount, idleCount int
	for _, f := range frames {
		switch f.Kind {
		case "done":
			doneCount++
		case "idle":
			idleCount++
		}
	}
	if doneCount != 1 {
		t.Errorf("mid-call event should be folded into the current turn (1 done); got %d", doneCount)
	}
	if idleCount != 1 {
		t.Errorf("expected exactly 1 idle frame; got %d", idleCount)
	}

	if got := callCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (call #1 had no tools but mid-call input forced call #2); got %d", got)
	}
}
