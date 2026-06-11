package service

import (
	"context"
	"errors"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/llm_provider/domain"
)

// fakeModelRepo embeds the interface so it satisfies domain.ModelRepo; only the
// methods ValidateModelName actually calls are overridden. Any other call would
// nil-panic, which keeps the test honest about what the code under test touches.
type fakeModelRepo struct {
	domain.ModelRepo
	count int64
}

func (f fakeModelRepo) CountModelsByName(context.Context, string) (int64, error) {
	return f.count, nil
}

// fakeGroupRepo embeds the interface and overrides only CountGroupsByName —
// the sole group-repo method ValidateModelName touches. Any other call would
// nil-panic, keeping the test honest about what the code under test reads.
type fakeGroupRepo struct {
	domain.GroupRepo
	count int64
}

func (f fakeGroupRepo) CountGroupsByName(context.Context, string) (int64, error) {
	return f.count, nil
}

func newWriter(modelCount, groupCount int64) *ModelWriter {
	return NewModelWriter(&ModelWriterDeps{
		ModelRepo: fakeModelRepo{count: modelCount},
		GroupRepo: fakeGroupRepo{count: groupCount},
	})
}

func TestValidateModelName(t *testing.T) {
	tests := []struct {
		name       string
		modelCount int64
		groupCount int64
		want       error
	}{
		{"free name", 0, 0, nil},
		{"existing model", 1, 0, domain.ErrModelNameConflict},
		{"existing standalone group", 0, 1, domain.ErrModelNameConflictsGroup},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newWriter(tt.modelCount, tt.groupCount).ValidateModelName(context.Background(), "gpt-4o")
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateModelName() = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestValidateModelNameIgnoresProviderAllowedModels guards the contract: model
// names are validated only against the model/group namespace. Provider
// allowed_models is deprecated and must play no role, so a free name resolves
// to nil regardless of any provider state.
func TestValidateModelNameIgnoresProviderAllowedModels(t *testing.T) {
	if err := newWriter(0, 0).ValidateModelName(context.Background(), "gpt-4o"); err != nil {
		t.Fatalf("ValidateModelName() = %v, want nil", err)
	}
}
