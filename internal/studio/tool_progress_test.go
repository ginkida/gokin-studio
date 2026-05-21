package studio

import (
	"context"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

// progressTool emits a sequence of stdout chunks via the engine's
// ProgressCallback, then returns success with the joined content. Models a
// long-running bash command that streams output through the studio's
// chat:tool_progress event.
type progressTool struct {
	chunks []string
}

func (p *progressTool) Name() string        { return "progress_tool" }
func (p *progressTool) Description() string { return "emits progress chunks" }
func (p *progressTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "progress_tool"}
}
func (p *progressTool) Validate(_ map[string]any) error { return nil }
func (p *progressTool) Execute(ctx context.Context, _ map[string]any) (tools.ToolResult, error) {
	cb := tools.GetProgressCallback(ctx)
	var all strings.Builder
	for _, c := range p.chunks {
		all.WriteString(c)
		if cb != nil {
			cb(0, c) // progress=0 → partial-output path
		}
	}
	return tools.ToolResult{Content: all.String(), Success: true}, nil
}

// TestAgentLoop_ToolProgressEmitsEvents is the iter 1030+ regression guard.
// The engine layer has a ProgressCallback hook that bash uses to stream
// partial stdout. Before this iteration studio didn't wire it through, so
// long-running bash showed only a spinner with no live progress. Now the
// agent loop installs a callback that emits a chat:tool_progress event per
// partial chunk. This test verifies each partial chunk fans out to an
// event with the correct ProjectID/SessionID/Tool/Text fields.
func TestAgentLoop_ToolProgressEmitsEvents(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{ID: "1", Name: "progress_tool", Args: map[string]any{}}}},
		{text: "done"},
	}}
	reg := tools.NewRegistry()
	reg.Register(&progressTool{chunks: []string{"first\n", "second\n", "third\n"}})
	p, rec := newTestProject(t, mc, reg)

	runAgent(p, "go")

	progressEvents := rec.find(EventChatToolProgress)
	if len(progressEvents) != 3 {
		t.Fatalf("expected 3 chat:tool_progress events, got %d", len(progressEvents))
	}
	want := []string{"first\n", "second\n", "third\n"}
	for i, ev := range progressEvents {
		data, ok := ev.data.(ChatToolProgressEvent)
		if !ok {
			t.Fatalf("progress event %d wrong type: %T", i, ev.data)
		}
		if data.Text != want[i] {
			t.Errorf("progress[%d] text = %q, want %q", i, data.Text, want[i])
		}
		if data.Tool != "progress_tool" {
			t.Errorf("progress[%d] tool = %q, want progress_tool", i, data.Tool)
		}
		if data.ProjectID != p.ID {
			t.Errorf("progress[%d] projectID = %q, want %q", i, data.ProjectID, p.ID)
		}
	}
	// Sanity: the result event MUST still fire (progress events don't
	// replace the authoritative result).
	if results := rec.find(EventChatToolResult); len(results) != 1 {
		t.Errorf("expected exactly 1 chat:tool_result, got %d", len(results))
	}
}

// TestAgentLoop_ToolProgressIgnoresCounterUpdates: the engine's
// ProgressCallback is also called with progress=-1 to communicate a "still
// running, N KB output" counter. Studio's elapsed-time chip already conveys
// that signal, and emitting counter events would flood the bus. Verifies
// we skip those without errors.
type counterOnlyTool struct{}

func (c *counterOnlyTool) Name() string        { return "counter_tool" }
func (c *counterOnlyTool) Description() string { return "emits only counter pings" }
func (c *counterOnlyTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "counter_tool"}
}
func (c *counterOnlyTool) Validate(_ map[string]any) error { return nil }
func (c *counterOnlyTool) Execute(ctx context.Context, _ map[string]any) (tools.ToolResult, error) {
	cb := tools.GetProgressCallback(ctx)
	if cb != nil {
		cb(-1, "Output: 4 KB")
		cb(-1, "Output: 8 KB")
		cb(0, "") // empty step — should also be skipped
	}
	return tools.ToolResult{Content: "done", Success: true}, nil
}

func TestAgentLoop_ToolProgressIgnoresCounterUpdates(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{ID: "1", Name: "counter_tool", Args: map[string]any{}}}},
		{text: "ok"},
	}}
	reg := tools.NewRegistry()
	reg.Register(&counterOnlyTool{})
	p, rec := newTestProject(t, mc, reg)

	runAgent(p, "go")

	if got := rec.find(EventChatToolProgress); len(got) != 0 {
		t.Errorf("expected 0 progress events from counter-only tool, got %d", len(got))
	}
}
