package studio

import (
	"testing"

	"google.golang.org/genai"
)

// TestChatSessionInfo_EmptyHistory verifies the zero-state: a brand-new session
// reports Messages=0 and all metadata fields match what was set at construction.
func TestChatSessionInfo_EmptyHistory(t *testing.T) {
	sess := NewChatSession("My Chat")
	info := sess.Info()

	if info.ID != sess.ID {
		t.Errorf("ID = %q, want %q", info.ID, sess.ID)
	}
	if info.Name != "My Chat" {
		t.Errorf("Name = %q, want 'My Chat'", info.Name)
	}
	if info.Messages != 0 {
		t.Errorf("Messages = %d for empty history, want 0", info.Messages)
	}
	if info.Active {
		t.Errorf("Active = true for new session, want false")
	}
	if info.LastUsedAt != 0 {
		t.Errorf("LastUsedAt = %d for unused session, want 0", info.LastUsedAt)
	}
}

// TestChatSessionInfo_TextHistory verifies that text turns are counted once per
// content entry regardless of how many text parts it contains. A single
// content entry with one text part should count as 1 message.
func TestChatSessionInfo_TextHistory(t *testing.T) {
	sess := NewChatSession("Chat")
	sess.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("hi there")}},
	}
	info := sess.Info()
	if info.Messages != 2 {
		t.Errorf("Messages = %d, want 2", info.Messages)
	}
}

// TestChatSessionInfo_ToolOnlyHistory verifies that content entries containing
// only FunctionCall or FunctionResponse parts (no text) are NOT counted as
// messages. These are internal tool-use plumbing turns, not visible messages.
func TestChatSessionInfo_ToolOnlyHistory(t *testing.T) {
	sess := NewChatSession("Chat")
	sess.history = []*genai.Content{
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "bash",
			Args: map[string]any{"cmd": "ls"},
		}}}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name:     "bash",
			Response: map[string]any{"result": "file.go"},
		}}}},
	}
	info := sess.Info()
	if info.Messages != 0 {
		t.Errorf("Messages = %d for tool-only history, want 0", info.Messages)
	}
}

// TestChatSessionInfo_MixedHistory verifies that mixed histories (text turns
// interleaved with tool turns) count only the text turns.
func TestChatSessionInfo_MixedHistory(t *testing.T) {
	sess := NewChatSession("Chat")
	sess.history = []*genai.Content{
		// user text: counts
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("list files")}},
		// model FunctionCall (no text): does NOT count
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "bash"}}}},
		// user FunctionResponse (no text): does NOT count
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "bash"}}}},
		// model text: counts
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("Here are the files.")}},
	}
	info := sess.Info()
	if info.Messages != 2 {
		t.Errorf("Messages = %d for mixed history, want 2 (text turns only)", info.Messages)
	}
}

// TestChatSessionInfo_ActiveFlag verifies that the Active field mirrors the
// session's active state so the frontend can show the generation indicator.
func TestChatSessionInfo_ActiveFlag(t *testing.T) {
	sess := NewChatSession("Chat")
	sess.mu.Lock()
	sess.active = true
	sess.mu.Unlock()

	info := sess.Info()
	if !info.Active {
		t.Error("Active = false when session.active = true, want true")
	}
}

// TestChatSessionInfo_LastUsedAt verifies that LastUsedAt is propagated
// correctly so the frontend can sort sessions by recency.
func TestChatSessionInfo_LastUsedAt(t *testing.T) {
	sess := NewChatSession("Chat")
	sess.mu.Lock()
	sess.lastUsedAt = 9999
	sess.mu.Unlock()

	info := sess.Info()
	if info.LastUsedAt != 9999 {
		t.Errorf("LastUsedAt = %d, want 9999", info.LastUsedAt)
	}
}
