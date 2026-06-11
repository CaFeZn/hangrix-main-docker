package service

import (
	"context"
	"errors"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// Compile-time guard: *GroupRouter must satisfy domain.Lookup.
var _ domain.Lookup = (*GroupRouter)(nil)

// GroupRouter implements domain.Lookup by combining provider-level routing
// with model-group resolution. It is the single entry-point for the proxy.
type GroupRouter struct {
	repo      domain.Repo
	groupRepo domain.GroupRepo
	modelRepo domain.ModelRepo
}

// GroupRouterDeps declares the Ioc dependencies for NewGroupRouter.
type GroupRouterDeps struct {
	Repo      domain.Repo
	GroupRepo domain.GroupRepo
	ModelRepo domain.ModelRepo
}

// NewGroupRouter constructs a GroupRouter that satisfies domain.Lookup.
func NewGroupRouter(deps *GroupRouterDeps) *GroupRouter {
	return &GroupRouter{
		repo:      deps.Repo,
		groupRepo: deps.GroupRepo,
		modelRepo: deps.ModelRepo,
	}
}

// ResolveModel resolves a model name to an ordered list of ready-to-dispatch candidates.
//
// Algorithm:
//  1. Interpret the name as a model/group name. Every model definition owns a
//     backing group of the same name, so this is the only routing path. Load
//     the group's members (already joined with provider info), filter to
//     Health(now)==Available, and return them ordered by priority.
//  2. If no group matches, return ErrNoModelMatch.
//  3. If the group exists but all members are unavailable, return ErrGroupAllUnavailable.
//
// The legacy single-provider fallback (scanning a provider's allowed_models)
// has been removed — allowed_models is deprecated.
func (r *GroupRouter) ResolveModel(ctx context.Context, model string) (domain.RouteResolution, error) {
	g, err := r.groupRepo.GetGroupByName(ctx, model)
	if err != nil {
		if errors.Is(err, domain.ErrGroupNotFound) {
			return domain.RouteResolution{}, domain.ErrNoModelMatch
		}
		return domain.RouteResolution{}, err
	}

	members, err := r.groupRepo.ListMembersByGroupID(ctx, g.ID)
	if err != nil {
		return domain.RouteResolution{}, err
	}
	now := time.Now()
	candidates := make([]domain.ResolvedCandidate, 0, len(members))
	for _, m := range members {
		if m.Health(now) == domain.MemberHealthAvailable {
			prov, err := r.repo.GetProviderByID(ctx, m.ProviderID)
			if err != nil {
				// Provider deleted concurrently — skip this member.
				continue
			}
			candidates = append(candidates, domain.ResolvedCandidate{
				Provider: prov,
				Model:    m.Model,
				MemberID: m.ID,
			})
		}
	}
	if len(candidates) == 0 {
		return domain.RouteResolution{}, domain.ErrGroupAllUnavailable
	}
	return domain.RouteResolution{
		Kind:       domain.RouteKindGroup,
		GroupName:  g.Name,
		Candidates: candidates,
	}, nil
}

// ReportAttempt feeds a dispatch outcome back to the state machine.
// memberID=0 is a no-op (non-group route).
func (r *GroupRouter) ReportAttempt(ctx context.Context, memberID int64, outcome domain.AttemptOutcome) error {
	if memberID == 0 {
		return nil // non-group route
	}

	m, err := r.groupRepo.GetMemberByID(ctx, memberID)
	if err != nil {
		return err
	}

	now := time.Now()
	patch := domain.HealthPatch{
		LastCheckedAt: &now,
	}

	if outcome.Success {
		zero := int32(0)
		patch.BackoffStep = &zero
		patch.AutoDisabledUntil = nil // explicitly clear auto-disable
		patch.LastSuccessAt = &now
	} else {
		patch.LastFailureAt = &now
		patch.LastFailureMsg = &outcome.Message
		if isRetryableFailure(outcome.StatusCode) {
			newStep, until := NextBackoff(m.BackoffStep)
			patch.BackoffStep = &newStep
			patch.AutoDisabledUntil = &until
		}
		// Non-retryable failures (4xx client errors): record the failure
		// but do NOT increment backoff or set auto_disabled_until.
		// The member remains available for the next request.
	}

	return r.groupRepo.UpdateMemberHealth(ctx, memberID, patch)
}

// GetModelSpec returns the read-only ModelSpec for a given model name.
// It is the implementation of domain.Lookup.GetModelSpec and is used by
// both the proxy (for reasoning-effort translation) and the Agent API.
func (r *GroupRouter) GetModelSpec(ctx context.Context, name string) (*domain.ModelSpec, error) {
	md, err := r.modelRepo.GetModelByName(ctx, name)
	if err == nil {
		return &domain.ModelSpec{
			Name:               md.Name,
			ContextWindow:      md.ContextWindow,
			MaxOutputTokens:    md.MaxOutputTokens,
			Vision:             md.Vision,
			Reasoning:          md.Reasoning,
			ReasoningEffortMap: md.ReasoningEffortMap,
		}, nil
	}
	if !errors.Is(err, domain.ErrModelNotFound) {
		return nil, err
	}

	return r.getGroupModelSpec(ctx, name)
}

func (r *GroupRouter) getGroupModelSpec(ctx context.Context, name string) (*domain.ModelSpec, error) {
	g, err := r.groupRepo.GetGroupByName(ctx, name)
	if err != nil {
		if errors.Is(err, domain.ErrGroupNotFound) {
			return nil, domain.ErrModelNotFound
		}
		return nil, err
	}
	members, err := r.groupRepo.ListMembersByGroupID(ctx, g.ID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	spec, found := r.groupSpecFromMembers(ctx, name, members, func(m *domain.GroupMember) bool {
		return m.Health(now) == domain.MemberHealthAvailable
	})
	if found {
		return spec, nil
	}

	// If the group exists but every member is temporarily unavailable, still
	// return a conservative spec from all known concrete model definitions so
	// agents can boot and report the routing failure through the normal LLM path.
	spec, found = r.groupSpecFromMembers(ctx, name, members, func(_ *domain.GroupMember) bool { return true })
	if found {
		return spec, nil
	}
	return nil, domain.ErrModelNotFound
}

func (r *GroupRouter) groupSpecFromMembers(ctx context.Context, name string, members []*domain.GroupMember, include func(*domain.GroupMember) bool) (*domain.ModelSpec, bool) {
	out := &domain.ModelSpec{Name: name}
	for _, m := range members {
		if !include(m) {
			continue
		}
		md, err := r.modelRepo.GetModelByName(ctx, m.Model)
		if err != nil {
			continue
		}
		if out.ContextWindow == 0 || md.ContextWindow < out.ContextWindow {
			out.ContextWindow = md.ContextWindow
		}
		if out.MaxOutputTokens == 0 || md.MaxOutputTokens < out.MaxOutputTokens {
			out.MaxOutputTokens = md.MaxOutputTokens
		}
		out.Vision = out.Vision || md.Vision
		out.Reasoning = out.Reasoning || md.Reasoning
	}
	if out.ContextWindow == 0 || out.MaxOutputTokens == 0 {
		return nil, false
	}
	return &domain.ModelSpec{
		Name:               out.Name,
		ContextWindow:      out.ContextWindow,
		MaxOutputTokens:    out.MaxOutputTokens,
		Vision:             out.Vision,
		Reasoning:          out.Reasoning,
		ReasoningEffortMap: map[string]string{},
	}, true
}

// RecordUsage delegates to the underlying repo. It is required by the
// domain.Lookup interface so the proxy can record usage for both group and
// non-group routes.
func (r *GroupRouter) RecordUsage(ctx context.Context, u *domain.UsageRecord) error {
	return r.repo.RecordUsage(ctx, u)
}
