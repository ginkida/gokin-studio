package studio

import (
	"fmt"
	"sync"
	"time"
)

// EventLogCapacity is the ring-buffer size. Older entries get overwritten
// when capacity is exceeded — bounds memory growth so a long-running session
// with many errors doesn't accumulate megabytes of log lines. 500 is enough
// to see a few hours of errors without dominating memory.
const EventLogCapacity = 500

// EventLogEntry is one logged event. Level is "info", "warn", or "error".
// Source identifies the subsystem (e.g. "config", "agent", "tool", "session").
// TimestampMs is unix milliseconds for predictable JSON serialization.
// Count is 1 for a single event; >1 indicates the same (level, source, message)
// was repeated within DedupWindow and collapsed onto this entry.
type EventLogEntry struct {
	TimestampMs int64  `json:"timestampMs"`
	Level       string `json:"level"`
	Source      string `json:"source"`
	Message     string `json:"message"`
	Count       int    `json:"count,omitempty"`
}

// EventLog is a bounded ring buffer of recent events. Thread-safe.
// Used to surface backend warnings/errors in the Diagnostics modal so users
// can debug their own issues without needing to launch with --verbose.
type EventLog struct {
	mu      sync.RWMutex
	entries []EventLogEntry
	next    int  // next write index; len(entries) == cap once full
	full    bool // true once we've wrapped around
	// Dedup guard: if the same (level, source, message) is appended again
	// within DedupWindow of the previous, we increment the previous entry's
	// Count instead of adding a new row. Prevents a frontend render-loop
	// error from flooding the log and pushing all interesting context out.
	lastKey     string
	lastIdx     int
	lastWriteMs int64
}

// DedupWindow is the time window in which an identical event is collapsed
// onto the previous entry (Count incremented). 2 seconds is short enough
// that a slow-running bug still produces distinct rows over time, but long
// enough to dedup a tight render-loop or a retry storm.
const DedupWindow = 2000 // ms

// NewEventLog creates an empty ring buffer with the configured capacity.
func NewEventLog() *EventLog {
	return &EventLog{
		entries: make([]EventLogEntry, EventLogCapacity),
	}
}

// Log appends a new entry. Level is normalized to one of "info"/"warn"/"error";
// anything else becomes "info". Truncates Message at 2 KB so a runaway error
// chain can't bloat memory. If an identical (level, source, message) was
// appended within DedupWindow milliseconds, increments the previous entry's
// Count instead of adding a new row — prevents render-loop floods.
func (l *EventLog) Log(level, source, message string) {
	if l == nil {
		return
	}
	level = normalizeLogLevel(level)
	// iter 870+: redact likely secrets BEFORE truncation so a key landing
	// in the first 2 KB doesn't leak through. Applied to both frontend
	// (ErrorBoundary/window.onerror/unhandledrejection) and backend
	// (audit/save-failure) log entries.
	message = sanitizeLogMessage(message)
	if len(message) > 2048 {
		message = message[:2048] + "…"
	}
	nowMs := time.Now().UnixMilli()
	key := level + "\x00" + source + "\x00" + message

	l.mu.Lock()
	defer l.mu.Unlock()

	// Dedup check: same key + within window → just bump the count.
	// `l.full || l.next > 0` confirms there IS a previous entry; otherwise
	// lastIdx points at index 0 with a zero-valued entry that we shouldn't
	// touch.
	if (l.full || l.next > 0) && key == l.lastKey && nowMs-l.lastWriteMs <= DedupWindow {
		prev := &l.entries[l.lastIdx]
		if prev.Count == 0 {
			prev.Count = 1
		}
		prev.Count++
		prev.TimestampMs = nowMs
		l.lastWriteMs = nowMs
		return
	}

	entry := EventLogEntry{
		TimestampMs: nowMs,
		Level:       level,
		Source:      source,
		Message:     message,
		Count:       1,
	}
	l.entries[l.next] = entry
	l.lastIdx = l.next
	l.lastKey = key
	l.lastWriteMs = nowMs
	l.next = (l.next + 1) % EventLogCapacity
	if l.next == 0 {
		l.full = true
	}
	// Tee to disk if persistence is configured. Released the in-memory lock
	// first via deferred unlock — but since we have `defer l.mu.Unlock()`
	// above, persistEntry will run while we still hold it. That's OK
	// because persistEntry only touches the disk-registry state, not l.mu.
	// (See event_log_disk.go.)
	l.persistEntry(entry)
}

// Snapshot returns a copy of all entries in chronological order (oldest first).
// The returned slice is independent of the internal buffer — callers can
// mutate or hand it to Wails without locking.
func (l *EventLog) Snapshot() []EventLogEntry {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !l.full {
		// Buffer has only filled up to `next` slots.
		out := make([]EventLogEntry, l.next)
		copy(out, l.entries[:l.next])
		return out
	}
	// Buffer wrapped — chronological order is [next..end] then [0..next].
	out := make([]EventLogEntry, 0, EventLogCapacity)
	out = append(out, l.entries[l.next:]...)
	out = append(out, l.entries[:l.next]...)
	return out
}

// Clear empties the buffer. Used by the UI's "Clear logs" button.
// Also removes the disk persistence file (if any) — a clean slate means
// the next startup starts fresh, no phantom history hydration.
func (l *EventLog) Clear() {
	if l == nil {
		return
	}
	l.mu.Lock()
	// Reset the slice in place to drop references but keep backing array.
	for i := range l.entries {
		l.entries[i] = EventLogEntry{}
	}
	l.next = 0
	l.full = false
	l.lastKey = ""
	l.lastIdx = 0
	l.lastWriteMs = 0
	l.mu.Unlock()
	// Wipe the disk side (no-op when persistence isn't configured).
	l.clearDisk()
}

// Len reports the current number of entries (capped at capacity).
// Useful for tests; the frontend just uses len(Snapshot()).
func (l *EventLog) Len() int {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.full {
		return EventLogCapacity
	}
	return l.next
}

func normalizeLogLevel(level string) string {
	switch level {
	case "info", "warn", "error":
		return level
	}
	return "info"
}

// --- Studio Wails bindings ---

// LogEvent appends to the studio's event log. Safe to call from any
// goroutine. Used internally by error sites; also exposed to the frontend
// so the UI can log its own events (e.g. uncaught errors via window.onerror)
// for the user to inspect.
func (s *Studio) LogEvent(level, source, message string) {
	s.ensureEventLog()
	s.eventLog.Log(level, source, message)
}

// GetRecentLogs returns a snapshot of the ring buffer in chronological order.
// Wails binding — returns the chronologically-oldest entry first so the UI
// can render newest-on-top by reversing.
func (s *Studio) GetRecentLogs() []EventLogEntry {
	s.ensureEventLog()
	return s.eventLog.Snapshot()
}

// ClearLogs empties the event log buffer.
func (s *Studio) ClearLogs() {
	s.ensureEventLog()
	s.eventLog.Clear()
}

// RecentErrorCount returns the number of error-level entries logged in the
// last `windowMs` milliseconds. A window of 0 means "no time limit" (entire
// buffer). Multi-count deduped entries (iter 720+) are counted by their Count
// field, not as a single row — so a render-loop bug surfaces as the real
// occurrence count rather than 1. Used by the status-bar error indicator
// (iter 990+) to surface backend issues without the user opening Diagnostics.
//
// Safe to call from any goroutine. Reads via Snapshot() so it doesn't block
// concurrent Log() callers for more than a brief RLock.
func (s *Studio) RecentErrorCount(windowMs int64) int {
	s.ensureEventLog()
	entries := s.eventLog.Snapshot()
	cutoff := int64(0)
	if windowMs > 0 {
		cutoff = time.Now().UnixMilli() - windowMs
	}
	count := 0
	// Walk newest-first; we can early-exit once entries fall before cutoff.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if cutoff > 0 && e.TimestampMs < cutoff {
			break
		}
		if e.Level != "error" {
			continue
		}
		count += max(e.Count, 1)
	}
	return count
}

// RecentErrors returns up to `limit` most-recent error-level entries
// (newest first). limit=0 means "no limit" (return all errors in buffer).
// Used by the status-bar tooltip (iter 990+) to preview which errors are
// driving the count, without forcing the user to open Diagnostics.
func (s *Studio) RecentErrors(limit int) []EventLogEntry {
	s.ensureEventLog()
	entries := s.eventLog.Snapshot()
	out := make([]EventLogEntry, 0)
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Level != "error" {
			continue
		}
		out = append(out, entries[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ensureEventLog lazy-inits the log so tests that construct &Studio{}
// directly don't NPE. Idempotent — safe to call from every public method.
//
// NOTE: must NOT acquire s.mu — saveConfig (and other callers that already
// hold s.mu) call s.logf on failure paths; taking s.mu here would deadlock.
// Instead we use sync.Once on a dedicated field; the EventLog itself is
// thread-safe so no further coordination is needed.
func (s *Studio) ensureEventLog() {
	s.eventLogOnce.Do(func() {
		if s.eventLog == nil {
			s.eventLog = NewEventLog()
		}
	})
}

// logf is a convenience for the error sites — formats with fmt.Sprintf and
// appends to the event log. Cheaper than constructing the string at each
// call site.
func (s *Studio) logf(level, source, format string, args ...any) {
	s.LogEvent(level, source, fmt.Sprintf(format, args...))
}
