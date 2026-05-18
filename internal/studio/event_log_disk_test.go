package studio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freshEventLog returns an EventLog that doesn't share the package-global
// disk-registry entry with prior tests, since tests share process state.
// Combined with t.TempDir paths, each test gets isolated state.
func freshEventLog() *EventLog {
	return NewEventLog()
}

func TestEventLogDisk_SetPathAndPersist(t *testing.T) {
	l := freshEventLog()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	l.SetPersistPath(path)
	if got := l.PersistPath(); got != path {
		t.Errorf("PersistPath=%q, want %q", got, path)
	}

	l.Log("error", "config", "save failed")
	l.Log("warn", "agent", "retrying")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("disk file not written: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("got %d lines, want 2: %q", len(lines), string(data))
	}
	if !strings.Contains(lines[0], `"save failed"`) {
		t.Errorf("line 0 missing message: %q", lines[0])
	}
	if !strings.Contains(lines[1], `"retrying"`) {
		t.Errorf("line 1 missing message: %q", lines[1])
	}
}

func TestEventLogDisk_DisabledByDefault(t *testing.T) {
	l := freshEventLog()
	// No SetPersistPath called.
	l.Log("error", "x", "should not touch disk")
	if l.PersistPath() != "" {
		t.Errorf("PersistPath=%q, want empty when not configured", l.PersistPath())
	}
}

func TestEventLogDisk_LoadFromDisk_NoFile(t *testing.T) {
	l := freshEventLog()
	dir := t.TempDir()
	path := filepath.Join(dir, "no-events.log")
	if err := l.LoadFromDisk(path); err != nil {
		t.Errorf("expected no error when file doesn't exist; got %v", err)
	}
	if l.Len() != 0 {
		t.Errorf("ring populated despite missing file: len=%d", l.Len())
	}
}

func TestEventLogDisk_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	// First "session": write some events.
	l1 := freshEventLog()
	l1.SetPersistPath(path)
	l1.Log("info", "test", "first")
	l1.Log("error", "test", "second")
	l1.Log("warn", "test", "third")

	// Second "session": load from disk into a fresh ring.
	l2 := freshEventLog()
	if err := l2.LoadFromDisk(path); err != nil {
		t.Fatalf("LoadFromDisk failed: %v", err)
	}
	snap := l2.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("ring size after load=%d, want 3", len(snap))
	}
	if snap[0].Message != "first" || snap[1].Message != "second" || snap[2].Message != "third" {
		t.Errorf("messages out of order: %+v", snap)
	}
}

func TestEventLogDisk_LoadSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	content := `{"timestampMs":1,"level":"info","source":"x","message":"ok"}` + "\n" +
		`this is not json` + "\n" +
		`{"timestampMs":2,"level":"error","source":"x","message":"survived"}` + "\n" +
		`` + "\n" + // empty line — should also be skipped
		`{}` + "\n" // empty JSON object — accepted but empty fields
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	l := freshEventLog()
	if err := l.LoadFromDisk(path); err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}
	snap := l.Snapshot()
	if len(snap) != 3 {
		t.Errorf("got %d entries, want 3 (skipped invalid + empty)", len(snap))
	}
	if snap[0].Message != "ok" || snap[1].Message != "survived" {
		t.Errorf("ordering wrong: %+v", snap)
	}
}

func TestEventLogDisk_LoadFromDisk_EmptyPath(t *testing.T) {
	l := freshEventLog()
	if err := l.LoadFromDisk(""); err == nil {
		t.Error("expected error for empty path")
	}
	if err := l.LoadFromDisk("   "); err == nil {
		t.Error("expected error for whitespace path")
	}
}

func TestEventLogDisk_ClearWipesDiskToo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	l := freshEventLog()
	l.SetPersistPath(path)
	l.Log("info", "x", "before clear")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("disk file should exist before clear: %v", err)
	}

	l.Clear()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("disk file should be removed after Clear; err=%v", err)
	}
	// And memory is empty.
	if len(l.Snapshot()) != 0 {
		t.Error("ring not empty after Clear")
	}
}

func TestEventLogDisk_RotationOnSizeOverflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	l := freshEventLog()
	l.SetPersistPath(path)

	// Force the file past EventLogDiskMaxBytes by writing many medium-sized
	// events. Each event is ~150 bytes when serialized (incl. quotes), so
	// EventLogDiskMaxBytes / 150 ≈ 7000 lines. We can't reasonably write
	// that many in a unit test; use a smaller cap via direct file munging
	// instead: write a synthetic file just past the cap, then trigger one
	// more Log() and check the file shrunk.

	// Seed the disk file with ~1.1 MB of lines.
	bigLine := strings.Repeat("x", 200) // 200 bytes per line
	var sb strings.Builder
	totalLines := (EventLogDiskMaxBytes/200 + 100) // overshoot slightly
	for i := range totalLines {
		fmt.Fprintf(&sb, `{"timestampMs":%d,"level":"info","source":"r","message":"%s"}`+"\n", i, bigLine)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// One more Log() should trigger rotateLocked.
	l.Log("info", "trigger", "rotate-now")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file missing after rotation: %v", err)
	}
	if info.Size() > EventLogDiskMaxBytes {
		t.Errorf("file still oversized after rotation: %d bytes (cap %d)", info.Size(), EventLogDiskMaxBytes)
	}
	// Rotation kept the last half; trigger event should be in there.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "rotate-now") {
		t.Error("post-rotation file missing the just-written entry")
	}
}

func TestEventLogDisk_LoadFromDisk_RingCapacityHonored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	// Write more lines to disk than the ring can hold; LoadFromDisk should
	// keep only the most recent EventLogCapacity by virtue of the ring's
	// wrap-around.
	var sb strings.Builder
	total := EventLogCapacity + 50
	for i := range total {
		fmt.Fprintf(&sb, `{"timestampMs":%d,"level":"info","source":"r","message":"msg-%d"}`+"\n", i, i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	l := freshEventLog()
	if err := l.LoadFromDisk(path); err != nil {
		t.Fatal(err)
	}
	snap := l.Snapshot()
	if len(snap) != EventLogCapacity {
		t.Errorf("len(snap)=%d, want capacity %d", len(snap), EventLogCapacity)
	}
	// The oldest survivor should be msg-50 (events 0-49 were wrapped out).
	if snap[0].Message != "msg-50" {
		t.Errorf("oldest=%q, want msg-50", snap[0].Message)
	}
}

func TestEventLogDisk_NilSafe(t *testing.T) {
	var l *EventLog
	l.SetPersistPath("/tmp/x") // should not panic
	if got := l.PersistPath(); got != "" {
		t.Errorf("PersistPath on nil=%q, want empty", got)
	}
	if err := l.LoadFromDisk("/tmp/x"); err == nil {
		t.Error("LoadFromDisk on nil should return error")
	}
}

func TestEventLogDisk_PersistDoesNotLeakOnFailure(t *testing.T) {
	// Point persistence at an unreadable path; Log() should still succeed
	// in-memory without panic or error propagation.
	dir := t.TempDir()
	unwritable := filepath.Join(dir, "nope", "deep", "events.log")

	l := freshEventLog()
	l.SetPersistPath(unwritable)
	// MkdirAll inside persistEntry will succeed (it creates the path), so
	// to actually force a write failure, point at a path under a regular
	// file we create:
	regular := filepath.Join(dir, "is-a-file")
	if err := os.WriteFile(regular, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	l.SetPersistPath(filepath.Join(regular, "events.log"))

	// Should not panic, should not raise.
	l.Log("error", "x", "test")
	if l.Len() != 1 {
		t.Errorf("in-memory ring should still have the entry; len=%d", l.Len())
	}
}
