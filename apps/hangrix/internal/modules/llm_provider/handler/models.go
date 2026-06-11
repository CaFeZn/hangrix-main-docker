package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	authdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/service"
	"github.com/hangrix/hangrix/pkg/actor"
)

// ---- model DTOs ----

type modelListItemDTO struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	DisplayName    string    `json:"display_name"`
	ContextWindow  int32     `json:"context_window"`
	MaxOutputTokens int32   `json:"max_output_tokens"`
	Vision         bool      `json:"vision"`
	MemberCount    int       `json:"member_count"`
	AvailableCount int       `json:"available_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type modelDetailDTO struct {
	ID                 int64             `json:"id"`
	Name               string            `json:"name"`
	DisplayName        string            `json:"display_name"`
	ContextWindow      int32             `json:"context_window"`
	MaxOutputTokens    int32             `json:"max_output_tokens"`
	Vision             bool              `json:"vision"`
	ReasoningEffortMap map[string]string `json:"reasoning_effort_map"`
	GroupID            int64             `json:"group_id"`
	Members            []groupMemberDTO  `json:"members"`
	ActorID            int64             `json:"actor_id"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

type createModelReq struct {
	Name               string                   `json:"name"`
	DisplayName        string                   `json:"display_name"`
	ContextWindow      int32                    `json:"context_window"`
	MaxOutputTokens    int32                    `json:"max_output_tokens"`
	Vision             bool                     `json:"vision"`
	ReasoningEffortMap map[string]string        `json:"reasoning_effort_map"`
	Members            []createGroupMemberReq   `json:"members"`
}

type patchModelReq struct {
	DisplayName        *string                  `json:"display_name,omitempty"`
	ContextWindow      *int32                   `json:"context_window,omitempty"`
	MaxOutputTokens    *int32                   `json:"max_output_tokens,omitempty"`
	Vision             *bool                    `json:"vision,omitempty"`
	ReasoningEffortMap map[string]string        `json:"reasoning_effort_map,omitempty"`
	Members            []createGroupMemberReq   `json:"members,omitempty"`
}

// ---- model handlers ----

func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.modelRepo.ListModels(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]modelListItemDTO, 0, len(models))
	for _, m := range models {
		members, err := h.groupRepo.ListMembersByGroupID(r.Context(), m.GroupID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		now := time.Now()
		avail := 0
		for _, mb := range members {
			if mb.Health(now) == domain.MemberHealthAvailable {
				avail++
			}
		}
		items = append(items, modelListItemDTO{
			ID:              m.ID,
			Name:            m.Name,
			DisplayName:     m.DisplayName,
			ContextWindow:   m.ContextWindow,
			MaxOutputTokens: m.MaxOutputTokens,
			Vision:          m.Vision,
			MemberCount:     len(members),
			AvailableCount:  avail,
			CreatedAt:       m.CreatedAt,
			UpdatedAt:       m.UpdatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createModel(w http.ResponseWriter, r *http.Request) {
	var req createModelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !modelNameRe.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	if req.ContextWindow <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "context_window must be > 0")
		return
	}
	if req.MaxOutputTokens <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "max_output_tokens must be > 0")
		return
	}

	// Validate effort map
	if err := service.ValidateEffortMap(req.ReasoningEffortMap); err != nil {
		httpx.WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Validate the model name is free within the model/group namespace
	// (see ValidateModelName).
	if h.modelWriter != nil {
		if err := h.modelWriter.ValidateModelName(r.Context(), req.Name); err != nil {
			if errors.Is(err, domain.ErrModelNameConflict) || errors.Is(err, domain.ErrModelNameConflictsGroup) {
				httpx.WriteError(w, http.StatusConflict, err.Error())
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
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

	if req.ReasoningEffortMap == nil {
		req.ReasoningEffortMap = make(map[string]string)
	}

	model := &domain.ModelDefinition{
		Name:               req.Name,
		DisplayName:        req.DisplayName,
		ContextWindow:      req.ContextWindow,
		MaxOutputTokens:    req.MaxOutputTokens,
		Vision:             req.Vision,
		Reasoning:          true, // reasoning is always enabled by default
		ReasoningEffortMap: req.ReasoningEffortMap,
		ActorID:            actorID,
	}

	members := make([]*domain.GroupMember, 0, len(req.Members))
	for _, m := range req.Members {
		members = append(members, &domain.GroupMember{
			ProviderID: m.ProviderID,
			Model:      m.Model,
		})
	}

	created, err := h.modelWriter.CreateModelTx(r.Context(), model, members)
	if err != nil {
		if errors.Is(err, domain.ErrModelNameConflict) {
			httpx.WriteError(w, http.StatusConflict, "model name already taken")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	detail, err := h.buildModelDetail(r.Context(), created.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, detail)
}

func (h *Handler) getModel(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	m, err := h.modelRepo.GetModelByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrModelNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "model not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail, err := h.buildModelDetail(r.Context(), m.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) patchModel(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	existing, err := h.modelRepo.GetModelByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrModelNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "model not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req patchModelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}

	updated := *existing

	if req.DisplayName != nil {
		updated.DisplayName = *req.DisplayName
	}
	if req.ContextWindow != nil {
		if *req.ContextWindow <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, "context_window must be > 0")
			return
		}
		updated.ContextWindow = *req.ContextWindow
	}
	if req.MaxOutputTokens != nil {
		if *req.MaxOutputTokens <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, "max_output_tokens must be > 0")
			return
		}
		updated.MaxOutputTokens = *req.MaxOutputTokens
	}
	if req.Vision != nil {
		updated.Vision = *req.Vision
	}
	if req.ReasoningEffortMap != nil {
		if err := service.ValidateEffortMap(req.ReasoningEffortMap); err != nil {
			httpx.WriteError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		updated.ReasoningEffortMap = req.ReasoningEffortMap
	}

	var members []*domain.GroupMember
	if req.Members != nil {
		members = make([]*domain.GroupMember, 0, len(req.Members))
		for i, m := range req.Members {
			members = append(members, &domain.GroupMember{
				ProviderID: m.ProviderID,
				Model:      m.Model,
				Priority:   int32(i),
			})
		}
	}

	out, err := h.modelRepo.UpdateModelWithMembersAtomic(r.Context(), &updated, members)
	if err != nil {
		if errors.Is(err, domain.ErrModelNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "model not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	detail, err := h.buildModelDetail(r.Context(), out.ID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

func (h *Handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !modelNameRe.MatchString(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	m, err := h.modelRepo.GetModelByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, domain.ErrModelNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "model not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// DeleteModelAtomic deletes the model first (FK RESTRICT), then
	// the backing group (CASCADE members), in a single transaction.
	if err := h.modelRepo.DeleteModelAtomic(r.Context(), m.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) buildModelDetail(ctx context.Context, modelID int64) (*modelDetailDTO, error) {
	m, err := h.modelRepo.GetModelByID(ctx, modelID)
	if err != nil {
		return nil, err
	}
	members, err := h.groupRepo.ListMembersByGroupID(ctx, m.GroupID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	memberDTOs := make([]groupMemberDTO, 0, len(members))
	for _, mb := range members {
		memberDTOs = append(memberDTOs, toMemberDTO(mb, now))
	}
	return &modelDetailDTO{
		ID:                 m.ID,
		Name:               m.Name,
		DisplayName:        m.DisplayName,
		ContextWindow:      m.ContextWindow,
		MaxOutputTokens:    m.MaxOutputTokens,
		Vision:             m.Vision,
		ReasoningEffortMap: m.ReasoningEffortMap,
		GroupID:            m.GroupID,
		Members:            memberDTOs,
		ActorID:            m.ActorID,
		CreatedAt:          m.CreatedAt,
		UpdatedAt:          m.UpdatedAt,
	}, nil
}
