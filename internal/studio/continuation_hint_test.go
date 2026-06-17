package studio

import (
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

// TestInjectContinuationHint_InjectsHintAfterCompaction verifies that
// injectContinuationHint appends a synthetic user turn containing the original
// task, recently-read files, and recently-written files.
func TestInjectContinuationHint_InjectsHintAfterCompaction(t *testing.T) {
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}

	readT := tools.NewFileReadTracker()
	readT.CheckAndRecord("main.go", 0, 0, 100)
	readT.CheckAndRecord("utils.go", 0, 0, 200)

	writeT := tools.NewFileWriteTracker()
	writeT.Record("output.go")

	result := injectContinuationHint(history, "Fix the bug in main.go", readT, writeT)

	if len(result) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(result))
	}

	last := result[len(result)-1]
	if last.Role != genai.RoleUser {
		t.Errorf("expected last entry role=user, got %q", last.Role)
	}
	if len(last.Parts) == 0 {
		t.Fatal("expected hint part in last entry")
	}
	hint := last.Parts[0].Text
	if !strings.Contains(hint, "compacted") {
		t.Error("hint should mention compaction")
	}
	if !strings.Contains(hint, "Fix the bug in main.go") {
		t.Error("hint should contain original task")
	}
	if !strings.Contains(hint, "main.go") {
		t.Error("hint should list recently-read main.go")
	}
	if !strings.Contains(hint, "output.go") {
		t.Error("hint should list recently-modified output.go")
	}
}

// TestInjectContinuationHint_AppendsToLastUserMessage verifies that when the
// last history entry is already a user message, the hint is appended as an
// extra Part rather than creating a new Content entry.
func TestInjectContinuationHint_AppendsToLastUserMessage(t *testing.T) {
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("task")}},
	}

	result := injectContinuationHint(history, "task", nil, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry (appended to existing), got %d", len(result))
	}
	if len(result[0].Parts) != 2 {
		t.Errorf("expected 2 parts in user message, got %d", len(result[0].Parts))
	}
}

// TestInjectContinuationHint_EmptyHistoryNoOp verifies that an empty history
// is returned unchanged rather than panicking.
func TestInjectContinuationHint_EmptyHistoryNoOp(t *testing.T) {
	result := injectContinuationHint(nil, "task", nil, nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}
	result2 := injectContinuationHint([]*genai.Content{}, "task", nil, nil)
	if len(result2) != 0 {
		t.Errorf("expected empty for empty input, got %d entries", len(result2))
	}
}

// TestFileWriteTracker_RecordAndRecentlyModified verifies basic write tracking.
func TestFileWriteTracker_RecordAndRecentlyModified(t *testing.T) {
	wt := tools.NewFileWriteTracker()
	wt.Record("a.go")
	wt.Record("b.go")
	wt.Record("a.go") // dedup: a.go gets bumped to top

	files := wt.RecentlyModifiedFiles(5)
	if len(files) != 2 {
		t.Fatalf("expected 2 distinct files, got %d", len(files))
	}
	if files[0] != "a.go" {
		t.Errorf("expected a.go (most-recently re-touched) first, got %q", files[0])
	}
	if files[1] != "b.go" {
		t.Errorf("expected b.go second, got %q", files[1])
	}
}

// TestFileWriteTracker_Reset verifies entries are cleared on Reset.
func TestFileWriteTracker_Reset(t *testing.T) {
	wt := tools.NewFileWriteTracker()
	wt.Record("x.go")
	wt.Reset()
	if files := wt.RecentlyModifiedFiles(5); len(files) != 0 {
		t.Errorf("expected empty after Reset, got %v", files)
	}
}
