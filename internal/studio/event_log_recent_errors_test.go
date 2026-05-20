package studio

import (
	"testing"
	"time"
)

// TestRecentErrorCount_EmptyLog: zero entries, count=0.
func TestRecentErrorCount_EmptyLog(t *testing.T) {
	s := NewStudio()
	if got := s.RecentErrorCount(0); got != 0 {
		t.Errorf("expected 0 errors on empty log, got %d", got)
	}
	if got := s.RecentErrorCount(60_000); got != 0 {
		t.Errorf("expected 0 errors in last 60s on empty log, got %d", got)
	}
}

// TestRecentErrorCount_MixedLevels: only error-level entries count.
func TestRecentErrorCount_MixedLevels(t *testing.T) {
	s := NewStudio()
	s.LogEvent("info", "test", "info msg")
	s.LogEvent("warn", "test", "warn msg")
	s.LogEvent("error", "test", "err 1")
	s.LogEvent("error", "test", "err 2")
	s.LogEvent("warn", "test", "another warn")
	s.LogEvent("error", "test", "err 3")

	if got := s.RecentErrorCount(0); got != 3 {
		t.Errorf("expected 3 errors (all time), got %d", got)
	}
}

// TestRecentErrorCount_RespectsWindow: entries older than the window are
// excluded. We force the timestamps by manipulating the buffer directly
// since we can't time-travel the system clock.
func TestRecentErrorCount_RespectsWindow(t *testing.T) {
	s := NewStudio()
	s.ensureEventLog()
	now := time.Now().UnixMilli()

	// Manually inject 3 errors with controlled timestamps so we can test
	// the window cutoff deterministically.
	s.eventLog.mu.Lock()
	s.eventLog.entries[0] = EventLogEntry{TimestampMs: now - 10_000, Level: "error", Source: "old", Message: "10s ago", Count: 1}
	s.eventLog.entries[1] = EventLogEntry{TimestampMs: now - 3_000, Level: "error", Source: "mid", Message: "3s ago", Count: 1}
	s.eventLog.entries[2] = EventLogEntry{TimestampMs: now - 500, Level: "error", Source: "fresh", Message: "500ms ago", Count: 1}
	s.eventLog.next = 3
	s.eventLog.mu.Unlock()

	// 1-second window — only the 500ms-ago entry should count.
	if got := s.RecentErrorCount(1000); got != 1 {
		t.Errorf("1s window: expected 1, got %d", got)
	}
	// 5-second window — should pick up the 3s-ago + 500ms-ago entries.
	if got := s.RecentErrorCount(5000); got != 2 {
		t.Errorf("5s window: expected 2, got %d", got)
	}
	// Unlimited window — all 3.
	if got := s.RecentErrorCount(0); got != 3 {
		t.Errorf("unlimited window: expected 3, got %d", got)
	}
}

// TestRecentErrorCount_HonorsDedupCount: a deduped entry with Count=5
// contributes 5 to the total, not 1. Without this, a render-loop bug that
// flooded a single message would understate the actual error rate.
func TestRecentErrorCount_HonorsDedupCount(t *testing.T) {
	s := NewStudio()
	s.ensureEventLog()
	now := time.Now().UnixMilli()
	s.eventLog.mu.Lock()
	s.eventLog.entries[0] = EventLogEntry{TimestampMs: now, Level: "error", Source: "x", Message: "boom", Count: 5}
	s.eventLog.next = 1
	s.eventLog.mu.Unlock()

	if got := s.RecentErrorCount(0); got != 5 {
		t.Errorf("expected 5 (dedup count), got %d", got)
	}
}

// TestRecentErrorCount_NilEventLogSafe: bare Studio (no NewStudio call) must
// lazy-init the event log instead of NPE.
func TestRecentErrorCount_NilEventLogSafe(t *testing.T) {
	s := &Studio{}
	if got := s.RecentErrorCount(0); got != 0 {
		t.Errorf("expected 0 on fresh studio, got %d", got)
	}
}

// TestRecentErrors_NewestFirstWithLimit: returns up to limit error entries
// with newest first. Verifies ordering AND limit-cap.
func TestRecentErrors_NewestFirstWithLimit(t *testing.T) {
	s := NewStudio()
	s.LogEvent("info", "x", "noise")
	s.LogEvent("error", "x", "err A") // oldest
	s.LogEvent("warn", "x", "noise")
	s.LogEvent("error", "x", "err B")
	s.LogEvent("error", "x", "err C") // newest

	got := s.RecentErrors(0)
	if len(got) != 3 {
		t.Fatalf("expected 3 errors, got %d", len(got))
	}
	if got[0].Message != "err C" || got[1].Message != "err B" || got[2].Message != "err A" {
		t.Errorf("expected newest-first [C,B,A], got %+v", got)
	}

	got = s.RecentErrors(2)
	if len(got) != 2 {
		t.Fatalf("limit=2: expected 2 errors, got %d", len(got))
	}
	if got[0].Message != "err C" || got[1].Message != "err B" {
		t.Errorf("limit=2 newest-first: got %+v", got)
	}
}

// TestRecentErrors_EmptyLogReturnsEmpty: empty buffer returns empty slice
// (not nil — frontend expects iterable JSON).
func TestRecentErrors_EmptyLogReturnsEmpty(t *testing.T) {
	s := NewStudio()
	got := s.RecentErrors(0)
	if got == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// TestRecentErrors_OnlyInfoLogsReturnsEmpty: info-only buffer returns empty.
func TestRecentErrors_OnlyInfoLogsReturnsEmpty(t *testing.T) {
	s := NewStudio()
	s.LogEvent("info", "x", "1")
	s.LogEvent("info", "x", "2")
	s.LogEvent("warn", "x", "3")
	if got := s.RecentErrors(0); len(got) != 0 {
		t.Errorf("expected 0 errors, got %d (%v)", len(got), got)
	}
}

// TestRecentErrors_NilEventLogSafe — same lazy-init guarantee as
// RecentErrorCount.
func TestRecentErrors_NilEventLogSafe(t *testing.T) {
	s := &Studio{}
	if got := s.RecentErrors(0); got == nil || len(got) != 0 {
		t.Errorf("expected empty slice on bare studio, got %v", got)
	}
}
