package studio

import (
	"strings"
	"testing"
)

// TestSaveUserSnippet_RoundTrip covers the basic save+list flow.
func TestSaveUserSnippet_RoundTrip(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}

	id, err := s.SaveUserSnippet("lint", "Run the project's linter and fix any issues you find.")
	if err != nil {
		t.Fatalf("SaveUserSnippet: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty ID")
	}

	got, err := s.ListUserSnippets()
	if err != nil {
		t.Fatalf("ListUserSnippets: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 snippet, got %d", len(got))
	}
	if got[0].Name != "lint" {
		t.Errorf("name = %q, want %q", got[0].Name, "lint")
	}
	if got[0].Body == "" {
		t.Error("body should not be empty")
	}
	if got[0].UpdatedAt == 0 {
		t.Error("UpdatedAt should be populated")
	}
}

// TestSaveUserSnippet_StripsLeadingSlash so users can paste either form.
func TestSaveUserSnippet_StripsLeadingSlash(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}
	id, err := s.SaveUserSnippet("/refactor", "Refactor this code.")
	if err != nil {
		t.Fatalf("SaveUserSnippet: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty ID")
	}
	got, _ := s.ListUserSnippets()
	if len(got) != 1 || got[0].Name != "refactor" {
		t.Errorf("expected name 'refactor' (slash stripped), got %+v", got)
	}
}

// TestSaveUserSnippet_Validation covers reject paths.
func TestSaveUserSnippet_Validation(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}

	cases := []struct {
		name, body string
		want       string // expected error substring; "" = expect success
	}{
		{"", "body", "name cannot be empty"},
		{"   ", "body", "name cannot be empty"},
		{"valid", "", "body cannot be empty"},
		{"valid", "   ", "body cannot be empty"},
		{strings.Repeat("a", 31), "body", "cannot exceed"},
		{"has space", "body", "letters, digits"},
		{"has/slash", "body", "letters, digits"},
		{"has.dot", "body", "letters, digits"},
		{"clear", "body", "built-in command"},
		{"HELP", "body", "built-in command"}, // case-insensitive reservation check
		{"export", "body", "built-in command"},
		{"valid-name_2", "body", ""}, // alphanumeric + dash + underscore allowed
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.SaveUserSnippet(c.name, c.body)
			if c.want == "" {
				if err != nil {
					t.Errorf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", c.want)
				return
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

// TestSaveUserSnippet_DedupByName verifies that re-saving with the same
// (case-insensitive) name updates in-place — the ID stays stable.
func TestSaveUserSnippet_DedupByName(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}

	id1, err := s.SaveUserSnippet("lint", "First version")
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	id2, err := s.SaveUserSnippet("LINT", "Updated version")
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if id1 != id2 {
		t.Errorf("dedup failed: ID changed from %q to %q on update", id1, id2)
	}

	got, _ := s.ListUserSnippets()
	if len(got) != 1 {
		t.Fatalf("expected 1 snippet after dedup, got %d", len(got))
	}
	if got[0].Body != "Updated version" {
		t.Errorf("body not updated: %q", got[0].Body)
	}
	// The latest case is preserved (not lowercased).
	if got[0].Name != "LINT" {
		t.Errorf("expected case from latest save, got %q", got[0].Name)
	}
}

// TestDeleteUserSnippet covers the delete flow + idempotency.
func TestDeleteUserSnippet(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}

	id, _ := s.SaveUserSnippet("temp", "to be removed")
	if err := s.DeleteUserSnippet(id); err != nil {
		t.Fatalf("DeleteUserSnippet: %v", err)
	}
	got, _ := s.ListUserSnippets()
	if len(got) != 0 {
		t.Errorf("expected empty after delete, got %d", len(got))
	}

	// Idempotent: deleting again is not an error.
	if err := s.DeleteUserSnippet(id); err != nil {
		t.Errorf("expected idempotent delete, got %v", err)
	}
	// Empty ID rejected.
	if err := s.DeleteUserSnippet(""); err == nil {
		t.Error("expected error for empty ID")
	}
}

// TestListUserSnippets_SortedByName verifies alphabetical ordering.
func TestListUserSnippets_SortedByName(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}

	if _, err := s.SaveUserSnippet("zebra", "z"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveUserSnippet("alpha", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveUserSnippet("Beta", "b"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.ListUserSnippets()
	if len(got) != 3 {
		t.Fatalf("expected 3 snippets, got %d", len(got))
	}
	if got[0].Name != "alpha" || got[1].Name != "Beta" || got[2].Name != "zebra" {
		t.Errorf("sort order wrong: %v %v %v", got[0].Name, got[1].Name, got[2].Name)
	}
}

// TestListUserSnippets_Empty returns an empty slice (not nil) when no file exists.
func TestListUserSnippets_Empty(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}

	got, err := s.ListUserSnippets()
	if err != nil {
		t.Fatalf("ListUserSnippets: %v", err)
	}
	if got == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d snippets", len(got))
	}
}

// TestSaveUserSnippet_BodyTruncated caps oversize bodies at the max.
func TestSaveUserSnippet_BodyTruncated(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}

	bigBody := strings.Repeat("x", UserSnippetBodyMaxBytes+5_000)
	if _, err := s.SaveUserSnippet("big", bigBody); err != nil {
		t.Fatalf("SaveUserSnippet: %v", err)
	}
	got, _ := s.ListUserSnippets()
	if len(got[0].Body) != UserSnippetBodyMaxBytes {
		t.Errorf("body length = %d, want %d", len(got[0].Body), UserSnippetBodyMaxBytes)
	}
}
