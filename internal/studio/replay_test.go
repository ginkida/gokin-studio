package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// withTempHistoryDir overrides the global history directory for the test and
// restores it afterwards. historyDir() reads from configDir() which in turn
// uses an env var we can hijack.
func withTempHistoryDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", dir)
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })
	return filepath.Join(dir, "history")
}

func TestReplayRoundtrip(t *testing.T) {
	// This test doesn't rely on the env override because replay.go uses the
	// same historyDir() helper as history.go. But we still use t.TempDir to
	// avoid touching real data — so override explicitly.
	_ = withTempHistoryDir(t)

	pid := "proj1"
	sid := "sess1"

	r := NewReplayLogger(pid, sid)
	defer r.Complete()

	successTrue := true
	successFalse := false
	r.Append(ReplayEvent{Type: "user", Text: "hello"})
	r.Append(ReplayEvent{Type: "tool_call", Tool: "bash", Args: map[string]any{"command": "ls"}})
	r.Append(ReplayEvent{Type: "tool_result", Tool: "bash", Text: "file1\nfile2\n", Success: &successTrue})
	r.Append(ReplayEvent{Type: "tool_call", Tool: "edit", Args: map[string]any{"file_path": "foo.go"}})
	r.Append(ReplayEvent{Type: "tool_result", Tool: "edit", Text: "failed: path not found", Success: &successFalse})
	r.Append(ReplayEvent{Type: "assistant_text", Text: "partial reply before crash"})

	events, err := LoadReplay(pid, sid)
	if err != nil {
		t.Fatalf("LoadReplay returned error: %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("expected 6 events, got %d", len(events))
	}
	if events[0].Type != "user" || events[0].Text != "hello" {
		t.Errorf("event 0 wrong: %+v", events[0])
	}
	if events[2].Tool != "bash" || events[2].Success == nil || *events[2].Success != true {
		t.Errorf("event 2 wrong: %+v", events[2])
	}
	if events[4].Success == nil || *events[4].Success != false {
		t.Errorf("event 4 should be failure, got %+v", events[4])
	}
	if events[5].Type != "assistant_text" || !strings.Contains(events[5].Text, "partial reply") {
		t.Errorf("event 5 wrong: %+v", events[5])
	}
}

func TestReplayCompleteRemovesFile(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid, sid := "proj2", "sess2"
	r := NewReplayLogger(pid, sid)
	r.Append(ReplayEvent{Type: "user", Text: "test"})
	// Confirm the file exists before Complete().
	if _, err := os.Stat(replayPath(pid, sid)); err != nil {
		t.Fatalf("expected replay file to exist: %v", err)
	}
	r.Complete()
	if _, err := os.Stat(replayPath(pid, sid)); !os.IsNotExist(err) {
		t.Errorf("expected replay file removed after Complete(), got err=%v", err)
	}
	events, err := LoadReplay(pid, sid)
	if err != nil {
		t.Fatalf("LoadReplay after Complete() returned error: %v", err)
	}
	if events != nil {
		t.Errorf("expected no events after Complete(), got %v", events)
	}
}

func TestReplayLoadMissingIsNil(t *testing.T) {
	_ = withTempHistoryDir(t)
	events, err := LoadReplay("nope", "nope")
	if err != nil {
		t.Errorf("LoadReplay for missing file should return nil error, got %v", err)
	}
	if events != nil {
		t.Errorf("expected nil events, got %v", events)
	}
}

func TestHistoryV1LegacyRoundtrip(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "legacy"
	// Write a v1 (bare array) history file directly.
	legacy := `[{"role":"user","text":"hi"},{"role":"model","text":"hello"}]`
	if err := os.MkdirAll(historyDir(), 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(historyPath(pid), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	hist, err := LoadHistory(pid)
	if err != nil {
		t.Fatalf("LoadHistory v1 failed: %v", err)
	}
	if len(hist) != 2 || hist[0].Role != "user" || hist[1].Role != "model" {
		t.Errorf("v1 load produced wrong history: %+v", hist)
	}
	// Name field must be empty for legacy files.
	if got := LoadHistoryName(pid); got != "" {
		t.Errorf("v1 file should have empty name, got %q", got)
	}
}

func TestSaveHistoryWithNameKeepsEmptySessionWhenNameSet(t *testing.T) {
	// Fresh session with just a name (never received a message) must be
	// persisted so the tab survives a restart — previous behaviour dropped
	// the save when history was empty, which wiped unused sessions.
	_ = withTempHistoryDir(t)
	pid := "emptysess"
	if err := SaveHistoryWithName(pid, "Brainstorm", nil); err != nil {
		t.Fatalf("SaveHistoryWithName empty+name failed: %v", err)
	}
	if got := LoadHistoryName(pid); got != "Brainstorm" {
		t.Errorf("expected saved name 'Brainstorm', got %q", got)
	}
	hist, err := LoadHistory(pid)
	if err != nil {
		t.Fatalf("LoadHistory returned error: %v", err)
	}
	if len(hist) != 0 {
		t.Errorf("expected empty history, got %d entries", len(hist))
	}
}

func TestSaveHistoryWithNameSkipsTrulyEmpty(t *testing.T) {
	// No history and no name — nothing to preserve, skip the write.
	_ = withTempHistoryDir(t)
	pid := "reallyempty"
	if err := SaveHistoryWithName(pid, "", nil); err != nil {
		t.Fatalf("error: %v", err)
	}
	if _, err := os.Stat(historyPath(pid)); !os.IsNotExist(err) {
		t.Errorf("expected no file for empty history + empty name; got err=%v", err)
	}
}

func TestHistoryV2SaveAndLoadPreservesName(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "v2test"
	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}
	if err := SaveHistoryWithName(pid, "my renamed chat", hist); err != nil {
		t.Fatalf("SaveHistoryWithName failed: %v", err)
	}
	if got := LoadHistoryName(pid); got != "my renamed chat" {
		t.Errorf("expected name 'my renamed chat', got %q", got)
	}
	loaded, err := LoadHistory(pid)
	if err != nil {
		t.Fatalf("LoadHistory v2 failed: %v", err)
	}
	if len(loaded) != 1 || firstText(loaded[0]) != "hello" {
		t.Errorf("v2 load produced wrong history: %+v", loaded)
	}
}

// TestReplayLogger_NilReceiverSafe verifies that all ReplayLogger methods are
// safe to call on a nil receiver. The agent loop holds a *ReplayLogger that may
// be nil in tests or if allocation fails — callers must not have to guard.
func TestReplayLogger_NilReceiverSafe(t *testing.T) {
	var r *ReplayLogger
	// Must not panic — all three methods have nil-receiver guards.
	r.Append(ReplayEvent{Type: "user", Text: "hello"})
	r.Complete()
	r.Close()
}

// TestReplayLogger_AppendAfterClose verifies that calling Append after Close is
// a no-op: the event is silently dropped and the on-disk file is unchanged.
// This prevents in-flight agent goroutines from writing stale events after the
// logger is shut down.
func TestReplayLogger_AppendAfterClose(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid, sid := "proj-closed", "sess-closed"
	r := NewReplayLogger(pid, sid)

	// Append one event while open, then close.
	r.Append(ReplayEvent{Type: "user", Text: "before close"})
	r.Close()

	// Append after close must not write to the file.
	r.Append(ReplayEvent{Type: "user", Text: "after close — must not appear"})

	events, err := LoadReplay(pid, sid)
	if err != nil {
		t.Fatalf("LoadReplay: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event (pre-close), got %d", len(events))
	}
	if len(events) > 0 && events[0].Text != "before close" {
		t.Errorf("unexpected event text: %q", events[0].Text)
	}
}

// TestLoadReplay_EmptyLines verifies that empty lines in a replay log are
// skipped without returning an error. Replay files can accumulate empty lines
// if I/O is interrupted mid-write or from future format changes.
func TestLoadReplay_EmptyLines(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid, sid := "proj-emptylines", "default"

	// Write a file that has an empty line between two valid events.
	dir := historyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, pid+"_"+sid+".replay.jsonl")
	content := `{"type":"user","text":"first"}` + "\n" +
		"\n" + // empty line — must be skipped
		`{"type":"assistant","text":"second"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := LoadReplay(pid, sid)
	if err != nil {
		t.Fatalf("LoadReplay with empty lines: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events (empty lines skipped), got %d", len(events))
	}
}

// TestLoadReplay_CorruptLines verifies that lines with invalid JSON in a
// replay log are silently skipped so one bad line doesn't lose the rest of
// the recovery data.
func TestLoadReplay_CorruptLines(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid, sid := "proj-corrupt", "default"

	dir := historyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, pid+"_"+sid+".replay.jsonl")
	content := `{"type":"user","text":"good event"}` + "\n" +
		`{not valid json at all` + "\n" + // corrupt line — must be skipped
		`{"type":"assistant","text":"also good"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	events, err := LoadReplay(pid, sid)
	if err != nil {
		t.Fatalf("LoadReplay with corrupt lines: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events (corrupt line skipped), got %d", len(events))
	}
}

// TestLoadReplay_NonNotExistError verifies that when os.Open fails with a
// non-NotExist error (permission denied), LoadReplay returns a non-nil error
// rather than nil/nil. Covers the `return nil, err` branch in LoadReplay.
func TestLoadReplay_NonNotExistError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission-denied as root")
	}

	// Use an isolated temp dir so chmod doesn't interfere with other tests.
	baseDir := t.TempDir()
	prev := os.Getenv("GOKIN_CONFIG_DIR")
	_ = os.Setenv("GOKIN_CONFIG_DIR", baseDir)
	t.Cleanup(func() { _ = os.Setenv("GOKIN_CONFIG_DIR", prev) })

	hDir := historyDir()
	if err := os.MkdirAll(hDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Remove read+execute permission on the history directory so os.Open
	// returns EACCES (not ENOENT) for any path inside it.
	if err := os.Chmod(hDir, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(hDir, 0o700) }) // restore before t.TempDir cleanup

	_, err := LoadReplay("proj-perm-denied", "sess")
	if err == nil {
		t.Error("expected permission-denied error from LoadReplay, got nil")
	}
}

// TestReplayLogger_Append_OpenFileError verifies that Append silently swallows
// an os.OpenFile error (e.g. the directory is not writable) and does NOT panic.
// Replay logging must never crash the agent loop — a dropped event is better
// than a broken turn. This covers the `if err != nil { return }` guard after
// os.OpenFile in Append.
func TestReplayLogger_Append_OpenFileError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission-denied as root")
	}

	_ = withTempHistoryDir(t)
	hDir := historyDir()
	if err := os.MkdirAll(hDir, 0o700); err != nil {
		t.Fatal(err)
	}

	pid, sid := "proj-append-perm", "sess"
	r := NewReplayLogger(pid, sid)
	// Append once while dir is writable so the logger is valid.
	r.Append(ReplayEvent{Type: "user", Text: "before chmod"})

	// Remove all permissions so subsequent Appends fail at os.OpenFile.
	// Using 0o000 (no r/w/x) prevents traversal into the directory, which
	// causes os.OpenFile to fail even for existing files.
	if err := os.Chmod(hDir, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(hDir, 0o700) })

	// Must not panic. The event is silently dropped.
	r.Append(ReplayEvent{Type: "user", Text: "after chmod — should be dropped"})

	// Restore so we can read and clean up.
	_ = os.Chmod(hDir, 0o700)

	events, err := LoadReplay(pid, sid)
	if err != nil {
		t.Fatalf("LoadReplay: %v", err)
	}
	// Only the first event (written while writable) should be present.
	if len(events) != 1 {
		t.Errorf("expected 1 event (chmod-dropped event not written), got %d", len(events))
	}
}

// TestReplayLogger_AppendAfterComplete verifies that calling Append after
// Complete is also a no-op. Complete both removes the file and sets closed=true,
// so no new file should be created.
func TestReplayLogger_AppendAfterComplete(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid, sid := "proj-complete", "sess-complete"
	r := NewReplayLogger(pid, sid)
	r.Append(ReplayEvent{Type: "user", Text: "during turn"})
	r.Complete() // removes the file and marks closed

	// Append after Complete must NOT recreate the replay file.
	r.Append(ReplayEvent{Type: "user", Text: "after complete — must not appear"})

	events, err := LoadReplay(pid, sid)
	if err != nil {
		t.Fatalf("LoadReplay: %v", err)
	}
	if events != nil {
		t.Errorf("expected nil events after Complete+Append, got %d events", len(events))
	}
}
