package runtime

import (
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/llm"
)

// TestTrimTrailingDanglingToolCalls covers the guard that drops trailing
// assistant(tool_calls=…) messages that have no corresponding tool messages
// after them — the classic "container crashed mid-turn in a previous wake"
// scenario that causes upstream 400 errors.
func TestTrimTrailingDanglingToolCalls(t *testing.T) {
	t.Parallel()

	tcs := []struct {
		name string
		in   []llm.Message
		want int // expected len after trim
	}{
		{
			name: "empty",
			in:   nil,
			want: 0,
		},
		{
			name: "clean assistant no tool calls",
			in: []llm.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi there"},
			},
			want: 2,
		},
		{
			name: "clean assistant with tool calls + tool results",
			in: []llm.Message{
				{Role: "user", Content: "check file"},
				{Role: "assistant", Content: "checking", ToolCalls: []llm.ToolCall{{ID: "tc_1", Name: "read"}}},
				{Role: "tool", ToolCallID: "tc_1", Content: "contents"},
				{Role: "assistant", Content: "done"},
			},
			want: 4,
		},
		{
			name: "dangling tool_use at end — trimmed",
			in: []llm.Message{
				{Role: "user", Content: "check file"},
				{Role: "assistant", Content: "checking", ToolCalls: []llm.ToolCall{{ID: "tc_1", Name: "read"}}},
			},
			want: 1, // only the user message remains
		},
		{
			name: "dangling tool_use with preceding event — trimmed",
			in: []llm.Message{
				{Role: "user", Content: "event 1"},
				{Role: "assistant", Content: "done 1"},
				{Role: "user", Content: "event 2"},
				{Role: "assistant", Content: "working", ToolCalls: []llm.ToolCall{{ID: "tc_broken", Name: "read"}}},
			},
			want: 3, // drops the dangling assistant; event 2 stays (it's user)
		},
		{
			name: "multiple dangling tool_uses at end — all trimmed",
			in: []llm.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "first", ToolCalls: []llm.ToolCall{{ID: "a", Name: "read"}}},
				{Role: "tool", ToolCallID: "a", Content: "ok"},
				{Role: "assistant", Content: "second", ToolCalls: []llm.ToolCall{{ID: "b", Name: "glob"}}},
				{Role: "assistant", Content: "third", ToolCalls: []llm.ToolCall{{ID: "c", Name: "grep"}}},
			},
			want: 3, // user + first assistant + first tool remain; second/third are dangling
		},
		{
			name: "compact_session summary at end — no trim",
			in: []llm.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "compacting", ToolCalls: []llm.ToolCall{{ID: "cs", Name: "compact_session"}}},
				{Role: "user", Kind: llm.KindSummary, Content: "summary here"},
			},
			want: 3, // last message is user (summary), not assistant, so nothing trimmed
		},
	}

	for _, tc := range tcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := trimTrailingDanglingToolCalls(tc.in)
			if len(got) != tc.want {
				t.Errorf("len=%d, want %d; got=%+v", len(got), tc.want, got)
			}
		})
	}
}
