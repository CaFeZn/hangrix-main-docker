package handler

import "testing"

func TestModelNameRe(t *testing.T) {
	valid := []string{
		"gpt-4o",
		"gpt-4.1", // dotted upstream identifiers must be accepted
		"claude-3.5-sonnet",
		"qwen2.5",
		"a",
		"model_with_underscore",
	}
	for _, name := range valid {
		if !modelNameRe.MatchString(name) {
			t.Errorf("modelNameRe rejected valid name %q", name)
		}
	}

	invalid := []string{
		"",
		".gpt-4o", // must start with an alphanumeric
		"-gpt",    // must start with an alphanumeric
		"GPT-4o",  // upper-case not allowed
		"gpt 4o",  // spaces not allowed
		"gpt/4o",  // slashes not allowed
	}
	for _, name := range invalid {
		if modelNameRe.MatchString(name) {
			t.Errorf("modelNameRe accepted invalid name %q", name)
		}
	}
}
