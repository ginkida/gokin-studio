package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTempUserTemplatesDir reuses GOKIN_CONFIG_DIR override so user-template
// files don't collide with the user's real config or with other tests.
func withTempUserTemplatesDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := os.Getenv("GOKIN_CONFIG_DIR")
	os.Setenv("GOKIN_CONFIG_DIR", dir)
	t.Cleanup(func() {
		if prev == "" {
			os.Unsetenv("GOKIN_CONFIG_DIR")
		} else {
			os.Setenv("GOKIN_CONFIG_DIR", prev)
		}
	})
	return dir
}

func TestSaveUserPromptTemplate_RoundTrip(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	id, err := s.SaveUserPromptTemplate("My Template", "for X projects", "you are an expert in X")
	if err != nil {
		t.Fatalf("SaveUserPromptTemplate: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty ID")
	}
	tmpls, err := s.ListUserPromptTemplates()
	if err != nil {
		t.Fatalf("ListUserPromptTemplates: %v", err)
	}
	if len(tmpls) != 1 {
		t.Fatalf("expected 1 template, got %d", len(tmpls))
	}
	if tmpls[0].Name != "My Template" || tmpls[0].Prompt != "you are an expert in X" {
		t.Errorf("template fields wrong: %+v", tmpls[0])
	}
	if tmpls[0].Category != userPromptTemplatesCategory {
		t.Errorf("category = %q, want %q", tmpls[0].Category, userPromptTemplatesCategory)
	}
}

func TestSaveUserPromptTemplate_RejectsEmptyName(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	for _, n := range []string{"", "   ", "\t\n  "} {
		if _, err := s.SaveUserPromptTemplate(n, "", "p"); err == nil {
			t.Errorf("expected error for empty name %q, got nil", n)
		}
	}
}

func TestSaveUserPromptTemplate_RejectsEmptyPrompt(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	for _, p := range []string{"", "   ", "\t\n  "} {
		if _, err := s.SaveUserPromptTemplate("name", "", p); err == nil {
			t.Errorf("expected error for empty prompt %q, got nil", p)
		}
	}
}

func TestSaveUserPromptTemplate_TruncatesLongFields(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	longName := strings.Repeat("n", UserPromptNameMaxBytes+50)
	longDesc := strings.Repeat("d", UserPromptDescriptionMaxBytes+50)
	longPrompt := strings.Repeat("p", UserPromptPromptMaxBytes+5000)

	if _, err := s.SaveUserPromptTemplate(longName, longDesc, longPrompt); err != nil {
		t.Fatalf("SaveUserPromptTemplate: %v", err)
	}
	tmpls, _ := s.ListUserPromptTemplates()
	if len(tmpls) != 1 {
		t.Fatalf("expected 1 template, got %d", len(tmpls))
	}
	if len(tmpls[0].Name) != UserPromptNameMaxBytes {
		t.Errorf("name length = %d, want %d", len(tmpls[0].Name), UserPromptNameMaxBytes)
	}
	if len(tmpls[0].Description) != UserPromptDescriptionMaxBytes {
		t.Errorf("description length = %d, want %d", len(tmpls[0].Description), UserPromptDescriptionMaxBytes)
	}
	if len(tmpls[0].Prompt) != UserPromptPromptMaxBytes {
		t.Errorf("prompt length = %d, want %d", len(tmpls[0].Prompt), UserPromptPromptMaxBytes)
	}
}

// TestSaveUserPromptTemplate_DedupByNameUpdatesInPlace verifies that saving
// a second template with the same name (case-insensitive) updates the
// existing one rather than creating a duplicate. ID stays stable so any
// stored references keep working.
func TestSaveUserPromptTemplate_DedupByNameUpdatesInPlace(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	id1, err := s.SaveUserPromptTemplate("React Dev", "first version", "v1 prompt")
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	// Same name with different case + new content.
	id2, err := s.SaveUserPromptTemplate("REACT DEV", "second version", "v2 prompt")
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if id1 != id2 {
		t.Errorf("dedup failed: ID changed from %q to %q", id1, id2)
	}
	tmpls, _ := s.ListUserPromptTemplates()
	if len(tmpls) != 1 {
		t.Fatalf("expected 1 template after dedup, got %d", len(tmpls))
	}
	// Update should keep the new (case-preserved) Name and the new content.
	if tmpls[0].Name != "REACT DEV" {
		t.Errorf("Name = %q, want %q (last save's case)", tmpls[0].Name, "REACT DEV")
	}
	if tmpls[0].Prompt != "v2 prompt" {
		t.Errorf("Prompt = %q, want %q", tmpls[0].Prompt, "v2 prompt")
	}
	if !strings.Contains(tmpls[0].Description, "second version") {
		t.Errorf("Description not updated: %q", tmpls[0].Description)
	}
}

func TestSaveUserPromptTemplate_DistinctNamesNotDeduped(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	_, _ = s.SaveUserPromptTemplate("React Dev", "", "p1")
	_, _ = s.SaveUserPromptTemplate("Vue Dev", "", "p2")
	tmpls, _ := s.ListUserPromptTemplates()
	if len(tmpls) != 2 {
		t.Errorf("expected 2 distinct templates, got %d", len(tmpls))
	}
}

func TestDeleteUserPromptTemplate_Removes(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	id, _ := s.SaveUserPromptTemplate("delete me", "", "p")
	if err := s.DeleteUserPromptTemplate(id); err != nil {
		t.Fatalf("DeleteUserPromptTemplate: %v", err)
	}
	tmpls, _ := s.ListUserPromptTemplates()
	if len(tmpls) != 0 {
		t.Errorf("expected 0 templates after delete, got %d", len(tmpls))
	}
}

func TestDeleteUserPromptTemplate_UnknownIDIdempotent(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	if err := s.DeleteUserPromptTemplate("no-such"); err != nil {
		t.Errorf("expected nil for unknown ID, got %v", err)
	}
}

func TestDeleteUserPromptTemplate_RejectsEmptyID(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	if err := s.DeleteUserPromptTemplate(""); err == nil {
		t.Error("expected error for empty ID")
	}
}

// TestDeleteUserPromptTemplate_LastOneRemovesFile covers the same
// "remove file when empty" semantic that drafts/pins use. Avoids
// leaving a stale `[]` on disk after deleting the last template.
func TestDeleteUserPromptTemplate_LastOneRemovesFile(t *testing.T) {
	dir := withTempUserTemplatesDir(t)
	s := &Studio{}
	id, _ := s.SaveUserPromptTemplate("only", "", "p")
	_ = s.DeleteUserPromptTemplate(id)
	if _, err := os.Stat(filepath.Join(dir, "user_prompt_templates.json")); !os.IsNotExist(err) {
		t.Errorf("file should be removed when empty, stat err = %v", err)
	}
}

func TestListUserPromptTemplates_EmptyForNoFile(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	tmpls, err := s.ListUserPromptTemplates()
	if err != nil {
		t.Errorf("expected nil for missing file, got %v", err)
	}
	if tmpls == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(tmpls) != 0 {
		t.Errorf("expected 0 templates, got %d", len(tmpls))
	}
}

func TestListUserPromptTemplates_SortedNewestFirst(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	_, _ = s.SaveUserPromptTemplate("A", "", "p1")
	time.Sleep(2 * time.Millisecond)
	_, _ = s.SaveUserPromptTemplate("B", "", "p2")
	time.Sleep(2 * time.Millisecond)
	_, _ = s.SaveUserPromptTemplate("C", "", "p3")
	tmpls, _ := s.ListUserPromptTemplates()
	if len(tmpls) != 3 {
		t.Fatalf("expected 3 templates, got %d", len(tmpls))
	}
	if tmpls[0].Name != "C" || tmpls[1].Name != "B" || tmpls[2].Name != "A" {
		t.Errorf("sort order wrong: %s %s %s", tmpls[0].Name, tmpls[1].Name, tmpls[2].Name)
	}
}

// TestListUserPromptTemplates_FallbackDescription verifies templates
// without an explicit description get a "Saved YYYY-MM-DD" placeholder
// rather than rendering an empty row in the picker.
func TestListUserPromptTemplates_FallbackDescription(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	_, _ = s.SaveUserPromptTemplate("name", "", "prompt")
	tmpls, _ := s.ListUserPromptTemplates()
	if len(tmpls) != 1 {
		t.Fatalf("expected 1 template, got %d", len(tmpls))
	}
	if !strings.HasPrefix(tmpls[0].Description, "Saved ") {
		t.Errorf("expected fallback 'Saved <date>', got %q", tmpls[0].Description)
	}
}

func TestListUserPromptTemplates_CorruptFileReturnsError(t *testing.T) {
	dir := withTempUserTemplatesDir(t)
	s := &Studio{}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user_prompt_templates.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListUserPromptTemplates(); err == nil {
		t.Error("expected error for corrupt file")
	}
}

// TestLoadUserPromptTemplates_EmptyFileReturnsNil covers the `len(data) == 0`
// early-return — happens after a truncated write (e.g. crash mid-flush).
// An empty file should be treated as "no templates", not an error.
func TestLoadUserPromptTemplates_EmptyFileReturnsNil(t *testing.T) {
	dir := withTempUserTemplatesDir(t)
	s := &Studio{}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user_prompt_templates.json"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	tmpls, err := s.ListUserPromptTemplates()
	if err != nil {
		t.Errorf("expected no error for empty file, got %v", err)
	}
	if len(tmpls) != 0 {
		t.Errorf("expected 0 templates from empty file, got %d", len(tmpls))
	}
}

// TestSaveUserPromptTemplate_CorruptFileReturnsError verifies that the
// error from a corrupt file path propagates through Save (not silently
// overwriting the user's broken-but-recoverable file).
func TestSaveUserPromptTemplate_CorruptFileReturnsError(t *testing.T) {
	dir := withTempUserTemplatesDir(t)
	s := &Studio{}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user_prompt_templates.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveUserPromptTemplate("name", "", "p"); err == nil {
		t.Error("expected error when underlying file is corrupt, got nil")
	}
}

// TestDeleteUserPromptTemplate_CorruptFileReturnsError mirrors the Save
// case — Delete shouldn't silently swallow a load error.
func TestDeleteUserPromptTemplate_CorruptFileReturnsError(t *testing.T) {
	dir := withTempUserTemplatesDir(t)
	s := &Studio{}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user_prompt_templates.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUserPromptTemplate("any-id"); err == nil {
		t.Error("expected error for corrupt file, got nil")
	}
}

// TestSaveUserPromptTemplate_PreservesLeadingWhitespace verifies leading
// whitespace in the prompt body is preserved (it might be intentional
// indentation in a structured prompt). Only trailing whitespace is
// trimmed, since that's typically just textarea cursor noise.
func TestSaveUserPromptTemplate_PreservesLeadingWhitespace(t *testing.T) {
	withTempUserTemplatesDir(t)
	s := &Studio{}
	prompt := "  indented\n  body\n  "
	if _, err := s.SaveUserPromptTemplate("indent", "", prompt); err != nil {
		t.Fatalf("SaveUserPromptTemplate: %v", err)
	}
	tmpls, _ := s.ListUserPromptTemplates()
	if len(tmpls) != 1 {
		t.Fatalf("expected 1 template, got %d", len(tmpls))
	}
	// Trailing whitespace stripped; leading "  " preserved.
	if !strings.HasPrefix(tmpls[0].Prompt, "  indented") {
		t.Errorf("leading whitespace lost: %q", tmpls[0].Prompt)
	}
	if strings.HasSuffix(tmpls[0].Prompt, " ") || strings.HasSuffix(tmpls[0].Prompt, "\n") {
		t.Errorf("trailing whitespace preserved (should be stripped): %q", tmpls[0].Prompt)
	}
}
