package tools

import (
	"strings"
	"testing"
)

func TestAskUserValidateBoundsStructuredInput(t *testing.T) {
	tool := NewAskUserTool()
	tests := []struct {
		name string
		args map[string]any
	}{
		{"oversized question", map[string]any{"question": strings.Repeat("q", AskUserQuestionMaxBytes+1)}},
		{"wrong options type", map[string]any{"question": "Q?", "options": "yes"}},
		{"non-string option", map[string]any{"question": "Q?", "options": []any{"yes", 42}}},
		{"too many options", map[string]any{"question": "Q?", "options": make([]string, AskUserMaxOptions+1)}},
		{"oversized option", map[string]any{"question": "Q?", "options": []string{strings.Repeat("x", AskUserOptionMaxBytes+1)}}},
		{"duplicate option", map[string]any{"question": "Q?", "options": []string{"yes", "yes"}}},
		{"default absent", map[string]any{"question": "Q?", "options": []string{"yes", "no"}, "default": "maybe"}},
		{"default wrong type", map[string]any{"question": "Q?", "options": []string{"yes"}, "default": 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tool.Validate(tc.args); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestAskUserValidateAcceptsStringSliceAndEmptyOptions(t *testing.T) {
	tool := NewAskUserTool()
	for _, args := range []map[string]any{
		{"question": "Free-form?", "options": []string{}},
		{"question": "Choose", "options": []string{"yes", "no"}, "default": "yes"},
		{"question": strings.Repeat("q", AskUserQuestionMaxBytes)},
	} {
		if err := tool.Validate(args); err != nil {
			t.Fatalf("valid ask_user args rejected: %v", err)
		}
	}
}
