package studio

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

// seedHistory replaces the source session's history with the given turns.
// Helper to keep individual fork tests focused on the assertion they care
// about rather than each rebuilding history fixtures.
func seedHistory(t *testing.T, s *Studio, projectID, sessionID string, turns ...*genai.Content) {
	t.Helper()
	p, ok := s.projects[projectID]
	if !ok {
		t.Fatalf("project %q not in studio", projectID)
	}
	sess, ok := p.sessions[sessionID]
	if !ok {
		t.Fatalf("session %q not in project %q", sessionID, projectID)
	}
	sess.history = turns
}

func userTurn(text string) *genai.Content {
	return &genai.Content{Role: "user", Parts: []*genai.Part{genai.NewPartFromText(text)}}
}

func modelTurn(text string) *genai.Content {
	return &genai.Content{Role: "model", Parts: []*genai.Part{genai.NewPartFromText(text)}}
}

func TestForkChatSession_UnknownProject(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	if _, err := s.ForkChatSession("no-such", "default", 0, ""); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

func TestForkChatSession_UnknownSession(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	if _, err := s.ForkChatSession(pInfo.ID, "no-such-session", 0, ""); err == nil {
		t.Error("expected error for unknown session, got nil")
	}
}

func TestForkChatSession_NegativeIndexRejected(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	if _, err := s.ForkChatSession(pInfo.ID, "default", -1, ""); err == nil {
		t.Error("expected error for negative userIndexFromEnd, got nil")
	}
}

func TestForkChatSession_NoMatchingUserTurn(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	// History has 1 user turn; asking for index-from-end=5 should fail.
	seedHistory(t, s, pInfo.ID, "default", userTurn("hello"))

	if _, err := s.ForkChatSession(pInfo.ID, "default", 5, ""); err == nil {
		t.Error("expected error when index exceeds available user turns, got nil")
	}
}

// TestForkChatSession_IncludesChosenTurn verifies the fork includes the user
// turn at the chosen index AND every preceding turn — i.e. forking from the
// most recent user turn (idx=0) preserves the entire chat including that turn.
func TestForkChatSession_IncludesChosenTurn(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	seedHistory(t, s, pInfo.ID, "default",
		userTurn("first user"),
		modelTurn("first reply"),
		userTurn("second user"),
		modelTurn("second reply"),
		userTurn("third user"),
	)

	info, err := s.ForkChatSession(pInfo.ID, "default", 0, "")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}

	p := s.projects[pInfo.ID]
	forked := p.sessions[info.ID]
	if len(forked.history) != 5 {
		t.Errorf("forked history len = %d, want 5", len(forked.history))
	}
	last := forked.history[len(forked.history)-1]
	if last.Role != "user" || last.Parts[0].Text != "third user" {
		t.Errorf("last fork turn = %+v, want user 'third user'", last)
	}
}

// TestForkChatSession_TrimsAfterChosenTurn verifies that forking at index=1
// (the second-most-recent user turn) drops the third user turn and the
// model's reply that came after the chosen turn — but keeps everything up
// to and including the chosen turn.
func TestForkChatSession_TrimsAfterChosenTurn(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	seedHistory(t, s, pInfo.ID, "default",
		userTurn("first user"),
		modelTurn("first reply"),
		userTurn("second user"),
		modelTurn("second reply"),
		userTurn("third user"),
		modelTurn("third reply"),
	)

	// Fork at index-from-end=1 → "second user" turn.
	info, err := s.ForkChatSession(pInfo.ID, "default", 1, "")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}

	p := s.projects[pInfo.ID]
	forked := p.sessions[info.ID]
	if len(forked.history) != 3 {
		t.Errorf("forked history len = %d, want 3 (first user, first reply, second user)", len(forked.history))
	}
	last := forked.history[len(forked.history)-1]
	if last.Role != "user" || last.Parts[0].Text != "second user" {
		t.Errorf("last fork turn = %+v, want user 'second user'", last)
	}
	// Source must remain untouched.
	src := p.sessions["default"]
	if len(src.history) != 6 {
		t.Errorf("source history mutated: len = %d, want 6", len(src.history))
	}
}

// TestForkChatSession_DeepCopiesHistory verifies that mutating a Part in the
// source after forking doesn't bleed into the fork (and vice versa). This
// guards against the common bug of forgetting to deep-copy slice elements
// when "copying" a slice via the built-in copy().
func TestForkChatSession_DeepCopiesHistory(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	seedHistory(t, s, pInfo.ID, "default",
		userTurn("original"),
	)

	info, err := s.ForkChatSession(pInfo.ID, "default", 0, "")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}

	p := s.projects[pInfo.ID]
	src := p.sessions["default"]
	forked := p.sessions[info.ID]

	// Mutate the source's part text after forking.
	src.history[0].Parts[0].Text = "MUTATED"
	if forked.history[0].Parts[0].Text != "original" {
		t.Errorf("fork leaked source mutation: got %q, want %q",
			forked.history[0].Parts[0].Text, "original")
	}

	// And vice versa — mutating the fork shouldn't affect the source.
	forked.history[0].Parts[0].Text = "FORK_CHANGED"
	if src.history[0].Parts[0].Text != "MUTATED" {
		t.Errorf("source bled fork mutation: got %q, want %q",
			src.history[0].Parts[0].Text, "MUTATED")
	}
}

// TestForkChatSession_AutoNamesWithBranchSuffix verifies the default name
// follows the "<source name> (branch)" pattern so users can see lineage at
// a glance in the session tab list.
func TestForkChatSession_AutoNamesWithBranchSuffix(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	p := s.projects[pInfo.ID]
	p.sessions["default"].Name = "API Design"
	seedHistory(t, s, pInfo.ID, "default", userTurn("hi"))

	info, err := s.ForkChatSession(pInfo.ID, "default", 0, "")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}
	if info.Name != "API Design (branch)" {
		t.Errorf("auto name = %q, want %q", info.Name, "API Design (branch)")
	}
}

func TestForkChatSession_UsesCustomName(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	seedHistory(t, s, pInfo.ID, "default", userTurn("hi"))

	info, err := s.ForkChatSession(pInfo.ID, "default", 0, "Try GPT-style answer")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}
	if info.Name != "Try GPT-style answer" {
		t.Errorf("custom name = %q, want %q", info.Name, "Try GPT-style answer")
	}
}

func TestForkChatSession_TruncatesLongName(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	seedHistory(t, s, pInfo.ID, "default", userTurn("hi"))

	long := strings.Repeat("a", 80)
	info, err := s.ForkChatSession(pInfo.ID, "default", 0, long)
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}
	if len(info.Name) != 60 {
		t.Errorf("long name not truncated: len=%d, want 60", len(info.Name))
	}
}

// TestForkChatSession_PersistsImmediately verifies the fork's history is
// written to disk synchronously so a crash before the user sends anything
// in the new session doesn't lose the branch.
//
// Forking at index=1 (second-most-recent user turn) on a history of
// [user, model, user] preserves [user, model, user] (everything up to and
// including the user turn at idx=1 from end, which is the FIRST user turn).
// We don't fork at idx=0 here because that would only persist 1 turn (the
// user turn) and not exercise the through-load reconstruction much.
func TestForkChatSession_PersistsImmediately(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	seedHistory(t, s, pInfo.ID, "default",
		userTurn("first user"),
		modelTurn("first reply"),
		userTurn("second user"),
		modelTurn("second reply"),
	)

	info, err := s.ForkChatSession(pInfo.ID, "default", 1, "branch1")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}

	// Reload via GetHistory which reads from the on-disk file. Fork at
	// idx-from-end=1 (= "first user") → keep [first user] only, so we should
	// see exactly 1 user message after persistence + reload.
	hist, err := s.GetHistory(pInfo.ID, info.ID)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(hist) != 1 {
		t.Errorf("persisted history len = %d, want 1 ('first user')", len(hist))
	}
	if len(hist) > 0 && hist[0].Content != "first user" {
		t.Errorf("hist[0].Content = %q, want %q", hist[0].Content, "first user")
	}
}

// TestForkChatSession_EmptySessionIDDefaultsToDefault matches the behaviour
// of every other session-aware method (GetHistory, EditUserMessage, etc.).
func TestForkChatSession_EmptySessionIDDefaultsToDefault(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	seedHistory(t, s, pInfo.ID, "default", userTurn("hi"))

	info, err := s.ForkChatSession(pInfo.ID, "", 0, "")
	if err != nil {
		t.Fatalf("ForkChatSession with empty sid: %v", err)
	}
	if info == nil || info.ID == "" {
		t.Errorf("expected new session info, got %+v", info)
	}
}

// TestForkChatSession_SkipsToolOnlyUserTurns verifies the user-turn counter
// only counts turns that actually contain text (mirrors EditUserMessage).
// A user turn with only FunctionResponse parts (tool result) is skipped so
// the index points to a real human message the user can recognise.
func TestForkChatSession_SkipsToolOnlyUserTurns(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	seedHistory(t, s, pInfo.ID, "default",
		userTurn("real first"),
		modelTurn("first reply"),
		// A tool-result turn dressed as "user" with no text parts.
		&genai.Content{Role: "user", Parts: []*genai.Part{genai.NewPartFromFunctionResponse("t", map[string]any{"r": "x"})}},
		userTurn("real second"),
	)

	info, err := s.ForkChatSession(pInfo.ID, "default", 1, "")
	if err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}
	p := s.projects[pInfo.ID]
	forked := p.sessions[info.ID]
	last := forked.history[len(forked.history)-1]
	if last.Role != "user" || last.Parts[0].Text != "real first" {
		t.Errorf("idx=1 should resolve to 'real first', got %+v", last)
	}
}
