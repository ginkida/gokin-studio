package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTempPinsDir reuses the GOKIN_CONFIG_DIR override so pins land in a
// throwaway tempdir per test. (Same pattern as drafts_test.go's helper.)
func withTempPinsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := os.Getenv("GOKIN_CONFIG_DIR")
	os.Setenv("GOKIN_CONFIG_DIR", dir)
	t.Cleanup(func() {
		if prev == "" {
			os.Unsetenv("GOKIN_CONFIG_DIR")
		} else {
			os.Setenv("GOKIN_CONFIG_DIR", prev)
		}
	})
	return dir
}

func TestPinMessage_RoundTrip(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	id, err := s.PinMessage(pInfo.ID, "default", "user", "remember this", "msg-abc")
	if err != nil {
		t.Fatalf("PinMessage: %v", err)
	}
	if id == "" {
		t.Error("PinMessage returned empty pin ID")
	}

	pins, err := s.ListPinnedMessages(pInfo.ID, "default")
	if err != nil {
		t.Fatalf("ListPinnedMessages: %v", err)
	}
	if len(pins) != 1 {
		t.Fatalf("expected 1 pin, got %d", len(pins))
	}
	if pins[0].Content != "remember this" || pins[0].Role != "user" || pins[0].MessageID != "msg-abc" {
		t.Errorf("pin fields wrong: %+v", pins[0])
	}
	if pins[0].PinnedAt == 0 {
		t.Error("PinnedAt should be set")
	}
}

func TestPinMessage_RejectsEmptyContent(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	for _, c := range []string{"", "   ", "\t\n  "} {
		if _, err := s.PinMessage(pInfo.ID, "default", "user", c, ""); err == nil {
			t.Errorf("expected error for content %q, got nil", c)
		}
	}
}

func TestPinMessage_RejectsBadRole(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	for _, r := range []string{"", "tool", "model", "system", "USER"} {
		if _, err := s.PinMessage(pInfo.ID, "default", r, "x", ""); err == nil {
			t.Errorf("expected error for role %q, got nil", r)
		}
	}
}

func TestPinMessage_RejectsEmptyProjectID(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	if _, err := s.PinMessage("", "default", "user", "x", ""); err == nil {
		t.Error("expected error for empty projectID")
	}
}

func TestPinMessage_UnknownProject(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	if _, err := s.PinMessage("ghost", "default", "user", "x", ""); err == nil {
		t.Error("expected error for unknown project")
	}
}

func TestPinMessage_TruncatesLongContent(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	huge := strings.Repeat("a", PinContentMaxBytes+5000)
	if _, err := s.PinMessage(pInfo.ID, "default", "user", huge, ""); err != nil {
		t.Fatalf("PinMessage huge: %v", err)
	}
	pins, _ := s.ListPinnedMessages(pInfo.ID, "default")
	if len(pins) != 1 || len(pins[0].Content) != PinContentMaxBytes {
		t.Errorf("truncation failed: len=%d, want %d", len(pins[0].Content), PinContentMaxBytes)
	}
}

func TestPinMessage_DedupSameContent(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	id1, err := s.PinMessage(pInfo.ID, "default", "user", "same text", "")
	if err != nil {
		t.Fatalf("first PinMessage: %v", err)
	}
	// Pinning identical (role, content) returns the same pin ID instead of
	// creating a duplicate. This keeps the modal from filling with copies
	// when the user accidentally pins the same thing twice.
	id2, err := s.PinMessage(pInfo.ID, "default", "user", "same text", "different-msg-id")
	if err != nil {
		t.Fatalf("dup PinMessage: %v", err)
	}
	if id1 != id2 {
		t.Errorf("dedup failed: got new id %q, want %q", id2, id1)
	}
	pins, _ := s.ListPinnedMessages(pInfo.ID, "default")
	if len(pins) != 1 {
		t.Errorf("expected 1 pin after dedup, got %d", len(pins))
	}
}

func TestPinMessage_DifferentRoleNotDeduped(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	id1, _ := s.PinMessage(pInfo.ID, "default", "user", "ambiguous", "")
	id2, _ := s.PinMessage(pInfo.ID, "default", "assistant", "ambiguous", "")
	if id1 == id2 {
		t.Errorf("user vs assistant should be distinct pins, got same ID %q", id1)
	}
	pins, _ := s.ListPinnedMessages(pInfo.ID, "default")
	if len(pins) != 2 {
		t.Errorf("expected 2 pins (different roles), got %d", len(pins))
	}
}

func TestUnpinMessage_Removes(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	id, _ := s.PinMessage(pInfo.ID, "default", "user", "delete me", "")
	if err := s.UnpinMessage(pInfo.ID, "default", id); err != nil {
		t.Fatalf("UnpinMessage: %v", err)
	}
	pins, _ := s.ListPinnedMessages(pInfo.ID, "default")
	if len(pins) != 0 {
		t.Errorf("expected 0 pins after unpin, got %d", len(pins))
	}
}

func TestUnpinMessage_UnknownIDIdempotent(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	// Removing a non-existent pin shouldn't error — the user just clicked
	// unpin twice in quick succession.
	if err := s.UnpinMessage(pInfo.ID, "default", "no-such-pin"); err != nil {
		t.Errorf("expected nil for unknown pin, got %v", err)
	}
}

func TestUnpinMessage_LastOneRemovesFile(t *testing.T) {
	dir := withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	id, _ := s.PinMessage(pInfo.ID, "default", "user", "x", "")
	_ = s.UnpinMessage(pInfo.ID, "default", id)

	// File should be gone, not left as `[]`.
	want := filepath.Join(dir, "pins", pInfo.ID+"_default.json")
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Errorf("pins file should be removed when empty, stat err = %v", err)
	}
}

func TestListPinnedMessages_SortedNewestFirst(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	_, _ = s.PinMessage(pInfo.ID, "default", "user", "first", "")
	time.Sleep(2 * time.Millisecond) // ensure distinct timestamps
	_, _ = s.PinMessage(pInfo.ID, "default", "user", "second", "")
	time.Sleep(2 * time.Millisecond)
	_, _ = s.PinMessage(pInfo.ID, "default", "user", "third", "")

	pins, _ := s.ListPinnedMessages(pInfo.ID, "default")
	if len(pins) != 3 {
		t.Fatalf("expected 3 pins, got %d", len(pins))
	}
	// Newest first.
	if pins[0].Content != "third" || pins[1].Content != "second" || pins[2].Content != "first" {
		t.Errorf("sort order wrong: %v %v %v", pins[0].Content, pins[1].Content, pins[2].Content)
	}
}

func TestListPinnedMessages_EmptyForUnknownProject(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)

	pins, err := s.ListPinnedMessages("", "default")
	if err != nil {
		t.Errorf("expected nil error for empty project, got %v", err)
	}
	if len(pins) != 0 {
		t.Errorf("expected empty slice, got %v", pins)
	}
}

func TestListPinnedMessages_NoFileReturnsEmpty(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	pins, err := s.ListPinnedMessages(pInfo.ID, "default")
	if err != nil {
		t.Errorf("expected no error for missing file, got %v", err)
	}
	if pins == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(pins) != 0 {
		t.Errorf("expected 0 pins, got %d", len(pins))
	}
}

func TestPinMessage_EmptySessionDefaultsToDefault(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	if _, err := s.PinMessage(pInfo.ID, "", "user", "x", ""); err != nil {
		t.Fatalf("PinMessage empty sid: %v", err)
	}
	// Should be readable as both "" and "default" — they're aliased.
	a, _ := s.ListPinnedMessages(pInfo.ID, "")
	b, _ := s.ListPinnedMessages(pInfo.ID, "default")
	if len(a) != 1 || len(b) != 1 {
		t.Errorf("alias mismatch: empty=%d default=%d", len(a), len(b))
	}
}

func TestRemoveProjectPins_ClearsAllSessions(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pA := addTestProject(t, s, "A")
	pB := addTestProject(t, s, "B")

	// Pin in two sessions of A and one in B.
	extraA, _ := s.CreateChatSession(pA.ID)
	_, _ = s.PinMessage(pA.ID, "default", "user", "A1", "")
	_, _ = s.PinMessage(pA.ID, extraA.ID, "user", "A2", "")
	_, _ = s.PinMessage(pB.ID, "default", "user", "B1", "")

	removeProjectPins(pA.ID)

	if pins, _ := s.ListPinnedMessages(pA.ID, "default"); len(pins) != 0 {
		t.Errorf("A/default pins should be gone, got %d", len(pins))
	}
	if pins, _ := s.ListPinnedMessages(pA.ID, extraA.ID); len(pins) != 0 {
		t.Errorf("A/extra pins should be gone, got %d", len(pins))
	}
	if pins, _ := s.ListPinnedMessages(pB.ID, "default"); len(pins) != 1 {
		t.Errorf("B/default pins should be untouched, got %d", len(pins))
	}
}

func TestRemoveProject_ClearsPins(t *testing.T) {
	withTempHistoryDir(t)
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")

	_, _ = s.PinMessage(pInfo.ID, "default", "user", "remember", "")
	if err := s.RemoveProject(pInfo.ID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	// Re-add a project with the same ID semantics — pins should be gone.
	pInfo2 := addTestProject(t, s, "P2")
	pins, _ := s.ListPinnedMessages(pInfo2.ID, "default")
	if len(pins) != 0 {
		t.Errorf("pins survived RemoveProject: %d", len(pins))
	}
}

func TestDeleteChatSession_ClearsPins(t *testing.T) {
	withTempHistoryDir(t)
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	extra, _ := s.CreateChatSession(pInfo.ID)

	_, _ = s.PinMessage(pInfo.ID, extra.ID, "user", "in chat 2", "")
	if err := s.DeleteChatSession(pInfo.ID, extra.ID); err != nil {
		t.Fatalf("DeleteChatSession: %v", err)
	}
	pins, _ := s.ListPinnedMessages(pInfo.ID, extra.ID)
	if len(pins) != 0 {
		t.Errorf("pins survived DeleteChatSession: %d", len(pins))
	}
}

func TestUnpinMessage_EmptyProjectIDIsError(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	if err := s.UnpinMessage("", "default", "any"); err == nil {
		t.Error("expected error for empty projectID")
	}
}

func TestUnpinMessage_CorruptFileReturnsError(t *testing.T) {
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	dir := os.Getenv("GOKIN_CONFIG_DIR")
	if err := os.MkdirAll(filepath.Join(dir, "pins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pins", pInfo.ID+"_default.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.UnpinMessage(pInfo.ID, "default", "any"); err == nil {
		t.Error("expected error for corrupt pins file in UnpinMessage, got nil")
	}
}

func TestListPinnedMessages_CorruptFileReturnsError(t *testing.T) {
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	dir := os.Getenv("GOKIN_CONFIG_DIR")
	if err := os.MkdirAll(filepath.Join(dir, "pins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pins", pInfo.ID+"_default.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListPinnedMessages(pInfo.ID, "default"); err == nil {
		t.Error("expected error for corrupt pins file in ListPinnedMessages, got nil")
	}
}

// TestLoadPinsFile_EmptyFileReturnsNil covers the `len(data) == 0` early-out
// in loadPinsFile — happens when a write was truncated mid-flight on crash.
func TestLoadPinsFile_EmptyFileReturnsNil(t *testing.T) {
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	dir := os.Getenv("GOKIN_CONFIG_DIR")
	if err := os.MkdirAll(filepath.Join(dir, "pins"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Touch an empty file at the expected pins path.
	if err := os.WriteFile(filepath.Join(dir, "pins", pInfo.ID+"_default.json"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	pins, err := s.ListPinnedMessages(pInfo.ID, "default")
	if err != nil {
		t.Errorf("expected nil for empty file, got %v", err)
	}
	if len(pins) != 0 {
		t.Errorf("expected 0 pins, got %d", len(pins))
	}
}

// TestRemoveSessionPins_EmptyProjectIDIsNoop covers the empty-pid early-return.
// TestRemoveProjectPins_ReadDirErrorIsSwallowed covers the early-return when
// the pins directory doesn't exist (or isn't readable). removeProjectPins is
// best-effort cleanup — it should NOT panic / return an error here.
func TestRemoveProjectPins_ReadDirErrorIsSwallowed(t *testing.T) {
	withTempPinsDir(t)
	// pinsDir() doesn't exist yet — no PinMessage call has been made.
	// removeProjectPins must return cleanly without a panic.
	removeProjectPins("any-project") // should not panic
}

// TestRemoveProjectPins_SkipsSubdirectories covers the `e.IsDir() { continue }`
// branch — defends against future code accidentally treating a subdirectory
// as a pins file and trying to delete it.
func TestRemoveProjectPins_SkipsSubdirectories(t *testing.T) {
	withTempPinsDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	// Create at least one pins file so the dir exists.
	_, _ = s.PinMessage(pInfo.ID, "default", "user", "x", "")

	// Drop a subdirectory whose name matches the project's prefix — the
	// IsDir guard should skip it instead of trying to os.Remove the dir.
	pinsDirPath := filepath.Join(os.Getenv("GOKIN_CONFIG_DIR"), "pins")
	subdir := filepath.Join(pinsDirPath, sanitiseDraftKey(pInfo.ID)+"_subdir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}

	removeProjectPins(pInfo.ID) // should succeed without removing the subdir

	if _, err := os.Stat(subdir); os.IsNotExist(err) {
		t.Error("subdirectory was wrongly removed")
	}
}

func TestRemoveSessionPins_EmptyProjectIDIsNoop(t *testing.T) {
	withTempPinsDir(t)
	// Should not panic or error.
	removeSessionPins("", "default")
}

// TestRemoveSessionPins_EmptySessionDefaultsToDefault verifies the implicit
// "default" fallback so callers can pass "" the same way other APIs do.
func TestRemoveSessionPins_EmptySessionDefaultsToDefault(t *testing.T) {
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	_, _ = s.PinMessage(pInfo.ID, "default", "user", "x", "")
	removeSessionPins(pInfo.ID, "")
	pins, _ := s.ListPinnedMessages(pInfo.ID, "default")
	if len(pins) != 0 {
		t.Errorf("expected 0 pins after remove with empty sid, got %d", len(pins))
	}
}

func TestPinMessage_CorruptFileReturnsError(t *testing.T) {
	// newStudioForTest() calls withTempHistoryDir() which also sets
	// GOKIN_CONFIG_DIR — so we resolve the live config dir AFTER setup so
	// the corrupt file lands in the same tree the studio reads from.
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	dir := os.Getenv("GOKIN_CONFIG_DIR")

	// Write garbage as the pins file directly.
	if err := os.MkdirAll(filepath.Join(dir, "pins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pins", pInfo.ID+"_default.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.PinMessage(pInfo.ID, "default", "user", "x", ""); err == nil {
		t.Error("expected error for corrupt pins file, got nil")
	}
}
