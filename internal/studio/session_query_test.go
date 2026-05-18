package studio

import (
	"testing"
	"time"

	"google.golang.org/genai"
)

// TestGetHistory_EmptySessionIDDefaultsToDefault verifies that passing an
// empty sessionID to GetHistory is treated as "default". This mirrors the
// frontend's behavior when it sends the initial history load without an
// explicit session.
func TestGetHistory_EmptySessionIDDefaultsToDefault(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-gh-empty", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}

	msgs, err := s.GetHistory("pid-gh-empty", "") // empty sessionID
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
}

// TestGetHistory_UnknownProject verifies that an unknown project ID returns an
// error rather than a nil slice so callers can distinguish "no history" from
// "project doesn't exist".
func TestGetHistory_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.GetHistory("no-such-id", "default"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestGetHistory_NilSession verifies that GetHistory returns (nil, nil) rather
// than an error when the requested session doesn't exist and there is no
// "default" session to fall back to. The frontend interprets a nil slice as
// "no messages" and shows the welcome screen, which is the right behaviour
// when a session was explicitly deleted.
func TestGetHistory_NilSession(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-gh-nil", Name: "P", Directory: t.TempDir()})
	p.studio = s
	// Remove "default" so GetSession("nonexistent") returns nil.
	delete(p.sessions, "default")
	s.projects[p.ID] = p

	msgs, err := s.GetHistory("pid-gh-nil", "nonexistent-session")
	if err != nil {
		t.Fatalf("expected (nil, nil), got error: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil slice, got %v", msgs)
	}
}

// TestGetHistory_FiltersFunctionCallTurns verifies that model turns that contain
// only FunctionCall parts (and no visible text) are excluded from GetHistory
// output, along with the corresponding user turns that hold FunctionResponse
// parts. Only user-text and model-text turns should appear.
func TestGetHistory_FiltersFunctionCallTurns(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Simulate a full tool-use exchange:
	// 1. user text
	// 2. model FunctionCall (no text) — should be filtered
	// 3. user FunctionResponse (no text) — should be filtered
	// 4. model text (the final answer)
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("list files")}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "bash", Args: map[string]any{"cmd": "ls"}}}}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "bash", Response: map[string]any{"result": "file.go\n"}}}}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("Here are the files.")}},
	}

	msgs, err := s.GetHistory("pid", "default")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	// Only the user text turn and the final model text turn should appear.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "list files" {
		t.Errorf("msg[0] = {role:%q content:%q}, want {user, 'list files'}", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Here are the files." {
		t.Errorf("msg[1] = {role:%q content:%q}, want {assistant, 'Here are the files.'}", msgs[1].Role, msgs[1].Content)
	}
}

// TestListChatSessions_UnknownProject verifies that ListChatSessions returns an
// error for a project ID that doesn't exist. The frontend depends on this to
// detect stale references (e.g. after a project is removed mid-session).
func TestListChatSessions_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	_, err := s.ListChatSessions("no-such-project")
	if err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestGetSession_EmptyIDDefaultsToDefault verifies that passing an empty
// session ID is treated as "default", matching the frontend's behavior when no
// session is explicitly selected.
func TestGetSession_EmptyIDDefaultsToDefault(t *testing.T) {
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	// GetSession("") should return the "default" session.
	got := p.GetSession("")
	if got == nil {
		t.Fatal("GetSession('') returned nil, want default session")
	}
	if got.ID != "default" {
		t.Errorf("GetSession('') returned session ID %q, want 'default'", got.ID)
	}
}

// TestGetSession_FallbackToDefault verifies that requesting a session ID that
// doesn't exist falls back to the "default" session rather than returning nil.
func TestGetSession_FallbackToDefault(t *testing.T) {
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	got := p.GetSession("nonexistent-session-id")
	if got == nil {
		t.Fatal("GetSession(nonexistent) returned nil, want default session")
	}
	if got.ID != "default" {
		t.Errorf("GetSession(nonexistent) returned ID %q, want 'default'", got.ID)
	}
}

// TestListChatSessions_ZeroLastUsedTiebreaker verifies the CreatedAt sort
// applied when multiple sessions all have lastUsedAt==0 and none of them is
// the "default" session. Older sessions (smaller CreatedAt) should sort first.
func TestListChatSessions_ZeroLastUsedTiebreaker(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-tie", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Remove the auto-created "default" session so we control the full set.
	delete(p.sessions, "default")

	// Create two sessions with 0 lastUsedAt (neither used). createdAt differs.
	sOld := NewChatSession("Old Chat")
	sNew := NewChatSession("New Chat")
	// Explicitly set CreatedAt so the sort is deterministic regardless of
	// time resolution (two consecutive time.Now() calls can return the same value).
	sOld.CreatedAt = time.Unix(1, 0)
	sNew.CreatedAt = time.Unix(2, 0)
	sOld.lastUsedAt = 0
	sNew.lastUsedAt = 0
	p.sessions[sOld.ID] = sOld
	p.sessions[sNew.ID] = sNew

	sessions, err := s.ListChatSessions("pid-tie")
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	// Older CreatedAt (sOld) should sort first.
	if sessions[0].ID != sOld.ID {
		t.Errorf("sessions[0].ID = %q, want %q (older session first)", sessions[0].ID, sOld.ID)
	}
}

// TestListChatSessions_DefaultLastAmongZeros verifies that the "default"
// session sorts last when it shares a zero lastUsedAt with other sessions.
// This keeps the UI stable: newly-added sessions don't displace default.
func TestListChatSessions_DefaultLastAmongZeros(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-deflt", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Add a second session with 0 lastUsedAt — it should appear BEFORE default.
	extra := NewChatSession("Extra Chat")
	extra.lastUsedAt = 0
	p.sessions[extra.ID] = extra

	sessions, err := s.ListChatSessions("pid-deflt")
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	// "default" must be last.
	if sessions[len(sessions)-1].ID != "default" {
		t.Errorf("last session = %q, want 'default'", sessions[len(sessions)-1].ID)
	}
}

// TestListChatSessions_SortOrder verifies that ListChatSessions returns sessions
// ordered by lastUsedAt descending (most recently used first), with
// never-used sessions (lastUsedAt=0) sorted to the bottom, and "default"
// placed last among sessions with the same (zero) lastUsedAt.
func TestListChatSessions_SortOrder(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Create two additional sessions alongside the existing "default".
	info2, err := s.CreateChatSession("pid")
	if err != nil {
		t.Fatalf("CreateChatSession 2: %v", err)
	}
	info3, err := s.CreateChatSession("pid")
	if err != nil {
		t.Fatalf("CreateChatSession 3: %v", err)
	}

	// Simulate different usage times: session3 newest, session2 older, default unused.
	p.sessions[info3.ID].lastUsedAt = 2000
	p.sessions[info2.ID].lastUsedAt = 1000
	// p.sessions["default"].lastUsedAt remains 0

	sessions, err := s.ListChatSessions("pid")
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	// Expected order: info3 (2000) > info2 (1000) > default (0).
	if sessions[0].ID != info3.ID {
		t.Errorf("sessions[0].ID = %q, want %q (most recent)", sessions[0].ID, info3.ID)
	}
	if sessions[1].ID != info2.ID {
		t.Errorf("sessions[1].ID = %q, want %q", sessions[1].ID, info2.ID)
	}
	if sessions[2].ID != "default" {
		t.Errorf("sessions[2].ID = %q, want 'default' (never used)", sessions[2].ID)
	}
}
