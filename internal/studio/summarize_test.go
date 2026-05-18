package studio

import (
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func TestSummarizeSession_EmptyProjectID(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.SummarizeSession("", ""); err == nil {
		t.Error("expected error for empty projectID")
	}
}

func TestSummarizeSession_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.SummarizeSession("ghost", ""); err == nil {
		t.Error("expected error for unknown project")
	}
}

func TestSummarizeSession_UnknownSession(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	if _, err := s.SummarizeSession(pInfo.ID, "no-such-session"); err == nil {
		t.Error("expected error for unknown session")
	}
}

func TestSummarizeSession_NoHistoryRejects(t *testing.T) {
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	pInfo := addTestProject(t, s, "P")
	_, err := s.SummarizeSession(pInfo.ID, "default")
	if err == nil {
		t.Error("expected error for empty session, got nil")
	}
	if !strings.Contains(err.Error(), "no visible history") {
		t.Errorf("expected 'no visible history' error, got %q", err.Error())
	}
}

// TestSummarizeSession_HappyPath drives the full flow with a mock LLM
// client that returns canned summary text. Verifies the call goes through
// AND that the prompt is sent with the session history snapshot.
func TestSummarizeSession_HappyPath(t *testing.T) {
	_ = withTempHistoryDir(t)
	mc := &mockClient{responses: []mockResp{{text: "- did X\n- decided Y\n- next: Z"}}}
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	s.projects[p.ID] = p
	p.studio = s

	// Seed visible history.
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("how do I X?")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("you do X by ...")}},
	}

	out, err := s.SummarizeSession(p.ID, "default")
	if err != nil {
		t.Fatalf("SummarizeSession: %v", err)
	}
	if !strings.Contains(out, "did X") {
		t.Errorf("summary missing expected content: %q", out)
	}
	// Mock client's SendMessageWithHistory should have been called once.
	if mc.callCount != 1 {
		t.Errorf("mock callCount = %d, want 1", mc.callCount)
	}
}

// TestSummarizeSession_SkipsThinkingParts verifies the snapshot strips
// thinking (model-deliberation) content before sending it to the
// summariser — secret reasoning shouldn't seed a summary.
func TestSummarizeSession_SkipsThinkingParts(t *testing.T) {
	_ = withTempHistoryDir(t)
	mc := &mockClient{responses: []mockResp{{text: "summary OK"}}}
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	s.projects[p.ID] = p
	p.studio = s

	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
		{Role: "model", Parts: []*genai.Part{
			{Text: "secretthinking", Thought: true},
			{Text: "visible answer"},
		}},
	}

	out, err := s.SummarizeSession(p.ID, "default")
	if err != nil {
		t.Fatalf("SummarizeSession: %v", err)
	}
	if out != "summary OK" {
		t.Errorf("unexpected summary: %q", out)
	}
}

// TestSummarizeSession_FunctionResponseTurnsAreSkipped verifies that a
// session whose history is ONLY function-response turns (no visible text)
// is treated as empty.
func TestSummarizeSession_FunctionResponseTurnsAreSkipped(t *testing.T) {
	_ = withTempHistoryDir(t)
	mc := &mockClient{responses: []mockResp{{text: "should not be called"}}}
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	s.projects[p.ID] = p
	p.studio = s

	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromFunctionResponse("t", map[string]any{"r": "x"})}},
	}

	if _, err := s.SummarizeSession(p.ID, "default"); err == nil {
		t.Error("expected 'no visible history' error for function-response-only history")
	}
	if mc.callCount != 0 {
		t.Errorf("LLM should not be called when no visible history; got %d calls", mc.callCount)
	}
}

// TestSummarizeSession_EmptyResponseRejected verifies the model returning
// empty text surfaces as an error rather than being silently returned.
func TestSummarizeSession_EmptyResponseRejected(t *testing.T) {
	_ = withTempHistoryDir(t)
	mc := &mockClient{responses: []mockResp{{text: "   \n  "}}} // whitespace-only
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	s.projects[p.ID] = p
	p.studio = s

	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}

	_, err := s.SummarizeSession(p.ID, "default")
	if err == nil {
		t.Error("expected error for empty model response")
	}
	if !strings.Contains(err.Error(), "empty summary") {
		t.Errorf("expected 'empty summary' error, got %q", err.Error())
	}
}

// TestSummarizeSession_EmptySessionDefaultsToDefault verifies the empty-
// sessionID convention is preserved.
func TestSummarizeSession_EmptySessionDefaultsToDefault(t *testing.T) {
	_ = withTempHistoryDir(t)
	mc := &mockClient{responses: []mockResp{{text: "ok"}}}
	p, _ := newTestProject(t, mc, tools.NewRegistry())
	s := newStudioForTest(t)
	s.projects[p.ID] = p
	p.studio = s

	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}
	if _, err := s.SummarizeSession(p.ID, ""); err != nil {
		t.Errorf("SummarizeSession with empty sid: %v", err)
	}
}
