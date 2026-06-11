// Package config owns the agent's startup configuration: every HANGRIX_*
// environment variable the process reads, validated up-front so a typo or
// missing required value surfaces as one structured error before any
// component tries to use it.
//
// The Config struct is shared via the ioc container — other modules
// (llmclient, toolregistry, systemprompt, …) declare a *Config field on
// their Deps struct and read whatever subset they need.
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config captures every HANGRIX_* the agent reads at startup. Fields are
// plain strings (no env access at read time) so consumers can rely on a
// stable value for the lifetime of the process and tests can construct a
// Config without touching os.Getenv.
//
// PlatformBaseURL is the one network anchor the agent needs. Both the
// LLM proxy (`<base>/api/llm/v1/responses`) and the platform v1 REST
// endpoints (`<base>/api/v1/...`) are derived from it by the llm and
// tools/platform modules respectively.
type Config struct {
	SessionToken     string
	PlatformBaseURL  string
	Model            string
	SessionID        string
	Role             string
	HostRepo         string
	IssueNumber      string
	WorkingBranch    string
	BaseBranch       string
	// HostAddendumPath is the legacy file-mount path the runner used
	// to bind-mount the resolved role prompt into the container. Kept
	// for backward compat with any out-of-band tooling that still sets
	// HANGRIX_HOST_ADDENDUM, but the workflow-driven dispatch path
	// uses HostAddendumBody (HANGRIX_ROLE_PROMPT) instead.
	HostAddendumPath string
	// HostAddendumBody carries the resolved role prompt directly as
	// text (HANGRIX_ROLE_PROMPT). When set it wins over HostAddendumPath
	// — bind-mounting a file is no longer how the runner side delivers
	// the addendum to the agent.
	HostAddendumBody string
	McpServers       []string

	// RepoPermission gates the write-type platform tools. The exact
	// value "read" makes the catalogue read-only (every mutating
	// platform tool is hidden from the LLM); anything else (including
	// empty/unset) means write. Local tools are never affected — this
	// concerns the platform REST API only. Set via HANGRIX_REPO_PERMISSION.
	RepoPermission string

	// PlatformTools is a JSON array of shell-style glob patterns that
	// whitelists which platform tools (issue_*, contribution_*, …) are
	// shown to the LLM. A platform tool surfaces only if its name matches
	// at least one pattern; an empty array or unset value hides every
	// platform tool (strict whitelist). Local and MCP tools are never
	// affected by this whitelist. Composes with RepoPermission: a
	// read-only role still hides write platform tools regardless of the
	// whitelist. Set via HANGRIX_PLATFORM_TOOLS.
	PlatformTools string

	// LLMReasoningEffort is the reasoning_effort from agents.yml
	// (typically "low", "medium", or "high" — the LLM proxy and each
	// upstream adapter decide what to do with it). Surfaced by the runner
	// as HANGRIX_LLM_REASONING_EFFORT. Empty means "not configured" —
	// the agent omits the `reasoning.effort` field on the wire and the
	// upstream applies its model default.
	LLMReasoningEffort string

	// LLMThinking is the Anthropic thinking-mode toggle from agents.yml:
	// "adaptive" (Claude 4.6+ canonical), "enabled" (legacy budget_tokens),
	// or "disabled". Surfaced by the runner as HANGRIX_LLM_THINKING. The
	// agent forwards it as the top-level `thinking` string on the proxy
	// wire; only the Anthropic upstream adapter consumes it. Empty = omit.
	LLMThinking string

	// CompactTokenThreshold is the input-token usage above which the
	// runtime nudges the LLM (via a synthetic system reminder injected
	// at the next turn boundary) to call compact_session. 0 disables the
	// nudge — the LLM still decides on its own when to compact.
	// Set explicitly via HANGRIX_COMPACT_TOKEN_THRESHOLD; when unset,
	// defaults to 80000. A negative value disables the nudge.
	CompactTokenThreshold int

	// LLMReasoningTimeoutSeconds is the per-call wall-clock ceiling the
	// runtime enforces on a single Create() invocation. When exceeded the
	// agent cancels the HTTP request and — if retries remain — retries
	// with the same request snapshot. <=0 disables the protection (the
	// call falls through to the http.Client's 5-minute timeout). Set via
	// HANGRIX_LLM_REASONING_TIMEOUT_SECONDS; default 200.
	LLMReasoningTimeoutSeconds int
	// LLMReasoningTimeoutRetries is the number of retries after the first
	// timeout. Default 1 means 2 total attempts. Only reasoning-timeout
	// errors are retried at this level; transport/5xx/429 retries stay
	// inside llm.Client.Create. Set via HANGRIX_LLM_REASONING_TIMEOUT_RETRIES.
	// Clamped to >=0 to prevent negative values from zeroing maxAttempts.
	LLMReasoningTimeoutRetries int
}

// LLMEndpoint returns the URL the agent POSTs `/responses` against.
// Centralised here so the suffix lives next to its sibling
// PlatformV1BaseURL — neither leaks into other modules.
func (c *Config) LLMEndpoint() string {
	if c.PlatformBaseURL == "" {
		return ""
	}
	return strings.TrimRight(c.PlatformBaseURL, "/") + "/api/llm/v1"
}

// PlatformV1BaseURL returns the base path for the v1 REST API
// (`<base>/api/v1`) that the platform tool wrappers use to construct
// per-endpoint URLs (e.g. `<base>/api/v1/issues/current`).
func (c *Config) PlatformV1BaseURL() string {
	if c.PlatformBaseURL == "" {
		return ""
	}
	return strings.TrimRight(c.PlatformBaseURL, "/") + "/api/v1"
}

// NewConfig is the ioc-shaped provider: zero parameters, returns *Config.
// Missing-required values panic with one consolidated message so the
// runner sees a single line on stderr rather than a cascade of nil
// dereferences when downstream code reaches for an empty endpoint.
func NewConfig() *Config {
	cfg := &Config{
		SessionToken:               os.Getenv("HANGRIX_SESSION_TOKEN"),
		PlatformBaseURL:            os.Getenv("HANGRIX_PLATFORM_BASE_URL"),
		Model:                      os.Getenv("HANGRIX_LLM_MODEL"),
		SessionID:                  os.Getenv("HANGRIX_SESSION_ID"),
		Role:                       os.Getenv("HANGRIX_ROLE"),
		HostRepo:                   os.Getenv("HANGRIX_HOST_REPO"),
		IssueNumber:                os.Getenv("HANGRIX_ISSUE_NUMBER"),
		WorkingBranch:              os.Getenv("HANGRIX_WORKING_BRANCH"),
		BaseBranch:                 os.Getenv("HANGRIX_BASE_BRANCH"),
		HostAddendumPath:           os.Getenv("HANGRIX_HOST_ADDENDUM"),
		HostAddendumBody:           os.Getenv("HANGRIX_ROLE_PROMPT"),
		RepoPermission:             os.Getenv("HANGRIX_REPO_PERMISSION"),
		PlatformTools:              os.Getenv("HANGRIX_PLATFORM_TOOLS"),
		McpServers:                 parseMcpServers(os.Getenv("HANGRIX_MCP_SERVERS")),
		LLMReasoningEffort:         strings.TrimSpace(os.Getenv("HANGRIX_LLM_REASONING_EFFORT")),
		LLMThinking:                strings.ToLower(strings.TrimSpace(os.Getenv("HANGRIX_LLM_THINKING"))),
		CompactTokenThreshold:      parseCompactThreshold(os.Getenv("HANGRIX_COMPACT_TOKEN_THRESHOLD"), os.Getenv("HANGRIX_PLATFORM_BASE_URL"), os.Getenv("HANGRIX_LLM_MODEL"), os.Getenv("HANGRIX_SESSION_TOKEN")),
		LLMReasoningTimeoutSeconds: parseIntDefault(os.Getenv("HANGRIX_LLM_REASONING_TIMEOUT_SECONDS"), 200),
		LLMReasoningTimeoutRetries: clampNonNegative(parseIntDefault(os.Getenv("HANGRIX_LLM_REASONING_TIMEOUT_RETRIES"), 1)),
	}

	var missing []string
	if cfg.SessionToken == "" {
		missing = append(missing, "HANGRIX_SESSION_TOKEN")
	}
	if cfg.PlatformBaseURL == "" {
		missing = append(missing, "HANGRIX_PLATFORM_BASE_URL")
	}
	if cfg.Model == "" {
		missing = append(missing, "HANGRIX_LLM_MODEL")
	}
	if len(missing) > 0 {
		panic(fmt.Errorf("config: missing required env: %s", strings.Join(missing, ", ")))
	}
	return cfg
}

// parseMcpServers splits a comma-separated env value into a slice of
// trimmed, non-empty server names. Empty input → nil (no servers).
func parseMcpServers(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fetchModelContextWindow calls GET /api/v1/models/{model} on the platform
// and returns the model's context_window. Uses a 5s timeout so a slow/hung
// platform never blocks agent boot indefinitely. Returns 0 on any failure
// (network, 401/404, bad JSON) — the caller falls back to the default.
func fetchModelContextWindow(platformBaseURL, model, sessionToken string) int32 {
	if platformBaseURL == "" || model == "" {
		return 0
	}
	u := strings.TrimRight(platformBaseURL, "/") + "/api/v1/models/" + model
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", "hangrix-agent/1.0")
	if sessionToken != "" {
		req.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		log.Printf("warning: failed to fetch model spec from %s: %v", u, err)
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("warning: platform returned %d for model spec %s", resp.StatusCode, u)
		return 0
	}
	var body struct {
		ContextWindow int32 `json:"context_window"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		log.Printf("warning: failed to decode model spec from %s: %v", u, err)
		return 0
	}
	if body.ContextWindow <= 0 {
		return 0
	}
	return body.ContextWindow
}

// parseCompactThreshold reads HANGRIX_COMPACT_TOKEN_THRESHOLD. When
// explicitly set, returns that value (negative disables). When unset,
// fetches the model spec from the platform API to derive 80% of context_window,
// falling back to 80000 on any failure (with a warning log).
func parseCompactThreshold(raw, platformBaseURL, model, sessionToken string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if cw := fetchModelContextWindow(platformBaseURL, model, sessionToken); cw > 0 {
			return int(cw) * 80 / 100
		}
		return 80000
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 80000
	}
	if n < 0 {
		return 0
	}
	return n
}

// parseIntDefault reads an env value as an int, falling back to def when
// empty or unparseable. Used for simple count/duration env vars that
// have a sensible default.
func parseIntDefault(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// clampNonNegative floors the value to 0 when negative. This guards
// against misconfiguration (e.g. HANGRIX_LLM_REASONING_TIMEOUT_RETRIES=-1)
// that would otherwise zero out maxAttempts and skip the LLM call entirely.
func clampNonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
