package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// defaultAnthropicBaseURL is the canonical Anthropic Messages host.
// Used when a provider row of type `anthropic` has an empty BaseURL.
const defaultAnthropicBaseURL = "https://api.anthropic.com"

// anthropicAPIVersion is the wire version we negotiate. Hard-coded
// because translating across versions is not in scope today; bumping
// it is a deliberate code change with a translator review.
const anthropicAPIVersion = "2023-06-01"

// defaultMaxTokens applies when the caller omits max_output_tokens.
// Anthropic requires max_tokens to be set; OpenAI's Responses API
// treats it as optional. 4096 is a safe ceiling for short interactive
// turns; thinking-enabled requests bump this in buildAnthropicBody.
const defaultMaxTokens = 4096

// Anthropic talks to upstreams that speak Anthropic's Messages API
// (POST /v1/messages). Translates a typed Request into Messages shape
// and back.
//
// Thinking shapes are supported:
//
//   - Request.Thinking == "adaptive" — emit `thinking: {type:
//     "adaptive"}` (Claude 4.6+ canonical, the only mode Opus 4.7/4.8
//     accepts). Effort is forwarded as `output_config.effort`.
//     This is the RECOMMENDED path for all currently-supported models.
//   - Request.Thinking == "enabled" — emit legacy `thinking: {type:
//     "enabled", budget_tokens: N}` with N derived from
//     ReasoningEffort. DEPRECATED — rejected by Opus 4.7/4.8,
//     deprecated on 4.6. Kept only for older Claudes (Opus 4.5, …).
//     New integrations should use "adaptive" instead.
//     max_tokens is bumped above the budget.
//
// "disabled" / "" omit the `thinking` field. `output_config.effort` is
// still emitted whenever ReasoningEffort is non-empty AND Thinking is
// not "enabled" (the legacy budget_tokens path predates output_config).
//
// `temperature` is dropped on "adaptive" and "enabled" paths because
// extended thinking on Claude disallows it. On "disabled" / "", the
// model is in normal (non-thinking) mode so temperature is left intact.
// Tool calling is fully bidirectional: Request.Tools become the `tools` array; prior
// KindToolCall / KindToolResult input items materialise as
// (assistant.tool_use, user.tool_result) content blocks; upstream
// tool_use blocks decode back into Response.ToolCalls so the rest of
// the agent pipeline sees the same shape as OpenAI-style providers.
type Anthropic struct{}

func NewAnthropic() *Anthropic { return &Anthropic{} }

func (*Anthropic) Type() domain.ProviderType { return domain.ProviderTypeAnthropic }

func (*Anthropic) Respond(ctx context.Context, req *Request) (*Response, error) {
	base := strings.TrimRight(req.BaseURL, "/")
	if base == "" {
		base = defaultAnthropicBaseURL
	}
	body, err := json.Marshal(buildAnthropicBody(req))
	if err != nil {
		return nil, fmt.Errorf("encode anthropic request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	// Anthropic uses x-api-key + anthropic-version; do NOT set
	// Authorization: Bearer (different scheme, would just be ignored
	// but could leak the key through their access logs).
	httpReq.Header.Set("x-api-key", req.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := req.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call anthropic: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Message: anthropicErrorMessage(raw), Raw: raw}
	}
	return parseAnthropicBody(raw, resp.StatusCode)
}

// anthropicRequest / anthropicMessage / anthropicContentBlock are the
// typed shapes the /v1/messages wire expects. Thinking and OutputConfig
// are optional (pointers) so an unrequested extended-thinking session
// doesn't carry empty objects on the wire.
type anthropicRequest struct {
	Model        string                 `json:"model"`
	System       string                 `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	MaxTokens    int                    `json:"max_tokens"`
	Temperature  *float64               `json:"temperature,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

// anthropicContentBlock covers every block type Anthropic accepts on
// the request and response side: text, thinking (with optional
// signature), tool_use (assistant's request to call a tool), and
// tool_result (caller's reply to a prior tool_use). Per-kind fields
// use omitempty so a text block doesn't carry empty tool_use fields
// and vice versa.
type anthropicContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// tool_use blocks (assistant → caller). ID is the toolu_ handle
	// the matching tool_result must echo back. Input is the tool's
	// JSON-object argument — RawMessage so the agent's already-JSON
	// argument string round-trips as a nested object on the wire,
	// not as a re-quoted string.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result blocks (caller → assistant). ToolUseID points back
	// to the tool_use this is answering; Content is the textual
	// payload the tool produced.
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// anthropicTool mirrors the entry shape in the request's `tools`
// array. Note `input_schema` not `parameters` — Anthropic uses its
// own JSON-Schema field name.
type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// anthropicThinking is the `thinking` block.
// Type == "adaptive" is the RECOMMENDED path (Claude 4.6+).
// Type == "enabled" with BudgetTokens is DEPRECATED — rejected by
// Opus 4.7/4.8, deprecated on 4.6. Kept only for legacy Opus 4.5.
// For "adaptive", BudgetTokens is omitted via omitempty.
type anthropicThinking struct {
	Type string `json:"type"`
	// BudgetTokens is DEPRECATED. Only meaningful with Type == "enabled"
	// (legacy manual mode); for Type == "adaptive" it's omitted.
	BudgetTokens int `json:"budget_tokens,omitempty"`
}

// anthropicOutputConfig is the Claude 4.6+ `output_config` block. Today
// the only field is Effort (low / medium / high / xhigh / max);
// future knobs (verbosity, format hints) add fields here.
type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

// buildAnthropicBody emits an Anthropic Messages request body. Four
// translation points worth calling out:
//
//   - Instructions → `system` field (top-level, not a message).
//   - Request.Thinking selects the thinking shape:
//     "adaptive" → `thinking: {type: "adaptive"}` (Claude 4.6+
//     canonical — RECOMMENDED for all currently-supported models);
//     "enabled" → DEPRECATED legacy `thinking: {type: "enabled",
//     budget_tokens: N}` with N from ReasoningEffort, and max_tokens
//     bumped above the budget (rejected by Opus 4.7/4.8);
//     "disabled" / "" → omit the `thinking` field, temperature intact.
//   - ReasoningEffort → `output_config.effort` (Claude 4.6+ canonical
//     way to control effort), emitted whenever effort is non-empty
//     AND we are NOT using the legacy budget_tokens path.
//   - InputItem array → flat Anthropic messages, with KindReasoning
//     items folded into the next assistant message as a `thinking`
//     content block so multi-turn conversations preserve prior
//     chain-of-thought.
//
// Temperature is dropped for "adaptive" and "enabled" thinking because
// Claude Opus 4.7+ returns 400 on non-default sampling while extended
// thinking is active. On "disabled" / "", the model is in normal mode
// and temperature is left intact.
func buildAnthropicBody(req *Request) anthropicRequest {
	maxTokens := defaultMaxTokens
	if req.MaxOutputTokens > 0 {
		maxTokens = req.MaxOutputTokens
	}
	body := anthropicRequest{
		Model:       req.Model,
		System:      req.Instructions,
		Messages:    inputItemsToAnthropicMessages(req.Input),
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
	}
	switch req.Thinking {
	case "adaptive":
		// Claude 4.6+ canonical: let the model decide when to think.
		// Effort guidance is emitted separately via output_config.
		body.Thinking = &anthropicThinking{Type: "adaptive"}
		body.Temperature = nil
	case "enabled":
		// DEPRECATED — legacy manual budget_tokens path.
		// Rejected by Opus 4.7/4.8; deprecated on 4.6.
		// Kept only for older Claudes (Opus 4.5, …).
		// New integrations should use "adaptive" instead.
		// See https://platform.claude.com/docs/en/build-with-claude/adaptive-thinking
		if budget := thinkingBudgetTokens(req.ReasoningEffort); budget > 0 {
			body.Thinking = &anthropicThinking{
				Type:         "enabled",
				BudgetTokens: budget,
			}
			// max_tokens must exceed budget_tokens with headroom for
			// the final answer. Bump if the current ceiling is too
			// tight.
			if minMax := budget + 4096; body.MaxTokens < minMax {
				body.MaxTokens = minMax
			}
		}
		body.Temperature = nil
	case "disabled":
		// Extended thinking is explicitly off → model is in normal
		// (non-thinking) mode where temperature is supported.
		// The thinking field is omitted (equivalent to {type: "disabled"}
		// per the Anthropic Messages API).
	}
	// output_config.effort is the Claude 4.6+ canonical effort knob.
	// Emit whenever the caller asked for an effort AND we are not on
	// the legacy budget_tokens path (the two are alternative ways to
	// say the same thing and double-counting confuses older models).
	if req.ReasoningEffort != "" && req.Thinking != "enabled" {
		body.OutputConfig = &anthropicOutputConfig{Effort: req.ReasoningEffort}
	}
	if len(req.Tools) > 0 {
		body.Tools = make([]anthropicTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Parameters
			if schema == nil {
				// Anthropic requires input_schema; fall back to the
				// permissive empty-object form so a poorly-described
				// upstream tool is still callable rather than 400-ing
				// the whole request.
				schema = map[string]any{"type": "object"}
			}
			body.Tools = append(body.Tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: schema,
			})
		}
	}
	return body
}

// thinkingBudgetTokens maps an OpenAI `reasoning.effort` enum to an
// Anthropic `thinking.budget_tokens` value for the legacy manual
// thinking shape (`thinking: {type: enabled}`).
//
// DEPRECATED: The budget_tokens path is rejected by Opus 4.7/4.8 and
// deprecated on 4.6. Use the adaptive path (`thinking: {type: adaptive}`)
// with `output_config.effort` instead.
// See https://platform.claude.com/docs/en/build-with-claude/adaptive-thinking
//
// Anthropic requires budget_tokens ≥ 1024 and strictly less than
// max_tokens. Unknown values (the parser allows arbitrary strings for
// upstream forward-compat) return 0 — thinking stays disabled rather
// than guessing a budget that might exceed max_tokens. xhigh and max are
// only reachable through the Claude 4.6+ `output_config.effort` knob
// (adaptive thinking handles them); the legacy path tops out at high.
func thinkingBudgetTokens(effort string) int {
	switch effort {
	case "minimal", "low":
		return 1024
	case "medium":
		return 4096
	case "high", "xhigh", "max":
		return 16384
	}
	return 0
}

// inputItemsToAnthropicMessages folds the flat InputItem array into
// the nested-content-block shape Anthropic expects.
//
// Two side-buckets run in parallel:
//
//   - pendingA: the assistant message under construction (zero or
//     more of: thinking, text, tool_use blocks, in spec-order). A
//     single assistant turn can contain many tool_use blocks
//     side-by-side with text, and Anthropic preserves the order.
//   - pendingU: the user message under construction. Accumulates
//     consecutive tool_result blocks (and optionally text) so a turn
//     that resolved N parallel tool_use calls becomes ONE user
//     message with N tool_result blocks — the protocol shape
//     Anthropic enforces.
//
// When the stream type switches direction (assistant → user or vice
// versa), the bucket on the other side flushes. A trailing
// flushA/flushU at the end emits anything left.
func inputItemsToAnthropicMessages(items []InputItem) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(items))

	var pendingA struct {
		active            bool
		text              string
		thinking          string
		thinkingSignature string
		toolUses          []anthropicContentBlock
	}
	flushA := func() {
		if !pendingA.active {
			return
		}
		blocks := []anthropicContentBlock{}
		if pendingA.thinking != "" {
			blocks = append(blocks, anthropicContentBlock{
				Type:      "thinking",
				Thinking:  pendingA.thinking,
				Signature: pendingA.thinkingSignature,
			})
		}
		if pendingA.text != "" {
			blocks = append(blocks, anthropicContentBlock{Type: "text", Text: pendingA.text})
		}
		blocks = append(blocks, pendingA.toolUses...)
		if len(blocks) > 0 {
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		}
		pendingA.active = false
		pendingA.text = ""
		pendingA.thinking = ""
		pendingA.thinkingSignature = ""
		pendingA.toolUses = nil
	}

	var pendingU []anthropicContentBlock
	flushU := func() {
		if len(pendingU) == 0 {
			return
		}
		out = append(out, anthropicMessage{Role: "user", Content: pendingU})
		pendingU = nil
	}

	for _, it := range items {
		switch it.Kind {
		case KindMessage:
			role := it.Role
			if role == "" {
				role = "user"
			}
			if role == "system" {
				// System content rides on the top-level `system`
				// field; we drop inline system messages rather than
				// pretending they're user content.
				continue
			}
			if role == "assistant" {
				flushU()
				pendingA.active = true
				pendingA.text += it.Text
				continue
			}
			// role == "user" (or any unknown role we treat as user).
			flushA()
			pendingU = append(pendingU, anthropicContentBlock{Type: "text", Text: it.Text})
		case KindReasoning:
			flushU()
			pendingA.active = true
			pendingA.thinking += it.Reasoning
			if it.ReasoningSignature != "" {
				pendingA.thinkingSignature = it.ReasoningSignature
			}
		case KindToolCall:
			flushU()
			pendingA.active = true
			input := json.RawMessage(strings.TrimSpace(it.ToolArgs))
			if len(input) == 0 || string(input) == "null" {
				// Anthropic requires input to be a JSON object even
				// when the tool takes no arguments. Empty-string from
				// the agent maps to `{}`; a literal "null" argument
				// string is also normalised so the upstream never sees
				// a non-object input on a tool_use block.
				input = json.RawMessage("{}")
			}
			pendingA.toolUses = append(pendingA.toolUses, anthropicContentBlock{
				Type:  "tool_use",
				ID:    it.ToolCallID,
				Name:  it.ToolName,
				Input: input,
			})
		case KindToolResult:
			flushA()
			pendingU = append(pendingU, anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: it.ToolCallID,
				Content:   it.ToolResult,
			})
		}
	}
	flushA()
	flushU()
	return stripTrailingAssistantAnthropic(out)
}

// stripTrailingAssistantAnthropic removes trailing assistant messages
// from the output slice so the conversation always ends with a user
// message. The Anthropic Messages API requires messages to alternate
// between user and assistant and to both start and end with a user
// message — ending with an assistant message is rejected.
func stripTrailingAssistantAnthropic(msgs []anthropicMessage) []anthropicMessage {
	for len(msgs) > 0 && msgs[len(msgs)-1].Role == "assistant" {
		msgs = msgs[:len(msgs)-1]
	}
	return msgs
}

// parseAnthropicBody decodes an Anthropic Messages response into a
// typed Response. content blocks split between Text (concatenated
// `text` blocks), Reasoning (concatenated `thinking` blocks), and
// ToolCalls (one per `tool_use` block, in order). Signed thinking
// captures the signature so the next turn can echo it back to satisfy
// Anthropic's strict-mode verification.
//
// Tool-use blocks carry their `input` as a JSON object on the wire;
// we re-encode it back to a string so it matches the OpenAI-shaped
// `function.arguments` the rest of the proxy / agent path expects.
func parseAnthropicBody(raw []byte, statusCode int) (*Response, error) {
	var wire struct {
		ID      string `json:"id"`
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		} `json:"content"`
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w (body=%s)", err, snippet(raw))
	}
	out := &Response{ID: wire.ID, Raw: raw, StatusCode: statusCode}
	out.Usage = Usage{
		PromptTokens:     int32(wire.Usage.Input),
		CompletionTokens: int32(wire.Usage.Output),
		TotalTokens:      int32(wire.Usage.Input + wire.Usage.Output),
	}
	var text, thinking strings.Builder
	for _, b := range wire.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			thinking.WriteString(b.Thinking)
			if b.Signature != "" {
				out.ReasoningSignature = b.Signature
			}
		case "tool_use":
			args := strings.TrimSpace(string(b.Input))
			if args == "" || args == "null" {
				args = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        b.ID,
				Name:      b.Name,
				Arguments: args,
			})
		}
	}
	out.Text = text.String()
	out.Reasoning = thinking.String()
	return out, nil
}

// anthropicErrorMessage extracts the human-readable message out of an
// Anthropic error envelope so UpstreamError surfaces something
// actionable instead of "upstream 400: ...".
func anthropicErrorMessage(raw []byte) string {
	if len(raw) == 0 {
		return "anthropic upstream error"
	}
	var obj struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Error.Message != "" {
		return obj.Error.Message
	}
	return snippet(raw)
}
