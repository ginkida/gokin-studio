package tools

import (
	"strings"
	"testing"
)

// ── clampTTLMinutes ──────────────────────────────────────────────────────────

func TestClampTTLMinutes_NegativeBecomesZero(t *testing.T) {
	if got := clampTTLMinutes(-1); got != 0 {
		t.Errorf("clampTTLMinutes(-1) = %d, want 0", got)
	}
}

func TestClampTTLMinutes_ZeroPassesThrough(t *testing.T) {
	if got := clampTTLMinutes(0); got != 0 {
		t.Errorf("clampTTLMinutes(0) = %d, want 0", got)
	}
}

func TestClampTTLMinutes_NormalValuePassesThrough(t *testing.T) {
	const oneWeek = 10080
	if got := clampTTLMinutes(oneWeek); got != oneWeek {
		t.Errorf("clampTTLMinutes(%d) = %d, want %d", oneWeek, got, oneWeek)
	}
}

func TestClampTTLMinutes_CapAtOneYear(t *testing.T) {
	// A value far above maxMemoryTTLMinutes must be clamped.
	huge := 999999999
	if got := clampTTLMinutes(huge); got != maxMemoryTTLMinutes {
		t.Errorf("clampTTLMinutes(%d) = %d, want maxMemoryTTLMinutes (%d)", huge, got, maxMemoryTTLMinutes)
	}
}

func TestClampTTLMinutes_ExactlyAtCap(t *testing.T) {
	if got := clampTTLMinutes(maxMemoryTTLMinutes); got != maxMemoryTTLMinutes {
		t.Errorf("clampTTLMinutes(maxMemoryTTLMinutes) = %d, want %d", got, maxMemoryTTLMinutes)
	}
}

func TestClampTTLMinutes_OverflowWouldWrapToNegative(t *testing.T) {
	// Before the fix, int64 overflow of time.Duration(n)*time.Minute produced
	// a negative TTL. Clamping to maxMemoryTTLMinutes (525600) prevents this.
	// Verify the clamped value, when multiplied by time.Minute, stays positive.
	const minute = int64(60_000_000_000) // time.Minute in nanoseconds
	clamped := clampTTLMinutes(999999999)
	dur := int64(clamped) * minute
	if dur < 0 {
		t.Errorf("clamped TTL produced negative time.Duration (%d ns)", dur)
	}
}

// ── truncate (rune-safe) ─────────────────────────────────────────────────────

func TestTruncate_ShortStringPassesThrough(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short string = %q, want %q", got, "hello")
	}
}

func TestTruncate_ExactLengthPassesThrough(t *testing.T) {
	s := "abcde"
	if got := truncate(s, 5); got != s {
		t.Errorf("truncate exact length = %q, want %q", got, s)
	}
}

func TestTruncate_LongStringGetsEllipsis(t *testing.T) {
	s := "abcdefghij" // 10 chars
	got := truncate(s, 7)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncate did not append ellipsis: %q", got)
	}
	if len([]rune(got)) > 7 {
		t.Errorf("truncated string too long: len=%d, want ≤7", len([]rune(got)))
	}
}

func TestTruncate_MultibyteSafe(t *testing.T) {
	// 5 Cyrillic letters — each is 2 bytes in UTF-8.
	// Raw byte truncation would cut mid-rune; rune-safe truncation must not.
	s := "АБВГД"
	got := truncate(s, 4)
	// Result must be valid UTF-8
	for i, r := range got {
		_ = i
		if r == '�' {
			t.Fatalf("truncate produced replacement char at %d: %q", i, got)
		}
	}
	if len([]rune(got)) > 4 {
		t.Errorf("rune length %d exceeds maxLen 4", len([]rune(got)))
	}
}

func TestTruncate_MaxLenThreeOrLessNoEllipsis(t *testing.T) {
	// maxLen ≤ 3: no room for ellipsis — return first maxLen runes
	got := truncate("hello world long", 3)
	if len([]rune(got)) > 3 {
		t.Errorf("truncate maxLen=3 too long: %q", got)
	}
}

func TestTruncate_ZeroMaxLen(t *testing.T) {
	// maxLen ≤ 0 → return original
	got := truncate("hello", 0)
	if got != "hello" {
		t.Errorf("truncate maxLen=0 = %q, want %q", got, "hello")
	}
}
