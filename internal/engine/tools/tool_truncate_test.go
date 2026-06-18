package tools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/config"
)

func TestTruncateToolResultContent_ShortContentPassthrough(t *testing.T) {
	s := "hello world"
	if got := truncateToolResultContent(s, ""); got != s {
		t.Errorf("short content should pass through unchanged, got %q", got)
	}
}

func TestTruncateToolResultContent_EmptyPassthrough(t *testing.T) {
	if got := truncateToolResultContent("", "hint"); got != "" {
		t.Errorf("empty string should pass through, got %q", got)
	}
}

func TestTruncateToolResultContent_RuneSafe(t *testing.T) {
	// Build a string that is just over the cap where the last chars are a
	// 3-byte Cyrillic rune — byte-slicing at the cap would split it and
	// produce invalid UTF-8; rune-slicing should not.
	maxChars := config.DefaultToolResultMaxChars
	// Fill with single-byte ASCII up to just under the cap, then append a
	// 2-byte rune (ñ = 0xC3 0xB1) that would straddle the byte boundary.
	ascii := strings.Repeat("a", maxChars-1)
	content := ascii + "ñ" + strings.Repeat("b", 100) // ñ is 2 bytes

	result := truncateToolResultContent(content, "")

	if !utf8.ValidString(result) {
		t.Error("truncated result contains invalid UTF-8")
	}
	if !strings.Contains(result, "TRUNCATED") {
		t.Error("expected truncation notice in result")
	}
}

func TestTruncateToolResultContent_HintIncluded(t *testing.T) {
	maxChars := config.DefaultToolResultMaxChars
	content := strings.Repeat("x", maxChars+100)

	withHint := truncateToolResultContent(content, "(grep with pattern)")
	if !strings.Contains(withHint, "(grep with pattern)") {
		t.Error("hint should appear in truncation notice")
	}

	withoutHint := truncateToolResultContent(content, "")
	if strings.Contains(withoutHint, "grep") {
		t.Error("hint should not appear when empty")
	}
}

func TestToMap_ErrorFieldCapped(t *testing.T) {
	maxChars := config.DefaultToolResultMaxChars
	longError := strings.Repeat("e", maxChars+500)
	r := ToolResult{Success: false, Error: longError}

	m := r.ToMap()
	got, _ := m["error"].(string)
	if len([]rune(got)) > maxChars+200 {
		t.Errorf("error field not capped: got %d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "TRUNCATED") {
		t.Error("expected TRUNCATED notice in capped error field")
	}
}
