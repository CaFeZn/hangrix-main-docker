package domain

import (
	"context"
	"errors"
	"time"
)

// ---- model errors ----

var (
	ErrModelNotFound    = errors.New("model definition not found")
	ErrModelNameConflict = errors.New("model name already taken")
	// ErrModelNameConflictsGroup is returned when a proposed model name collides
	// with an existing standalone model group (a model always allocates a backing
	// group of the same name, so the name must be free in the model/group namespace).
	ErrModelNameConflictsGroup = errors.New("model name conflicts with an existing model group")
)

// ---- ModelDefinition ----

// ModelDefinition is one row in llm_models. It declares the spec of a model
// (context window, output tokens, vision/reasoning support) and ties 1:1 to
// a llm_model_groups row that carries the multi-provider dispatch.
type ModelDefinition struct {
	ID                 int64
	Name               string            // [a-z0-9][a-z0-9._-]{0,63}; globally unique
	DisplayName        string
	ContextWindow      int32
	MaxOutputTokens    int32
	Vision             bool
	Reasoning          bool
	ReasoningEffortMap map[string]string // logical effort → provider native value
	GroupID            int64
	ActorID            int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ---- ModelSpec ----

// ModelSpec is the read-only projection returned to agent runtimes.
// It carries just the fields an agent needs to self-tune (context window,
// output limit, reasoning map, vision cap) without exposing internal IDs.
type ModelSpec struct {
	Name               string
	ContextWindow      int32
	MaxOutputTokens    int32
	Vision             bool
	Reasoning          bool
	ReasoningEffortMap map[string]string
}

// ---- ModelRepo ----

// ModelRepo is the persistence abstraction for llm_models rows.
type ModelRepo interface {
	CreateModel(ctx context.Context, m *ModelDefinition) (*ModelDefinition, error)
	GetModelByName(ctx context.Context, name string) (*ModelDefinition, error)
	GetModelByID(ctx context.Context, id int64) (*ModelDefinition, error)
	ListModels(ctx context.Context) ([]*ModelDefinition, error)
	UpdateModel(ctx context.Context, m *ModelDefinition) (*ModelDefinition, error)
	DeleteModel(ctx context.Context, id int64) error

	// CountModelsByName returns the number of models with the given name (0 or 1).
	CountModelsByName(ctx context.Context, name string) (int64, error)

	// CreateModelAtomic creates a model, its backing group, and members in a
	// single transaction. Returns the created model (with populated ID).
	CreateModelAtomic(ctx context.Context, m *ModelDefinition, members []*GroupMember) (*ModelDefinition, error)

	// DeleteModelAtomic deletes a model and its backing group in a single
	// transaction. Model row is deleted first (FK RESTRICT), then the group.
	DeleteModelAtomic(ctx context.Context, id int64) error

	// UpdateModelWithMembersAtomic updates a model's fields and optionally
	// replaces its group members in a single transaction. members==nil means
	// "leave members unchanged".
	UpdateModelWithMembersAtomic(ctx context.Context, m *ModelDefinition, members []*GroupMember) (*ModelDefinition, error)
}
