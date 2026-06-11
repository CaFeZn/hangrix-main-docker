package runtime

import (
	"encoding/json"
	"testing"
)

func TestQuestionnaireEventKind(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "empty payload",
			payload: "",
			want:    "",
		},
		{
			name:    "null payload",
			payload: "null",
			want:    "",
		},
		{
			name:    "questionnaire_answered cause",
			payload: `{"cause_kind":"questionnaire_answered","questionnaire_id":42}`,
			want:    "questionnaire.answered",
		},
		{
			name:    "questionnaire_closed cause",
			payload: `{"cause_kind":"questionnaire_closed","questionnaire_id":7}`,
			want:    "questionnaire.closed",
		},
		{
			name:    "regular issue.comment event",
			payload: `{"cause_kind":"comment_mentioned","body":"hello","comment_id":123}`,
			want:    "",
		},
		{
			name:    "no cause_kind field",
			payload: `{"repo_id":1,"issue_number":42}`,
			want:    "",
		},
		{
			name:    "server-side payload with extra fields",
			payload: `{"repo_id":6,"issue_number":279,"actor_id":1,"cause_kind":"questionnaire_answered","cause_id":"42","questionnaire_id":7,"answer_id":123,"respondent_id":1,"result":{"questions":[],"answers":[]}}`,
			want:    "questionnaire.answered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload json.RawMessage
			if tt.payload != "" {
				payload = json.RawMessage(tt.payload)
			}
			got := questionnaireEventKind(payload)
			if got != tt.want {
				t.Errorf("questionnaireEventKind(%q) = %q, want %q", tt.payload, got, tt.want)
			}
		})
	}
}
