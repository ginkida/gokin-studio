package tools

import (
	"strings"
	"testing"
)

// ── editedRegionSnippet ─────────────────────────────────────────────────────

func TestEditedRegionSnippet_ContainsUpdatedRegionHeader(t *testing.T) {
	content := "line1\nline2\nline3\n"
	snippet := editedRegionSnippet(content, 2)
	if !strings.Contains(snippet, "Updated region") {
		t.Errorf("expected 'Updated region' header in snippet, got: %s", snippet)
	}
}

func TestEditedRegionSnippet_ContainsTargetLine(t *testing.T) {
	content := "aaa\nbbb\nccc\n"
	snippet := editedRegionSnippet(content, 2)
	if !strings.Contains(snippet, "bbb") {
		t.Errorf("expected target line content in snippet, got: %s", snippet)
	}
}

func TestEditedRegionSnippet_IncludesContextLines(t *testing.T) {
	content := "aaa\nbbb\nccc\nddd\neee\nfff\nggg\n"
	snippet := editedRegionSnippet(content, 4) // target = "ddd"
	// Should include 4 lines before and after (capped at file bounds)
	for _, want := range []string{"aaa", "bbb", "ccc", "ddd", "eee", "fff", "ggg"} {
		if !strings.Contains(snippet, want) {
			t.Errorf("expected %q in context snippet, got: %s", want, snippet)
		}
	}
}

func TestEditedRegionSnippet_LineNumbersPresent(t *testing.T) {
	content := "aaa\nbbb\nccc\n"
	snippet := editedRegionSnippet(content, 2)
	// Should contain line numbers like "    2\t" or "    2 "
	if !strings.Contains(snippet, "2") {
		t.Errorf("expected line number 2 in snippet, got: %s", snippet)
	}
}

func TestEditedRegionSnippet_EmptyContent(t *testing.T) {
	snippet := editedRegionSnippet("", 1)
	// Should return empty for no content, not panic
	_ = snippet
}

func TestEditedRegionSnippet_LineClampedToOne(t *testing.T) {
	content := "only\n"
	// line 0 → clamped to 1
	snippet := editedRegionSnippet(content, 0)
	if snippet == "" {
		t.Fatal("expected non-empty snippet for line=0 (clamped to 1)")
	}
}

func TestEditedRegionSnippet_LineClampedToMax(t *testing.T) {
	content := "a\nb\nc\n"
	// line 999 → clamped to len(lines)
	snippet := editedRegionSnippet(content, 999)
	_ = snippet // should not panic
}

func TestEditedRegionSnippet_TruncatesLongLine(t *testing.T) {
	longLine := strings.Repeat("X", 500)
	content := "before\n" + longLine + "\nafter\n"
	snippet := editedRegionSnippet(content, 2)
	// Should contain truncated line (not all 500 X's)
	if strings.Count(snippet, "X") >= 500 {
		t.Error("long line should be truncated in the snippet")
	}
	if !strings.Contains(snippet, "…") {
		t.Error("truncated long line should end with ellipsis")
	}
}

// ── renderLineContext ────────────────────────────────────────────────────────

func TestRenderLineContext_MarksTargetLine(t *testing.T) {
	lines := []string{"aaa", "bbb", "ccc", "ddd"}
	ctx := renderLineContext(lines, 2, 1)
	if !strings.Contains(ctx, "→") {
		t.Errorf("expected '→' marker on target line, got: %s", ctx)
	}
}

func TestRenderLineContext_IncludesTargetContent(t *testing.T) {
	lines := []string{"aaa", "bbb_target", "ccc"}
	ctx := renderLineContext(lines, 2, 1)
	if !strings.Contains(ctx, "bbb_target") {
		t.Errorf("expected target content in context, got: %s", ctx)
	}
}

func TestRenderLineContext_ClampedStart(t *testing.T) {
	lines := []string{"first", "second", "third"}
	// Line 1 with around=5 → clamped to start of file
	ctx := renderLineContext(lines, 1, 5)
	if !strings.Contains(ctx, "first") {
		t.Errorf("expected first line in context, got: %s", ctx)
	}
}

func TestRenderLineContext_ClampedEnd(t *testing.T) {
	lines := []string{"first", "second", "third"}
	// Line 3 with around=5 → clamped to end of file
	ctx := renderLineContext(lines, 3, 5)
	if !strings.Contains(ctx, "third") {
		t.Errorf("expected last line in context, got: %s", ctx)
	}
}

func TestRenderLineContext_OutOfBoundsLine(t *testing.T) {
	lines := []string{"a", "b"}
	// lineNum=0 and lineNum > len → should return ""
	if got := renderLineContext(lines, 0, 1); got != "" {
		t.Errorf("expected empty for lineNum=0, got: %q", got)
	}
	if got := renderLineContext(lines, 10, 1); got != "" {
		t.Errorf("expected empty for lineNum > len, got: %q", got)
	}
}

func TestRenderLineContext_EmptyLines(t *testing.T) {
	ctx := renderLineContext(nil, 1, 1)
	if ctx != "" {
		t.Errorf("expected empty for nil lines, got: %q", ctx)
	}
}

func TestRenderLineContext_AroundZero(t *testing.T) {
	lines := []string{"a", "b", "c"}
	ctx := renderLineContext(lines, 2, 0)
	// Only the target line itself
	if !strings.Contains(ctx, "b") {
		t.Errorf("expected target content for around=0, got: %s", ctx)
	}
}

// ── integration: editedRegionSnippet in edit success message ────────────────

func TestEditTool_SuccessMessageContainsSnippet(t *testing.T) {
	content := "package p\n\nfunc Foo() {\n\treturn\n}\n"
	et, target := seedEditTarget(t, content)
	ctx := editCtxWithTracker(target, content)

	res, err := et.Execute(ctx, map[string]any{
		"file_path":  target,
		"old_string": "return",
		"new_string": "return nil",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success: %s", res.Error)
	}
	// Result should contain the "Updated region" snippet
	if !strings.Contains(res.Content, "Updated region") {
		t.Errorf("edit success result should contain snippet, got: %s", res.Content)
	}
}

func TestEditTool_LineRangeSuccessMessageContainsSnippet(t *testing.T) {
	content := "a\nb\nc\nd\n"
	et, target := seedEditTarget(t, content)
	ctx := editCtxWithTracker(target, content)

	res, err := et.Execute(ctx, map[string]any{
		"file_path":  target,
		"line_start": 2,
		"line_end":   2,
		"new_string": "REPLACED",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success: %s", res.Error)
	}
	if !strings.Contains(res.Content, "Updated region") {
		t.Errorf("line-range edit should include snippet, got: %s", res.Content)
	}
}

func TestEditTool_InsertAfterLineSuccessMessageContainsSnippet(t *testing.T) {
	content := "a\nb\nc\n"
	et, target := seedEditTarget(t, content)
	ctx := editCtxWithTracker(target, content)

	res, err := et.Execute(ctx, map[string]any{
		"file_path":         target,
		"insert_after_line": 1,
		"new_string":        "INSERTED",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success: %s", res.Error)
	}
	if !strings.Contains(res.Content, "Updated region") {
		t.Errorf("insert-after-line should include snippet, got: %s", res.Content)
	}
}
