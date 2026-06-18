package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/cache"
)

// TestGrep_ContextLinesDoNotInflateCount pins the fix: context lines are
// displayed so the model sees surrounding code, but must NOT be counted as
// matches — otherwise grep -C2 misreports match density and misleads the model.
func TestGrep_ContextLinesDoNotInflateCount(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte("a\nb\nMATCH\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gt := NewGrepTool(dir)
	re := regexp.MustCompile("MATCH")

	// context_lines=2 → 1 match + 4 surrounding lines = 5 displayed entries.
	matches := gt.searchFile(f, re, 2)
	if got := countRealMatches(matches); got != 1 {
		t.Errorf("real match count = %d, want 1 (context lines must not inflate)", got)
	}
	if len(matches) != 5 {
		t.Errorf("displayed entries = %d, want 5 (match + 4 context)", len(matches))
	}

	// context_lines=0 → only the match, count unchanged.
	if got := countRealMatches(gt.searchFile(f, re, 0)); got != 1 {
		t.Errorf("no-context real count = %d, want 1", got)
	}

	// A context line that ALSO matches is counted as a match (not missed).
	f2 := filepath.Join(dir, "g.txt")
	if err := os.WriteFile(f2, []byte("MATCH\nx\nMATCH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// With context_lines=1 the two matches' windows overlap into one block of 3
	// lines, 2 of which are real matches.
	m2 := gt.searchFile(f2, re, 1)
	if got := countRealMatches(m2); got != 2 {
		t.Errorf("overlapping matches real count = %d, want 2", got)
	}
}

// TestGrep_CachedPathDoesNotInflateCount pins the cached-path half of the fix:
// the live path already counted only real matches, but the cache HIT summary
// used len(cached.Matches) — which includes context lines — so grep -C2 on a
// repeat call over-reported the count. The cache now stores the real MatchCount.
func TestGrep_CachedPathDoesNotInflateCount(t *testing.T) {
	dir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nb\nMATCH\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gt := NewGrepTool(dir)
	gt.SetCache(cache.NewSearchCache(100, 5*time.Minute))

	args := map[string]any{"pattern": "MATCH", "context_lines": 2}

	// First call: live path (populates the cache). 1 match + 4 context lines.
	r1, err := gt.Execute(context.Background(), args)
	if err != nil || !r1.Success {
		t.Fatalf("first grep: err=%v success=%v", err, r1.Success)
	}
	if !strings.Contains(r1.Content, "Found 1 match(es)") {
		t.Fatalf("live-path count wrong (want 'Found 1 match(es)'):\n%s", r1.Content)
	}

	// Second call: cache HIT — must still report 1, not 5 (the inflated len).
	r2, err := gt.Execute(context.Background(), args)
	if err != nil || !r2.Success {
		t.Fatalf("cached grep: err=%v success=%v", err, r2.Success)
	}
	if !strings.Contains(r2.Content, "(cached)") {
		t.Fatalf("expected a cache hit on the second call:\n%s", r2.Content)
	}
	if !strings.Contains(r2.Content, "Found 1 match(es)") {
		t.Errorf("cached path inflated the count by counting context lines:\n%s", r2.Content)
	}
}
