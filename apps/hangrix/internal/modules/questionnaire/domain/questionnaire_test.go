package domain

import (
	"strings"
	"testing"
)

func TestQuestionWithOtherOption(t *testing.T) {
	single := Question{
		ID: 1, Type: QtypeSingleChoice,
		Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
	}
	out := QuestionWithOtherOption(single)
	if len(out.Options) != 3 {
		t.Fatalf("expected 3 options (2 real + other), got %d", len(out.Options))
	}
	last := out.Options[2]
	if last.ID != OtherOptionID || last.Label != OtherOptionLabel {
		t.Fatalf("expected other option {__other__, 其他…}, got %+v", last)
	}
	// Original must not be mutated.
	if len(single.Options) != 2 {
		t.Error("original question mutated")
	}

	multi := Question{ID: 2, Type: QtypeMultiChoice, Options: []Option{{ID: "x", Label: "X"}}}
	out2 := QuestionWithOtherOption(multi)
	if len(out2.Options) != 2 {
		t.Fatalf("expected 2 options (1 real + other), got %d", len(out2.Options))
	}

	textQ := Question{ID: 3, Type: QtypeTextInput}
	out3 := QuestionWithOtherOption(textQ)
	if len(out3.Options) != 0 {
		t.Error("text_input should not get other option")
	}
}

func TestValidateAnswer_OtherOption(t *testing.T) {
	questions := []Question{
		{ID: 1, Type: QtypeSingleChoice, Required: true, Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
		{ID: 2, Type: QtypeMultiChoice, Required: false, Options: []Option{{ID: "c", Label: "C"}, {ID: "d", Label: "D"}}},
	}

	// Single choice: select __other__ with text → ok
	errs := ValidateAnswer(questions, map[int64]AnswerValue{
		1: {OptionIDs: []string{OtherOptionID}, Text: "my reason"},
	})
	if len(errs) != 0 {
		t.Errorf("single_choice __other__ with text should pass, got %v", errs)
	}

	// Single choice: select __other__ without text → error
	errs = ValidateAnswer(questions, map[int64]AnswerValue{
		1: {OptionIDs: []string{OtherOptionID}, Text: ""},
	})
	if !hasCode(errs, "other_text_required") {
		t.Errorf("expected other_text_required, got %v", errs)
	}

	// Single choice: select normal option with text → still error (text_for_choice_question)
	errs = ValidateAnswer(questions, map[int64]AnswerValue{
		1: {OptionIDs: []string{"a"}, Text: "extra"},
	})
	if !hasCode(errs, "text_for_choice_question") {
		t.Errorf("expected text_for_choice_question for normal option+text, got %v", errs)
	}

	// Multi choice: select __other__ + normal with text → ok (also satisfy required q1)
	errs = ValidateAnswer(questions, map[int64]AnswerValue{
		1: {OptionIDs: []string{"a"}},
		2: {OptionIDs: []string{"c", OtherOptionID}, Text: "reason"},
	})
	if len(errs) != 0 {
		t.Errorf("multi_choice __other__ with text should pass, got %v", errs)
	}

	// Multi choice: select only normal options with text → error
	errs = ValidateAnswer(questions, map[int64]AnswerValue{
		1: {OptionIDs: []string{"a"}},
		2: {OptionIDs: []string{"c"}, Text: "bad"},
	})
	if !hasCode(errs, "text_for_choice_question") {
		t.Errorf("expected text_for_choice_question for multi normal option+text, got %v", errs)
	}

	// Unknown option still rejected
	errs = ValidateAnswer(questions, map[int64]AnswerValue{
		1: {OptionIDs: []string{"xyz"}},
	})
	if !hasCode(errs, "unknown_option") {
		t.Errorf("expected unknown_option, got %v", errs)
	}
}

func TestCreateParams_Validate_RejectsReservedID(t *testing.T) {
	p := &CreateParams{
		Title: "Test",
		Questions: []CreateQuestion{
			{Position: 0, Text: "Q", Type: QtypeSingleChoice, Options: []Option{
				{ID: OtherOptionID, Label: "not allowed"},
				{ID: "a", Label: "A"},
			}},
		},
	}
	errs := p.Validate()
	if !hasCode(errs, "reserved") {
		t.Errorf("expected reserved error for __other__ ID, got %v", errs)
	}
}

func hasCode(errs []FieldError, code string) bool {
	for _, e := range errs {
		if e.Code == code {
			return true
		}
	}
	return false
}

func TestCreateParams_Validate_QuestionTextLength(t *testing.T) {
	tests := []struct {
		name      string
		textLen   int
		wantError bool
	}{
		{"299 chars — allowed", 299, false},
		{"300 chars — allowed (boundary)", 300, false},
		{"301 chars — rejected", 301, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := strings.Repeat("a", tt.textLen)
			p := &CreateParams{
				Title:   "Test",
				Questions: []CreateQuestion{
					{Position: 0, Text: text, Type: QtypeTextInput, Required: true},
				},
			}
			errs := p.Validate()
			hasTooLong := false
			for _, e := range errs {
				if e.Code == "too_long" {
					hasTooLong = true
					break
				}
			}
			if tt.wantError && !hasTooLong {
				t.Errorf("expected a too_long error for %d-char text, got none (errors: %v)", tt.textLen, errs)
			}
			if !tt.wantError && hasTooLong {
				t.Errorf("expected no too_long error for %d-char text, but got one", tt.textLen)
			}
		})
	}
}
