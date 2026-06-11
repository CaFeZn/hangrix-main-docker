// Package prompt assembles the agent's system prompt from two layers:
//
//	<runtime context KVs>             ← role / branch / session metadata
//	<baseline.md>                     ← embedded into the binary
//	===== HOST ROLE PROMPT =====
//	<file at HANGRIX_HOST_ADDENDUM>   ← host yaml's roles.<key>.prompt or
//	                                    .prompt_file body, snapshotted at
//	                                    session-spawn and bind-mounted in.
//
// The runtime context block sits at the very top so the LLM has the
// immediate facts in the highest position the architecture allows.
package prompt

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

//go:embed baseline.md
var baselineMD string

// Inputs is what Assemble needs from env / runtime. We thread it through
// a struct rather than ten arguments because the caller (runtime) wants
// to pass these through unchanged from os.Getenv reads in main.
type Inputs struct {
	// HostAddendumBody is the resolved role prompt text
	// (HANGRIX_ROLE_PROMPT). When set, it's used verbatim and
	// HostAddendumPath is ignored — the workflow-driven dispatch path
	// delivers the addendum inline as an env var rather than as a
	// bind-mounted file.
	HostAddendumBody string

	// HostAddendumPath is the legacy file-mount path
	// (HANGRIX_HOST_ADDENDUM). Read only when HostAddendumBody is
	// empty. Kept so out-of-band callers that still mount a file keep
	// working; new callers should set HostAddendumBody instead.
	HostAddendumPath string

	// Runtime context surfaced at the top of the prompt. Deliberately
	// excludes runner internals (LLM / MCP endpoints, credential
	// material) — the agent reaches those services through pre-wired
	// clients and does not need their addresses to operate.
	Role            string
	HostRepo        string
	IssueNumber     string
	WorkingBranch   string
	BaseBranch      string
	SessionID       string
	PlatformBaseURL string
}

// Assembled bundles the final prompt with debug provenance the runtime
// can log. KeptLayers tells the caller which of {baseline, host} actually
// contributed — a missing layer is intentional only if the caller meant
// for it to be missing.
type Assembled struct {
	Prompt     string
	KeptLayers []string
}

// Assemble produces the final system prompt. Errors are returned only
// for problems the operator should see (e.g. host addendum path set but
// unreadable); a missing-but-not-required layer is just dropped.
func Assemble(in Inputs) (*Assembled, error) {
	var (
		buf  strings.Builder
		kept []string
	)

	// (1) Runtime context. Plain key/value lines so the LLM can grep them
	// without us inventing a structured schema it has to parse.
	buf.WriteString("# Hangrix runtime context\n\n")
	writeKV(&buf, "role", in.Role)
	writeKV(&buf, "session_id", in.SessionID)
	writeKV(&buf, "host_repo", in.HostRepo)
	writeKV(&buf, "issue_number", in.IssueNumber)
	writeKV(&buf, "base_branch", in.BaseBranch)
	writeKV(&buf, "working_branch", in.WorkingBranch)
	writeKV(&buf, "platform_base_url", in.PlatformBaseURL)
	buf.WriteString("\n")

	// (2) Baseline. Always present; it's compiled in.
	buf.WriteString(baselineMD)
	kept = append(kept, "baseline")

	// (3) Host role prompt. Prefer the inline body (workflow-driven
	// dispatch path); fall back to the legacy file mount only when the
	// body is empty AND a path is configured.
	switch {
	case in.HostAddendumBody != "":
		buf.WriteString("\n\n===== HOST ROLE PROMPT =====\n\n")
		buf.WriteString(in.HostAddendumBody)
		kept = append(kept, "host")
	case in.HostAddendumPath != "":
		body, err := os.ReadFile(in.HostAddendumPath)
		if err != nil {
			return nil, fmt.Errorf("prompt: read host role prompt %s: %w", in.HostAddendumPath, err)
		}
		if len(body) > 0 {
			buf.WriteString("\n\n===== HOST ROLE PROMPT =====\n\n")
			buf.Write(body)
			kept = append(kept, "host")
		}
	}

	return &Assembled{Prompt: buf.String(), KeptLayers: kept}, nil
}

func writeKV(buf *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	buf.WriteString("- ")
	buf.WriteString(key)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteByte('\n')
}
