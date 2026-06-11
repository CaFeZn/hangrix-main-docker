package service

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// ModelWriter handles creation and validation of model definitions, including
// validation of naming constraints and effort maps. The actual transactional
// creation is delegated to ModelRepo.CreateModelAtomic.
type ModelWriter struct {
	modelRepo domain.ModelRepo
	groupRepo domain.GroupRepo
}

// ModelWriterDeps declares the ioc dependencies.
type ModelWriterDeps struct {
	ModelRepo domain.ModelRepo
	GroupRepo domain.GroupRepo
}

// NewModelWriter constructs a ModelWriter.
func NewModelWriter(deps *ModelWriterDeps) *ModelWriter {
	return &ModelWriter{
		modelRepo: deps.ModelRepo,
		groupRepo: deps.GroupRepo,
	}
}

// ValidateModelName checks that a proposed model name is free within the
// model/group namespace. Models and their backing groups share one unique
// namespace, so the name must not already be taken by a model or a standalone
// group. Provider allowed_models is deprecated and plays no role in routing,
// so it is not consulted here.
func (w *ModelWriter) ValidateModelName(ctx context.Context, name string) error {
	// Check models
	n, err := w.modelRepo.CountModelsByName(ctx, name)
	if err != nil {
		return err
	}
	if n > 0 {
		return domain.ErrModelNameConflict
	}
	// Check groups
	n, err = w.groupRepo.CountGroupsByName(ctx, name)
	if err != nil {
		return err
	}
	if n > 0 {
		return domain.ErrModelNameConflictsGroup
	}
	return nil
}

// ValidateEffortMap checks the effort map for basic sanity: keys and values
// must be non-empty strings ≤ 64 characters each. Returns nil when the map is
// valid or empty (empty map is always valid). No key-whitelist check is done
// — any key is allowed (per the secondary revision).
func ValidateEffortMap(effortMap map[string]string) error {
	for k, v := range effortMap {
		if k == "" {
			return fmt.Errorf("effort map key must not be empty")
		}
		if v == "" {
			return fmt.Errorf("effort map value for key %q must not be empty", k)
		}
		if utf8.RuneCountInString(k) > 64 {
			return fmt.Errorf("effort map key %q exceeds 64 characters", k)
		}
		if utf8.RuneCountInString(v) > 64 {
			return fmt.Errorf("effort map value for key %q exceeds 64 characters", k)
		}
	}
	return nil
}

// CreateModelTx validates and creates a model definition, its backing group,
// and members in a single transaction. Returns the created model.
func (w *ModelWriter) CreateModelTx(
	ctx context.Context,
	model *domain.ModelDefinition,
	members []*domain.GroupMember,
) (*domain.ModelDefinition, error) {
	return w.modelRepo.CreateModelAtomic(ctx, model, members)
}
