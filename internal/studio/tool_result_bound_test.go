package studio

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/config"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

type oversizedResultTool struct{}

func (*oversizedResultTool) Name() string        { return "oversized_result" }
func (*oversizedResultTool) Description() string { return "returns a large result" }
func (*oversizedResultTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "oversized_result"}
}
func (*oversizedResultTool) Validate(map[string]any) error { return nil }
func (*oversizedResultTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	return tools.NewSuccessResult(strings.Repeat("🙂", config.DefaultToolResultMaxChars+5000)), nil
}

func TestStudioBoundsToolResultBeforeUIAndModelHistory(t *testing.T) {
	reg := tools.NewRegistry()
	if err := reg.Register(&oversizedResultTool{}); err != nil {
		t.Fatal(err)
	}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: "oversized_result"}}},
		{text: "done"},
	}}
	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "get large output")

	events := rec.find(EventChatToolResult)
	if len(events) != 1 {
		t.Fatalf("tool result events = %d, want 1", len(events))
	}
	content := events[0].data.(ChatToolResultEvent).Content
	if !utf8.ValidString(content) || !strings.Contains(content, "OUTPUT TRUNCATED") || len([]rune(content)) > config.DefaultToolResultMaxChars+300 {
		t.Fatalf("UI result not safely bounded: runes=%d valid=%v", len([]rune(content)), utf8.ValidString(content))
	}

	mc.mu.Lock()
	responses := mc.lastFuncRespResults
	mc.mu.Unlock()
	if len(responses) != 1 {
		t.Fatalf("function responses = %d, want 1", len(responses))
	}
	modelContent, _ := responses[0].Response["result"].(string)
	if modelContent != content {
		t.Fatal("UI and model received different bounded tool results")
	}
}
