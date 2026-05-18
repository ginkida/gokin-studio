package studio

import (
	"testing"

	"google.golang.org/genai"
)

// mkAutoSession creates a new session with an auto-generated "Chat N" name and
// no history, simulating a tab that was opened but never used.
func mkAutoSession(name string) *ChatSession {
	s := NewChatSession(name)
	return s
}

// TestPruneAbandonedEmptySessions_SingleSession verifies the single-session
// guard: a project with only one session must never have it pruned, even if
// it is empty and auto-named.
func TestPruneAbandonedEmptySessions_SingleSession(t *testing.T) {
	p := NewProject(ProjectConfig{ID: "pid-prune1", Name: "P", Directory: t.TempDir()})
	// NewProject already creates "default". Only one session exists.
	if len(p.sessions) != 1 {
		t.Fatalf("expected 1 session after NewProject, got %d", len(p.sessions))
	}

	p.pruneAbandonedEmptySessions()

	if len(p.sessions) != 1 {
		t.Errorf("single session was removed — violates never-zero-sessions invariant")
	}
}

// TestPruneAbandonedEmptySessions_PreservesSession WithHistory verifies that
// a session with at least one history entry is never pruned regardless of its
// name.
func TestPruneAbandonedEmptySessions_PreservesSessionWithHistory(t *testing.T) {
	p := NewProject(ProjectConfig{ID: "pid-prune2", Name: "P", Directory: t.TempDir()})

	// Add a second auto-named session and give it a history entry.
	s2 := mkAutoSession("Chat 2")
	s2.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}
	p.sessions[s2.ID] = s2

	// Add a third auto-named empty session (candidate for pruning).
	s3 := mkAutoSession("Chat 3")
	p.sessions[s3.ID] = s3

	p.pruneAbandonedEmptySessions()

	// s2 (has history) must survive.
	if _, ok := p.sessions[s2.ID]; !ok {
		t.Error("session with history was pruned — should be preserved")
	}
	// s3 (empty, auto-named) should be pruned.
	if _, ok := p.sessions[s3.ID]; ok {
		t.Error("empty auto-named session still exists — should have been pruned")
	}
}

// TestPruneAbandonedEmptySessions_PreservesRenamedSession verifies that an
// empty session the user explicitly renamed is not pruned.
func TestPruneAbandonedEmptySessions_PreservesRenamedSession(t *testing.T) {
	p := NewProject(ProjectConfig{ID: "pid-prune3", Name: "P", Directory: t.TempDir()})

	// "default" session is auto-named "Chat 1" — pruning candidate.
	// Add a renamed empty session (user gave it a meaningful name).
	renamed := mkAutoSession("refactoring work") // custom name, not "Chat N"
	p.sessions[renamed.ID] = renamed

	// Add an auto-named empty session (should be pruned).
	ghost := mkAutoSession("Chat 2")
	p.sessions[ghost.ID] = ghost

	p.pruneAbandonedEmptySessions()

	// The renamed session must survive.
	if _, ok := p.sessions[renamed.ID]; !ok {
		t.Error("renamed session was pruned — should be preserved")
	}
	// The ghost session should be gone.
	if _, ok := p.sessions[ghost.ID]; ok {
		t.Error("empty auto-named ghost session still exists after pruning")
	}
}

// TestPruneAbandonedEmptySessions_NeverDeletesAll verifies the guardrail:
// when every session is an empty auto-named candidate, at least one survives
// so the project is never left with zero sessions.
func TestPruneAbandonedEmptySessions_NeverDeletesAll(t *testing.T) {
	p := NewProject(ProjectConfig{ID: "pid-prune4", Name: "P", Directory: t.TempDir()})

	// Add more empty auto-named sessions so every session is a prune candidate.
	for i := 2; i <= 4; i++ {
		s := mkAutoSession("Chat " + string(rune('0'+i)))
		p.sessions[s.ID] = s
	}
	// All sessions: "default" (Chat 1 implied) + Chat 2, 3, 4 — all empty auto-named.
	beforeCount := len(p.sessions)
	if beforeCount < 2 {
		t.Fatalf("expected >= 2 sessions before prune, got %d", beforeCount)
	}

	p.pruneAbandonedEmptySessions()

	if len(p.sessions) == 0 {
		t.Error("pruneAbandonedEmptySessions deleted ALL sessions — violates never-zero-sessions invariant")
	}
}

// TestPruneAbandonedEmptySessions_PreservesActiveSessions verifies that
// sessions with active=true (generation in progress) are never pruned even if
// they are empty and auto-named.
func TestPruneAbandonedEmptySessions_PreservesActiveSessions(t *testing.T) {
	p := NewProject(ProjectConfig{ID: "pid-prune5", Name: "P", Directory: t.TempDir()})

	// Mark the default session as active.
	p.sessions["default"].mu.Lock()
	p.sessions["default"].active = true
	p.sessions["default"].mu.Unlock()

	// Add a non-active empty auto-named session (pruning candidate).
	ghost := mkAutoSession("Chat 2")
	p.sessions[ghost.ID] = ghost

	p.pruneAbandonedEmptySessions()

	// Active default session must survive.
	if _, ok := p.sessions["default"]; !ok {
		t.Error("active session was pruned — active sessions must never be removed")
	}
	// Ghost should be gone.
	if _, ok := p.sessions[ghost.ID]; ok {
		t.Error("empty auto-named ghost session still exists after pruning")
	}
}
