package studio

import (
	"context"
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
	if got := projectPermMode(t, s, info.ID); got != "manual" {
		t.Errorf("PermissionMode = %q, want manual", got)
	}
	if err := s.SetProjectPermissionMode(info.ID, "acceptEdits"); err != nil {
		t.Fatal(err)
	}
	if got := projectPermMode(t, s, info.ID); got != "accept_edits" {
		t.Errorf("PermissionMode after acceptEdits = %q, want accept_edits", got)
	}

	if err := s.SetProjectPermissionMode(info.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := projectPermMode(t, s, info.ID); got != "auto" {
		t.Errorf("PermissionMode after auto = %q, want auto", got)
	}
	if err := s.SetProjectPermissionMode(info.ID, "skip"); err != nil {
		t.Fatal(err)
	}
	if got := projectPermMode(t, s, info.ID); got != "skip" {
		t.Errorf("PermissionMode after skip = %q, want skip", got)
	}

	// "" is valid and means auto.
	if err := s.SetProjectPermissionMode(info.ID, ""); err != nil {
		t.Fatalf("empty mode should be valid: %v", err)
	}
}

func TestSetProjectAcceptEditsPersistsAndUsesDedicatedDirective(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Accept edits")
	if err := s.SetProjectPermissionMode(info.ID, "accept_edits"); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	if got := project.ToConfig().PermissionMode; got != "accept_edits" {
		t.Fatalf("persisted permission mode = %q", got)
	}
	if got := NewProject(project.ToConfig()).PermissionMode; got != "accept_edits" {
		t.Fatalf("round-trip permission mode = %q", got)
	}
	directive := permissionDirective("accept_edits")
	for _, text := range []string{"Permission mode: Accept edits", "file and document edits", "Shell commands", "Git state changes"} {
		if !strings.Contains(directive, text) {
			t.Fatalf("Accept edits directive missing %q: %s", text, directive)
		}
	}
}

func TestSetProjectComputerUsePersistsAndInvalidatesClient(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	p := s.projects[info.ID]
	p.mu.Lock()
	p.client = &mockClient{}
	p.mu.Unlock()

	if err := s.SetProjectComputerUse(info.ID, true); err != nil {
		t.Fatal(err)
	}
	if !p.Info().ComputerUseEnabled {
		t.Fatal("computer use was not enabled in project info")
	}
	if !p.ToConfig().ComputerUseEnabled {
		t.Fatal("computer use was not persisted to project config")
	}
	p.mu.RLock()
	clientWasReset := p.client == nil
	p.mu.RUnlock()
	if !clientWasReset {
		t.Fatal("provider client was not reset after computer-use tool change")
	}
	if err := s.SetProjectComputerUse("missing", true); err == nil {
		t.Fatal("enabled computer use for unknown project")
	}
}

func TestDisablingComputerUseEmergencyStopsActiveSession(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")
	if err := s.SetProjectComputerUse(info.ID, true); err != nil {
		t.Fatal(err)
	}
	p := s.projects[info.ID]
	session := p.GetSession("default")
	runCtx, cancel := context.WithCancel(context.Background())
	session.mu.Lock()
	session.active = true
	session.cancelFn = cancel
	session.mu.Unlock()

	if err := s.SetProjectComputerUse(info.ID, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("disabling computer use did not cancel the active session")
	}
	if p.Info().ComputerUseEnabled || p.ToConfig().ComputerUseEnabled {
		t.Fatal("computer use remained enabled after emergency stop")
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
	if cfg.PermissionMode != "manual" {
		t.Errorf("ToConfig.PermissionMode = %q, want manual", cfg.PermissionMode)
	}
	// Restart-style round-trip.
	p2 := NewProject(cfg)
	if p2.PermissionMode != "manual" {
		t.Errorf("NewProject.PermissionMode = %q after round-trip, want manual", p2.PermissionMode)
	}
}

func TestSetSessionPermissionModeIsPlanOnlyAndSessionScoped(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Session plan")
	second, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionPermissionMode(info.ID, "default", "manual"); err == nil {
		t.Fatal("session override accepted a durable project mode")
	}
	if err := s.SetSessionPermissionMode(info.ID, "missing", "plan"); err == nil {
		t.Fatal("session override accepted an unknown chat")
	}
	if err := s.SetSessionPermissionMode(info.ID, "default", "plan"); err != nil {
		t.Fatal(err)
	}
	p := s.projects[info.ID]
	if got := p.GetSession("default").Info().PermissionMode; got != "plan" {
		t.Fatalf("default session mode = %q, want plan", got)
	}
	if got := p.GetSession(second.ID).Info().PermissionMode; got != "" {
		t.Fatalf("Plan leaked to sibling session: %q", got)
	}
	if got := p.Info().PermissionMode; got == "plan" {
		t.Fatal("Plan leaked into durable project permission mode")
	}
	p.GetSession("default").mu.Lock()
	p.GetSession("default").active = true
	p.GetSession("default").mu.Unlock()
	if err := s.SetSessionPermissionMode(info.ID, "default", ""); err == nil {
		t.Fatal("changed a running turn's snapshotted permission mode")
	}
	p.GetSession("default").mu.Lock()
	p.GetSession("default").active = false
	p.GetSession("default").mu.Unlock()
	if err := s.SetSessionPermissionMode(info.ID, "default", ""); err != nil {
		t.Fatal(err)
	}
	if got := p.GetSession("default").Info().PermissionMode; got != "" {
		t.Fatalf("cleared session mode = %q", got)
	}
}

// TestSendMessage_AskBeforeChangesDirective verifies the hard-gate guidance
// lands in the system instruction when the project is in ask mode.
func TestSendMessage_AskBeforeChangesDirective(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "done"}}}
	p, _ := newTestProject(t, mc, nil)
	p.SystemPrompt = "you are a test agent"
	p.PermissionMode = "ask"

	runAgent(p, "hello")

	mc.mu.Lock()
	got := mc.lastSystemInstruction
	mc.mu.Unlock()
	if !strings.Contains(got, "ask_user") || !strings.Contains(got, "Permission mode: Manual") {
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
