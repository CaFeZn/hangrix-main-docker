package runtime_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/platform"
)

// TestLoopSmoke is the end-to-end rehearsal: scripted LLM (returns
// one tool call, then a final message) + mock platform tools server
// (echoing `issue_read`) + real local tools, driven through the
// runtime loop. We assert the end state from the sink's recorded
// frame stream:
//
//   - one local tool call was executed (read on a temp file)
//   - one platform tool call was executed (issue_read)
//   - a final assistant message arrived
//   - a `done` frame closed the turn
//
// This intentionally goes end-to-end through the real llm/tools
// machinery — only the upstream HTTP servers and the wire transport
// are stubbed. A failure here means the seam between two of those
// packages broke.
func TestLoopSmoke(t *testing.T) {
	t.Parallel()

	// (1) Sandbox file that the local read tool will inspect.
	dir := t.TempDir()
	sandboxFile := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(sandboxFile, []byte("hello from sandbox\n"), 0o644); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}

	// (2) Mock LLM. First /responses call returns two tool calls (read +
	// issue_read). Second call (after tool results are fed back) returns
	// a final assistant message with no tool calls — that ends the turn.
	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		isFirst := llmCallCount.Add(1) == 1
		hasFCOutput := strings.Contains(string(body), "function_call_output")
		if isFirst && hasFCOutput {
			t.Errorf("first llm call should not include function_call_output; body=%s", body)
		}
		if !isFirst && !hasFCOutput {
			t.Errorf("second llm call should include function_call_output; body=%s", body)
		}

		w.Header().Set("Content-Type", "application/json")
		if isFirst {
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "tc_local_1",
						"name":      "read",
						"arguments": `{"path":"` + sandboxFile + `"}`,
					},
					{
						"type":      "function_call",
						"call_id":   "tc_platform_1",
						"name":      "issue_read",
						"arguments": `{}`,
					},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		resp := map[string]any{
			"id": "resp_2",
			"output": []map[string]any{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": "all done"},
					},
				},
			},
			"usage": map[string]any{"input_tokens": 20, "output_tokens": 3, "total_tokens": 23},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(llmServer.Close)

	// (3) Mock platform tools server. issue_read GETs /issues/current
	// and gets a canned {"data":...} reply.
	platformServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/issues/current" {
			http.Error(w, "unknown tool", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": "method not allowed",
				"errors":  []map[string]any{{"code": "method_not_allowed"}},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"text": "pong"},
		})
	}))
	t.Cleanup(platformServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	platformClient := platform.NewClient(platformServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, platform.All(platformClient, nil, false), nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot inspect the sandbox"}`))
	// Let the turn finish (tool dispatch + final message) before shutdown.
	time.Sleep(500 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop returned error: %v", err)
	}

	var (
		gotReadCall     bool
		gotPlatformCall bool
		gotDone         bool
		assistantMsgs   int
		finalContent    string
	)
	for _, f := range frames {
		switch f.Kind {
		case "tool_call":
			if f.Name == "read" {
				gotReadCall = true
				if !strings.Contains(string(f.Result), "hello from sandbox") {
					t.Errorf("read tool result missing file content: %s", f.Result)
				}
			}
			if f.Name == "issue_read" {
				gotPlatformCall = true
				if !strings.Contains(string(f.Result), "pong") {
					t.Errorf("issue_read result missing pong: %s", f.Result)
				}
			}
		case "message":
			assistantMsgs++
			if len(f.ToolCalls) == 0 {
				finalContent = f.Content
			}
		case "done":
			gotDone = true
		}
	}
	if !gotReadCall {
		t.Error("expected a tool_call for `read`")
	}
	if !gotPlatformCall {
		t.Error("expected a tool_call for `issue_read`")
	}
	if !gotDone {
		t.Error("expected a `done` frame")
	}
	if assistantMsgs < 2 {
		t.Errorf("expected ≥2 assistant messages (one with tool calls, one final), got %d", assistantMsgs)
	}
	if finalContent != "all done" {
		t.Errorf("final assistant content = %q, want %q", finalContent, "all done")
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (one with tools, one final), got %d", got)
	}
}

// TestLoopCompactSession verifies the compact_session interception:
// after the LLM calls compact_session(summary=...), the next LLM call's
// request body must NOT contain any pre-compact noise — only the system
// instructions, the summary block, and whatever was appended after.
// This is the regression test for the "messages with role 'tool' must
// be a response to a preceding message with 'tool_calls'" upstream 400
// that the old tail-window trim could trip on.
func TestLoopCompactSession(t *testing.T) {
	t.Parallel()

	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		callN := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "tc_noise",
						"name":      "glob",
						"arguments": `{"pattern":"*.go"}`,
					},
				},
				"usage": map[string]any{"input_tokens": 12, "output_tokens": 4, "total_tokens": 16},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 2:
			resp := map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "tc_compact",
						"name":      "compact_session",
						"arguments": `{"summary":"investigated repo. nothing actionable. next: handle a fresh event."}`,
					},
				},
				"usage": map[string]any{"input_tokens": 40, "output_tokens": 6, "total_tokens": 46},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 3:
			secondCallBody.Store(body)
			resp := map[string]any{
				"id": "resp_3",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "all clear"},
						},
					},
				},
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 3, "total_tokens": 11},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot look around"}`))
	time.Sleep(500 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 3 {
		t.Fatalf("expected 3 LLM calls (noise + compact + final), got %d", got)
	}

	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture third LLM call body")
	}
	body := string(rawBody)
	if strings.Contains(body, "tc_noise") {
		t.Errorf("post-compact LLM body still contains pre-compact tool call id `tc_noise`:\n%s", body)
	}
	if strings.Contains(body, "tc_compact") {
		t.Errorf("post-compact LLM body still contains compact_session call id `tc_compact`:\n%s", body)
	}
	if !strings.Contains(body, "previous_session_summary") {
		t.Errorf("post-compact LLM body missing summary wrapper:\n%s", body)
	}
	if !strings.Contains(body, "handle a fresh event") {
		t.Errorf("post-compact LLM body missing the summary text the LLM wrote:\n%s", body)
	}
	var parsed struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		t.Fatalf("decode post-compact body: %v", err)
	}
	if len(parsed.Input) != 1 {
		t.Errorf("post-compact input has %d items, want 1 (just the summary); items=%+v", len(parsed.Input), parsed.Input)
	}
	for _, item := range parsed.Input {
		itemType, _ := item["type"].(string)
		if itemType == "function_call" || itemType == "function_call_output" {
			callID, _ := item["call_id"].(string)
			t.Errorf("post-compact input leaked %s (call_id=%s); window should contain only the summary", itemType, callID)
		}
	}

	var sawCompactFrame bool
	for _, f := range frames {
		if f.Kind == "tool_call" && f.Name == "compact_session" {
			sawCompactFrame = true
			if !strings.Contains(string(f.Args), "handle a fresh event") {
				t.Errorf("compact_session tool_call args missing the summary text: %s", f.Args)
			}
		}
	}
	if !sawCompactFrame {
		t.Error("expected a tool_call frame for compact_session")
	}
}

// TestLoopAtMentionNudge verifies the at-mention reminder: when the
// model returns plain assistant text containing `@` and no tool calls,
// the loop should NOT close the turn — it must inject a system_reminder
// telling the model to use the issue_comment tool, then make a second
// LLM call so the model can retry.
func TestLoopAtMentionNudge(t *testing.T) {
	t.Parallel()

	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		callN := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "@agent-frontend please review"},
						},
					},
				},
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 4, "total_tokens": 12},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 2:
			secondCallBody.Store(body)
			resp := map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "acknowledged, will retry via tool"},
						},
					},
				},
				"usage": map[string]any{"input_tokens": 20, "output_tokens": 5, "total_tokens": 25},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"hello"}`))
	time.Sleep(500 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Fatalf("expected 2 LLM calls (nudge forces a retry), got %d", got)
	}
	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture second LLM call body")
	}
	body := string(rawBody)
	if !strings.Contains(body, "issue_comment") {
		t.Errorf("second LLM call body missing the at-mention reminder pointing at issue_comment:\n%s", body)
	}
	if !strings.Contains(body, "system_reminder") {
		t.Errorf("second LLM call body missing the system_reminder wrapper:\n%s", body)
	}

	var (
		assistantMsgs int
		doneFrames    int
	)
	for _, f := range frames {
		switch f.Kind {
		case "message":
			assistantMsgs++
		case "done":
			doneFrames++
		}
	}
	if assistantMsgs != 2 {
		t.Errorf("expected 2 assistant messages, got %d", assistantMsgs)
	}
	if doneFrames != 1 {
		t.Errorf("expected exactly 1 done frame, got %d", doneFrames)
	}
}

// TestLoopAtMentionNudgeWithToolCallsPreservesChain is the regression
// guard for the bug where the @-mention reminder was injected between
// an assistant(tool_calls=…) entry and its tool_result(s). Upstream
// requires the tool_result items to immediately follow the assistant
// message that produced the tool_calls — any user-role item wedged in
// between makes the API reject the next call with a 400.
func TestLoopAtMentionNudgeWithToolCallsPreservesChain(t *testing.T) {
	t.Parallel()

	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		callN := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "@agent-frontend please review"},
						},
					},
					{
						"type":      "function_call",
						"call_id":   "call_abc",
						"name":      "noop_unknown_tool",
						"arguments": "{}",
					},
				},
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 4, "total_tokens": 12},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 2:
			secondCallBody.Store(body)
			resp := map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{
						"type": "message",
						"role": "assistant",
						"content": []map[string]any{
							{"type": "output_text", "text": "ack"},
						},
					},
				},
				"usage": map[string]any{"input_tokens": 20, "output_tokens": 1, "total_tokens": 21},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"hello"}`))
	time.Sleep(500 * time.Millisecond)
	if _, err := h.shutdown(t); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", got)
	}

	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture second LLM call body")
	}

	var parsed struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		t.Fatalf("unmarshal second call body: %v", err)
	}

	var fcIdx = -1
	for i, item := range parsed.Input {
		if item["type"] == "function_call" && item["call_id"] == "call_abc" {
			fcIdx = i
			break
		}
	}
	if fcIdx < 0 {
		t.Fatalf("function_call for call_abc not found in second call input:\n%s", string(rawBody))
	}
	if fcIdx+1 >= len(parsed.Input) {
		t.Fatalf("function_call is the last input item — no tool result followed:\n%s", string(rawBody))
	}
	next := parsed.Input[fcIdx+1]
	if next["type"] != "function_call_output" || next["call_id"] != "call_abc" {
		t.Fatalf("expected function_call_output immediately after function_call, got %v:\n%s", next, string(rawBody))
	}

	var reminderIdx = -1
	for i, item := range parsed.Input {
		if item["role"] != "user" {
			continue
		}
		content, _ := item["content"].([]any)
		if len(content) == 0 {
			continue
		}
		first, _ := content[0].(map[string]any)
		text, _ := first["text"].(string)
		if strings.Contains(text, "system_reminder") && strings.Contains(text, "issue_comment") {
			reminderIdx = i
			break
		}
	}
	if reminderIdx < 0 {
		t.Fatalf("at-mention reminder not found in second call input:\n%s", string(rawBody))
	}
	if reminderIdx <= fcIdx+1 {
		t.Fatalf("at-mention reminder (idx=%d) must come AFTER the tool result (idx=%d), but didn't — chain is broken:\n%s", reminderIdx, fcIdx+1, string(rawBody))
	}
}

// TestLoopSleepGate verifies the sleep batching guard: when the LLM
// returns sleep alongside other tool calls in the same response, the
// loop must only execute sleep and reject the rest with errors.
func TestLoopSleepGate(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		callN := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "tc_sleep",
						"name":      "sleep",
						"arguments": `{"seconds":5}`,
					},
					{
						"type":      "function_call",
						"call_id":   "tc_other",
						"name":      "glob",
						"arguments": `{"pattern":"*.go"}`,
					},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot do something"}`))
	time.Sleep(500 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 1 {
		t.Fatalf("expected exactly 1 LLM call (sleep-gate prevents second round), got %d", got)
	}

	var (
		sawSleepCall bool
		sawOtherCall bool
		sawDone      bool
		otherResult  string
	)
	for _, f := range frames {
		switch f.Kind {
		case "tool_call":
			if f.Name == "sleep" {
				sawSleepCall = true
				if !strings.Contains(string(f.Result), "scheduled") {
					t.Errorf("sleep result missing 'scheduled': %s", f.Result)
				}
			}
			if f.Name == "glob" {
				sawOtherCall = true
				otherResult = string(f.Result)
			}
		case "done":
			sawDone = true
		}
	}
	if !sawSleepCall {
		t.Error("expected a tool_call for sleep")
	}
	if !sawOtherCall {
		t.Error("expected a tool_call for the rejected call (glob)")
	}
	if !strings.Contains(otherResult, "batched with sleep") {
		t.Errorf("rejected call result should explain batching violation, got: %s", otherResult)
	}
	if !sawDone {
		t.Error("expected a done frame after sleep-gate")
	}
}

// TestLoopAskQuestionGate verifies the ask_question batching guard.
func TestLoopAskQuestionGate(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		callN := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch callN {
		case 1:
			resp := map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type":      "function_call",
						"call_id":   "tc_aq",
						"name":      "ask_question",
						"arguments": `{"title":"test","questions":[{"type":"text_input","text":"q?"}]}`,
					},
					{
						"type":      "function_call",
						"call_id":   "tc_other",
						"name":      "glob",
						"arguments": `{"pattern":"*.go"}`,
					},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 2:
			resp := map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{
						"type":    "message",
						"role":    "assistant",
						"content": []map[string]any{{"type": "output_text", "text": "Questionnaire created. Waiting for answer."}},
					},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	platformServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/issues/current/questionnaires" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"id":    7,
					"title": "test",
				},
			})
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	t.Cleanup(platformServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	platformClient := platform.NewClient(platformServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, platform.All(platformClient, bundle.Async, false), nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot do something"}`))
	time.Sleep(500 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Fatalf("expected exactly 2 LLM calls (ask_question + reaction round), got %d", got)
	}

	var (
		sawAQCall    bool
		sawOtherCall bool
		sawDone      bool
		otherResult  string
	)
	for _, f := range frames {
		switch f.Kind {
		case "tool_call":
			if f.Name == "ask_question" {
				sawAQCall = true
				if !strings.Contains(string(f.Result), "scheduled") {
					t.Errorf("ask_question result missing 'scheduled': %s", f.Result)
				}
			}
			if f.Name == "glob" {
				sawOtherCall = true
				otherResult = string(f.Result)
			}
		case "done":
			sawDone = true
		}
	}
	if !sawAQCall {
		t.Error("expected a tool_call for ask_question")
	}
	if !sawOtherCall {
		t.Error("expected a tool_call for the rejected call (glob)")
	}
	if !strings.Contains(otherResult, "batched with ask_question") {
		t.Errorf("rejected call result should explain batching violation, got: %s", otherResult)
	}
	if !sawDone {
		t.Error("expected a done frame after async-gate")
	}
}

// TestLoopReasoningTimeoutRetrySuccess verifies the retry path: when the
// first LLM attempt hits a reasoning timeout (DeadlineExceeded), the loop
// retries with the same request snapshot. The second attempt succeeds.
func TestLoopReasoningTimeoutRetrySuccess(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		n := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			time.Sleep(200 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_timeout",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "too late"}}},
				},
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
			})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_ok",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "retry succeeded"}}},
				},
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 4, "total_tokens": 12},
			})
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async, withReasoning(100*time.Millisecond, 1))
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot hello"}`))
	time.Sleep(time.Second)
	if _, err := h.shutdown(t); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (1 timeout + 1 retry), got %d", got)
	}
}

// TestLoopReasoningTimeoutRetryExhausted verifies that after maxAttempts
// calls, each returning DeadlineExceeded, the loop stops and returns a
// descriptive timeout error.
func TestLoopReasoningTimeoutRetryExhausted(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		llmCallCount.Add(1)
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_late",
			"output": []map[string]any{
				{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "too late"}}},
			},
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async, withReasoning(100*time.Millisecond, 1))
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot hello"}`))
	// Loop self-terminates on exhausted-retries error; pushing shutdown
	// is still safe (buffered channel; loop already exited).
	_, err := h.shutdown(t)
	if err == nil {
		t.Fatal("expected loop to return a timeout error, got nil")
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Errorf("expected 2 LLM calls (both timing out), got %d", got)
	}
	if !strings.Contains(err.Error(), "LLM reasoning timeout after 2 attempt(s)") {
		t.Errorf("error message should indicate exhausted retries, got: %v", err)
	}
	if !strings.Contains(err.Error(), "threshold=0s") {
		t.Errorf("error message should include threshold, got: %v", err)
	}
}

// TestLoopReasoningTimeoutNonTimeoutErrorNoRetry verifies that non-timeout
// errors (here a 400 from the upstream) are NOT retried at the reasoning-
// timeout layer.
func TestLoopReasoningTimeoutNonTimeoutErrorNoRetry(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		llmCallCount.Add(1)
		http.Error(w, "upstream rejected request", http.StatusBadRequest)
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async, withReasoning(time.Second, 2))
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot hello"}`))
	_, err := h.shutdown(t)
	if err == nil {
		t.Fatal("expected loop to return an error for the 400, got nil")
	}
	if got := llmCallCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 LLM call (no retry for non-timeout error), got %d", got)
	}
	if !strings.Contains(err.Error(), "llm call failed") {
		t.Errorf("error should be propagated from the LLM layer, got: %v", err)
	}
}

// TestLoopReasoningTimeoutInboxDrain verifies that inbox events arriving
// while an LLM call is in flight are still drained into context, causing
// the loop to make another round with the new input visible.
func TestLoopReasoningTimeoutInboxDrain(t *testing.T) {
	t.Parallel()

	proceedCh := make(chan struct{})
	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		n := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			<-proceedCh
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "thinking..."}}},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13},
			})
		case 2:
			body, _ := io.ReadAll(r.Body)
			secondCallBody.Store(body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "all done"}}},
				},
				"usage": map[string]any{"input_tokens": 5, "output_tokens": 2, "total_tokens": 7},
			})
		default:
			http.Error(w, "unexpected extra LLM call", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async, withReasoning(5*time.Second, 0))
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot hello"}`))
	time.Sleep(100 * time.Millisecond)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"INBOX_EVENT_MARKER"}`))
	time.Sleep(100 * time.Millisecond)
	close(proceedCh)
	time.Sleep(500 * time.Millisecond)

	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Fatalf("expected 2 LLM calls (first unblocked + second round for inbox event), got %d", got)
	}

	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture second LLM call body")
	}
	if !strings.Contains(string(rawBody), "INBOX_EVENT_MARKER") {
		t.Errorf("second LLM call body should contain the inbox event text, got:\n%s", string(rawBody))
	}

	var doneFrames int
	for _, f := range frames {
		if f.Kind == "done" {
			doneFrames++
		}
	}
	if doneFrames != 1 {
		t.Errorf("expected exactly 1 done frame, got %d", doneFrames)
	}
}

// TestLoopEmptyResponseRecovery covers the OpenAI Response API edge case
// where a reasoning model returns a 2xx body with `output:null` because
// reasoning tokens consumed the entire output budget. Without the empty-
// response guard the loop would call the turn done and silently drop the
// task. With the guard, a system_reminder is injected and the next LLM
// call gets a chance to emit actual output.
func TestLoopEmptyResponseRecovery(t *testing.T) {
	t.Parallel()

	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		n := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			// Mirror the exact bug payload: completed but output:null,
			// reasoning_tokens consumed the whole output budget.
			_, _ = w.Write([]byte(`{
                "id": "resp_empty",
                "object": "response",
                "status": "completed",
                "output": null,
                "usage": {
                    "input_tokens": 100,
                    "output_tokens": 50,
                    "total_tokens": 150,
                    "output_tokens_details": {"reasoning_tokens": 50}
                }
            }`))
		case 2:
			secondCallBody.Store(body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_ok",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "back on track"}}},
				},
				"usage": map[string]any{"input_tokens": 110, "output_tokens": 4, "total_tokens": 114},
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
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot do something"}`))
	time.Sleep(500 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 2 {
		t.Fatalf("expected 2 LLM calls (empty + retry with nudge), got %d", got)
	}

	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture second LLM call body")
	}
	if !strings.Contains(string(rawBody), "returned no assistant message and no tool calls") {
		t.Errorf("second LLM call body should contain the nudge reminder, got:\n%s", string(rawBody))
	}

	var (
		finalContent string
		doneFrames   int
	)
	for _, f := range frames {
		switch f.Kind {
		case "message":
			if len(f.ToolCalls) == 0 && f.Content != "" {
				finalContent = f.Content
			}
		case "done":
			doneFrames++
		}
	}
	if finalContent != "back on track" {
		t.Errorf("final assistant content = %q, want %q", finalContent, "back on track")
	}
	if doneFrames != 1 {
		t.Errorf("expected exactly 1 done frame, got %d", doneFrames)
	}
}

// TestLoopEmptyResponseExhausts verifies that if the LLM keeps returning
// empty responses past the retry cap, the loop fails the turn with a
// descriptive error rather than wedging forever.
func TestLoopEmptyResponseExhausts(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
            "id": "resp_empty",
            "status": "completed",
            "output": null,
            "usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15, "output_tokens_details": {"reasoning_tokens": 5}}
        }`))
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot do something"}`))
	_, err := h.shutdown(t)
	if err == nil {
		t.Fatal("expected loop to return an empty-response error, got nil")
	}
	// initial + 3 retries = 4 calls before bailing
	if got := llmCallCount.Load(); got != 4 {
		t.Errorf("expected 4 LLM calls (initial + 3 retries), got %d", got)
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("error should mention empty response, got: %v", err)
	}
}

// TestLoopFutureTenseNudge covers the "model promised to continue but
// emitted no tool call" failure mode. baseline.md forbids the pattern,
// but reasoning models still slip into it. The loop must inject a
// reminder and give the model another round instead of silently
// declaring the turn done.
func TestLoopFutureTenseNudge(t *testing.T) {
	t.Parallel()

	var (
		llmCallCount   atomic.Int32
		secondCallBody atomic.Value // []byte
	)
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		n := llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			// Verbatim shape of the production bug: assistant text says
			// "继续中。…我会去做…" with no tool call.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_1",
				"output": []map[string]any{
					{
						"type":    "message",
						"role":    "assistant",
						"content": []map[string]any{{"type": "output_text", "text": "继续中。后端我会按既定方案直接完成并提交第一条 contribution。"}},
					},
				},
				"usage": map[string]any{"input_tokens": 100, "output_tokens": 30, "total_tokens": 130},
			})
		case 2:
			secondCallBody.Store(body)
			// After the nudge, model emits an actual tool call (glob)
			// and the loop proceeds. We end with no tool calls on the
			// 3rd round to close the turn.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_2",
				"output": []map[string]any{
					{"type": "function_call", "call_id": "tc_glob", "name": "glob", "arguments": `{"pattern":"*.go"}`},
				},
				"usage": map[string]any{"input_tokens": 120, "output_tokens": 8, "total_tokens": 128},
			})
		case 3:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_3",
				"output": []map[string]any{
					{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "Done."}}},
				},
				"usage": map[string]any{"input_tokens": 130, "output_tokens": 3, "total_tokens": 133},
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
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot get to work"}`))
	time.Sleep(500 * time.Millisecond)
	frames, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	if got := llmCallCount.Load(); got != 3 {
		t.Fatalf("expected 3 LLM calls (text-only + nudge round + closing), got %d", got)
	}

	rawBody, _ := secondCallBody.Load().([]byte)
	if len(rawBody) == 0 {
		t.Fatal("did not capture second LLM call body")
	}
	if !strings.Contains(string(rawBody), "described future action") {
		t.Errorf("second LLM call body should contain the future-tense reminder, got:\n%s", string(rawBody))
	}

	var (
		sawGlobCall bool
		doneFrames  int
	)
	for _, f := range frames {
		if f.Kind == "tool_call" && f.Name == "glob" {
			sawGlobCall = true
		}
		if f.Kind == "done" {
			doneFrames++
		}
	}
	if !sawGlobCall {
		t.Error("expected a tool_call for glob after the nudge")
	}
	if doneFrames != 1 {
		t.Errorf("expected exactly 1 done frame, got %d", doneFrames)
	}
}

// TestLoopFutureTenseNudgeOnce verifies the per-turn one-shot cap:
// if the model keeps producing future-tense text after the nudge, the
// loop closes the turn on the second offense instead of looping
// forever.
func TestLoopFutureTenseNudgeOnce(t *testing.T) {
	t.Parallel()

	var llmCallCount atomic.Int32
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		llmCallCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Every call returns the same future-tense narration with no
		// tool call. After the first nudge, the loop should close the
		// turn rather than nudging again forever.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_promise",
			"output": []map[string]any{
				{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "继续中，下一步我会去查代码。"}}},
			},
			"usage": map[string]any{"input_tokens": 50, "output_tokens": 10, "total_tokens": 60},
		})
	}))
	t.Cleanup(llmServer.Close)

	llmClient := llm.New(llmServer.URL, "test-token")
	bundle := local.Build()
	registry := tools.Build(bundle.Tools, nil, nil, []string{"*"})

	h := startLoop(t, llmClient, registry, bundle.Async)
	h.fake.pushEvent("issue.comment.mentioned", json.RawMessage(`{"body":"@bot work"}`))
	time.Sleep(500 * time.Millisecond)
	_, err := h.shutdown(t)
	if err != nil {
		t.Fatalf("loop returned error: %v", err)
	}
	// initial + 1 retry after nudge, then the loop gives up — 2 total
	if got := llmCallCount.Load(); got != 2 {
		t.Errorf("expected exactly 2 LLM calls (one-shot nudge cap), got %d", got)
	}
}

// Ensure context import survives in case helpers ever drop it.
var _ = context.Background
