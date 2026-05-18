package studio

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestExportProjectAllSessions_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ExportProjectAllSessions("ghost"); err == nil {
		t.Error("expected error for unknown project")
	}
}

// TestExportProjectAllSessions_NoVisibleHistory verifies that when no
// session has any text turns, the export still returns the header + a
// "No sessions" footnote rather than failing.
func TestExportProjectAllSessions_NoVisibleHistory(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	out, err := s.ExportProjectAllSessions(pInfo.ID)
	if err != nil {
		t.Fatalf("ExportProjectAllSessions: %v", err)
	}
	if !strings.Contains(out, "P") {
		t.Errorf("missing project name in header: %s", out)
	}
	if !strings.Contains(out, "No sessions with visible history") {
		t.Errorf("expected 'No sessions' footnote, got %q", out)
	}
}

// TestExportProjectAllSessions_IncludesAllSessions verifies that two
// sessions with content both appear in the output, ordered most-recently-
// used first.
func TestExportProjectAllSessions_IncludesAllSessions(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "MyProj")
	p := projectFromInfo(t, s, pInfo)

	// Session A — older.
	p.sessions["default"].Name = "Session A"
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("question A")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("answer A")}},
	}
	p.sessions["default"].lastUsedAt = time.Now().Add(-2 * time.Hour).UnixMilli()

	sessB, err := s.CreateChatSession(pInfo.ID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	p.sessions[sessB.ID].Name = "Session B"
	p.sessions[sessB.ID].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("question B")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("answer B")}},
	}
	p.sessions[sessB.ID].lastUsedAt = time.Now().UnixMilli() // most recent

	out, err := s.ExportProjectAllSessions(pInfo.ID)
	if err != nil {
		t.Fatalf("ExportProjectAllSessions: %v", err)
	}

	// Both sessions appear.
	if !strings.Contains(out, "Session A") {
		t.Error("Session A missing from export")
	}
	if !strings.Contains(out, "Session B") {
		t.Error("Session B missing from export")
	}
	if !strings.Contains(out, "question A") || !strings.Contains(out, "answer A") {
		t.Error("Session A history missing")
	}
	if !strings.Contains(out, "question B") || !strings.Contains(out, "answer B") {
		t.Error("Session B history missing")
	}

	// Session B (most recent) should appear before Session A in the output.
	idxA := strings.Index(out, "## Session A")
	idxB := strings.Index(out, "## Session B")
	if idxB < 0 || idxA < 0 {
		t.Fatalf("section headers missing: A=%d B=%d", idxA, idxB)
	}
	if idxB > idxA {
		t.Errorf("expected Session B before Session A (most-recent-first), got A=%d B=%d", idxA, idxB)
	}
}

// TestExportProjectAllSessions_SkipsEmptySessions verifies that sessions
// with no text turns (only tool calls / function responses) are omitted
// rather than appearing as empty sections.
func TestExportProjectAllSessions_SkipsEmptySessions(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, pInfo)

	// Default has real content.
	p.sessions["default"].Name = "Real"
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}

	// New session has only function-response turns (no visible text).
	sessEmpty, _ := s.CreateChatSession(pInfo.ID)
	p.sessions[sessEmpty.ID].Name = "EmptyOne"
	p.sessions[sessEmpty.ID].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromFunctionResponse("t", map[string]any{"r": "x"})}},
	}

	out, _ := s.ExportProjectAllSessions(pInfo.ID)
	if !strings.Contains(out, "## Real") {
		t.Error("Real session missing from export")
	}
	if strings.Contains(out, "## EmptyOne") {
		t.Error("EmptyOne should be skipped (no visible text)")
	}
}

// TestExportProjectAllSessions_FiltersThinkingParts verifies thinking
// (model-deliberation) parts are excluded — same as ExportChat.
func TestExportProjectAllSessions_FiltersThinkingParts(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	p := projectFromInfo(t, s, pInfo)
	p.sessions["default"].history = []*genai.Content{
		{Role: "model", Parts: []*genai.Part{
			{Text: "secretthinking", Thought: true},
			{Text: "visible answer"},
		}},
	}
	out, _ := s.ExportProjectAllSessions(pInfo.ID)
	if strings.Contains(out, "secretthinking") {
		t.Error("thinking part leaked into export")
	}
	if !strings.Contains(out, "visible answer") {
		t.Error("visible answer missing")
	}
}

// TestExportProjectAllSessions_SinglePluralization verifies "1 session"
// vs "N sessions" in the header (no extra "s" for the singular).
func TestExportProjectAllSessions_SinglePluralization(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	out, _ := s.ExportProjectAllSessions(pInfo.ID)
	if !strings.Contains(out, "1 session_") {
		t.Errorf("expected '1 session' (no plural s), got: %q",
			firstLineContaining(out, "Exported"))
	}
}

// firstLineContaining returns the first line of s containing substr, or
// the empty string if none. Tiny test helper to keep error messages tight.
func firstLineContaining(s, substr string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

// TestPlural2_PluralForm covers the three branches of plural2.
func TestPlural2(t *testing.T) {
	if got := plural2(1, ""); got != "" {
		t.Errorf("plural2(1) = %q, want \"\"", got)
	}
	if got := plural2(0, ""); got != "s" {
		t.Errorf("plural2(0) = %q, want \"s\"", got)
	}
	if got := plural2(5, ""); got != "s" {
		t.Errorf("plural2(5) = %q, want \"s\"", got)
	}
}
