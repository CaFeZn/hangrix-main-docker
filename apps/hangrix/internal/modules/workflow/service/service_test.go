package service

import (
	"testing"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/workflow/domain"
)

func TestToJobCheckItem(t *testing.T) {
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	run := &domain.WorkflowRun{
		ID:           1001,
		WorkflowName: "ci",
		EventName:    domain.EventContributionPush,
	}

	tests := []struct {
		name       string
		job        domain.WorkflowJobRun
		wantName   string
		wantStatus string
		wantConc   string
	}{
		{
			name: "pending",
			job: domain.WorkflowJobRun{
				ID:          1,
				JobKey:      "lint",
				DisplayName: "lint",
				Status:      domain.JobStatusPending,
			},
			wantName:   "ci / lint",
			wantStatus: "pending",
			wantConc:   "",
		},
		{
			name: "running",
			job: domain.WorkflowJobRun{
				ID:          2,
				JobKey:      "build",
				DisplayName: "build",
				Status:      domain.JobStatusRunning,
				StartedAt:   &now,
			},
			wantName:   "ci / build",
			wantStatus: "running",
			wantConc:   "",
		},
		{
			name: "success",
			job: domain.WorkflowJobRun{
				ID:          3,
				JobKey:      "test",
				DisplayName: "test",
				Status:      domain.JobStatusSuccess,
				StartedAt:   &now,
				FinishedAt:  &now,
			},
			wantName:   "ci / test",
			wantStatus: "completed",
			wantConc:   "success",
		},
		{
			name: "failed",
			job: domain.WorkflowJobRun{
				ID:          4,
				JobKey:      "test",
				DisplayName: "test",
				Status:      domain.JobStatusFailed,
				StartedAt:   &now,
				FinishedAt:  &now,
			},
			wantName:   "ci / test",
			wantStatus: "completed",
			wantConc:   "failure",
		},
		{
			name: "cancelled",
			job: domain.WorkflowJobRun{
				ID:          5,
				JobKey:      "deploy",
				DisplayName: "deploy",
				Status:      domain.JobStatusCancelled,
				StartedAt:   &now,
				FinishedAt:  &now,
			},
			wantName:   "ci / deploy",
			wantStatus: "completed",
			wantConc:   "cancelled",
		},
		{
			name: "skipped",
			job: domain.WorkflowJobRun{
				ID:          6,
				JobKey:      "e2e",
				DisplayName: "e2e",
				Status:      domain.JobStatusSkipped,
				FinishedAt:  &now,
			},
			wantName:   "ci / e2e",
			wantStatus: "completed",
			wantConc:   "skipped",
		},
		{
			name: "empty display name falls back to job key",
			job: domain.WorkflowJobRun{
				ID:          7,
				JobKey:      "lint",
				DisplayName: "",
				Status:      domain.JobStatusPending,
			},
			wantName:   "ci / lint",
			wantStatus: "pending",
			wantConc:   "",
		},
		{
			name: "unknown status passes through raw",
			job: domain.WorkflowJobRun{
				ID:          8,
				JobKey:      "custom",
				DisplayName: "custom",
				Status:      domain.JobStatus("queued"),
			},
			wantName:   "ci / custom",
			wantStatus: "queued",
			wantConc:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toJobCheckItem(run, &tc.job)

			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Conclusion != tc.wantConc {
				t.Errorf("Conclusion = %q, want %q", got.Conclusion, tc.wantConc)
			}
			if got.JobRunID != tc.job.ID {
				t.Errorf("JobRunID = %d, want %d", got.JobRunID, tc.job.ID)
			}
			if got.RunID != run.ID {
				t.Errorf("RunID = %d, want %d", got.RunID, run.ID)
			}
			if got.WorkflowName != run.WorkflowName {
				t.Errorf("WorkflowName = %q, want %q", got.WorkflowName, run.WorkflowName)
			}
			if got.EventName != string(run.EventName) {
				t.Errorf("EventName = %q, want %q", got.EventName, run.EventName)
			}

			// Verify started_at / finished_at mapping
			if tc.job.StartedAt != nil && got.StartedAt == nil {
				t.Error("StartedAt is nil, expected a value")
			}
			if tc.job.StartedAt == nil && got.StartedAt != nil {
				t.Error("StartedAt is set, expected nil")
			}
			if tc.job.FinishedAt != nil && got.FinishedAt == nil {
				t.Error("FinishedAt is nil, expected a value")
			}
			if tc.job.FinishedAt == nil && got.FinishedAt != nil {
				t.Error("FinishedAt is set, expected nil")
			}
		})
	}
}
