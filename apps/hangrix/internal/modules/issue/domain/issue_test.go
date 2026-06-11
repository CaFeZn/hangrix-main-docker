package domain

import (
	"errors"
	"testing"
)

func TestCanTransitionState(t *testing.T) {
	tests := []struct {
		from, to State
		wantErr  error
	}{
		// open ↔ closed is allowed both ways at the domain level.
		// (The sub-issue gate is applied by the handler, not the domain.)
		{StateOpen, StateClosed, nil},
		// no-op transitions are always nil.
		{StateOpen, StateOpen, nil},
		{StateClosed, StateClosed, nil},
		// closed → open is permanently blocked (issue #276).
		{StateClosed, StateOpen, ErrClosedIssueImmutable},
		// any transition involving merged is blocked.
		{StateClosed, StateMerged, ErrInvalidState},
		{StateMerged, StateOpen, ErrInvalidState},
		{StateMerged, StateClosed, ErrInvalidState},
	}
	for _, tt := range tests {
		err := CanTransitionState(tt.from, tt.to)
		if !errors.Is(err, tt.wantErr) {
			t.Errorf("CanTransitionState(%q, %q) = %v, want %v", tt.from, tt.to, err, tt.wantErr)
		}
	}
}
