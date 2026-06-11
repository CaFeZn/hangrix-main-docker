// helpers.go is the home of package-level helpers shared between the
// two Connect handlers in this package:
//
//   - agent_connect.go's history-replay path needs the text-rendering
//     functions that turn a persisted message row back into the
//     LLM-visible shape it was when the agent first emitted it.
//
//   - runner_connect.go's Tasks RPC needs decoders for the frozen
//     role_config snapshot the agent_session row carries, plus the
//     trigger-actor mapper that pulls the human-readable
//     pusher/author out of a workflow run's trigger payload.
//
// These functions were originally inlined into the legacy chi-based
// agent.go; they survived the Connect cutover because the underlying
// persistence shape didn't change — the wire just got swapped from
// JSON-over-HTTP to protobuf-over-Connect.
package handler

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"

	workflowdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/pkg/actor"
)

// ---- History-replay text rendering ----
//
// Used by agent_connect.go.messageToHistoryItemsProto when serving
// FetchHistory: a persisted event-kind row's `content` is rendered on
// the fly from its stored `payload` so the rendered string the LLM
// sees on replay reads byte-identically to what it saw on the
// original live wake.

// renderHistoryEvent mirrors the agent loop's renderEventMessage so a
// replayed event reads byte-identically to a freshly-delivered one.
// The stored payload is the full inbound frame; we extract
// event/payload and remap questionnaire causes (issue.comment +
// cause_kind=questionnaire_*) to questionnaire.* the same way the
// live path does.
func renderHistoryEvent(eventName string, framePayload []byte) string {
	var inner struct {
		Payload json.RawMessage `json:"payload"`
	}
	if len(framePayload) > 0 {
		_ = json.Unmarshal(framePayload, &inner)
	}
	if kind := questionnaireRemap(inner.Payload); kind != "" {
		eventName = kind
	}
	payloadStr := "{}"
	if len(inner.Payload) > 0 {
		var p any
		if err := json.Unmarshal(inner.Payload, &p); err == nil {
			if compact, err := json.Marshal(p); err == nil {
				var buf strings.Builder
				_ = xml.EscapeText(&buf, compact)
				payloadStr = buf.String()
			}
		}
	}
	return fmt.Sprintf(
		`<hangrix-event kind="platform.%s"><payload>%s</payload></hangrix-event>`,
		eventName, payloadStr,
	)
}

// questionnaireRemap is the server-side twin of the agent loop's
// questionnaireEventKind: a questionnaire answer/close lands as
// issue.comment with cause_kind embedded in the payload, but the LLM
// should see it labelled as questionnaire.answered / questionnaire.closed.
func questionnaireRemap(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var evt struct {
		CauseKind string `json:"cause_kind"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		return ""
	}
	switch evt.CauseKind {
	case "questionnaire_answered":
		return "questionnaire.answered"
	case "questionnaire_closed":
		return "questionnaire.closed"
	}
	return ""
}

// extractToolResult pulls the result blob out of a stored tool_call
// payload and returns it as the JSON text the agent's loop fed into
// AppendToolResult on the live path. Falls back to "null" when the
// payload is missing or malformed — the LLM tolerates a null result
// better than a missing tool message would (the latter would leave an
// orphan assistant(tool_calls) entry).
func extractToolResult(payload []byte) string {
	if len(payload) == 0 {
		return "null"
	}
	var p struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "null"
	}
	if len(p.Result) == 0 {
		return "null"
	}
	return string(p.Result)
}

// extractCompactSummary returns the summary the LLM passed into a
// compact_session tool_call. Empty string when the args are missing
// or don't parse — the caller falls back to treating the row as a
// normal tool result so the anchor logic degrades gracefully on
// malformed data.
func extractCompactSummary(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var p struct {
		Args struct {
			Summary string `json:"summary"`
		} `json:"args"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.Args.Summary)
}

// ---- role_config snapshot decoders ----
//
// These decode the frozen JSON written by
// agent_session/service.buildRoleSnapshot. They feed into
// runner_connect.go's AgentSessionTask assembly. The DTO types
// (agentBuildSpec / volumeDTO / workflowStepDTO) are the intermediate
// Go representation between "JSON in the DB" and "proto on the wire"
// — convenient because the json:"…" tags map straight to the column
// shape, so the decoders don't have to learn the proto layout.

// agentBuildSpec mirrors agentsconfig.Build on the JSON-snapshot
// shape. The runner runs `docker build -f <Dockerfile> -t
// <agent_image> [--build-arg K=V ...] <context>` when this is set.
// Empty / absent means the runner pulls / uses `agent_image` as-is.
type agentBuildSpec struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// volumeDTO mirrors agentsconfig.Volume on the JSON-snapshot shape.
type volumeDTO struct {
	Name  string `json:"name"`
	Mount string `json:"mount"`
}

// workflowStepDTO mirrors the frozen workflowdb.WorkflowJobRun.steps
// shape so the Connect handler can decode it into proto WorkflowStep.
type workflowStepDTO struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	// Shell-step fields (type "" / "run").
	Run string            `json:"run,omitempty"`
	Env map[string]string `json:"env,omitempty"`
	Dir string            `json:"dir,omitempty"`
	// With carries typed-step params (e.g. release tag/notes/assets),
	// decoded per step kind on the runner side.
	With map[string]any `json:"with,omitempty"`
}

func extractEntrypoint(roleConfig []byte) []string {
	if len(roleConfig) == 0 {
		return nil
	}
	var snap struct {
		Container struct {
			Entrypoint []string `json:"entrypoint"`
		} `json:"container"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return nil
	}
	if len(snap.Container.Entrypoint) == 0 {
		return nil
	}
	return snap.Container.Entrypoint
}

func extractVolumes(roleConfig []byte) []volumeDTO {
	if len(roleConfig) == 0 {
		return nil
	}
	var snap struct {
		Container struct {
			Volumes []volumeDTO `json:"volumes"`
		} `json:"container"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return nil
	}
	return snap.Container.Volumes
}

func extractMcpServers(roleConfig []byte) []string {
	if len(roleConfig) == 0 {
		return nil
	}
	var snap struct {
		McpServers []string `json:"mcp_servers"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return nil
	}
	return snap.McpServers
}

// extractLLMReasoningEffort returns "" when absent — the agent omits
// `reasoning.effort` on the wire and the upstream applies its
// model-default thinking budget.
func extractLLMReasoningEffort(roleConfig []byte) string {
	if len(roleConfig) == 0 {
		return ""
	}
	var snap struct {
		LLMReasoningEffort string `json:"llm_reasoning_effort"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return ""
	}
	return snap.LLMReasoningEffort
}

// extractLLMThinking returns "" when absent — the agent omits
// `thinking` on the wire and the upstream applies its default
// (extended thinking off for Claude 4.7/4.8).
func extractLLMThinking(roleConfig []byte) string {
	if len(roleConfig) == 0 {
		return ""
	}
	var snap struct {
		LLMThinking string `json:"llm_thinking"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return ""
	}
	return snap.LLMThinking
}

func extractBuild(roleConfig []byte) *agentBuildSpec {
	if len(roleConfig) == 0 {
		return nil
	}
	var snap struct {
		Container struct {
			Build *agentBuildSpec `json:"build"`
		} `json:"container"`
	}
	if err := json.Unmarshal(roleConfig, &snap); err != nil {
		return nil
	}
	if snap.Container.Build == nil || snap.Container.Build.Dockerfile == "" {
		return nil
	}
	return snap.Container.Build
}

// ---- workflow trigger actor mapping ----

// triggerActorFromRun resolves (kind, id, display_name) for the actor
// who caused a workflow run, used to surface HANGRIX_TRIGGER_ACTOR_*
// env vars to the running job. For push events it reads
// pusher_user_id / pusher_agent_role from the trigger payload; for
// other event types it falls back to system.
func triggerActorFromRun(run *workflowdomain.WorkflowRun) (kind, id, display string) {
	if len(run.TriggerPayloadJSON) == 0 {
		return string(actor.KindSystem), "system:server", "System"
	}
	var payload struct {
		PusherUserID    int64  `json:"pusher_user_id"`
		PusherAgentRole string `json:"pusher_agent_role"`
		AuthorID        int64  `json:"author_id"`
		AuthorUsername  string `json:"author_username"`
		AgentRole       string `json:"agent_role"`
	}
	if err := json.Unmarshal(run.TriggerPayloadJSON, &payload); err != nil {
		return string(actor.KindSystem), "system:server", "System"
	}
	if payload.PusherAgentRole != "" {
		ref := actor.AgentRef(payload.PusherAgentRole)
		return string(ref.Kind), ref.ID, ref.DisplayName
	}
	if payload.PusherUserID > 0 {
		ref := actor.UserRef(payload.PusherUserID, "")
		return string(ref.Kind), ref.ID, ref.DisplayName
	}
	if payload.AgentRole != "" {
		ref := actor.AgentRef(payload.AgentRole)
		return string(ref.Kind), ref.ID, ref.DisplayName
	}
	if payload.AuthorID > 0 {
		display := payload.AuthorUsername
		ref := actor.UserRef(payload.AuthorID, display)
		return string(ref.Kind), ref.ID, ref.DisplayName
	}
	return string(actor.KindSystem), "system:server", "System"
}
