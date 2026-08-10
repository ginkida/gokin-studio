package studio

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func TestSideQuestionStreamsWithoutChangingTranscript(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{
		ID: "side-project", Name: "Side", Directory: t.TempDir(),
		Provider: "glm", Model: "glm-5.1",
	})
	p.studio = s
	s.projects[p.ID] = p
	session := p.sessions["default"]
	session.history = []*genai.Content{
		genai.NewContentFromText("Main question", genai.RoleUser),
		{Role: "model", Parts: []*genai.Part{
			{Text: "hidden reasoning", Thought: true, ThoughtSignature: []byte("secret")},
			genai.NewPartFromText("Main answer"),
		}},
	}

	mc := &mockClient{responses: []mockResp{{text: "Side answer", inputTokens: 120, outputTokens: 30}}}
	var capturedAllowed map[string]bool
	var capturedPrompt string
	p.testExecutionClientFactory = func(
		_ Settings, _, _, _ string, systemPrompt string, _ string,
		allowedTools map[string]bool, disablePluginAgents bool,
	) (client.Client, *tools.Registry, error) {
		if !disablePluginAgents {
			t.Fatal("side chat must disable plugin agents")
		}
		capturedAllowed = allowedTools
		capturedPrompt = systemPrompt
		return mc, tools.NewRegistry(), nil
	}

	events := make(chan struct {
		name string
		data SideChatEvent
	}, 4)
	s.testSideChatEmitter = func(name string, data SideChatEvent) {
		events <- struct {
			name string
			data SideChatEvent
		}{name, data}
	}

	if err := s.StartSideQuestion(p.ID, "default", "side_1", "What does that mean?"); err != nil {
		t.Fatalf("StartSideQuestion: %v", err)
	}
	var seenDelta, seenComplete bool
	deadline := time.After(2 * time.Second)
	for !seenComplete {
		select {
		case event := <-events:
			switch event.name {
			case EventSideChatDelta:
				seenDelta = event.data.Text == "Side answer"
			case EventSideChatComplete:
				seenComplete = true
				if event.data.Text != "Side answer" || event.data.InputTokens != 120 || event.data.OutputTokens != 30 {
					t.Fatalf("complete event = %#v", event.data)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for side-chat completion")
		}
	}
	if !seenDelta {
		t.Fatal("side-chat delta was not emitted")
	}
	if capturedAllowed == nil || len(capturedAllowed) != 0 {
		t.Fatalf("allowed tools = %#v, want a non-nil empty allowlist", capturedAllowed)
	}
	if !strings.Contains(capturedPrompt, "ephemeral side question") || !strings.Contains(capturedPrompt, "do not call tools") {
		t.Fatalf("side-chat system prompt does not enforce isolation: %q", capturedPrompt)
	}

	session.mu.RLock()
	defer session.mu.RUnlock()
	if len(session.history) != 2 {
		t.Fatalf("main transcript length = %d, want 2", len(session.history))
	}
	for _, content := range session.history {
		for _, part := range content.Parts {
			if part != nil && (strings.Contains(part.Text, "What does that mean?") || strings.Contains(part.Text, "Side answer")) {
				t.Fatalf("ephemeral content leaked into main transcript: %q", part.Text)
			}
		}
	}
	if session.usage == nil || session.usage.TurnCount != 1 || session.usage.TotalInputTokens != 120 || session.usage.TotalOutputTokens != 30 {
		t.Fatalf("side-chat usage was not recorded: %#v", session.usage)
	}
	persisted, err := LoadHistory(projectSessionStorageKey(p.ID, session.ID))
	if err != nil {
		t.Fatalf("LoadHistory after side chat: %v", err)
	}
	if len(persisted) != 2 {
		t.Fatalf("persisted main transcript length = %d, want 2", len(persisted))
	}
	for _, content := range persisted {
		for _, part := range content.Parts {
			if part != nil && (strings.Contains(part.Text, "What does that mean?") || strings.Contains(part.Text, "Side answer")) {
				t.Fatalf("ephemeral content leaked into history file: %q", part.Text)
			}
		}
	}
	if len(mc.sendHistoryCalls) != 1 || len(mc.sendHistoryCalls[0]) != 2 {
		t.Fatalf("side-chat history calls = %#v", mc.sendHistoryCalls)
	}
	for _, part := range mc.sendHistoryCalls[0][1].Parts {
		if part != nil && (part.Thought || len(part.ThoughtSignature) != 0 || strings.Contains(part.Text, "hidden reasoning")) {
			t.Fatalf("hidden reasoning leaked into side context: %#v", part)
		}
	}
}

type blockingSideClient struct {
	*mockClient
	entered chan struct{}
}

func (c *blockingSideClient) SendMessageWithHistory(ctx context.Context, _ []*genai.Content, _ string) (*client.StreamingResponse, error) {
	close(c.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSideQuestionCancelAndDuplicateGuard(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{
		ID: "side-cancel", Name: "Side", Directory: t.TempDir(),
		Provider: "glm", Model: "glm-5.1",
	})
	p.studio = s
	s.projects[p.ID] = p
	blocking := &blockingSideClient{mockClient: &mockClient{}, entered: make(chan struct{})}
	p.testExecutionClientFactory = func(
		Settings, string, string, string, string, string, map[string]bool, bool,
	) (client.Client, *tools.Registry, error) {
		return blocking, tools.NewRegistry(), nil
	}
	emitted := make(chan string, 1)
	s.testSideChatEmitter = func(name string, _ SideChatEvent) { emitted <- name }

	if err := s.StartSideQuestion(p.ID, "default", "running", "Question"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocking.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("side question did not enter provider call")
	}
	if err := s.StartSideQuestion(p.ID, "default", "second", "Another"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("duplicate side question accepted: %v", err)
	}
	if err := s.CancelSideQuestion(p.ID, "default", "running"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.sideChatMu.Lock()
		_, active := s.sideChatRuns["running"]
		s.sideChatMu.Unlock()
		if !active {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.sideChatMu.Lock()
	_, active := s.sideChatRuns["running"]
	s.sideChatMu.Unlock()
	if active {
		t.Fatal("cancelled side question remained registered")
	}
	select {
	case name := <-emitted:
		t.Fatalf("explicit cancellation emitted %q", name)
	default:
	}
}

func TestSideChatHistorySnapshotConvertsToolsAndBoundsParts(t *testing.T) {
	large := strings.Repeat("x", sideChatPartMaxBytes+100)
	history := []*genai.Content{
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("orphan")}},
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("question")}},
		{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"path": "a.go"}}},
			{Text: "private", Thought: true},
		}},
		{Role: "user", Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{
				Name: "read", Response: map[string]any{"output": large},
			}},
		}},
	}
	got := sideChatHistorySnapshot(history)
	if len(got) != 3 || got[0].Role != "user" {
		t.Fatalf("sanitized history = %#v", got)
	}
	if text := got[1].Parts[0].Text; !strings.Contains(text, "Tool call: read") || strings.Contains(text, "private") {
		t.Fatalf("tool call conversion = %q", text)
	}
	if size := len(got[2].Parts[0].Text); size > sideChatPartMaxBytes+64 {
		t.Fatalf("bounded tool result size = %d", size)
	}
}

func TestSideQuestionValidation(t *testing.T) {
	s := newStudioForTest(t)
	for _, requestID := range []string{"", "has space", "bad/slash", strings.Repeat("x", sideChatRequestIDMaxBytes+1)} {
		if err := s.StartSideQuestion("missing", "default", requestID, "Question"); err == nil {
			t.Fatalf("invalid request ID %q was accepted", requestID)
		}
	}
	if err := s.StartSideQuestion("missing", "default", "valid-id", "   "); err == nil {
		t.Fatal("blank question was accepted")
	}
}
