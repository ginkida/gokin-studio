package studio

import (
	"context"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

// newStudioForTest wires a minimal in-memory Studio instance suitable for
// exercising app-level methods that don't need a real LLM client (export,
// session CRUD, etc.). Uses a temp config dir so disk writes are isolated.
func newStudioForTest(t *testing.T) *Studio {
	t.Helper()
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	s.ctx = context.Background()
	return s
}

func TestExportChatIncludesSessionHistory(t *testing.T) {
	s := newStudioForTest(t)

	p := NewProject(ProjectConfig{
		ID:        "pid",
		Name:      "TestProject",
		Directory: t.TempDir(),
	})
	p.studio = s
	s.projects[p.ID] = p

	// Populate the default session's history directly (bypassing the agent
	// loop). Mix a text turn with a function-response turn to verify export
	// only renders human-readable parts.
	def := p.sessions["default"]
	def.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("how do I run tests?")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("Run `go test ./...`.")}},
		// A function response (role=user, FunctionResponse part, no text) —
		// should be filtered out of the export.
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name: "bash", Response: map[string]any{"result": "ok"},
		}}}},
	}

	md, err := s.ExportChat("pid", "default")
	if err != nil {
		t.Fatalf("ExportChat returned error: %v", err)
	}
	if !strings.Contains(md, "how do I run tests?") {
		t.Errorf("export missing user message: %q", md)
	}
	if !strings.Contains(md, "go test ./...") {
		t.Errorf("export missing assistant reply: %q", md)
	}
	if strings.Contains(md, "FunctionResponse") || strings.Contains(md, "function_response") {
		t.Errorf("export leaked function-response internals: %q", md)
	}
	if !strings.Contains(md, "TestProject") {
		t.Errorf("export missing project name in header: %q", md)
	}
}

// TestExportChat_UnknownProject verifies that ExportChat returns an error for
// a project ID that doesn't exist rather than panicking.
func TestExportChat_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ExportChat("no-such-project", "default"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestExportChat_FiltersThinkingParts verifies that thinking (reasoning) parts
// in model turns are excluded from the exported markdown, so the human-readable
// transcript only contains visible text even when extended thinking was active.
func TestExportChat_FiltersThinkingParts(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-export-think", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("question")}},
		{Role: "model", Parts: []*genai.Part{
			{Text: "internal deliberation", Thought: true}, // must be excluded
			genai.NewPartFromText("visible answer"),
		}},
	}

	md, err := s.ExportChat("pid-export-think", "default")
	if err != nil {
		t.Fatalf("ExportChat: %v", err)
	}
	if strings.Contains(md, "internal deliberation") {
		t.Errorf("thinking part leaked into export: %q", md)
	}
	if !strings.Contains(md, "visible answer") {
		t.Errorf("visible answer missing from export: %q", md)
	}
}

func TestExportChatUnknownSession(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	if _, err := s.ExportChat("pid", "no-such-session"); err == nil {
		t.Fatal("expected error for unknown session, got nil")
	}
}

func TestExportChatDefaultsToDefaultSession(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}

	md, err := s.ExportChat("pid", "")
	if err != nil {
		t.Fatalf("ExportChat(\"\") error: %v", err)
	}
	if !strings.Contains(md, "hello") {
		t.Errorf("empty sessionID should default to 'default' and include its history; got %q", md)
	}
}

// TestGetHistory_FiltersThinkingParts verifies that GetHistory omits thinking
// (reasoning) parts so internal model deliberation never appears as a regular
// assistant message when the user reloads a session.
func TestGetHistory_FiltersThinkingParts(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Mix a normal text turn with a model turn that has BOTH a thinking part
	// and a visible text part (the typical shape after extended thinking).
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("solve this")}},
		{Role: "model", Parts: []*genai.Part{
			{Text: "<thinking>internal reasoning here</thinking>", Thought: true},
			genai.NewPartFromText("Here is the solution."),
		}},
	}

	msgs, err := s.GetHistory("pid", "default")
	if err != nil {
		t.Fatalf("GetHistory error: %v", err)
	}
	// Two entries: user + assistant.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	// The assistant message must contain the visible text but NOT the thinking part.
	asst := msgs[1]
	if asst.Role != "assistant" {
		t.Errorf("expected role=assistant, got %q", asst.Role)
	}
	if !strings.Contains(asst.Content, "Here is the solution.") {
		t.Errorf("assistant message missing visible text; got %q", asst.Content)
	}
	if strings.Contains(asst.Content, "internal reasoning") {
		t.Errorf("thinking part leaked into GetHistory output: %q", asst.Content)
	}
}

// TestCreateChatSession_NamingAfterDeletion verifies that creating a session
// after a deletion produces a unique "Chat N" name. Before the fix, new
// sessions used `len(sessions)+1` as the index, so deleting "Chat 2" and
// creating a new tab would produce another "Chat 2" that duplicated "Chat 3".
func TestCreateChatSession_NamingAfterDeletion(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// The project already has one default session (Chat 1).
	// Create two more so we have Chat 1, Chat 2, Chat 3.
	info2, err := s.CreateChatSession("pid")
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	info3, err := s.CreateChatSession("pid")
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	// Verify they were named sequentially.
	if info2.Name != "Chat 2" {
		t.Errorf("expected 'Chat 2', got %q", info2.Name)
	}
	if info3.Name != "Chat 3" {
		t.Errorf("expected 'Chat 3', got %q", info3.Name)
	}

	// Delete the middle one ("Chat 2"). Sessions remaining: Chat 1, Chat 3.
	if err := s.DeleteChatSession("pid", info2.ID); err != nil {
		t.Fatalf("DeleteChatSession: %v", err)
	}

	// Create another. With the old len+1 logic this would produce "Chat 3"
	// again (len=2 → 2+1=3). With the max+1 fix it must produce "Chat 4".
	info4, err := s.CreateChatSession("pid")
	if err != nil {
		t.Fatalf("CreateChatSession after deletion: %v", err)
	}
	if info4.Name != "Chat 4" {
		t.Errorf("expected 'Chat 4' after deletion, got %q", info4.Name)
	}
}

// TestDeleteChatSession_CannotDeleteLast verifies that the backend rejects
// deleting the final remaining session so a project is never left with zero chats.
func TestDeleteChatSession_CannotDeleteLast(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Only one session exists.
	if len(p.sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(p.sessions))
	}
	var onlyID string
	for id := range p.sessions {
		onlyID = id
	}

	err := s.DeleteChatSession("pid", onlyID)
	if err == nil {
		t.Fatal("expected error when deleting the last session, got nil")
	}
	// Session must still exist.
	if len(p.sessions) != 1 {
		t.Errorf("session count changed after failed delete: %d", len(p.sessions))
	}
}

// TestCreateChatSession_NamingGapFill verifies the scenario where multiple
// sessions share non-contiguous numbers. New sessions always get max+1, not
// gap-fill, so the sequence is always strictly increasing.
func TestCreateChatSession_NamingGapFill(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Create 4 sessions: Chat 1 (default), Chat 2, Chat 3, Chat 4.
	ids := make([]string, 0)
	for i := 0; i < 3; i++ {
		info, err := s.CreateChatSession("pid")
		if err != nil {
			t.Fatalf("CreateChatSession %d: %v", i+2, err)
		}
		ids = append(ids, info.ID)
	}
	// Delete Chat 2 and Chat 3, leaving Chat 1 and Chat 4.
	for _, id := range ids[:2] {
		if err := s.DeleteChatSession("pid", id); err != nil {
			t.Fatalf("DeleteChatSession: %v", err)
		}
	}

	// Next session should be Chat 5 (not Chat 2 or Chat 3).
	info, err := s.CreateChatSession("pid")
	if err != nil {
		t.Fatalf("CreateChatSession after deletions: %v", err)
	}
	if info.Name != "Chat 5" {
		t.Errorf("expected 'Chat 5', got %q", info.Name)
	}
}

// TestEditUserMessage_TrimsHistoryAndRequeues verifies that EditUserMessage
// trims history back to just before the target user turn and re-sends the
// edited text as a new user message (kicking off a new agent turn).
func TestEditUserMessage_TrimsHistoryAndRequeues(t *testing.T) {
	// Use a mock client that returns one text response per call.
	mc := &mockClient{responses: []mockResp{
		{text: "First reply."},
		{text: "Edited reply."},
	}}
	p, rec := newTestProject(t, mc, tools.NewRegistry())

	// Wire a studio so EditUserMessage can route back through s.SendMessage.
	s := newStudioForTest(t)
	s.projects[p.ID] = p
	p.studio = s

	// Build up a conversation: send "hello" → agent replies "First reply."
	// Run synchronously (runAgent bypasses the WaitGroup).
	runAgent(p, "hello")

	// Verify baseline history: user("hello"), model("First reply.") = 2 entries.
	sess := p.GetSession("default")
	sess.mu.RLock()
	beforeLen := len(sess.history)
	sess.mu.RUnlock()
	if beforeLen != 2 {
		t.Fatalf("expected 2 history entries after first turn, got %d", beforeLen)
	}

	// Edit the last user message (index 0 = last user turn from end) to "edited".
	// EditUserMessage trims history back to before that turn and re-queues via
	// s.SendMessage (async — tracked by s.wg).
	if err := s.EditUserMessage(p.ID, "default", 0, "edited"); err != nil {
		t.Fatalf("EditUserMessage: %v", err)
	}
	// Wait for the async re-send goroutine to finish.
	s.wg.Wait()

	// After edit + re-send: user("edited"), model("Edited reply.") = 2 entries.
	sess.mu.RLock()
	afterLen := len(sess.history)
	sess.mu.RUnlock()
	if afterLen != 2 {
		t.Errorf("expected 2 history entries after edit, got %d", afterLen)
	}

	// The second text event should carry the new reply text.
	texts := rec.find(EventChatText)
	found := false
	for _, e := range texts {
		if e.data.(ChatTextEvent).Text == "Edited reply." {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Edited reply.' in text events; got: %v", texts)
	}

	// Total LLM calls: 1 for original + 1 for the edit re-send = 2.
	if mc.callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", mc.callCount)
	}
}
