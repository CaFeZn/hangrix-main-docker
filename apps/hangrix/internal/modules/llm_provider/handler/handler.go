// Package handler exposes the llm_provider admin HTTP surface mounted at
// /api/admin/llm/. Every route is RequireAdmin-gated; provider credentials
// are platform-level operations.
//
// Response shape rules:
//   - The provider's encrypted api_key is NEVER returned. A derived boolean
//     `has_api_key` lets the UI distinguish "configured" from "unset".
//   - Session-token issuance is no longer part of this module — every
//     agent_session in the runner module mints its own identity token
//     on creation. See modules/runner/handler.AdminHandler for the
//     equivalent admin surface (POST /api/admin/runners/{id}/sessions).
package handler

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	actordomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/actor/domain"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/infra"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/service"
	"github.com/hangrix/hangrix/pkg/actor"
)

// providerNameRe matches the URL-path slug for a provider. Lower-case so
// the resulting `/v1/<name>/...` proxy path is unambiguous on case-insensitive
// filesystems and HTTP middleware.
var providerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// modelNameRe matches a model (or model-group) name. Unlike a provider slug,
// model names additionally allow `.` and `_` so common upstream identifiers
// like `gpt-4.1` or `claude-3.5-sonnet` are valid. Matches the contract
// documented on domain.ModelDefinition.Name.
var modelNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// defaultUsageLimit is the page size when the caller passes no `limit`;
// maxUsageLimit caps a hostile caller asking for a million rows.
const (
	defaultUsageLimit = 100
	maxUsageLimit     = 500
)

type Handler struct {
	repo          domain.Repo
	usage         *infra.PostgresRepo
	middleware    authdomain.Middleware
	actorResolver actordomain.Resolver
	groupRepo     domain.GroupRepo
	modelRepo     domain.ModelRepo
	modelWriter   *service.ModelWriter
	lookup        domain.Lookup // GroupRouter, for member health derivation
}

// HandlerDeps wires the same Postgres instance into both the narrow domain
// interface (for everything mutating) and the concrete impl (for the
// admin-only usage read, which has no need to sit on the cross-module
// interface).
type HandlerDeps struct {
	Repo          domain.Repo
	Usage         *infra.PostgresRepo
	Middleware    authdomain.Middleware
	ActorResolver actordomain.Resolver
	GroupRepo     domain.GroupRepo
	ModelRepo     domain.ModelRepo
	ModelWriter   *service.ModelWriter
	Lookup        domain.Lookup
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		repo:          deps.Repo,
		usage:         deps.Usage,
		middleware:    deps.Middleware,
		actorResolver: deps.ActorResolver,
		groupRepo:     deps.GroupRepo,
		modelRepo:     deps.ModelRepo,
		modelWriter:   deps.ModelWriter,
		lookup:        deps.Lookup,
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/admin/llm", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Use(h.middleware.RequireAdmin)

		r.Post("/providers", h.createProvider)
		r.Get("/providers", h.listProviders)
		r.Get("/providers/{name}", h.getProvider)
		r.Patch("/providers/{name}", h.patchProvider)
		r.Post("/providers/{name}/disabled", h.setProviderDisabled)
		r.Delete("/providers/{name}", h.deleteProvider)

		r.Get("/usage", h.listUsage)
		r.Get("/usage/{id}", h.getUsage)
		r.Get("/usage/export", h.exportUsage)

		// Model groups
		r.Get("/groups", h.listGroups)
		r.Post("/groups", h.createGroup)
		r.Get("/groups/{name}", h.getGroup)
		r.Patch("/groups/{name}", h.patchGroup)
		r.Delete("/groups/{name}", h.deleteGroup)
		r.Post("/groups/{name}/members/{memberID}/disabled", h.setMemberDisabled)

		// Models
		r.Get("/models", h.listModels)
		r.Post("/models", h.createModel)
		r.Get("/models/{name}", h.getModel)
		r.Patch("/models/{name}", h.patchModel)
		r.Delete("/models/{name}", h.deleteModel)
	})

	// Model groups — frontend-facing routes at /api/admin/model-groups
	r.Route("/api/admin/model-groups", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Use(h.middleware.RequireAdmin)

		r.Get("/", h.listModelGroups)
		r.Post("/", h.createModelGroup)
		r.Get("/{name}", h.getModelGroup)
		r.Patch("/{name}", h.patchModelGroup)
		r.Delete("/{name}", h.deleteModelGroup)
		r.Post("/{name}/entries/{entryID}/disable", h.disableModelGroupEntry)
		r.Post("/{name}/entries/{entryID}/enable", h.enableModelGroupEntry)
	})
}

// ---- provider DTOs ----

// publicProvider intentionally omits the encrypted api key. `has_api_key`
// surfaces "is something configured" so the UI can show a green dot without
// ever transporting the secret (sealed or otherwise).
type publicProvider struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	BaseURL   string    `json:"base_url"`
	HasAPIKey bool      `json:"has_api_key"`
	Disabled  bool      `json:"disabled"`
	CreatedBy int64     `json:"created_by,omitempty"`
	ActorID   int64     `json:"actor_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (h *Handler) toPublicProvider(p *domain.Provider) publicProvider {
	createdBy := int64(0)
	if h.actorResolver != nil {
		uid, ok := h.actorResolver.UserID(context.Background(), p.ActorID)
		if ok {
			createdBy = uid
		}
	}
	return publicProvider{
		ID:        p.ID,
		Name:      p.Name,
		Type:      string(p.Type),
		BaseURL:   p.BaseURL,
		HasAPIKey: p.ApiKey != "",
		Disabled:  p.Disabled,
		CreatedBy: createdBy,
		ActorID:   p.ActorID,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

// ---- provider routes ----

type createProviderReq struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)

	var req createProviderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.TrimSpace(req.Type)
	if !providerNameRe.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	if !domain.ProviderType(req.Type).Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid type")
		return
	}
	if req.APIKey == "" {
		if domain.ProviderType(req.Type) == domain.ProviderTypeMock {
			req.APIKey = "mock" // placeholder — mock provider never uses the key
		} else {
			httpx.WriteError(w, http.StatusBadRequest, "api_key is required")
			return
		}
	}

	var actorID int64
	if caller != nil && h.actorResolver != nil {
		resolved, err := h.actorResolver.From(r.Context(), actor.UserRef(caller.ID, ""))
		if err == nil {
			actorID = resolved.ActorID
		}
	}
	in := &domain.Provider{
		Name:    req.Name,
		Type:    domain.ProviderType(req.Type),
		BaseURL: strings.TrimSpace(req.BaseURL),
		ApiKey:  req.APIKey,
		ActorID: actorID,
	}
	out, err := h.repo.CreateProvider(r.Context(), in)
	if err != nil {
		if errors.Is(err, domain.ErrProviderConflict) {
			httpx.WriteError(w, http.StatusConflict, "name already taken")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, h.toPublicProvider(out))
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.ListProviders(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicProvider, 0, len(rows))
	for _, p := range rows {
		items = append(items, h.toPublicProvider(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.toPublicProvider(p))
}

type patchProviderReq struct {
	BaseURL  *string `json:"base_url,omitempty"`
	APIKey   *string `json:"api_key,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

// patchProvider applies a partial update. Name and type are intentionally
// immutable — changing either would invalidate every session token bound to
// the row, so the contract is "delete and recreate" instead.
func (h *Handler) patchProvider(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	var req patchProviderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	updated := *existing
	updated.ApiKey = "" // empty signals "leave the sealed blob alone"

	if req.BaseURL != nil {
		updated.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.APIKey != nil && *req.APIKey != "" {
		updated.ApiKey = *req.APIKey
	}
	if req.Disabled != nil {
		updated.Disabled = *req.Disabled
	}

	out, err := h.repo.UpdateProvider(r.Context(), &updated)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "provider not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.toPublicProvider(out))
}

type setDisabledReq struct {
	Disabled bool `json:"disabled"`
}

// setProviderDisabled is the one-shot enable/disable toggle. Separate from
// patchProvider so the admin UI can flip a switch without round-tripping
// base_url (which would race a concurrent edit).
func (h *Handler) setProviderDisabled(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	var req setDisabledReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	out, err := h.repo.SetProviderDisabled(r.Context(), existing.ID, req.Disabled)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "provider not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.toPublicProvider(out))
}

func (h *Handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadProviderByName(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteProvider(r.Context(), p.ID); err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "provider not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- usage ----

type publicUsage struct {
	ID               int64     `json:"id"`
	SessionID        *int64    `json:"session_id,omitempty"`
	ProviderID       int64     `json:"provider_id"`
	ProviderName     string    `json:"provider_name"`
	Model            string    `json:"model"`
	PromptTokens     int32     `json:"prompt_tokens"`
	CompletionTokens int32     `json:"completion_tokens"`
	TotalTokens      int32     `json:"total_tokens"`
	LatencyMS        int32     `json:"latency_ms"`
	StatusCode       int32     `json:"status_code"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	RequestPath      string    `json:"request_path,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

func (h *Handler) listUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := defaultUsageLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if n > maxUsageLimit {
			n = maxUsageLimit
		}
		limit = n
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid offset")
			return
		}
		offset = n
	}

	var providerID *int64
	if name := strings.TrimSpace(q.Get("provider")); name != "" {
		if !providerNameRe.MatchString(name) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid provider")
			return
		}
		p, err := h.repo.GetProviderByName(r.Context(), name)
		if err != nil {
			if errors.Is(err, domain.ErrProviderNotFound) {
				httpx.WriteError(w, http.StatusNotFound, "provider not found")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		providerID = &p.ID
	}

	var since *time.Time
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid since (RFC3339 required)")
			return
		}
		since = &t
	}

	rows, err := h.usage.ListUsage(r.Context(), providerID, since, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := h.usage.CountUsage(r.Context(), providerID, since)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]publicUsage, 0, len(rows))
	for _, u := range rows {
		items = append(items, publicUsage{
			ID:               u.ID,
			SessionID:        u.SessionID,
			ProviderID:       u.ProviderID,
			ProviderName:     u.ProviderName,
			Model:            u.Model,
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
			LatencyMS:        u.LatencyMS,
			StatusCode:       u.StatusCode,
			ErrorMessage:     u.ErrorMessage,
			RequestPath:      u.RequestPath,
			CreatedAt:        u.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// publicUsageDetail is the single-row DTO for GET /api/admin/llm/usage/{id}.
// It includes the large body columns the list endpoint deliberately omits.
type publicUsageDetail struct {
	ID           int64     `json:"id"`
	ProviderName string    `json:"provider_name"`
	Model        string    `json:"model"`
	CreatedAt    time.Time `json:"created_at"`
	StatusCode   int32     `json:"status_code"`
	RequestBody  string    `json:"request_body"`
	ResponseBody string    `json:"response_body"`
}

func (h *Handler) getUsage(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}

	row, err := h.usage.GetUsageByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "usage record not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	httpx.WriteJSON(w, http.StatusOK, publicUsageDetail{
		ID:           row.ID,
		ProviderName: row.ProviderName,
		Model:        row.Model,
		CreatedAt:    row.CreatedAt,
		StatusCode:   row.StatusCode,
		RequestBody:  row.RequestBody,
		ResponseBody: row.ResponseBody,
	})
}

// ---- export ----

func (h *Handler) exportUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := strings.TrimSpace(q.Get("format"))
	if format != "csv" && format != "jsonl" {
		httpx.WriteError(w, http.StatusBadRequest, "format must be csv or jsonl")
		return
	}

	var providerID *int64
	if name := strings.TrimSpace(q.Get("provider")); name != "" {
		if !providerNameRe.MatchString(name) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid provider")
			return
		}
		p, err := h.repo.GetProviderByName(r.Context(), name)
		if err != nil {
			if errors.Is(err, domain.ErrProviderNotFound) {
				httpx.WriteError(w, http.StatusNotFound, "provider not found")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		providerID = &p.ID
	}

	var since *time.Time
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid since (RFC3339 required)")
			return
		}
		since = &t
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("llm-usage-%s-%s.zip", format, ts)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	zw := zip.NewWriter(w)
	defer zw.Close()

	entryName := "llm-usage." + format
	fw, err := zw.Create(entryName)
	if err != nil {
		return
	}

	ctx := r.Context()
	if format == "csv" {
		err = h.usage.ExportUsageCSV(ctx, fw, providerID, since)
	} else {
		err = h.usage.ExportUsageJSONL(ctx, fw, providerID, since)
	}
	if err != nil {
		return
	}
}

// ---- model group DTOs ----

type groupMemberDTO struct {
	ID                int64   `json:"id"`
	ProviderID        int64   `json:"provider_id"`
	ProviderName      string  `json:"provider_name"`
	Model             string  `json:"model"`
	Priority          int32   `json:"priority"`
	Health            string  `json:"health"`
	ManualDisabled    bool    `json:"manual_disabled"`
	AutoDisabledUntil *string `json:"auto_disabled_until,omitempty"`
	RecoverInSeconds  int64   `json:"recover_in_seconds"`
	BackoffStep       int32   `json:"backoff_step"`
	LastFailureAt     *string `json:"last_failure_at,omitempty"`
	LastFailureMsg    string  `json:"last_failure_msg"`
	LastSuccessAt     *string `json:"last_success_at,omitempty"`
	LastCheckedAt     *string `json:"last_checked_at,omitempty"`
}

type groupSummaryDTO struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	MemberCount    int       `json:"member_count"`
	AvailableCount int       `json:"available_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type groupDetailDTO struct {
	ID          int64            `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Members     []groupMemberDTO `json:"members"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type createGroupReq struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Members     []createGroupMemberReq `json:"members"`
}

// ---- frontend-aligned model group DTOs (matching apps/web/app/types/model-group.ts) ----

type mgEntryDTO struct {
	ID                  int64   `json:"id"`
	ModelName           string  `json:"model_name"`
	Priority            int32   `json:"priority"`
	Status              string  `json:"status"`
	ConsecutiveFailures int32   `json:"consecutive_failures"`
	DisabledUntil       *string `json:"disabled_until,omitempty"`
	LastCheckedAt       *string `json:"last_checked_at,omitempty"`
	RemainingSeconds    int64   `json:"remaining_seconds"`
}

type mgDTO struct {
	ID        int64        `json:"id"`
	Name      string       `json:"name"`
	Entries   []mgEntryDTO `json:"entries"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type mgListItemDTO struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	EntryCount     int       `json:"entry_count"`
	AvailableCount int       `json:"available_count"`
	CreatedAt      time.Time `json:"created_at"`
}

type mgCreateEntryReq struct {
	ModelName string `json:"model_name"`
	Priority  int32  `json:"priority"`
}

type mgCreateReq struct {
	Name    string             `json:"name"`
	Entries []mgCreateEntryReq `json:"entries"`
}

type mgPatchReq struct {
	Entries []mgCreateEntryReq `json:"entries"`
}

func toMgEntryDTO(m *domain.GroupMember, now time.Time) mgEntryDTO {
	d := mgEntryDTO{
		ID:                  m.ID,
		ModelName:           m.Model,
		Priority:            m.Priority,
		Status:              string(m.Health(now)),
		ConsecutiveFailures: m.BackoffStep,
	}
	if m.AutoDisabledUntil != nil {
		s := m.AutoDisabledUntil.Format(time.RFC3339)
		d.DisabledUntil = &s
		remaining := m.AutoDisabledUntil.Sub(now).Seconds()
		if remaining > 0 {
			d.RemainingSeconds = int64(remaining)
		}
	}
	if m.LastCheckedAt != nil {
		s := m.LastCheckedAt.Format(time.RFC3339)
		d.LastCheckedAt = &s
	}
	return d
}

func (h *Handler) buildMgDTO(ctx context.Context, groupID int64) (*mgDTO, error) {
	g, err := h.groupRepo.GetGroupByID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	members, err := h.groupRepo.ListMembersByGroupID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	entries := make([]mgEntryDTO, 0, len(members))
	for _, m := range members {
		entries = append(entries, toMgEntryDTO(m, now))
	}
	return &mgDTO{
		ID:        g.ID,
		Name:      g.Name,
		Entries:   entries,
		CreatedAt: g.CreatedAt,
		UpdatedAt: g.UpdatedAt,
	}, nil
}

func (h *Handler) isBackingModelGroup(ctx context.Context, name string) (bool, error) {
	if h.modelRepo == nil {
		return false, nil
	}
	n, err := h.modelRepo.CountModelsByName(ctx, name)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (h *Handler) getUserModelGroupByName(ctx context.Context, name string) (*domain.ModelGroup, error) {
	g, err := h.groupRepo.GetGroupByName(ctx, name)
	if err != nil {
		return nil, err
	}
	backing, err := h.isBackingModelGroup(ctx, name)
	if err != nil {
		return nil, err
	}
	if backing {
		return nil, domain.ErrGroupNotFound
	}
	return g, nil
}

// resolveModelMembers looks up an LLM model definition by name and returns the
// backing group's members converted to domain.GroupMember entries suitable for
// insertion into a model group. Returns an error if the model name is not found.
func (h *Handler) resolveModelMembers(ctx context.Context, modelName string) ([]*domain.GroupMember, error) {
	md, err := h.modelRepo.GetModelByName(ctx, modelName)
	if err != nil {
		if errors.Is(err, domain.ErrModelNotFound) {
			return nil, fmt.Errorf("model %q is not a defined LLM model", modelName)
		}
		return nil, err
	}
	backing, err := h.groupRepo.ListMembersByGroupID(ctx, md.GroupID)
	if err != nil {
		return nil, err
	}
	if len(backing) == 0 {
		return nil, fmt.Errorf("model %q has no provider members configured", modelName)
	}
	members := make([]*domain.GroupMember, 0, len(backing))
	for _, bm := range backing {
		members = append(members, &domain.GroupMember{
			ProviderID: bm.ProviderID,
			Model:      bm.Model,
		})
	}
	return members, nil
}

// ---- model group handlers (/api/admin/model-groups) ----

func (h *Handler) listModelGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.groupRepo.ListGroups(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]mgListItemDTO, 0, len(groups))
	for _, g := range groups {
		backing, err := h.isBackingModelGroup(r.Context(), g.Name)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if backing {
			continue
		}
		members, err := h.groupRepo.ListMembersByGroupID(r.Context(), g.ID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		now := time.Now()
		avail := 0
		for _, m := range members {
			if m.Health(now) == domain.MemberHealthAvailable {
				avail++
			}
		}
		items = append(items, mgListItemDTO{
			ID:             g.ID,
			Name:           g.Name,
			EntryCount:     len(members),
			AvailableCount: avail,
			CreatedAt:      g.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createModelGroup(w http.ResponseWriter, r *http.Request) {
	var req mgCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !modelNameRe.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}

	// A group name shares the model/group namespace, so it must not collide with
	// an existing model definition.
	if h.modelRepo != nil {
		n, err := h.modelRepo.CountModelsByName(r.Context(), req.Name)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if n > 0 {
			httpx.WriteError(w, http.StatusConflict, "group name conflicts with an existing model definition")
			return
		}
	}

	caller, _ := authdomain.UserFromRequest(r)
	var actorID int64
	if caller != nil && h.actorResolver != nil {
		resolved, err := h.actorResolver.From(r.Context(), actor.UserRef(caller.ID, ""))
		if err == nil {
			actorID = resolved.ActorID
		}
	}

	g, err := h.groupRepo.CreateGroup(r.Context(), &domain.ModelGroup{
		Name:        req.Name,
		Description: "",
		ActorID:     actorID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrGroupNameConflict) {
			httpx.WriteError(w, http.StatusConflict, "group name already taken")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if len(req.Entries) > 0 {
		members := make([]*domain.GroupMember, 0)
		priorityOffset := int32(0)
		for _, e := range req.Entries {
			resolved, err := h.resolveModelMembers(r.Context(), e.ModelName)
			if err != nil {
				httpx.WriteError(w, http.StatusBadRequest, err.Error())
				return
			}
			for _, rm := range resolved {
				rm.Priority = priorityOffset
				priorityOffset++
				members = append(members, rm)
			}
		}
		if err := h.groupRepo.ReplaceMembers(r.Context(), g.ID, members); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	detail, err := h.buildMgDTO(r.Context(), g.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, detail)
}

func (h *Handler) getModelGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	g, err := h.getUserModelGroupByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrGroupNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail, err := h.buildMgDTO(r.Context(), g.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) patchModelGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	g, err := h.getUserModelGroupByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrGroupNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req mgPatchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if req.Entries != nil {
		members := make([]*domain.GroupMember, 0)
		priorityOffset := int32(0)
		for _, e := range req.Entries {
			resolved, err := h.resolveModelMembers(r.Context(), e.ModelName)
			if err != nil {
				httpx.WriteError(w, http.StatusBadRequest, err.Error())
				return
			}
			for _, rm := range resolved {
				rm.Priority = priorityOffset
				priorityOffset++
				members = append(members, rm)
			}
		}
		if err := h.groupRepo.ReplaceMembers(r.Context(), g.ID, members); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	detail, err := h.buildMgDTO(r.Context(), g.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) deleteModelGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	g, err := h.getUserModelGroupByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrGroupNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.groupRepo.DeleteGroup(r.Context(), g.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) disableModelGroupEntry(w http.ResponseWriter, r *http.Request) {
	h.toggleModelGroupEntry(w, r, true)
}

func (h *Handler) enableModelGroupEntry(w http.ResponseWriter, r *http.Request) {
	h.toggleModelGroupEntry(w, r, false)
}

func (h *Handler) toggleModelGroupEntry(w http.ResponseWriter, r *http.Request, disabled bool) {
	name := chi.URLParam(r, "name")
	entryIDStr := chi.URLParam(r, "entryID")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	entryID, err := strconv.ParseInt(entryIDStr, 10, 64)
	if err != nil || entryID <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid entry id")
		return
	}

	// Verify the entry belongs to the named group.
	g, err := h.getUserModelGroupByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrGroupNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	m, err := h.groupRepo.GetMemberByID(r.Context(), entryID)
	if err != nil {
		if errors.Is(err, domain.ErrGroupMemberNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "entry not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if m.GroupID != g.ID {
		httpx.WriteError(w, http.StatusNotFound, "entry not found")
		return
	}

	now := time.Now()
	patch := domain.HealthPatch{
		ManualDisabled: &disabled,
		LastCheckedAt:  &now,
	}
	if !disabled {
		zero := int32(0)
		patch.BackoffStep = &zero
		patch.AutoDisabledUntil = nil
		patch.LastSuccessAt = &now
	}

	if err := h.groupRepo.UpdateMemberHealth(r.Context(), entryID, patch); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	m, err = h.groupRepo.GetMemberByID(r.Context(), entryID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toMgEntryDTO(m, time.Now()))
}

type createGroupMemberReq struct {
	ProviderID int64  `json:"provider_id"`
	Model      string `json:"model"`
}

type patchGroupReq struct {
	Description *string                `json:"description,omitempty"`
	Members     []createGroupMemberReq `json:"members,omitempty"`
}

type setMemberDisabledReq struct {
	Disabled bool `json:"disabled"`
}

func toMemberDTO(m *domain.GroupMember, now time.Time) groupMemberDTO {
	d := groupMemberDTO{
		ID:             m.ID,
		ProviderID:     m.ProviderID,
		ProviderName:   m.ProviderName,
		Model:          m.Model,
		Priority:       m.Priority,
		Health:         string(m.Health(now)),
		ManualDisabled: m.ManualDisabled,
		BackoffStep:    m.BackoffStep,
		LastFailureMsg: m.LastFailureMsg,
	}
	if m.AutoDisabledUntil != nil {
		s := m.AutoDisabledUntil.Format(time.RFC3339)
		d.AutoDisabledUntil = &s
		remaining := m.AutoDisabledUntil.Sub(now).Seconds()
		if remaining > 0 {
			d.RecoverInSeconds = int64(remaining)
		}
	}
	if m.LastFailureAt != nil {
		s := m.LastFailureAt.Format(time.RFC3339)
		d.LastFailureAt = &s
	}
	if m.LastSuccessAt != nil {
		s := m.LastSuccessAt.Format(time.RFC3339)
		d.LastSuccessAt = &s
	}
	if m.LastCheckedAt != nil {
		s := m.LastCheckedAt.Format(time.RFC3339)
		d.LastCheckedAt = &s
	}
	return d
}

// ---- model group routes ----

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.groupRepo.ListGroups(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]groupSummaryDTO, 0, len(groups))
	for _, g := range groups {
		members, err := h.groupRepo.ListMembersByGroupID(r.Context(), g.ID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		now := time.Now()
		avail := 0
		for _, m := range members {
			if m.Health(now) == domain.MemberHealthAvailable {
				avail++
			}
		}
		items = append(items, groupSummaryDTO{
			ID:             g.ID,
			Name:           g.Name,
			Description:    g.Description,
			MemberCount:    len(members),
			AvailableCount: avail,
			CreatedAt:      g.CreatedAt,
			UpdatedAt:      g.UpdatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	var req createGroupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !modelNameRe.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}

	caller, _ := authdomain.UserFromRequest(r)
	var actorID int64
	if caller != nil && h.actorResolver != nil {
		resolved, err := h.actorResolver.From(r.Context(), actor.UserRef(caller.ID, ""))
		if err == nil {
			actorID = resolved.ActorID
		}
	}

	g, err := h.groupRepo.CreateGroup(r.Context(), &domain.ModelGroup{
		Name:        req.Name,
		Description: req.Description,
		ActorID:     actorID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrGroupNameConflict) {
			httpx.WriteError(w, http.StatusConflict, "group name already taken")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if len(req.Members) > 0 {
		members := make([]*domain.GroupMember, 0, len(req.Members))
		for i, m := range req.Members {
			members = append(members, &domain.GroupMember{
				ProviderID: m.ProviderID,
				Model:      m.Model,
				Priority:   int32(i),
			})
		}
		// Validate each member references an existing provider.
		for _, m := range members {
			if _, err := h.repo.GetProviderByID(r.Context(), m.ProviderID); err != nil {
				httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("provider %d not found", m.ProviderID))
				return
			}
		}
		if err := h.groupRepo.ReplaceMembers(r.Context(), g.ID, members); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	detail, err := h.buildGroupDetail(r.Context(), g.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, detail)
}

func (h *Handler) getGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	g, err := h.groupRepo.GetGroupByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrGroupNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail, err := h.buildGroupDetail(r.Context(), g.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) patchGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	g, err := h.groupRepo.GetGroupByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrGroupNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req patchGroupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if req.Description != nil {
		g.Description = *req.Description
		g, err = h.groupRepo.UpdateGroup(r.Context(), g)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if req.Members != nil {
		members := make([]*domain.GroupMember, 0, len(req.Members))
		for i, m := range req.Members {
			members = append(members, &domain.GroupMember{
				ProviderID: m.ProviderID,
				Model:      m.Model,
				Priority:   int32(i),
			})
		}
		// Validate each member references an existing provider.
		for _, m := range members {
			if _, err := h.repo.GetProviderByID(r.Context(), m.ProviderID); err != nil {
				httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("provider %d not found", m.ProviderID))
				return
			}
		}
		if err := h.groupRepo.ReplaceMembers(r.Context(), g.ID, members); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	detail, err := h.buildGroupDetail(r.Context(), g.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	g, err := h.groupRepo.GetGroupByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrGroupNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "group not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.groupRepo.DeleteGroup(r.Context(), g.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setMemberDisabled(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	memberIDStr := chi.URLParam(r, "memberID")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	memberID, err := strconv.ParseInt(memberIDStr, 10, 64)
	if err != nil || memberID <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid member_id")
		return
	}

	var req setMemberDisabledReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	m, err := h.groupRepo.GetMemberByID(r.Context(), memberID)
	if err != nil {
		if errors.Is(err, domain.ErrGroupMemberNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "member not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	g, err := h.groupRepo.GetGroupByID(r.Context(), m.GroupID)
	if err != nil || g.Name != name {
		httpx.WriteError(w, http.StatusNotFound, "member not found")
		return
	}

	now := time.Now()
	patch := domain.HealthPatch{
		ManualDisabled: &req.Disabled,
		LastCheckedAt:  &now,
	}
	if !req.Disabled {
		zero := int32(0)
		patch.BackoffStep = &zero
		patch.AutoDisabledUntil = nil
		patch.LastSuccessAt = &now
	}

	if err := h.groupRepo.UpdateMemberHealth(r.Context(), memberID, patch); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	m, err = h.groupRepo.GetMemberByID(r.Context(), memberID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toMemberDTO(m, time.Now()))
}

func (h *Handler) buildGroupDetail(ctx context.Context, groupID int64) (*groupDetailDTO, error) {
	g, err := h.groupRepo.GetGroupByID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	members, err := h.groupRepo.ListMembersByGroupID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	memberDTOs := make([]groupMemberDTO, 0, len(members))
	for _, m := range members {
		memberDTOs = append(memberDTOs, toMemberDTO(m, now))
	}
	return &groupDetailDTO{
		ID:          g.ID,
		Name:        g.Name,
		Description: g.Description,
		Members:     memberDTOs,
		CreatedAt:   g.CreatedAt,
		UpdatedAt:   g.UpdatedAt,
	}, nil
}

// ---- helpers ----

func (h *Handler) loadProviderByName(w http.ResponseWriter, r *http.Request) (*domain.Provider, bool) {
	name := chi.URLParam(r, "name")
	if !providerNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return nil, false
	}
	p, err := h.repo.GetProviderByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrProviderNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "provider not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return p, true
}
