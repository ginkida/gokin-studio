package client

import (
	"testing"

	"google.golang.org/genai"
)

// Additional regression guards for buildAssistantMessage covering branches the
// existing anthropic_convert_test.go cases (InjectsStubThinkingForLegacyToolUse,
// PreservesThinkingWithSignature, NoStubWhenThinkingDisabled, UsesFunctionCallID)
// do NOT exercise: the no-double-placeholder path, tool_use input structure, the
// missing-ID fallback, a plain text block, and the empty-content fallback. This
// assistant→wire serialisation has a history of provider 400s, so the branches
// matter.

func amContent(t *testing.T, msg map[string]interface{}) []map[string]interface{} {
	t.Helper()
	c, ok := msg["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("content is not []map[string]interface{}: %T", msg["content"])
	}
	return c
}

// When a real (signed) thinking part is present alongside tool_use, the legacy
// placeholder must NOT also be injected — exactly one thinking block.
func TestBuildAssistantMessage_NoDoublePlaceholderWhenThinkingPresent(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{EnableThinking: true, ThinkingBudget: 4096}}
	parts := []*genai.Part{
		{Thought: true, Text: "real reasoning", ThoughtSignature: []byte("sig")},
		{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "read"}},
	}
	content := amContent(t, c.buildAssistantMessage(parts))
	thinkingCount := 0
	for _, b := range content {
		if b["type"] == "thinking" {
			thinkingCount++
		}
	}
	if thinkingCount != 1 {
		t.Errorf("want exactly 1 thinking block (no placeholder when real thinking present); got %d: %+v", thinkingCount, content)
	}
}

func TestBuildAssistantMessage_ToolUseStructure(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{}}
	args := map[string]any{"file_path": "main.go"}
	parts := []*genai.Part{
		{FunctionCall: &genai.FunctionCall{ID: "call_xyz", Name: "read", Args: args}},
	}
	content := amContent(t, c.buildAssistantMessage(parts))
	tu := content[len(content)-1]
	if tu["type"] != "tool_use" || tu["id"] != "call_xyz" || tu["name"] != "read" {
		t.Errorf("tool_use block wrong: %+v", tu)
	}
	in, ok := tu["input"].(map[string]any)
	if !ok || in["file_path"] != "main.go" {
		t.Errorf("tool_use input wrong: %+v", tu["input"])
	}
}

func TestBuildAssistantMessage_MissingIDGetsFallback(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{}}
	parts := []*genai.Part{
		{FunctionCall: &genai.FunctionCall{Name: "read"}}, // no ID
	}
	content := amContent(t, c.buildAssistantMessage(parts))
	tu := content[len(content)-1]
	if tu["type"] != "tool_use" {
		t.Fatalf("want tool_use, got %+v", tu)
	}
	if id, _ := tu["id"].(string); id == "" {
		t.Error("missing FunctionCall ID should get a non-empty fallback tool_use id")
	}
}

func TestBuildAssistantMessage_TextBlock(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{}}
	parts := []*genai.Part{{Text: "Here is the answer."}}
	content := amContent(t, c.buildAssistantMessage(parts))
	if len(content) != 1 || content[0]["type"] != "text" || content[0]["text"] != "Here is the answer." {
		t.Errorf("text block wrong: %+v", content)
	}
}

func TestBuildAssistantMessage_EmptyContentFallback(t *testing.T) {
	// Anthropic/DeepSeek reject an empty content array — buildAssistantMessage
	// must emit a placeholder text block when nothing serialisable is present.
	c := &AnthropicClient{config: AnthropicConfig{}}
	content := amContent(t, c.buildAssistantMessage(nil))
	if len(content) != 1 || content[0]["type"] != "text" {
		t.Errorf("empty parts should yield a placeholder text block; got %+v", content)
	}
}
