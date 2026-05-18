package studio

import (
	"strings"
	"testing"
)

func TestListPromptTemplates_NotEmpty(t *testing.T) {
	s := &Studio{}
	templates := s.ListPromptTemplates()
	if len(templates) == 0 {
		t.Fatal("ListPromptTemplates returned empty slice")
	}
}

// TestListPromptTemplates_AllFieldsSet verifies every curated template has
// the required metadata. A template missing an ID would key a frontend
// duplicate; missing Name would render a blank entry; missing Category
// would dump it under "" in the picker. The empty/minimal preset is the
// only one allowed to have an empty Prompt.
func TestListPromptTemplates_AllFieldsSet(t *testing.T) {
	s := &Studio{}
	templates := s.ListPromptTemplates()
	for _, tpl := range templates {
		if tpl.ID == "" {
			t.Errorf("template %q has empty ID", tpl.Name)
		}
		if tpl.Name == "" {
			t.Errorf("template %q has empty Name", tpl.ID)
		}
		if tpl.Category == "" {
			t.Errorf("template %q has empty Category", tpl.ID)
		}
		if tpl.Description == "" {
			t.Errorf("template %q has empty Description", tpl.ID)
		}
		// "minimal" is intentionally empty; everything else must have content.
		if tpl.ID != "minimal" && strings.TrimSpace(tpl.Prompt) == "" {
			t.Errorf("template %q has empty Prompt", tpl.ID)
		}
	}
}

// TestListPromptTemplates_NoDuplicateIDs verifies template IDs are unique —
// the frontend uses ID as the React key.
func TestListPromptTemplates_NoDuplicateIDs(t *testing.T) {
	s := &Studio{}
	templates := s.ListPromptTemplates()
	seen := make(map[string]bool, len(templates))
	for _, tpl := range templates {
		if seen[tpl.ID] {
			t.Errorf("duplicate template ID: %q", tpl.ID)
		}
		seen[tpl.ID] = true
	}
}

// TestListPromptTemplates_NoDuplicateNames keeps the picker readable —
// two "Code Reviewer" entries would confuse users.
func TestListPromptTemplates_NoDuplicateNames(t *testing.T) {
	s := &Studio{}
	templates := s.ListPromptTemplates()
	seen := make(map[string]bool, len(templates))
	for _, tpl := range templates {
		if seen[tpl.Name] {
			t.Errorf("duplicate template Name: %q", tpl.Name)
		}
		seen[tpl.Name] = true
	}
}

// TestListPromptTemplates_ReturnsCopy verifies that a caller mutating the
// returned slice doesn't poison the singleton for subsequent calls.
func TestListPromptTemplates_ReturnsCopy(t *testing.T) {
	s := &Studio{}
	a := s.ListPromptTemplates()
	if len(a) == 0 {
		t.Fatal("expected non-empty templates")
	}
	originalName := a[0].Name
	a[0].Name = "MUTATED"

	b := s.ListPromptTemplates()
	if b[0].Name != originalName {
		t.Errorf("frontend mutation poisoned singleton: %q vs %q", b[0].Name, originalName)
	}
}

// TestListPromptTemplates_HasExpectedCategories verifies the picker has at
// least the four core categories — defends against a refactor accidentally
// dropping a whole category block.
func TestListPromptTemplates_HasExpectedCategories(t *testing.T) {
	s := &Studio{}
	templates := s.ListPromptTemplates()
	cats := make(map[string]bool)
	for _, tpl := range templates {
		cats[tpl.Category] = true
	}
	for _, want := range []string{"Coding", "Design", "Docs", "Ops", "Reset"} {
		if !cats[want] {
			t.Errorf("expected category %q in template set, got %v", want, keys(cats))
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestListPromptTemplates_MinimalIsEmpty verifies the "minimal" reset
// preset has an empty Prompt — that's how it functions as a reset.
func TestListPromptTemplates_MinimalIsEmpty(t *testing.T) {
	s := &Studio{}
	templates := s.ListPromptTemplates()
	for _, tpl := range templates {
		if tpl.ID == "minimal" {
			if tpl.Prompt != "" {
				t.Errorf("minimal template prompt = %q, want empty", tpl.Prompt)
			}
			return
		}
	}
	t.Error("minimal template not found")
}

// TestListPromptTemplates_NoTrailingNewlines verifies prompts don't end in
// extra whitespace. Trailing whitespace surfaces as visible cursor offset
// in the editor, which looks broken.
func TestListPromptTemplates_NoTrailingNewlines(t *testing.T) {
	s := &Studio{}
	templates := s.ListPromptTemplates()
	for _, tpl := range templates {
		if tpl.Prompt == "" {
			continue
		}
		if strings.HasSuffix(tpl.Prompt, "\n") || strings.HasSuffix(tpl.Prompt, " ") {
			t.Errorf("template %q has trailing whitespace", tpl.ID)
		}
	}
}
