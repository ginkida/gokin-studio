package studio

import (
	"strings"
	"testing"
)

func TestSetProjectPermissionMode(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")

	if err := s.SetProjectPermissionMode("nope", "ask"); err == nil {
		t.Error("expected error for unknown project")
	}
	if err := s.SetProjectPermissionMode(info.ID, "always"); err == nil {
		t.Error("expected error for invalid mode")
	}

	if err := s.SetProjectPermissionMode(info.ID, "ask"); err != nil {
		t.Fatal(err)
	}
	if got := projectPermMode(t, s, info.ID); got != "ask" {
		t.Errorf("PermissionMode = %q, want ask", got)
	}

	// "auto" normalises to the empty zero value.
	if err := s.SetProjectPermissionMode(info.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := projectPermMode(t, s, info.ID); got != "" {
		t.Errorf("PermissionMode after auto = %q, want empty", got)
	}

	// "" is valid and means auto.
	if err := s.SetProjectPermissionMode(info.ID, ""); err != nil {
		t.Fatalf("empty mode should be valid: %v", err)
	}
}

func TestSetProjectPermissionMode_PersistsToConfig(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Persist")

	if err := s.SetProjectPermissionMode(info.ID, "ask"); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()

	cfg := p.ToConfig()
	if cfg.PermissionMode != "ask" {
		t.Errorf("ToConfig.PermissionMode = %q, want ask", cfg.PermissionMode)
	}
	// Restart-style round-trip.
	p2 := NewProject(cfg)
	if p2.PermissionMode != "ask" {
		t.Errorf("NewProject.PermissionMode = %q after round-trip, want ask", p2.PermissionMode)
	}
}

// TestSendMessage_AskBeforeChangesDirective verifies the soft-enforcement
// directive lands in the system instruction when the project is in ask mode.
func TestSendMessage_AskBeforeChangesDirective(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "done"}}}
	p, _ := newTestProject(t, mc, nil)
	p.SystemPrompt = "you are a test agent"
	p.PermissionMode = "ask"

	runAgent(p, "hello")

	mc.mu.Lock()
	got := mc.lastSystemInstruction
	mc.mu.Unlock()
	if !strings.Contains(got, "ask_user") || !strings.Contains(got, "Permission mode: ask before changes") {
		t.Errorf("ask directive not injected; got %q", got)
	}
	if !strings.Contains(got, "you are a test agent") {
		t.Errorf("base system prompt missing; got %q", got)
	}
}

// TestSendMessage_AskAndPinnedCombined verifies that ask mode and pinned
// context coexist correctly after the SetTurnContext refactor:
// - system instruction carries the base prompt + ask directive (NOT the pin)
// - turn context carries the pinned content (outside the cached prefix)
func TestSendMessage_AskAndPinnedCombined(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "done"}}}
	p, _ := newTestProject(t, mc, nil)
	p.SystemPrompt = "base agent"
	p.PermissionMode = "ask"
	p.pinnedContext = "remember the deploy key"

	runAgent(p, "hello")

	mc.mu.Lock()
	gotSI := mc.lastSystemInstruction
	gotTC := mc.lastTurnContext
	mc.mu.Unlock()
	// Ask directive must be in the system instruction.
	if !strings.Contains(gotSI, "ask_user") {
		t.Errorf("ask directive missing from system instruction; got %q", gotSI)
	}
	// Pinned context must be in turn context (NOT in system instruction).
	if !strings.Contains(gotTC, "remember the deploy key") {
		t.Errorf("pinned context missing from turn context; got %q", gotTC)
	}
	if strings.Contains(gotSI, "remember the deploy key") {
		t.Errorf("pinned context must not be in system instruction; got %q", gotSI)
	}
}

func projectPermMode(t *testing.T, s *Studio, id string) string {
	t.Helper()
	s.mu.RLock()
	p := s.projects[id]
	s.mu.RUnlock()
	if p == nil {
		t.Fatalf("project %s not found", id)
	}
	return p.Info().PermissionMode
}
