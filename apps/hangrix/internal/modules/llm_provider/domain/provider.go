// Package domain declares the LLM-provider registry types and the
// cross-module interfaces other packages depend on.
//
// What this package no longer owns (post agent-identity-token refactor):
//
//   - Session-token issuance / validation. The session token is the
//     in-container agent's *identity*; one is minted per agent_session
//     row in modules/runner. The proxy authenticates via
//     runner/domain.SessionTokenValidator, not anything here.
//
// What this package still owns:
//
//   - The platform's set of registered LLM upstreams (one Provider per
//     vendor + API key + base URL).
//   - Routing of a request body's `model` field to an upstream, via the
//     model/group definitions (see GroupRouter.ResolveModel). The legacy
//     per-provider allowed_models routing table is deprecated and no longer
//     consulted.
//   - The append-only usage log that the proxy writes to after every
//     round-trip.
package domain

import (
	"context"
	"errors"
	"time"
)

// ProviderType selects the upstream wire-format the proxy translates to.
// v1 ships three; new types are added by extending the proxy's translator
// switch — the domain just stores the tag.
type ProviderType string

const (
	// ProviderTypeOpenAI talks to OpenAI's native Response API directly.
	// No translation. base_url defaults to https://api.openai.com.
	ProviderTypeOpenAI ProviderType = "openai"
	// ProviderTypeAnthropic translates OpenAI Response API <-> Anthropic
	// Messages API.
	ProviderTypeAnthropic ProviderType = "anthropic"
	// ProviderTypeOpenAICompat is OpenAI Response API forwarded as-is to a
	// caller-specified base_url (OpenRouter / vLLM / Together / Groq / ...).
	ProviderTypeOpenAICompat ProviderType = "openai-compat"
	// ProviderTypeMock is the built-in mock provider. It returns
	// deterministic, text-only responses without making any external
	// HTTP calls. Used for local testing and e2e agent-chain smoke
	// without a real LLM key.
	ProviderTypeMock ProviderType = "mock"
)

func (t ProviderType) Valid() bool {
	switch t {
	case ProviderTypeOpenAI, ProviderTypeAnthropic, ProviderTypeOpenAICompat, ProviderTypeMock:
		return true
	}
	return false
}

// Provider is one registered upstream. ApiKey is the encrypted form (a
// cryptobox-sealed blob); only the proxy ever decrypts it, and only at
// request-handling time. Handlers never return ApiKey on the wire.
type Provider struct {
	ID       int64
	Name     string // [a-z0-9-]{1,64}; appears in admin URLs but not in proxy routes
	Type     ProviderType
	BaseURL  string
	ApiKey   string // sealed blob; opaque to everyone except cryptobox
	// AllowedModels is deprecated and no longer used for routing. Routing is
	// driven entirely by model/group definitions (see GroupRouter.ResolveModel).
	// The column is retained for backward compatibility; new code must not read
	// it. Always empty for providers created after the routing migration.
	AllowedModels []string
	// Disabled flips the row out of routing without deleting it.
	Disabled  bool
	ActorID   int64 // FK to actors(id); replaces created_by
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UsageRecord is one row in the llm_usage_log table, written by the proxy
// after each upstream call. Consumed by M10+ cost dashboards; M6a just
// writes them.
//
// SessionID identifies the calling agent_sessions row when the request
// arrived with a session-token bearer. It's a plain integer (no FK
// across module boundaries) so this module stays decoupled from
// modules/runner.
type UsageRecord struct {
	SessionID        *int64
	ProviderID       int64
	Model            string
	PromptTokens     int32
	CompletionTokens int32
	TotalTokens      int32
	LatencyMS        int32
	StatusCode       int32
	ErrorMessage     string
	RequestPath      string
	RequestBody      string
	ResponseBody     string
}

// Errors.
var (
	ErrProviderNotFound = errors.New("llm provider not found")
	ErrProviderConflict = errors.New("llm provider name already taken")
	ErrInvalidName      = errors.New("invalid llm provider name")
	ErrInvalidProvider  = errors.New("invalid llm provider config")
	ErrNoModelMatch     = errors.New("no provider serves the requested model")
)

// Repo is the persistence abstraction. The Postgres impl in infra/
// satisfies both Repo and Lookup; the module binds the same instance to
// both interfaces.
type Repo interface {
	CreateProvider(ctx context.Context, p *Provider) (*Provider, error)
	GetProviderByName(ctx context.Context, name string) (*Provider, error)
	GetProviderByID(ctx context.Context, id int64) (*Provider, error)
	ListProviders(ctx context.Context) ([]*Provider, error)
	UpdateProvider(ctx context.Context, p *Provider) (*Provider, error)
	SetProviderDisabled(ctx context.Context, id int64, disabled bool) (*Provider, error)
	DeleteProvider(ctx context.Context, id int64) error

	RecordUsage(ctx context.Context, u *UsageRecord) error
}

// Lookup is the narrow read-only interface the proxy holds. Decoupled
// from Repo so a future read-replica / cache layer can satisfy this
// without reimplementing the write methods.
//
// As of the model-group feature, Lookup is now implemented by the service
// layer (GroupRouter), not directly by the infra PostgresRepo. The service
// aggregates provider + group data to produce a unified routing result.
type Lookup interface {
	RecordUsage(ctx context.Context, u *UsageRecord) error

	// ResolveModel resolves a model name to an ordered list of ready-to-dispatch
	// candidates. The name must match a model/group definition: it returns all
	// currently-available members ordered by priority. Returns ErrNoModelMatch
	// when no model/group matches, or ErrGroupAllUnavailable when the group
	// exists but has zero available members.
	ResolveModel(ctx context.Context, model string) (RouteResolution, error)

	// ReportAttempt feeds a dispatch outcome back to the state machine.
	// memberID=0 is a no-op (non-group route). For group members, a success
	// resets the backoff counter; a transient failure increments it.
	ReportAttempt(ctx context.Context, memberID int64, outcome AttemptOutcome) error

	// GetModelSpec returns the read-only ModelSpec for the given model name.
	// Returns ErrModelNotFound when the name is not a known model definition.
	// Agent API and proxy callers use this to fetch context-window / reasoning
	// metadata without touching the write-side ModelRepo.
	GetModelSpec(ctx context.Context, name string) (*ModelSpec, error)
}
