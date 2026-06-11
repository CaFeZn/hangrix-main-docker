// agent_run.go builds and creates the hidden internal `_agent` workflow run
// that the agent-session spawner kicks off on every wake.
//
// Why a workflow at all (vs the old runner-claims-a-session model):
//
//   - The same container/orchestration code path that runs CI jobs now
//     runs agent sessions. One runner driver, one mental model.
//   - Removes the bespoke runner→agent IPC: the agent talks to the
//     platform directly via `/api/agent/sessions/{id}/*` using its
//     session token. Runner only starts and stops the container.
//
// Each wake = one fresh container = one workflow_run. The agent reads
// /history, drains /inputs (the first frame is the trigger cause the
// spawner enqueued), runs to done, POSTs /idle, exits. The workflow_job
// completes when the agent process exits.
package service

import (
	"context"
	"strconv"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/workflowsconfig"
	"github.com/hangrix/hangrix/pkg/actor"
)

const (
	// agentBinVolumeName is the reserved volume name the runner recognises
	// as a request to bind-mount its embedded agent binary directory. Kept
	// in sync with `reservedAgentBinVolume` in apps/hangrix-runner/internal/
	// loop/session.go — the runner side is the source of truth for the
	// host path; the platform side just knows the convention name.
	agentBinVolumeName = "hangrix-agent"

	// agentBinMountPath is the in-container directory where the runner
	// bind-mounts the agent binary. The internal workflow's step runs
	// `<agentBinMountPath>/hangrix-agent`.
	agentBinMountPath = "/opt/hangrix"
)

// AgentRunSpec is the spawner→workflow handoff for one agent wake. Mirrors
// the role-snapshot env vars the runner used to inject on session start;
// they now ride on the workflow job step's env so the agent's
// HANGRIX_PLATFORM_BASE_URL-derived calls work the same way they did
// before the cutover.
type AgentRunSpec struct {
	// Repo identifies the host repository the agent is running for.
	Repo Ref

	// Container is the host repo's container spec from agents.yml
	// (image/build/entrypoint/env/volumes). The factory augments Volumes
	// with the reserved `hangrix-agent` mount before handing the snapshot
	// to CreateRun. CommitSHA pins the spec the workflow_run records.
	Container *agentsconfig.Container

	// CommitSHA is the host repo HEAD the spawner resolved when picking
	// up the role config. Stored on workflow_runs.commit_sha for audit.
	CommitSHA string

	// SessionID is the agent_sessions.id the agent will identify itself
	// with (HANGRIX_SESSION_ID). The session token authenticates against
	// this row.
	SessionID int64

	// SessionToken is the plaintext hgxs_ token the spawner minted (or
	// recovered, on rewake) for this session. Injected as
	// HANGRIX_SESSION_TOKEN. Never persisted in plaintext on workflow rows
	// — env_json stores the literal value but the row carries no other
	// long-lived secret, so the same operator-only access controls apply
	// as for any workflow env block.
	SessionToken string

	// RoleKey is the agent role key (e.g. "backend"). Injected as
	// HANGRIX_ROLE_KEY for the agent's identity / prompt resolution.
	RoleKey string

	// IssueNumber is the per-repo issue number this wake is bound to
	// (0 for repoless / smoke paths). Injected as HANGRIX_ISSUE_NUMBER.
	IssueNumber int32

	// CauseID is the upstream-event id the workflow_run references via
	// workflow_runs.cause_id. Same value the runner-relay model put on
	// agent_sessions.cause_id.
	CauseID *int64

	// CauseKind is the upstream-event category ("issue.comment",
	// "review_vote.posted", ...). Injected as HANGRIX_CAUSE_KIND so the
	// agent can render the cause in its own prompt.
	CauseKind string

	// CauseIDStr is the string form of CauseID the spawner already
	// computed (TriggerInput.CauseID — comment id, sha, etc.). Kept
	// separately because the workflow row only takes int64 ids.
	CauseIDStr string

	// WorkingBranch / BaseBranch are the per-issue git branches the
	// agent operates on. Mirror today's HANGRIX_WORKING_BRANCH /
	// HANGRIX_BASE_BRANCH env vars.
	WorkingBranch string
	BaseBranch    string

	// LLMModel / LLMReasoningEffort / LLMThinking carry the resolved
	// per-role LLM config. The agent reads these from HANGRIX_LLM_* env
	// vars and forwards to its proxy client.
	LLMModel            string
	LLMReasoningEffort  string
	LLMThinking         string

	// McpServers is the comma-joined list of MCP server names the role
	// declared. Empty string when none.
	McpServersCSV string

	// RepoOwner / RepoName / RepoFullName are the host repo identifiers
	// the agent's platform-tool clients use. Mirror HANGRIX_HOST_*.
	RepoOwner    string
	RepoName     string
	RepoFullName string

	// RepoSHA mirrors HANGRIX_REPO_SHA — the host-config commit the
	// session was spawned against.
	RepoSHA string

	// RepoPermission is the role's coarse read/write level. The agent
	// uses HANGRIX_REPO_PERMISSION to hide write tools when read-only.
	RepoPermission string

	// PlatformToolsJSON is the JSON-marshalled glob whitelist the role
	// declared (HANGRIX_PLATFORM_TOOLS). Empty string when none.
	PlatformToolsJSON string

	// Git author identity (HANGRIX_GIT_*) the agent's bash tool uses for
	// `git commit`. The spawner derives these from the role key.
	GitAuthorName     string
	GitAuthorEmail    string
	GitCommitterName  string
	GitCommitterEmail string

	// HostContainerEnv is the host repo's container.env merged with
	// per-role overrides — the agent inherits this verbatim (e.g.
	// NODE_ENV=development, OPENAI_API_KEY=${OPENAI_API_KEY}).
	HostContainerEnv map[string]string

	// RolePrompt is the Markdown body of the host repo's
	// `.hangrix/agents/<role>.md` (or the inline `prompt:` from
	// agents.yml), already resolved by LoadHostConfig. Injected into
	// the agent container as HANGRIX_ROLE_PROMPT so the agent's prompt
	// assembler can splice it under the baseline.md layer. Empty when
	// the role has no addendum.
	RolePrompt string

	// TriggerActor is the actor who triggered this wake; passed through
	// to workflow_runs.trigger_actor_*.
	TriggerActor actor.Ref
}

// BuildAgentWorkflowConfig assembles the in-memory WorkflowConfig that
// hosts one agent wake. Exposed for tests; production callers should go
// through CreateAgentRun.
func BuildAgentWorkflowConfig(spec AgentRunSpec) *workflowsconfig.WorkflowConfig {
	env := agentJobEnv(spec)
	return &workflowsconfig.WorkflowConfig{
		Name:       domain.InternalAgentWorkflowName,
		SourceFile: "<internal:_agent>",
		On: []workflowsconfig.EventTrigger{
			{Event: workflowsconfig.EventAgentWake},
		},
		Jobs: []workflowsconfig.JobDefinition{
			{
				Key:         "agent",
				DisplayName: "Agent",
				Steps: []workflowsconfig.StepDefinition{
					{
						Id:   "configure-git-credentials",
						Name: "Configure git credentials",
						Type: workflowsconfig.StepTypeRun,
						// Overwrites the per-host credential.helper the runner's
						// workflow clone wrote (which reads HANGRIX_WORKFLOW_TOKEN
						// — workflow tokens are read-only server-side, so any
						// agent `git push` would 403). Re-points it at
						// HANGRIX_SESSION_TOKEN so pushes flow as the session
						// identity, which the contribution-branch ACL accepts
						// (refs/heads/issue-<N>/<role>[/<slug>]).
						//
						// The token reference stays inside single quotes so the
						// inline helper is what lands in .git/config; git
						// re-evaluates the function on every credential request,
						// so a future rewake injecting a new token is picked up
						// automatically — no .git/config rewrite needed.
						Run: `git config credential."$HANGRIX_PLATFORM_BASE_URL".helper '!f() { echo username=x; echo "password=$HANGRIX_SESSION_TOKEN"; }; f'`,
						Env: env,
					},
					{
						Id:   "run",
						Name: "Run agent",
						Type: workflowsconfig.StepTypeRun,
						Run:  agentBinMountPath + "/hangrix-agent",
						Env:  env,
					},
				},
			},
		},
	}
}

// agentJobEnv mints the HANGRIX_* env block the agent reads at boot.
// Omits empty values so the JSON-stored env stays compact and the agent's
// `if val := os.Getenv(...); val != ""` checks stay correct.
func agentJobEnv(spec AgentRunSpec) map[string]string {
	env := map[string]string{}
	put := func(k, v string) {
		if v == "" {
			return
		}
		env[k] = v
	}

	put("HANGRIX_SESSION_ID", strconv.FormatInt(spec.SessionID, 10))
	put("HANGRIX_SESSION_TOKEN", spec.SessionToken)
	put("HANGRIX_ROLE", spec.RoleKey)
	put("HANGRIX_ROLE_KEY", spec.RoleKey)
	if spec.IssueNumber > 0 {
		put("HANGRIX_ISSUE_NUMBER", strconv.FormatInt(int64(spec.IssueNumber), 10))
	}
	put("HANGRIX_CAUSE_KIND", spec.CauseKind)
	put("HANGRIX_CAUSE_ID", spec.CauseIDStr)
	put("HANGRIX_WORKING_BRANCH", spec.WorkingBranch)
	put("HANGRIX_BASE_BRANCH", spec.BaseBranch)
	put("HANGRIX_LLM_MODEL", spec.LLMModel)
	put("HANGRIX_LLM_REASONING_EFFORT", spec.LLMReasoningEffort)
	put("HANGRIX_LLM_THINKING", spec.LLMThinking)
	put("HANGRIX_MCP_SERVERS", spec.McpServersCSV)
	// Role prompt rides as an env var rather than a bind-mounted file
	// (the legacy HANGRIX_HOST_ADDENDUM path) because the runner side
	// has no agent-specific mount machinery post-cutover — the agent
	// is just a workflow_job step. Markdown role prompts are typically
	// <10KB; well under the Linux env-size limit.
	put("HANGRIX_ROLE_PROMPT", spec.RolePrompt)
	put("HANGRIX_HOST_OWNER", spec.RepoOwner)
	put("HANGRIX_HOST_NAME", spec.RepoName)
	put("HANGRIX_HOST_REPO", spec.RepoFullName)
	put("HANGRIX_REPO_SHA", spec.RepoSHA)
	put("HANGRIX_REPO_PERMISSION", spec.RepoPermission)
	put("HANGRIX_PLATFORM_TOOLS", spec.PlatformToolsJSON)
	put("GIT_AUTHOR_NAME", spec.GitAuthorName)
	put("GIT_AUTHOR_EMAIL", spec.GitAuthorEmail)
	put("GIT_COMMITTER_NAME", spec.GitCommitterName)
	put("GIT_COMMITTER_EMAIL", spec.GitCommitterEmail)
	// Host container env merges UNDER role-injected HANGRIX_* keys: any
	// HANGRIX_-prefixed override in the user yaml is honoured, but the
	// session-identity keys above are authoritative.
	for k, v := range spec.HostContainerEnv {
		if _, locked := env[k]; locked {
			continue
		}
		env[k] = v
	}
	return env
}

// withAgentBinVolume returns a copy of the host container spec augmented
// with the reserved `hangrix-agent` volume. Idempotent: if the host yaml
// already declared the same volume name, the existing entry wins and the
// reserved bind mount is skipped (operators may want a custom mount
// path — runner-side recognition is name-only, so any mount works).
func withAgentBinVolume(in *agentsconfig.Container) *agentsconfig.Container {
	if in == nil {
		// Defensive — CreateAgentRun should always pass a valid container.
		return &agentsconfig.Container{
			Volumes: []agentsconfig.Volume{{Name: agentBinVolumeName, Mount: agentBinMountPath}},
		}
	}
	out := *in
	out.Volumes = append([]agentsconfig.Volume(nil), in.Volumes...)
	for _, v := range out.Volumes {
		if v.Name == agentBinVolumeName {
			return &out
		}
	}
	out.Volumes = append(out.Volumes, agentsconfig.Volume{
		Name:  agentBinVolumeName,
		Mount: agentBinMountPath,
	})
	return &out
}

// CreateAgentRun is the spawner-facing entry point: build the hidden
// workflow config, augment the container with the agent-binary volume,
// and hand off to CreateRun. Returns the (run, jobs) pair so the spawner
// can log them and (optionally) link the workflow_run id back onto the
// agent_session row.
func (s *Service) CreateAgentRun(ctx context.Context, spec AgentRunSpec) (*domain.WorkflowRun, []*domain.WorkflowJobRun, error) {
	cfg := BuildAgentWorkflowConfig(spec)
	container := withAgentBinVolume(spec.Container)
	// The hidden workflow's run actor is itself the agent role — that
	// keeps the audit trail readable ("workflow:_agent triggered by
	// user:42 → agent backend acts on issue X") instead of a generic
	// "workflow:_agent" tag.
	runActor := actor.AgentRef(spec.RoleKey)
	return s.CreateRun(ctx, CreateRunParams{
		Repo:         spec.Repo,
		Config:       cfg,
		EventName:    workflowsconfig.EventAgentWake,
		CauseID:      spec.CauseID,
		Ref:          spec.WorkingBranch,
		CommitSHA:    spec.CommitSHA,
		Container:    container,
		TriggerActor: spec.TriggerActor,
		RunActor:     runActor,
	})
}
