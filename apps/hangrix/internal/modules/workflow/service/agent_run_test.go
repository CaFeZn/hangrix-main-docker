package service

import (
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/workflowsconfig"
)

// agentRunStep returns the run step (Steps[1]) of the agent workflow.
// The first step is the credential-helper override; the second is the
// hangrix-agent invocation that the legacy tests targeted by Steps[0].
func agentRunStep(t *testing.T, cfg *workflowsconfig.WorkflowConfig) workflowsconfig.StepDefinition {
	t.Helper()
	if len(cfg.Jobs) != 1 || len(cfg.Jobs[0].Steps) != 2 {
		t.Fatalf("expected one job with two steps, got %+v", cfg.Jobs)
	}
	return cfg.Jobs[0].Steps[1]
}

func TestBuildAgentWorkflowConfig_Shape(t *testing.T) {
	spec := AgentRunSpec{
		SessionID:     42,
		SessionToken:  "hgxs_abc12345_secrethexsecrethexsecrethexsec01",
		RoleKey:       "backend",
		IssueNumber:   7,
		LLMModel:      "claude-opus-4-8",
		WorkingBranch: "issue/7",
		BaseBranch:    "main",
		RepoOwner:     "acme",
		RepoName:      "monorepo",
		RepoFullName:  "acme/monorepo",
	}
	cfg := BuildAgentWorkflowConfig(spec)
	if cfg.Name != domain.InternalAgentWorkflowName {
		t.Fatalf("name = %q, want %q", cfg.Name, domain.InternalAgentWorkflowName)
	}
	if len(cfg.On) != 1 || cfg.On[0].Event != workflowsconfig.EventAgentWake {
		t.Fatalf("triggers = %+v, want single _agent.wake", cfg.On)
	}
	step := agentRunStep(t, cfg)
	if step.Type != workflowsconfig.StepTypeRun {
		t.Errorf("step type = %q, want %q", step.Type, workflowsconfig.StepTypeRun)
	}
	if step.Run != "/opt/hangrix/hangrix-agent" {
		t.Errorf("step run = %q, want /opt/hangrix/hangrix-agent", step.Run)
	}
	wantEnv := map[string]string{
		"HANGRIX_SESSION_ID":     "42",
		"HANGRIX_SESSION_TOKEN":  spec.SessionToken,
		"HANGRIX_ROLE":           "backend",
		"HANGRIX_ROLE_KEY":       "backend",
		"HANGRIX_ISSUE_NUMBER":   "7",
		"HANGRIX_LLM_MODEL":      "claude-opus-4-8",
		"HANGRIX_WORKING_BRANCH": "issue/7",
		"HANGRIX_BASE_BRANCH":    "main",
		"HANGRIX_HOST_OWNER":     "acme",
		"HANGRIX_HOST_NAME":      "monorepo",
		"HANGRIX_HOST_REPO":      "acme/monorepo",
	}
	for k, v := range wantEnv {
		if got := step.Env[k]; got != v {
			t.Errorf("env[%q] = %q, want %q", k, got, v)
		}
	}
}

// TestBuildAgentWorkflowConfig_GitCredentialHelperStep pins the
// pre-run step that overwrites the per-host credential.helper the
// runner's workflow clone wrote (which reads HANGRIX_WORKFLOW_TOKEN —
// read-only server-side) so the agent's `git push` rides
// HANGRIX_SESSION_TOKEN instead. Without this step the agent's first
// push gets 403 forbidden from the git-receive-pack ACL.
func TestBuildAgentWorkflowConfig_GitCredentialHelperStep(t *testing.T) {
	cfg := BuildAgentWorkflowConfig(AgentRunSpec{SessionID: 1, RoleKey: "r"})
	if len(cfg.Jobs) != 1 || len(cfg.Jobs[0].Steps) < 2 {
		t.Fatalf("expected at least two steps, got %+v", cfg.Jobs)
	}
	prep := cfg.Jobs[0].Steps[0]
	if prep.Id != "configure-git-credentials" {
		t.Errorf("first step id = %q, want configure-git-credentials", prep.Id)
	}
	if !strings.Contains(prep.Run, "HANGRIX_PLATFORM_BASE_URL") {
		t.Errorf("prep.Run = %q, want it to reference HANGRIX_PLATFORM_BASE_URL", prep.Run)
	}
	if !strings.Contains(prep.Run, "HANGRIX_SESSION_TOKEN") {
		t.Errorf("prep.Run = %q, want HANGRIX_SESSION_TOKEN as the helper-resolved password", prep.Run)
	}
	if strings.Contains(prep.Run, "HANGRIX_WORKFLOW_TOKEN") {
		t.Errorf("prep.Run = %q, must NOT reference HANGRIX_WORKFLOW_TOKEN (workflow token is read-only)", prep.Run)
	}
}

func TestBuildAgentWorkflowConfig_OmitsEmpties(t *testing.T) {
	// Minimal spec — only the fields that must always be present. Empty
	// optional fields should not produce empty env entries.
	cfg := BuildAgentWorkflowConfig(AgentRunSpec{SessionID: 1, RoleKey: "r"})
	env := agentRunStep(t, cfg).Env
	for _, k := range []string{
		"HANGRIX_SESSION_TOKEN",
		"HANGRIX_ISSUE_NUMBER",
		"HANGRIX_LLM_MODEL",
		"HANGRIX_LLM_THINKING",
		"HANGRIX_MCP_SERVERS",
	} {
		if _, ok := env[k]; ok {
			t.Errorf("expected %q absent from env when spec field empty, got %+v", k, env)
		}
	}
}

func TestBuildAgentWorkflowConfig_HostEnvNeverOverridesIdentity(t *testing.T) {
	// A host yaml that sets HANGRIX_SESSION_TOKEN in container.env (either
	// accidentally or maliciously) MUST NOT shadow the spawner-injected
	// session token — that would let one agent impersonate any other.
	spec := AgentRunSpec{
		SessionID:    1,
		RoleKey:      "r",
		SessionToken: "hgxs_real_token",
		HostContainerEnv: map[string]string{
			"HANGRIX_SESSION_TOKEN": "hgxs_attacker_override",
			"NODE_ENV":              "development",
		},
	}
	env := agentRunStep(t, BuildAgentWorkflowConfig(spec)).Env
	if env["HANGRIX_SESSION_TOKEN"] != "hgxs_real_token" {
		t.Errorf("session token = %q, want spawner-injected value", env["HANGRIX_SESSION_TOKEN"])
	}
	if env["NODE_ENV"] != "development" {
		t.Errorf("NODE_ENV = %q, want host env passed through", env["NODE_ENV"])
	}
}

func TestWithAgentBinVolume_Injects(t *testing.T) {
	in := &agentsconfig.Container{
		Volumes: []agentsconfig.Volume{
			{Name: "pnpm-store", Mount: "/cache/pnpm"},
		},
	}
	out := withAgentBinVolume(in)
	if len(out.Volumes) != 2 {
		t.Fatalf("volumes len = %d, want 2", len(out.Volumes))
	}
	if out.Volumes[1].Name != "hangrix-agent" || out.Volumes[1].Mount != "/opt/hangrix" {
		t.Errorf("appended volume = %+v, want {hangrix-agent /opt/hangrix}", out.Volumes[1])
	}
	// Original must be untouched.
	if len(in.Volumes) != 1 {
		t.Errorf("input mutated: now has %d volumes", len(in.Volumes))
	}
}

func TestWithAgentBinVolume_HonoursUserOverride(t *testing.T) {
	// When the operator already declared the reserved name (perhaps with
	// a custom mount path), we should NOT append a second entry.
	in := &agentsconfig.Container{
		Volumes: []agentsconfig.Volume{
			{Name: "hangrix-agent", Mount: "/usr/local/lib/hangrix"},
		},
	}
	out := withAgentBinVolume(in)
	if len(out.Volumes) != 1 {
		t.Fatalf("expected no duplicate, got %+v", out.Volumes)
	}
	if out.Volumes[0].Mount != "/usr/local/lib/hangrix" {
		t.Errorf("user mount overwritten: %+v", out.Volumes[0])
	}
}
