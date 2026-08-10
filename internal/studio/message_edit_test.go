package studio

import (
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

// TestEditUserMessage_Validation checks every early-return error path in
// EditUserMessage without running any real LLM calls.
func TestEditUserMessage_Validation(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p
	// Seed one user turn so the "turn not found" test has context.
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}

	cases := []struct {
		name     string
		projID   string
		sessID   string
		idx      int
		text     string
		wantErrS string
	}{
		{"empty text", "pid", "default", 0, "", "cannot be empty"},
		{"whitespace text", "pid", "default", 0, "  ", "cannot be empty"},
		{"negative index", "pid", "default", -1, "edited", "must be >= 0"},
		{"unknown project", "no-such", "default", 0, "edited", "not found"},
		// Index beyond history: history has 1 user turn so index 5 is not found.
		{"index out of range", "pid", "default", 5, "edited", "not found in history"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.EditUserMessage(c.projID, c.sessID, c.idx, c.text)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErrS)
			}
			if !containsStr(err.Error(), c.wantErrS) {
				t.Errorf("error = %q, want substring %q", err.Error(), c.wantErrS)
			}
		})
	}
}

// TestEditUserMessage_ActiveSessionRejected verifies that editing a message
// while the agent is running returns an error instead of silently racing.
func TestEditUserMessage_ActiveSessionRejected(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "reply"}}}
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	s.projects[p.ID] = p
	p.studio = s

	// Seed history with a user message.
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("original")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("reply")}},
	}

	// Simulate an active session by setting the flag directly.
	p.sessions["default"].mu.Lock()
	p.sessions["default"].active = true
	p.sessions["default"].mu.Unlock()
	t.Cleanup(func() {
		p.sessions["default"].mu.Lock()
		p.sessions["default"].active = false
		p.sessions["default"].mu.Unlock()
	})

	err := s.EditUserMessage(p.ID, "default", 0, "edited")
	if err == nil {
		t.Fatal("expected error when session is active, got nil")
	}
	if !containsStr(err.Error(), "agent is running") {
		t.Errorf("error = %q, want 'agent is running'", err.Error())
	}
}

// TestDispatch_Validation checks that Dispatch rejects self-dispatch and empty tasks.
func TestDispatch_Validation(t *testing.T) {
	s := newStudioForTest(t)
	pA := addTestProject(t, s, "A")
	pB := addTestProject(t, s, "B")

	// Self-dispatch
	if err := s.Dispatch(pA.ID, pA.ID, "default", "do something"); err == nil {
		t.Error("expected error for self-dispatch, got nil")
	}

	// Empty task
	if err := s.Dispatch(pA.ID, pB.ID, "default", ""); err == nil {
		t.Error("expected error for empty task, got nil")
	}

	// Whitespace-only task
	if err := s.Dispatch(pA.ID, pB.ID, "default", "   "); err == nil {
		t.Error("expected error for whitespace-only task, got nil")
	}

	// Unknown source project
	if err := s.Dispatch("no-such", pB.ID, "default", "task"); err == nil {
		t.Error("expected error for unknown source project, got nil")
	}

	// Unknown target project
	if err := s.Dispatch(pA.ID, "no-such", "default", "task"); err == nil {
		t.Error("expected error for unknown target project, got nil")
	}

	// Empty fromSessionID defaults to "default" internally; still fails with
	// unknown target so the goroutine is never launched — covers line 1128-1130.
	if err := s.Dispatch(pA.ID, "no-such", "", "task"); err == nil {
		t.Error("expected error for empty sessionID + unknown target, got nil")
	}
}

// TestDispatch_LaunchesGoroutine verifies that a valid dispatch call (both
// projects exist, non-empty task) actually launches the background goroutine
// and passes the resolved session ID through. Uses testDispatchFn to avoid
// needing the Wails runtime context.
func TestDispatch_LaunchesGoroutine(t *testing.T) {
	s := newStudioForTest(t)
	pA := addTestProject(t, s, "A")
	pB := addTestProject(t, s, "B")

	type captured struct {
		fromName string
		toName   string
		fromSid  string
		task     string
	}
	var got captured
	s.testDispatchFn = func(from, to *Project, fromSid, task string, _ Settings) {
		got = captured{from.Name, to.Name, fromSid, task}
	}

	// Empty fromSessionID must default to "default" inside Dispatch.
	if err := s.Dispatch(pA.ID, pB.ID, "", "do work"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s.wg.Wait() // wait for goroutine to finish

	if got.fromSid != "default" {
		t.Errorf("fromSid = %q, want %q", got.fromSid, "default")
	}
	if got.task != "do work" {
		t.Errorf("task = %q, want %q", got.task, "do work")
	}
	if got.fromName != "A" || got.toName != "B" {
		t.Errorf("names = %q→%q, want A→B", got.fromName, got.toName)
	}
}

// TestEditUserMessage_EmptySessionIDDefaultsToDefault verifies that passing ""
// as the session ID is treated identically to "default", matching the
// frontend's behavior when no explicit session is selected.
func TestEditUserMessage_EmptySessionIDDefaultsToDefault(t *testing.T) {
	_ = withTempHistoryDir(t)
	mc := &mockClient{responses: []mockResp{{text: "reply to edit"}}}
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	s.projects[p.ID] = p
	p.studio = s

	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("original")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("reply")}},
	}

	// Empty sessionID should resolve to "default".
	err := s.EditUserMessage(p.ID, "", 0, "edited message")
	if err != nil {
		t.Fatalf("EditUserMessage with empty sessionID: %v", err)
	}
	s.wg.Wait()
}

// TestEditUserMessage_UnknownSessionInKnownProject verifies that using a
// session ID that doesn't exist within a known project returns an error,
// rather than silently falling back or panicking.
func TestEditUserMessage_UnknownSessionInKnownProject(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-edit-nosess", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	err := s.EditUserMessage(p.ID, "nonexistent-session", 0, "edited")
	if err == nil {
		t.Fatal("expected error for unknown session ID, got nil")
	}
	if !containsStr(err.Error(), "not found") {
		t.Errorf("error = %q, want substring 'not found'", err.Error())
	}
}

// TestEditUserMessage_SkipsNoTextUserTurns verifies that user turns containing
// only FunctionResponse parts (no text) are skipped when counting back from
// the end of history. This exercises the `if !hasText { continue }` branch
// in EditUserMessage's inner loop.
func TestEditUserMessage_SkipsNoTextUserTurns(t *testing.T) {
	_ = withTempHistoryDir(t)
	mc := &mockClient{responses: []mockResp{{text: "reply to edited"}}}
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	s.projects[p.ID] = p
	p.studio = s

	// History: user(text) → model → user(FunctionResponse, no text) → model(text)
	// The FunctionResponse user turn must be skipped; the text user turn at i=0 is found.
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("original question")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("thinking...")}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name:     "bash",
			Response: map[string]any{"result": "file.go"},
		}}}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("here you go")}},
	}

	// Index 0 from end should skip the FunctionResponse turn and find the text turn.
	err := s.EditUserMessage(p.ID, "default", 0, "edited question")
	if err != nil {
		t.Fatalf("EditUserMessage skipping no-text turns: %v", err)
	}
	s.wg.Wait()
}

// containsStr is a tiny helper so test cases don't need to import strings.
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (substr == "" || func() bool {
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	}())
}
