package handler

import (
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
	issuedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/issue/domain"
	orgdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/org/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/project/domain"
	repodomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/domain"
	userdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/user/domain"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type Handler struct {
	projects   domain.Store
	repos      repodomain.Store
	issues     issuedomain.Store
	users      userdomain.Repo
	resolver   orgdomain.Resolver
	middleware authdomain.Middleware
}

type HandlerDeps struct {
	Projects   domain.Store
	Repos      repodomain.Store
	Issues     issuedomain.Store
	Users      userdomain.Repo
	Resolver   orgdomain.Resolver
	Middleware authdomain.Middleware
}

func NewHandler(deps *HandlerDeps) *Handler {
	return &Handler{
		projects:   deps.Projects,
		repos:      deps.Repos,
		issues:     deps.Issues,
		users:      deps.Users,
		resolver:   deps.Resolver,
		middleware: deps.Middleware,
	}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/projects", func(r chi.Router) {
		r.Use(h.middleware.RequireAuth)
		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Get("/{owner}/{name}", h.get)
		r.Patch("/{owner}/{name}", h.update)
		r.Post("/{owner}/{name}/repos", h.addRepo)
		r.Delete("/{owner}/{name}/repos/{repo_id}", h.removeRepo)
		r.Post("/{owner}/{name}/issue-links", h.linkIssue)
		r.Post("/{owner}/{name}/repo-proposals", h.createRepoProposal)
		r.Patch("/{owner}/{name}/repo-proposals/{proposal_id}", h.updateRepoProposal)
	})
}

type publicProject struct {
	ID               int64                      `json:"id"`
	OwnerKind        string                     `json:"owner_kind"`
	OwnerID          int64                      `json:"owner_id"`
	OwnerName        string                     `json:"owner_name"`
	Name             string                     `json:"name"`
	Description      string                     `json:"description"`
	Visibility       string                     `json:"visibility"`
	Architecture     string                     `json:"architecture"`
	ModuleBoundaries string                     `json:"module_boundaries"`
	Repos            []*domain.ProjectRepo      `json:"repos,omitempty"`
	IssueLinks       []*domain.ProjectIssueLink `json:"issue_links,omitempty"`
	RepoProposals    []*domain.RepoProposal     `json:"repo_proposals,omitempty"`
	CreatedAt        time.Time                  `json:"created_at"`
	UpdatedAt        time.Time                  `json:"updated_at"`
}

func toPublic(p *domain.Project) publicProject {
	return publicProject{
		ID:               p.ID,
		OwnerKind:        string(p.OwnerKind),
		OwnerID:          p.OwnerID,
		OwnerName:        p.OwnerName,
		Name:             p.Name,
		Description:      p.Description,
		Visibility:       string(p.Visibility),
		Architecture:     p.Architecture,
		ModuleBoundaries: p.ModuleBoundaries,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)
	limit, offset := parsePaging(r)
	items, total, err := h.projects.ListReadable(r.Context(), caller.ID, caller.Role == userdomain.RoleAdmin, offset, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := make([]publicProject, 0, len(items))
	for _, p := range items {
		resp = append(resp, toPublic(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": resp, "total": total, "limit": limit, "offset": offset})
}

type createReq struct {
	Owner            string `json:"owner"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Visibility       string `json:"visibility"`
	Architecture     string `json:"architecture"`
	ModuleBoundaries string `json:"module_boundaries"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	caller, _ := authdomain.UserFromRequest(r)
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !nameRe.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	visibility := domain.Visibility(strings.TrimSpace(req.Visibility))
	if visibility == "" {
		visibility = domain.VisibilityPrivate
	}
	if !visibility.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid visibility")
		return
	}
	ownerKind, ownerID, _, ok := h.resolveCreateOwner(w, r, caller, strings.TrimSpace(req.Owner))
	if !ok {
		return
	}
	p, err := h.projects.Create(r.Context(), &domain.Project{
		OwnerKind:        ownerKind,
		OwnerID:          ownerID,
		Name:             req.Name,
		Description:      req.Description,
		Visibility:       visibility,
		Architecture:     req.Architecture,
		ModuleBoundaries: req.ModuleBoundaries,
		CreatedBy:        caller.ID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrProjectConflict) {
			httpx.WriteError(w, http.StatusConflict, "project already exists")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toPublic(p))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.resolveProject(w, r)
	if !ok {
		return
	}
	out := toPublic(p)
	h.fillProjectDetail(r, &out)
	httpx.WriteJSON(w, http.StatusOK, out)
}

type updateReq struct {
	Description      *string `json:"description"`
	Visibility       *string `json:"visibility"`
	Architecture     *string `json:"architecture"`
	ModuleBoundaries *string `json:"module_boundaries"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	p, ok := h.resolveProjectForManage(w, r)
	if !ok {
		return
	}
	var req updateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Description != nil {
		p.Description = *req.Description
	}
	if req.Visibility != nil {
		v := domain.Visibility(strings.TrimSpace(*req.Visibility))
		if !v.Valid() {
			httpx.WriteError(w, http.StatusBadRequest, "invalid visibility")
			return
		}
		p.Visibility = v
	}
	if req.Architecture != nil {
		p.Architecture = *req.Architecture
	}
	if req.ModuleBoundaries != nil {
		p.ModuleBoundaries = *req.ModuleBoundaries
	}
	updated, err := h.projects.Update(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPublic(updated))
}

type addRepoReq struct {
	RepoID  int64  `json:"repo_id"`
	Owner   string `json:"owner"`
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	Role    string `json:"role"`
}

func (h *Handler) addRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := h.resolveProjectForManage(w, r)
	if !ok {
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	var req addRepoReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	repo, ok := h.resolveRepoFromReq(w, r, req.RepoID, req.Owner, req.Name)
	if !ok {
		return
	}
	item, err := h.projects.AddRepo(r.Context(), p.ID, repo.ID, caller.ID, req.Purpose, req.Role)
	if err != nil {
		if errors.Is(err, domain.ErrLinkConflict) {
			httpx.WriteError(w, http.StatusConflict, "repo already linked")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, item)
}

func (h *Handler) removeRepo(w http.ResponseWriter, r *http.Request) {
	p, ok := h.resolveProjectForManage(w, r)
	if !ok {
		return
	}
	repoID, err := strconv.ParseInt(chi.URLParam(r, "repo_id"), 10, 64)
	if err != nil || repoID <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid repo_id")
		return
	}
	if err := h.projects.RemoveRepo(r.Context(), p.ID, repoID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type linkIssueReq struct {
	RepoID      int64  `json:"repo_id"`
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	IssueNumber int64  `json:"issue_number"`
	Kind        string `json:"kind"`
	Summary     string `json:"summary"`
}

func (h *Handler) linkIssue(w http.ResponseWriter, r *http.Request) {
	p, ok := h.resolveProjectForManage(w, r)
	if !ok {
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	var req linkIssueReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.IssueNumber <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "issue_number is required")
		return
	}
	repo, ok := h.resolveRepoFromReq(w, r, req.RepoID, req.Owner, req.Name)
	if !ok {
		return
	}
	iss, err := h.issues.GetByNumber(r.Context(), repo.ID, req.IssueNumber)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "issue not found")
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "implementation"
	}
	item, err := h.projects.LinkIssue(r.Context(), p.ID, repo.ID, iss.ID, caller.ID, kind, req.Summary)
	if err != nil {
		if errors.Is(err, domain.ErrLinkConflict) {
			httpx.WriteError(w, http.StatusConflict, "issue already linked")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, item)
}

type repoProposalReq struct {
	SourceRepoID   *int64 `json:"source_repo_id"`
	SourceIssueID  *int64 `json:"source_issue_id"`
	OwnerName      string `json:"owner_name"`
	RepoName       string `json:"repo_name"`
	Description    string `json:"description"`
	Reason         string `json:"reason"`
	ModuleBoundary string `json:"module_boundary"`
}

func (h *Handler) createRepoProposal(w http.ResponseWriter, r *http.Request) {
	p, ok := h.resolveProjectForManage(w, r)
	if !ok {
		return
	}
	caller, _ := authdomain.UserFromRequest(r)
	var req repoProposalReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.OwnerName = strings.TrimSpace(req.OwnerName)
	req.RepoName = strings.TrimSpace(req.RepoName)
	if req.OwnerName == "" {
		req.OwnerName = p.OwnerName
	}
	if !nameRe.MatchString(req.OwnerName) || !nameRe.MatchString(req.RepoName) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid owner_name or repo_name")
		return
	}
	item, err := h.projects.CreateRepoProposal(r.Context(), &domain.RepoProposal{
		ProjectID:      p.ID,
		SourceRepoID:   req.SourceRepoID,
		SourceIssueID:  req.SourceIssueID,
		OwnerName:      req.OwnerName,
		RepoName:       req.RepoName,
		Description:    req.Description,
		Reason:         req.Reason,
		ModuleBoundary: req.ModuleBoundary,
		Status:         domain.RepoProposalPending,
		CreatedBy:      caller.ID,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, item)
}

type updateProposalReq struct {
	Status       string `json:"status"`
	TargetRepoID *int64 `json:"target_repo_id"`
}

func (h *Handler) updateRepoProposal(w http.ResponseWriter, r *http.Request) {
	p, ok := h.resolveProjectForManage(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "proposal_id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid proposal_id")
		return
	}
	var req updateProposalReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	status := domain.RepoProposalStatus(strings.TrimSpace(req.Status))
	if !status.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "invalid status")
		return
	}
	item, err := h.projects.UpdateRepoProposalStatus(r.Context(), p.ID, id, status, req.TargetRepoID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) fillProjectDetail(r *http.Request, out *publicProject) {
	out.Repos, _ = h.projects.ListRepos(r.Context(), out.ID)
	out.IssueLinks, _ = h.projects.ListIssueLinks(r.Context(), out.ID)
	out.RepoProposals, _ = h.projects.ListRepoProposals(r.Context(), out.ID)
}

func (h *Handler) resolveCreateOwner(w http.ResponseWriter, r *http.Request, caller *userdomain.User, ownerName string) (domain.OwnerKind, int64, string, bool) {
	if ownerName == "" || ownerName == caller.Username {
		return domain.OwnerKindUser, caller.ID, caller.Username, true
	}
	owner, err := h.resolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "owner not found")
		return "", 0, "", false
	}
	if owner.Kind == orgdomain.OwnerKindUser {
		if owner.ID != caller.ID && caller.Role != userdomain.RoleAdmin {
			httpx.WriteError(w, http.StatusForbidden, "forbidden")
			return "", 0, "", false
		}
		return domain.OwnerKindUser, owner.ID, owner.Name, true
	}
	role, member, err := h.resolver.Membership(r.Context(), owner.ID, caller.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return "", 0, "", false
	}
	if caller.Role != userdomain.RoleAdmin && (!member || role != orgdomain.RoleOwner) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return "", 0, "", false
	}
	return domain.OwnerKindOrg, owner.ID, owner.Name, true
}

func (h *Handler) resolveProject(w http.ResponseWriter, r *http.Request) (*domain.Project, bool) {
	ownerName := chi.URLParam(r, "owner")
	name := chi.URLParam(r, "name")
	if !nameRe.MatchString(ownerName) || !nameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid project")
		return nil, false
	}
	owner, err := h.resolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "project not found")
		return nil, false
	}
	p, err := h.projects.GetByOwnerAndName(r.Context(), domain.OwnerKind(owner.Kind), owner.ID, name)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "project not found")
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	if !h.canRead(r, caller, p) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return p, true
}

func (h *Handler) resolveProjectForManage(w http.ResponseWriter, r *http.Request) (*domain.Project, bool) {
	p, ok := h.resolveProject(w, r)
	if !ok {
		return nil, false
	}
	caller, _ := authdomain.UserFromRequest(r)
	if !h.canManage(r, caller, p) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden")
		return nil, false
	}
	return p, true
}

func (h *Handler) resolveRepoFromReq(w http.ResponseWriter, r *http.Request, repoID int64, ownerName, name string) (*repodomain.Repo, bool) {
	if repoID > 0 {
		repo, err := h.repos.GetByID(r.Context(), repoID)
		if err != nil {
			httpx.WriteError(w, http.StatusNotFound, "repo not found")
			return nil, false
		}
		return repo, true
	}
	ownerName = strings.TrimSpace(ownerName)
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(ownerName) || !nameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid repo")
		return nil, false
	}
	owner, err := h.resolver.ResolveOwner(r.Context(), ownerName)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "repo not found")
		return nil, false
	}
	repo, err := h.repos.GetByOwnerAndName(r.Context(), repodomain.OwnerKind(owner.Kind), owner.ID, name)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "repo not found")
		return nil, false
	}
	return repo, true
}

func (h *Handler) canRead(r *http.Request, caller *userdomain.User, p *domain.Project) bool {
	if p.Visibility == domain.VisibilityPublic {
		return true
	}
	return h.canManage(r, caller, p) || h.isOrgMember(r, caller, p)
}

func (h *Handler) canManage(r *http.Request, caller *userdomain.User, p *domain.Project) bool {
	if caller == nil {
		return false
	}
	if caller.Role == userdomain.RoleAdmin {
		return true
	}
	switch p.OwnerKind {
	case domain.OwnerKindUser:
		return caller.ID == p.OwnerID
	case domain.OwnerKindOrg:
		role, ok, err := h.resolver.Membership(r.Context(), p.OwnerID, caller.ID)
		return err == nil && ok && role == orgdomain.RoleOwner
	default:
		return false
	}
}

func (h *Handler) isOrgMember(r *http.Request, caller *userdomain.User, p *domain.Project) bool {
	if caller == nil || p.OwnerKind != domain.OwnerKindOrg {
		return false
	}
	_, ok, err := h.resolver.Membership(r.Context(), p.OwnerID, caller.ID)
	return err == nil && ok
}

func parsePaging(r *http.Request) (int32, int32) {
	limit := int32(50)
	offset := int32(0)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 32); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 32); err == nil && n >= 0 {
			offset = int32(n)
		}
	}
	return limit, offset
}
