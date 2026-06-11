package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	repoinfra "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/infra"
	skilldomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/skill/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

type Handler struct {
	skills     skilldomain.Resolver
	repos      repodomain.Store
	storage    *repoinfra.Storage
	resolver   orgdomain.Resolver
	middleware authdomain.Middleware
}

type HandlerDeps struct {
	Skills     skilldomain.Resolver
	Repos      repodomain.Store
	Storage    *repoinfra.Storage
	Resolver   orgdomain.Resolver
	Middleware authdomain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		skills:     deps.Skills,
		repos:      deps.Repos,
		storage:    deps.Storage,
		resolver:   deps.Resolver,
		middleware: deps.Middleware,
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.With(h.middleware.RequireAuth).Get("/api/repos/{owner}/{name}/skills", h.list)
}

type listResp struct {
	Skills []listSkill `json:"skills"`
}

type listSkill struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type repoCtx struct {
	repo   *repodomain.Repo
	fsPath string
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.resolveRepo(w, r)
	if !ok {
		return
	}
	items, err := h.skills.List(r.Context(), rc.fsPath, rc.repo.DefaultBranch)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]listSkill, 0, len(items))
	for _, sk := range items {
		out = append(out, listSkill{
			Slug:        sk.Slug,
			Name:        sk.Name,
			Description: sk.Description,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, listResp{Skills: out})
}

func (h *Handler) resolveRepo(w http.ResponseWriter, r *http.Request) (*repoCtx, bool) {
	owner := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "name")
	resolved, err := h.resolver.ResolveOwner(r.Context(), owner)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "repo not found")
		return nil, false
	}
	repo, err := h.repos.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(resolved.Kind), resolved.ID, name)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "repo not found")
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	if repo.Visibility == repodomain.VisibilityPrivate {
		ok, err := h.canRead(r, caller, repo)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !ok {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return nil, false
		}
	}
	fsPath, err := h.storage.ResolvePath(repo.OwnerName, repo.Name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "resolve path: "+err.Error())
		return nil, false
	}
	return &repoCtx{repo: repo, fsPath: fsPath}, true
}

func (h *Handler) canRead(r *http.Request, caller *userdomain.User, repo *repodomain.Repo) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.Role == userdomain.RoleAdmin {
		return true, nil
	}
	switch repo.OwnerKind {
	case repodomain.OwnerKindUser:
		return caller.ID == repo.OwnerID, nil
	case repodomain.OwnerKindOrg:
		_, ok, err := h.resolver.Membership(r.Context(), repo.OwnerID, caller.ID)
		return ok, err
	}
	return false, nil
}
