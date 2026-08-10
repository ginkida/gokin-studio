package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReplayLoggerBoundsFileAndEventSize(t *testing.T) {
	_ = withTempHistoryDir(t)
	logger := NewReplayLogger("project", "session")
	logger.Append(ReplayEvent{Type: "assistant_text", Text: strings.Repeat("x", ReplayEventMaxBytes)})
	for i := 0; i < 40; i++ {
		logger.Append(ReplayEvent{Type: "assistant_text", Text: strings.Repeat("y", 240<<10)})
	}
	logger.Close()
	info, err := os.Stat(replayPath("project", "session"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > ReplayFileMaxBytes {
		t.Fatalf("replay grew to %d bytes, maximum %d", info.Size(), ReplayFileMaxBytes)
	}
	events, err := LoadReplay("project", "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || len(events) >= 40 {
		t.Fatalf("unexpected bounded event count: %d", len(events))
	}
}

func TestReplayRejectsOversizedAndSymlinkedStorage(t *testing.T) {
	_ = withTempHistoryDir(t)
	if err := os.MkdirAll(historyDir(), 0700); err != nil {
		t.Fatal(err)
	}

	oversized := replayPath("large", "default")
	f, err := os.OpenFile(oversized, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(ReplayFileMaxBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := LoadReplay("large", "default"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected oversized replay error, got %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("must remain intact"), 0600); err != nil {
		t.Fatal(err)
	}
	linked := replayPath("linked", "default")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	logger := NewReplayLogger("linked", "default")
	logger.Append(ReplayEvent{Type: "user", Text: "overwrite"})
	logger.Close()
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "must remain intact" {
		t.Fatalf("symlink target was modified: %q", got)
	}
}

func TestEventLogLoadRejectsOversizedAndSymlinkedStorage(t *testing.T) {
	dir := t.TempDir()
	oversized := filepath.Join(dir, "events-large.log")
	f, err := os.OpenFile(oversized, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(EventLogDiskMaxBytes + 65537); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	if err := NewEventLog().LoadFromDisk(oversized); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected oversized event-log error, got %v", err)
	}

	outside := filepath.Join(dir, "outside.log")
	if err := os.WriteFile(outside, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "events-link.log")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	if err := NewEventLog().LoadFromDisk(linked); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected symlink event-log error, got %v", err)
	}
	log := NewEventLog()
	log.SetPersistPath(linked)
	log.Log("error", "test", "must not append through symlink")
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{}\n" {
		t.Fatalf("event log modified symlink target: %q", got)
	}
}

func TestEventLogTruncationPreservesUTF8AndBoundsSource(t *testing.T) {
	log := NewEventLog()
	log.Log("error", strings.Repeat("источник", 100), strings.Repeat("🙂", 1000))
	entries := log.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if !utf8.ValidString(entries[0].Source) || len(entries[0].Source) > 128 {
		t.Fatalf("invalid or oversized source: %q", entries[0].Source)
	}
	if !utf8.ValidString(entries[0].Message) || len(entries[0].Message) > 2051 {
		t.Fatalf("invalid or oversized message: bytes=%d", len(entries[0].Message))
	}
}

func TestEventLogRotationRepairsSingleOversizedLegacyLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", EventLogDiskMaxBytes+(256<<10))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	log := NewEventLog()
	log.SetPersistPath(path)
	log.Log("info", "test", "trigger rotation")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > EventLogDiskMaxBytes {
		t.Fatalf("legacy event log remained oversized: %d", info.Size())
	}
}
