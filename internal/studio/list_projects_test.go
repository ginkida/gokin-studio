package studio

import (
	"testing"

	"google.golang.org/genai"
)

// TestListProjects_SortOrder verifies that ListProjects returns projects
// ordered by lastUsedAt descending (most-recently-used first), with
// never-used projects (lastUsedAt=0) at the bottom. Ties among non-zero
// timestamps are broken by name alphabetically.
func TestListProjects_SortOrder(t *testing.T) {
	s := newStudioForTest(t)

	// Add projects directly (bypassing AddProject's auto-timestamp) so we
	// can set controlled lastUsedAt values without fighting the creation time.
	pAlpha := NewProject(ProjectConfig{ID: "p-alpha", Name: "Alpha", Directory: t.TempDir()})
	pBeta := NewProject(ProjectConfig{ID: "p-beta", Name: "Beta", Directory: t.TempDir()})
	pGamma := NewProject(ProjectConfig{ID: "p-gamma", Name: "Gamma", Directory: t.TempDir()})

	pAlpha.lastUsedAt = 0    // never used → should sort last
	pBeta.lastUsedAt = 1000  // older
	pGamma.lastUsedAt = 3000 // newest → should sort first

	for _, p := range []*Project{pAlpha, pBeta, pGamma} {
		p.studio = s
		s.projects[p.ID] = p
	}

	list := s.ListProjects()
	if len(list) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(list))
	}

	// Expected order: gamma (3000) > beta (1000) > alpha (0).
	if list[0].ID != pGamma.ID {
		t.Errorf("list[0] = %q (%s), want %q (most recently used)", list[0].ID, list[0].Name, pGamma.ID)
	}
	if list[1].ID != pBeta.ID {
		t.Errorf("list[1] = %q (%s), want %q", list[1].ID, list[1].Name, pBeta.ID)
	}
	if list[2].ID != pAlpha.ID {
		t.Errorf("list[2] = %q (%s), want %q (never used — should be last)", list[2].ID, list[2].Name, pAlpha.ID)
	}
}

// TestListProjects_NameTiebreaker verifies that when two projects have the
// same (non-zero) lastUsedAt, they are sorted alphabetically by name.
func TestListProjects_NameTiebreaker(t *testing.T) {
	s := newStudioForTest(t)

	pZebra := NewProject(ProjectConfig{ID: "p-zebra", Name: "Zebra", Directory: t.TempDir()})
	pAnt := NewProject(ProjectConfig{ID: "p-ant", Name: "Ant", Directory: t.TempDir()})

	// Give both the same non-zero timestamp so the name tiebreaker fires.
	pZebra.lastUsedAt = 5000
	pAnt.lastUsedAt = 5000

	for _, p := range []*Project{pZebra, pAnt} {
		p.studio = s
		s.projects[p.ID] = p
	}

	list := s.ListProjects()
	if len(list) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(list))
	}
	// Alphabetical: "Ant" < "Zebra"
	if list[0].Name != "Ant" || list[1].Name != "Zebra" {
		t.Errorf("tie-breaker sort = [%q, %q], want ['Ant', 'Zebra']", list[0].Name, list[1].Name)
	}
}

// TestEditLastUserMessage delegates to EditUserMessage(idx=0) and should
// edit the most recent user turn in the session history.
func TestEditLastUserMessage(t *testing.T) {
	_ = withTempHistoryDir(t)
	mc := &mockClient{responses: []mockResp{{text: "re-run reply"}}}
	p, _ := newTestProject(t, mc, nil)
	s := newStudioForTest(t)
	p.studio = s
	s.projects[p.ID] = p

	// Seed two exchanges. The last user turn is "second message".
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("first message")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("reply 1")}},
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("second message")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("reply 2")}},
	}

	// EditLastUserMessage should act on the last user turn (index 0 from end).
	// Trim is synchronous; the re-send is async (studio WaitGroup).
	if err := s.EditLastUserMessage(p.ID, "default", "edited message"); err != nil {
		t.Fatalf("EditLastUserMessage: %v", err)
	}
	// Wait for the async agent goroutine to complete.
	s.wg.Wait()

	// After trim+re-send the history should contain:
	// [user:first, model:reply1, user:edited, model:re-run reply] — exactly 4 entries.
	p.sessions["default"].mu.RLock()
	histLen := len(p.sessions["default"].history)
	p.sessions["default"].mu.RUnlock()
	if histLen != 4 {
		t.Errorf("expected 4 history entries after edit+resend, got %d", histLen)
	}

	// Unknown project should return same error as EditUserMessage.
	err := s.EditLastUserMessage("no-such", "default", "text")
	if err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}
