package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestUserContentConcurrentSavesDoNotLoseEntries(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}
	const count = 32
	var wg sync.WaitGroup
	errs := make(chan error, count*2)
	for i := 0; i < count; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, err := s.SaveUserPromptTemplate(fmt.Sprintf("Template %02d", i), "", "Prompt body")
			errs <- err
		}(i)
		go func(i int) {
			defer wg.Done()
			_, err := s.SaveUserSnippet(fmt.Sprintf("snippet-%02d", i), "Snippet body")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	templates, err := s.ListUserPromptTemplates()
	if err != nil {
		t.Fatal(err)
	}
	snippets, err := s.ListUserSnippets()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != count || len(snippets) != count {
		t.Fatalf("concurrent saves preserved templates=%d/%d snippets=%d/%d", len(templates), count, len(snippets), count)
	}
}

func TestUserContentTruncationPreservesUTF8(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := &Studio{}
	if _, err := s.SaveUserPromptTemplate("Unicode", strings.Repeat("🙂", 100), strings.Repeat("界", 10_000)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveUserSnippet("unicode", strings.Repeat("🙂", 4_000)); err != nil {
		t.Fatal(err)
	}
	templates, _ := loadUserPromptTemplates()
	snippets, _ := loadUserSnippets()
	if len(templates) != 1 || !utf8.ValidString(templates[0].Description) || !utf8.ValidString(templates[0].Prompt) ||
		len(templates[0].Description) > UserPromptDescriptionMaxBytes || len(templates[0].Prompt) > UserPromptPromptMaxBytes {
		t.Fatalf("invalid truncated template: %#v", templates)
	}
	if len(snippets) != 1 || !utf8.ValidString(snippets[0].Body) || len(snippets[0].Body) > UserSnippetBodyMaxBytes {
		t.Fatalf("invalid truncated snippet: %#v", snippets)
	}
}

func TestUserContentLoadRejectsOversizedAndSymlinkedFiles(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(configDir(), 0700); err != nil {
		t.Fatal(err)
	}
	t.Run("oversized template file", func(t *testing.T) {
		f, err := os.OpenFile(userPromptTemplatesPath(), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(UserPromptTemplatesFileMaxBytes + 1); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		if _, err := loadUserPromptTemplates(); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("expected size error, got %v", err)
		}
	})
	t.Run("symlink snippet file", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`[]`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, userSnippetsPath()); err != nil {
			t.Fatal(err)
		}
		if _, err := loadUserSnippets(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("expected symlink error, got %v", err)
		}
	})
}

func TestUserContentLoadValidatesCountsAndReservedNames(t *testing.T) {
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(configDir(), 0700); err != nil {
		t.Fatal(err)
	}
	templates := make([]UserPromptTemplate, UserPromptTemplatesMaxCount+1)
	for i := range templates {
		templates[i] = UserPromptTemplate{ID: fmt.Sprintf("id-%d", i), Name: fmt.Sprintf("Name %d", i), Prompt: "x"}
	}
	data, _ := json.Marshal(templates)
	if err := os.WriteFile(userPromptTemplatesPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadUserPromptTemplates(); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("expected template count error, got %v", err)
	}

	// A name this build reserves must not cost the user their whole library.
	// Every release can add reserved names (this one added /btw, /sessions,
	// …), so a file written by an older build legitimately contains entries
	// that only became invalid on upgrade. Load drops exactly those.
	snippets := []UserSnippet{
		{ID: "id", Name: "clear", Body: "shadow built-in"},
		{ID: "keep", Name: "deploy", Body: "still mine"},
	}
	data, _ = json.Marshal(snippets)
	if err := os.WriteFile(userSnippetsPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadUserSnippets()
	if err != nil {
		t.Fatalf("a reserved name must not fail the whole file: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "keep" {
		t.Fatalf("expected only the reserved entry dropped, got %#v", loaded)
	}
	// Writing back stays strict.
	if err := saveUserSnippets(snippets); err == nil || !strings.Contains(err.Error(), "invalid snippet") {
		t.Fatalf("save must still reject a reserved name, got %v", err)
	}
}
