package studio

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEventLog_AppendAndSnapshot(t *testing.T) {
	l := NewEventLog()
	l.Log("info", "test", "first")
	l.Log("warn", "test", "second")
	l.Log("error", "test", "third")

	snap := l.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("len=%d, want 3", len(snap))
	}
	if snap[0].Message != "first" || snap[2].Message != "third" {
		t.Errorf("order wrong: %+v", snap)
	}
	if snap[0].Level != "info" || snap[1].Level != "warn" || snap[2].Level != "error" {
		t.Errorf("levels wrong: %+v", snap)
	}
}

func TestEventLog_LevelNormalization(t *testing.T) {
	l := NewEventLog()
	l.Log("INFO", "test", "uppercase fallback")
	l.Log("debug", "test", "unknown fallback")
	l.Log("", "test", "empty fallback")

	snap := l.Snapshot()
	for _, e := range snap {
		if e.Level != "info" {
			t.Errorf("level=%q, want normalized to 'info'", e.Level)
		}
	}
}

func TestEventLog_MessageTruncation(t *testing.T) {
	l := NewEventLog()
	huge := strings.Repeat("x", 5000)
	l.Log("info", "test", huge)
	snap := l.Snapshot()
	// "…" is 3 bytes in UTF-8, so truncated length is 2048 + 3 = 2051 max.
	if len(snap[0].Message) > 2051 {
		t.Errorf("Message not truncated, len=%d (want ≤ 2051)", len(snap[0].Message))
	}
	if len(snap[0].Message) <= 2048 {
		t.Errorf("Message wasn't actually truncated, len=%d (input was 5000)", len(snap[0].Message))
	}
	if !strings.HasSuffix(snap[0].Message, "…") {
		t.Errorf("expected ellipsis suffix, got tail: %q", snap[0].Message[len(snap[0].Message)-10:])
	}
}

func TestEventLog_RingBufferWrapAround(t *testing.T) {
	l := NewEventLog()
	// Fill past capacity to force wrap.
	total := EventLogCapacity + 50
	for i := range total {
		l.Log("info", "test", fmt.Sprintf("event-%d", i))
	}
	snap := l.Snapshot()
	if len(snap) != EventLogCapacity {
		t.Fatalf("len=%d, want capacity %d", len(snap), EventLogCapacity)
	}
	// Oldest preserved entry should be event-50 (events 0-49 were dropped).
	if snap[0].Message != "event-50" {
		t.Errorf("oldest=%q, want 'event-50'", snap[0].Message)
	}
	if snap[len(snap)-1].Message != fmt.Sprintf("event-%d", total-1) {
		t.Errorf("newest=%q, want event-%d", snap[len(snap)-1].Message, total-1)
	}
}

func TestEventLog_Clear(t *testing.T) {
	l := NewEventLog()
	l.Log("info", "test", "before")
	l.Log("error", "test", "before2")
	if l.Len() != 2 {
		t.Fatalf("len=%d before clear, want 2", l.Len())
	}
	l.Clear()
	if l.Len() != 0 {
		t.Errorf("len=%d after clear, want 0", l.Len())
	}
	if len(l.Snapshot()) != 0 {
		t.Errorf("snapshot non-empty after clear")
	}
	// New writes after clear still work.
	l.Log("info", "test", "after")
	if l.Len() != 1 {
		t.Errorf("len=%d after post-clear write, want 1", l.Len())
	}
}

func TestEventLog_LenAtCapacity(t *testing.T) {
	l := NewEventLog()
	for i := range EventLogCapacity + 5 {
		// Unique messages so dedup doesn't collapse them.
		l.Log("info", "test", fmt.Sprintf("msg-%d", i))
	}
	if l.Len() != EventLogCapacity {
		t.Errorf("Len=%d, want capacity %d", l.Len(), EventLogCapacity)
	}
}

func TestEventLog_NilSafe(t *testing.T) {
	// Nil receiver should not panic for any method.
	var l *EventLog
	l.Log("info", "test", "msg") // should be silent
	if got := l.Snapshot(); got != nil {
		t.Errorf("Snapshot on nil=%v, want nil", got)
	}
	l.Clear() // should be silent
	if got := l.Len(); got != 0 {
		t.Errorf("Len on nil=%d, want 0", got)
	}
}

func TestEventLog_ConcurrentWrites(t *testing.T) {
	l := NewEventLog()
	const goroutines = 10
	const perG = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for i := range perG {
				l.Log("info", "test", fmt.Sprintf("g%d-i%d", id, i))
			}
		}(g)
	}
	wg.Wait()
	// 1000 writes, capacity 500 → buffer full.
	if l.Len() != EventLogCapacity {
		t.Errorf("Len=%d, want %d", l.Len(), EventLogCapacity)
	}
}

func TestEventLog_TimestampPopulated(t *testing.T) {
	l := NewEventLog()
	before := time.Now().UnixMilli()
	l.Log("info", "test", "msg")
	after := time.Now().UnixMilli()
	snap := l.Snapshot()
	if snap[0].TimestampMs < before || snap[0].TimestampMs > after {
		t.Errorf("TimestampMs=%d not in [%d, %d]", snap[0].TimestampMs, before, after)
	}
}

func TestStudio_LogEventAndGet(t *testing.T) {
	s := NewStudio()
	s.LogEvent("info", "test", "first")
	s.LogEvent("error", "test", "second")
	logs := s.GetRecentLogs()
	if len(logs) != 2 {
		t.Fatalf("len=%d, want 2", len(logs))
	}
	if logs[0].Message != "first" || logs[1].Message != "second" {
		t.Errorf("order wrong: %+v", logs)
	}
}

func TestStudio_ClearLogs(t *testing.T) {
	s := NewStudio()
	s.LogEvent("info", "test", "msg")
	s.ClearLogs()
	if len(s.GetRecentLogs()) != 0 {
		t.Errorf("logs not cleared")
	}
}

func TestStudio_LogEventLazyInit(t *testing.T) {
	// Bare &Studio{} (no NewStudio) should still work for LogEvent.
	s := &Studio{}
	s.LogEvent("info", "test", "should not panic")
	if len(s.GetRecentLogs()) != 1 {
		t.Errorf("lazy init failed, logs=%v", s.GetRecentLogs())
	}
}

func TestStudio_LogfFormats(t *testing.T) {
	s := NewStudio()
	s.logf("warn", "config", "save failed: %v", fmt.Errorf("disk full"))
	logs := s.GetRecentLogs()
	if len(logs) != 1 {
		t.Fatalf("len=%d, want 1", len(logs))
	}
	if !strings.Contains(logs[0].Message, "save failed: disk full") {
		t.Errorf("message=%q, expected fmt-formatted", logs[0].Message)
	}
	if logs[0].Level != "warn" {
		t.Errorf("level=%q, want warn", logs[0].Level)
	}
}

func TestSummarizeEventForLog_ChatTextEvent(t *testing.T) {
	evt := ChatTextEvent{Text: "hello"}
	if got := summarizeEventForLog(evt); got != "hello" {
		t.Errorf("got %q, want 'hello'", got)
	}
}

func TestSummarizeEventForLog_Map(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"text key", map[string]any{"text": "t-msg"}, "t-msg"},
		{"message key", map[string]any{"message": "m-msg"}, "m-msg"},
		{"error key", map[string]any{"error": "e-msg"}, "e-msg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summarizeEventForLog(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSummarizeEventForLog_UnknownType(t *testing.T) {
	out := summarizeEventForLog(42)
	if !strings.Contains(out, "int") {
		t.Errorf("expected type fallback, got %q", out)
	}
}

func TestEventLog_DedupSameMessageWithinWindow(t *testing.T) {
	l := NewEventLog()
	// Three identical events in rapid succession should collapse onto one row.
	l.Log("error", "frontend", "render loop fail")
	l.Log("error", "frontend", "render loop fail")
	l.Log("error", "frontend", "render loop fail")

	snap := l.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected dedup to one row, got %d: %+v", len(snap), snap)
	}
	if snap[0].Count != 3 {
		t.Errorf("Count=%d, want 3 after triple log", snap[0].Count)
	}
}

func TestEventLog_DedupResetsOnDifferentMessage(t *testing.T) {
	l := NewEventLog()
	l.Log("error", "frontend", "msg-A")
	l.Log("error", "frontend", "msg-A") // dedups with previous
	l.Log("error", "frontend", "msg-B") // breaks the run

	snap := l.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(snap))
	}
	if snap[0].Message != "msg-A" || snap[0].Count != 2 {
		t.Errorf("first entry wrong: %+v", snap[0])
	}
	if snap[1].Message != "msg-B" || snap[1].Count != 1 {
		t.Errorf("second entry wrong: %+v", snap[1])
	}
}

func TestEventLog_DedupResetsOnDifferentLevel(t *testing.T) {
	l := NewEventLog()
	l.Log("warn", "x", "same text")
	l.Log("error", "x", "same text")
	snap := l.Snapshot()
	if len(snap) != 2 {
		t.Errorf("expected separate entries for different levels, got %d", len(snap))
	}
}

func TestEventLog_DedupResetsOnDifferentSource(t *testing.T) {
	l := NewEventLog()
	l.Log("error", "frontend", "x")
	l.Log("error", "backend", "x")
	snap := l.Snapshot()
	if len(snap) != 2 {
		t.Errorf("expected separate entries for different sources, got %d", len(snap))
	}
}

func TestEventLog_DedupExpiresAfterWindow(t *testing.T) {
	l := NewEventLog()
	l.Log("error", "x", "same")
	// Force the time bookkeeping back so the next call sees the previous
	// as outside the window.
	l.mu.Lock()
	l.lastWriteMs -= DedupWindow + 100
	l.mu.Unlock()
	l.Log("error", "x", "same")
	snap := l.Snapshot()
	if len(snap) != 2 {
		t.Errorf("expected separate entries after window expiry, got %d: %+v", len(snap), snap)
	}
}

func TestEventLog_CountFieldOnFreshEntry(t *testing.T) {
	l := NewEventLog()
	l.Log("info", "test", "first")
	snap := l.Snapshot()
	if snap[0].Count != 1 {
		t.Errorf("Count=%d on fresh entry, want 1", snap[0].Count)
	}
}

func TestEventLog_ClearResetsDedupState(t *testing.T) {
	l := NewEventLog()
	l.Log("error", "x", "msg")
	l.Log("error", "x", "msg") // dedup → count=2
	l.Clear()
	// After Clear, a logged "msg" should be a fresh entry (count=1), not
	// dedup'd onto the cleared slot.
	l.Log("error", "x", "msg")
	snap := l.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry post-clear, got %d", len(snap))
	}
	if snap[0].Count != 1 {
		t.Errorf("Count=%d post-clear, want 1", snap[0].Count)
	}
}
