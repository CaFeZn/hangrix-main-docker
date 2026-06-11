// Package domain declares model-group types and the GroupRepo interface.
// Model groups live alongside provider-level routing: the Lookup interface
// (defined in provider.go) is extended with ResolveModel / ReportAttempt
// so callers only see a single routing entry-point.
package domain

import (
	"context"
	"errors"
	"time"
)

// ---- group errors ----

var (
	ErrGroupNotFound       = errors.New("model group not found")
	ErrGroupNameConflict   = errors.New("model group name already taken")
	ErrGroupAllUnavailable = errors.New("all members of the model group are currently unavailable")
	ErrGroupMemberNotFound = errors.New("model group member not found")
)

// ---- ModelGroup ----

// ModelGroup is a named collection of (provider, model) members ordered by priority.
// The group name is used as the model string in agents.yml.
type ModelGroup struct {
	ID          int64
	Name        string
	Description string
	ActorID     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ---- MemberHealth ----

// MemberHealth is the derived availability status of a group member.
// It is never stored in the database; the service layer computes it
// from the raw fields (manual_disabled, auto_disabled_until, provider.disabled)
// evaluated against NOW().
type MemberHealth string

const (
	MemberHealthAvailable        MemberHealth = "available"
	MemberHealthAutoDisabled     MemberHealth = "auto_disabled"
	MemberHealthManualDisabled   MemberHealth = "manual_disabled"
	MemberHealthProviderDisabled MemberHealth = "provider_disabled"
)

// ---- GroupMember ----

// GroupMember is one (group, provider, model) triple with its runtime health state.
// ProviderName and ProviderDisabled are joined from llm_providers at read time.
type GroupMember struct {
	ID               int64
	GroupID          int64
	ProviderID       int64
	ProviderName     string // joined from llm_providers.name
	Model            string
	Priority         int32
	ManualDisabled   bool
	AutoDisabledUntil *time.Time
	BackoffStep      int32
	LastFailureAt    *time.Time
	LastFailureMsg   string
	LastSuccessAt    *time.Time
	LastCheckedAt    *time.Time
	ProviderDisabled bool // joined from llm_providers.disabled
}

// Health computes the derived MemberHealth by evaluating the raw fields against now.
// Order matters: manual > provider > auto > available.
func (m *GroupMember) Health(now time.Time) MemberHealth {
	if m.ManualDisabled {
		return MemberHealthManualDisabled
	}
	if m.ProviderDisabled {
		return MemberHealthProviderDisabled
	}
	if m.AutoDisabledUntil != nil && m.AutoDisabledUntil.After(now) {
		return MemberHealthAutoDisabled
	}
	return MemberHealthAvailable
}

// ---- HealthPatch ----

// HealthPatch carries the fields to atomically update on a member's health row.
// Nil pointer means "leave unchanged"; explicit zero value means "set to zero".
type HealthPatch struct {
	AutoDisabledUntil *time.Time
	BackoffStep       *int32
	ManualDisabled    *bool
	LastFailureAt     *time.Time
	LastFailureMsg    *string
	LastSuccessAt     *time.Time
	LastCheckedAt     *time.Time
}

// ---- GroupRepo ----

// GroupRepo is the persistence abstraction for model groups and their members.
// The Postgres implementation in infra/ satisfies this interface.
type GroupRepo interface {
	// Groups
	CreateGroup(ctx context.Context, g *ModelGroup) (*ModelGroup, error)
	GetGroupByName(ctx context.Context, name string) (*ModelGroup, error)
	GetGroupByID(ctx context.Context, id int64) (*ModelGroup, error)
	ListGroups(ctx context.Context) ([]*ModelGroup, error)
	UpdateGroup(ctx context.Context, g *ModelGroup) (*ModelGroup, error)
	DeleteGroup(ctx context.Context, id int64) error

	// Members
	ReplaceMembers(ctx context.Context, groupID int64, members []*GroupMember) error
	ListMembersByGroupID(ctx context.Context, groupID int64) ([]*GroupMember, error)
	GetMemberByID(ctx context.Context, id int64) (*GroupMember, error)
	UpdateMemberHealth(ctx context.Context, id int64, patch HealthPatch) error

	// CountGroupsByName reports whether a model-group name is already taken
	// (used to keep the model/group namespace unique).
	CountGroupsByName(ctx context.Context, name string) (int64, error)
}

// ---- Lookup extension types ----

// AttemptOutcome captures the result of a single upstream dispatch attempt.
type AttemptOutcome struct {
	Success    bool
	StatusCode int
	Message    string // only meaningful on failure
}

// RouteKind signals whether the resolved route is a group or a single provider.
type RouteKind string

const (
	RouteKindSingle RouteKind = "single"
	RouteKindGroup  RouteKind = "group"
)

// ResolvedCandidate is one ready-to-call upstream target returned by ResolveModel.
// MemberID is non-zero only for group-routed candidates; it is passed back to
// ReportAttempt so the state machine can update the correct member row without
// a secondary lookup.
type ResolvedCandidate struct {
	Provider *Provider
	Model    string // the actual upstream model name to use in the request
	MemberID int64  // 0 for non-group routes
}

// RouteResolution is the result of ResolveModel: a kind tag and an ordered list
// of ready-to-dispatch candidates (already filtered to health=available).
type RouteResolution struct {
	Kind       RouteKind
	GroupName  string // populated only for group routes, for error messages
	Candidates []ResolvedCandidate
}
