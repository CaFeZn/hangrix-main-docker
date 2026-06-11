// Package handler exposes the repo_silence module's HTTP surface: read
// silence state, enter/exit silence at the repo level, list audit entries
// and overrides, and manage per-session silence overrides. Mutating endpoints
// require write-level repo permission (owner, admin, or write member for
// user-owned repos; org owner for org-owned repos).
//
// Routes:
//
//	GET    /api/repos/{owner}/{name}/silence          — state + active overrides
//	POST   /api/repos/{owner}/{name}/silence/enter    — manual enter
//	POST   /api/repos/{owner}/{name}/silence/exit     — manual exit
//	GET    /api/repos/{owner}/{name}/silence/audit    — paginated audit log
//	GET    /api/repos/{owner}/{name}/silence/overrides — active overrides
//	POST   /api/agent-sessions/{id}/silence-override  — grant override
//	DELETE /api/agent-sessions/{id}/silence-override  — revoke override
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	silencedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo_silence/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

// nameRe is the canonical owner/repo-name regex, matching the pattern used
// by repo/handler for {owner} and {name} path segments.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,99}$`)

// RouteProvider interface assertion.
var _ interface{ RegisterRoutes(chi.Router) } = (*Handler)(nil)

// Handler serves the silence HTTP API.
type Handler struct {
	store      silencedomain.Store
	controller silencedomain.Controller
	repoStore  repodomain.Store
	resolver   orgdomain.Resolver
	sessions   runnerdomain.Repo
	members    repodomain.MemberStore
	middleware authdomain.Middleware
}

// HandlerDeps is the ioc dependency struct for Handler.
type HandlerDeps struct {
	Store      silencedomain.Store
	Controller silencedomain.Controller
	RepoStore  repodomain.Store
	Resolver   orgdomain.Resolver
	Sessions   runnerdomain.Repo
	Members    repodomain.MemberStore
	Middleware authdomain.Middleware
}

// NewHandler creates a Handler.
func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		store:      deps.Store,
		controller: deps.Controller,
		repoStore:  deps.RepoStore,
		resolver:   deps.Resolver,
		sessions:   deps.Sessions,
		members:    deps.Members,
		middleware: deps.Middleware,
	}
}

// RegisterRoutes mounts the silence API routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	// Repo-scoped routes.
	r.Route("/api/repos/{owner}/{name}/silence", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.getState)
		// Mutations require write-level permission.
		r.Group(func(r chi.Router) {
			r.Use(h.requireRepoManage)
			r.Post("/enter", h.enter)
			r.Post("/exit", h.exit)
		})
		r.Get("/audit", h.listAudit)
		r.Get("/overrides", h.listOverrides)
	})

	// Session-scoped override routes (alternate access path).
	r.Route("/api/agent-sessions/{id}/silence-override", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Post("/", h.grantOverride)
		r.Delete("/", h.revokeOverride)
	})
}

// ---- DTOs ----

type silenceStateResponse struct {
	Active         bool       `json:"active"`
	Source         string     `json:"source"`
	SourceRef      string     `json:"source_ref"`
	Reason         string     `json:"reason,omitempty"`
	EnteredAt      *time.Time `json:"entered_at,omitempty"`
	ExpectedExitAt *time.Time `json:"expected_exit_at,omitempty"`
	Overrides      int        `json:"overrides"`
}

type auditResponse struct {
	Items []auditItem `json:"items"`
	Total int         `json:"total"`
}

type auditItem struct {
	ID        int64     `json:"id"`
	Event     string    `json:"event"`
	Source    string    `json:"source"`
	ActorID   *int64    `json:"actor_id,omitempty"`
	SessionID *int64    `json:"session_id,omitempty"`
	Payload   string    `json:"payload,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type overrideResponse struct {
	Items []overrideItem `json:"items"`
}

type overrideItem struct {
	SessionID int64      `json:"session_id"`
	RepoID    int64      `json:"repo_id"`
	GrantedBy int64      `json:"granted_by"`
	Reason    string     `json:"reason,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	GrantedAt time.Time  `json:"granted_at"`
}

type enterReq struct {
	Reason string `json:"reason,omitempty"`
}

type exitReq struct {
	Reason string `json:"reason,omitempty"`
}

type grantOverrideReq struct {
	Reason    string     `json:"reason,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// ---- Repo-scoped handlers ----

// getState returns the current silence state for a repo plus active override count.
func (h *Handler) getState(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}

	state, err := h.store.GetState(r.Context(), repo.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	overrides, err := h.store.ListOverrides(r.Context(), repo.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if state == nil {
		httpx.WriteJSON(w, http.StatusOK, silenceStateResponse{
			Active:    false,
			Overrides: len(overrides),
		})
		return
	}

	httpx.WriteJSON(w, http.StatusOK, silenceStateResponse{
		Active:         state.Active,
		Source:         state.Source,
		SourceRef:      state.SourceRef,
		Reason:         state.Reason,
		EnteredAt:      state.EnteredAt,
		ExpectedExitAt: state.ExpectedExitAt,
		Overrides:      len(overrides),
	})
}

// enter puts the repo into silence mode manually.
func (h *Handler) enter(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}

	var req enterReq
	_ = json.NewDecoder(r.Body).Decode(&req)

	caller, _ := authdomain.UserFromRequest(r)
	actorID := caller.ID

	err := h.controller.Enter(r.Context(), repo.ID, silencedomain.EnterInput{
		Source:    silencedomain.SourceManual,
		SourceRef: caller.Username,
		Reason:    strings.TrimSpace(req.Reason),
		ActorID:   &actorID,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return updated state.
	h.getState(w, r)
}

// exit takes the repo out of silence mode.
func (h *Handler) exit(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForManage(w, r)
	if !ok {
		return
	}

	var req exitReq
	_ = json.NewDecoder(r.Body).Decode(&req)

	caller, _ := authdomain.UserFromRequest(r)
	actorID := caller.ID

	err := h.controller.Exit(r.Context(), repo.ID, silencedomain.ExitInput{
		Source:  silencedomain.SourceManual,
		Reason:  strings.TrimSpace(req.Reason),
		ActorID: &actorID,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.getState(w, r)
}

// listAudit returns the most recent audit entries for a repo.
func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	entries, err := h.store.ListAudit(r.Context(), repo.ID, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]auditItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, auditItem{
			ID:        e.ID,
			Event:     e.Event,
			Source:    e.Source,
			ActorID:   e.ActorID,
			SessionID: e.SessionID,
			Payload:   e.Payload,
			CreatedAt: e.CreatedAt,
		})
	}

	httpx.WriteJSON(w, http.StatusOK, auditResponse{Items: items, Total: len(items)})
}

// listOverrides returns the active overrides for a repo.
func (h *Handler) listOverrides(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.resolveRepoForRead(w, r)
	if !ok {
		return
	}

	overrides, err := h.store.ListOverrides(r.Context(), repo.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]overrideItem, 0, len(overrides))
	for _, o := range overrides {
		items = append(items, overrideItem{
			SessionID: o.SessionID,
			RepoID:    o.RepoID,
			GrantedBy: o.GrantedBy,
			Reason:    o.Reason,
			ExpiresAt: o.ExpiresAt,
			GrantedAt: o.GrantedAt,
		})
	}

	httpx.WriteJSON(w, http.StatusOK, overrideResponse{Items: items})
}

// ---- Session-scoped handlers ----

// grantOverride exempts a session from silence.
func (h *Handler) grantOverride(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	// Look up the session to get its repoID for permission check.
	sess, err := h.sessions.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "agent session not found")
		return
	}
	if sess.RepoID == nil {
		httpx.WriteError(w, http.StatusBadRequest, "session has no associated repo")
		return
	}

	// Verify the caller can write to the session's repo.
	caller, _ := authdomain.UserFromRequest(r)
	if !h.canManageRepo(r.Context(), caller, *sess.RepoID) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	var req grantOverrideReq
	_ = json.NewDecoder(r.Body).Decode(&req)

	err = h.controller.GrantOverride(r.Context(), sessionID, silencedomain.OverrideInput{
		RepoID:    *sess.RepoID,
		GrantedBy: caller.ID,
		Reason:    strings.TrimSpace(req.Reason),
		ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// revokeOverride removes a session's silence exemption.
func (h *Handler) revokeOverride(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := httpx.ParseID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	// Look up the session to get its repoID for permission check.
	sess, err := h.sessions.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "agent session not found")
		return
	}
	if sess.RepoID == nil {
		httpx.WriteError(w, http.StatusBadRequest, "session has no associated repo")
		return
	}

	// Verify the caller can write to the session's repo.
	caller, _ := authdomain.UserFromRequest(r)
	if !h.canManageRepo(r.Context(), caller, *sess.RepoID) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return
	}

	err = h.controller.RevokeOverride(r.Context(), sessionID, caller.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---- Helpers ----

// resolveRepoForRead loads the repo from {owner}/{name} and enforces read
// visibility. Returns false and writes the HTTP error on failure.
func (h *Handler) resolveRepoForRead(w http.ResponseWriter, r *http.Request) (*repodomain.Repo, bool) {
	repo, ok := h.loadRepoFromPath(w, r)
	if !ok {
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	if repo.Visibility == repodomain.VisibilityPrivate {
		if !h.canReadRepo(r.Context(), caller, repo) {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return nil, false
		}
	}
	return repo, true
}

// resolveRepoForManage loads the repo and enforces write-level
// authorization (matches repo/handler canWriteContents). Returns
// false and writes the HTTP error on failure.
func (h *Handler) resolveRepoForManage(w http.ResponseWriter, r *http.Request) (*repodomain.Repo, bool) {
	repo, ok := h.loadRepoFromPath(w, r)
	if !ok {
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	if !h.canManageRepo(r.Context(), caller, repo.ID) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return repo, true
}

// requireRepoManage is a chi middleware that enforces write-level permission
// on the {owner}/{name} repo before the handler runs.
func (h *Handler) requireRepoManage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repo, ok := h.loadRepoFromPath(w, r)
		if !ok {
			return
		}
		caller, _ := authdomain.UserFromRequest(r)
		if !h.canManageRepo(r.Context(), caller, repo.ID) {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) loadRepoFromPath(w http.ResponseWriter, r *http.Request) (*repodomain.Repo, bool) {
	ownerName := chi.URLParam(r, "owner")
	repoNameParam := chi.URLParam(r, "name")
	if !nameRe.MatchString(ownerName) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid owner")
		return nil, false
	}
	if !nameRe.MatchString(repoNameParam) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return nil, false
	}
	owner, err := h.resolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		if errors.Is(err, orgdomain.ErrOwnerNotFound) || errors.Is(err, orgdomain.ErrOrgReserved) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	repo, err := h.repoStore.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(owner.Kind), owner.ID, repoNameParam)
	if err != nil {
		if errors.Is(err, repodomain.ErrRepoNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return repo, true
}

// canReadRepo checks whether the caller can read a private repo.
func (h *Handler) canReadRepo(ctx context.Context, caller *userdomain.User, repo *repodomain.Repo) bool {
	return canAccessRepo(ctx, caller, repo, h.resolver)
}

// canManageRepo checks whether the caller may enter/exit silence or grant
// overrides. Matches repo/handler canWriteContents: owner, admin, or (for
// user-owned repos) write members. Org-owned repos require org owner.
func (h *Handler) canManageRepo(ctx context.Context, caller *userdomain.User, repoID int64) bool {
	if caller == nil {
		return false
	}
	if caller.Role == userdomain.RoleAdmin {
		return true
	}
	repo, err := h.repoStore.GetByID(ctx, repoID)
	if err != nil {
		return false
	}
	return canManageSilence(ctx, caller, repo, h.resolver, h.members)
}

// canAccessRepo is the shared repo read-access check, extracted so both the
// handler and the repo handler can stay consistent.
func canAccessRepo(ctx context.Context, caller *userdomain.User, repo *repodomain.Repo, resolver orgdomain.Resolver) bool {
	if caller == nil {
		return false
	}
	if caller.Role == userdomain.RoleAdmin {
		return true
	}
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		return caller.ID == repo.OwnerID
	case repodomain.OwnerKindOrg:
		_, ok, err := resolver.Membership(ctx, repo.OwnerID, caller.ID)
		return err == nil && ok
	}
	return false
}

// canManageSilence applies the repo/handler canWriteContents permission model:
// owner, admin, or (for user-owned repos) write members. Org-owned repos
// require org owner.
func canManageSilence(ctx context.Context, caller *userdomain.User, repo *repodomain.Repo, resolver orgdomain.Resolver, members repodomain.MemberStore) bool {
	if caller == nil {
		return false
	}
	if caller.Role == userdomain.RoleAdmin {
		return true
	}
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		if caller.ID == repo.OwnerID {
			return true
		}
		m, err := members.GetMember(ctx, repo.ID, caller.ID)
		if err != nil {
			if errors.Is(err, repodomain.ErrRepoMemberNotFound) {
				return false
			}
			return false
		}
		return m.Role == repodomain.MemberRoleWrite
	case repodomain.OwnerKindOrg:
		role, ok, err := resolver.Membership(ctx, repo.OwnerID, caller.ID)
		if err != nil || !ok {
			return false
		}
		return role == orgdomain.RoleOwner
	}
	return false
}
