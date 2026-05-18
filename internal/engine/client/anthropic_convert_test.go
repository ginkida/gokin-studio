package client

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

// Regression guard for the "result-vs-content key" bug: studio's agent loop
// emits FunctionResponse{Response: {"result": "<actual tool output>"}},
// but earlier versions of buildUserMessage only read resp["content"], so
// strict providers (Kimi) saw "Operation completed" for every tool call.
func TestBuildUserMessage_ToolResultAcceptsResultKey(t *testing.T) {
	c := &AnthropicClient{}

	parts := []*genai.Part{
		{
			FunctionResponse: &genai.FunctionResponse{
				ID:       "toolu_abc123",
				Name:     "bash",
				Response: map[string]any{"result": "total 42\nfile1\nfile2"},
			},
		},
	}

	msg := c.buildUserMessage(parts)
	content, ok := msg["content"].([]map[string]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("expected non-empty content array, got %#v", msg["content"])
	}
	block := content[0]
	if block["type"] != "tool_result" {
		t.Fatalf("expected tool_result block, got %v", block["type"])
	}
	if block["tool_use_id"] != "toolu_abc123" {
		t.Errorf("expected tool_use_id=toolu_abc123, got %v", block["tool_use_id"])
	}
	got, _ := block["content"].(string)
	if got != "total 42\nfile1\nfile2" {
		t.Errorf("expected real tool output, got %q", got)
	}
	if strings.Contains(got, "Operation completed") {
		t.Errorf("tool output replaced with fallback placeholder: %q", got)
	}
}

// "content" key still works as a fallback for agent-framework callers.
func TestBuildUserMessage_ToolResultAcceptsContentKey(t *testing.T) {
	c := &AnthropicClient{}

	parts := []*genai.Part{
		{
			FunctionResponse: &genai.FunctionResponse{
				ID:       "toolu_abc",
				Name:     "read",
				Response: map[string]any{"content": "file contents here"},
			},
		},
	}

	msg := c.buildUserMessage(parts)
	content := msg["content"].([]map[string]interface{})
	if got, _ := content[0]["content"].(string); got != "file contents here" {
		t.Errorf("expected 'file contents here', got %q", got)
	}
}

// Error responses should surface the error text rather than "Operation completed".
func TestBuildUserMessage_ToolResultErrorKey(t *testing.T) {
	c := &AnthropicClient{}

	parts := []*genai.Part{
		{
			FunctionResponse: &genai.FunctionResponse{
				ID:       "toolu_err",
				Name:     "bash",
				Response: map[string]any{"error": "permission denied"},
			},
		},
	}

	msg := c.buildUserMessage(parts)
	content := msg["content"].([]map[string]interface{})
	got, _ := content[0]["content"].(string)
	if !strings.Contains(got, "permission denied") {
		t.Errorf("expected error text in content, got %q", got)
	}
}

// Regression guard for the "tool_use without thinking" Kimi bug: when
// extended thinking is enabled, every assistant message that carries a
// tool_use block MUST include a thinking block too. Legacy history from
// before this change has no Thought part preserved — the client injects
// a stub so round-trips stay valid.
func TestBuildAssistantMessage_InjectsStubThinkingForLegacyToolUse(t *testing.T) {
	c := &AnthropicClient{
		config: AnthropicConfig{
			EnableThinking: true,
			ThinkingBudget: 4096,
		},
	}

	parts := []*genai.Part{
		{Text: "I'll list the files."},
		{FunctionCall: &genai.FunctionCall{
			ID:   "toolu_legacy",
			Name: "list_dir",
			Args: map[string]any{"path": "."},
		}},
	}

	msg := c.buildAssistantMessage(parts)
	blocks := msg["content"].([]map[string]interface{})

	// Expect thinking stub FIRST (Kimi requires reasoning_content before tool_use).
	if len(blocks) < 3 {
		t.Fatalf("expected 3 blocks (thinking, text, tool_use), got %d: %#v", len(blocks), blocks)
	}
	if blocks[0]["type"] != "thinking" {
		t.Errorf("expected first block to be thinking stub, got %v", blocks[0]["type"])
	}
}

// When Thought parts ARE preserved, buildAssistantMessage should emit the
// real thinking block with its signature — not a stub.
func TestBuildAssistantMessage_PreservesThinkingWithSignature(t *testing.T) {
	c := &AnthropicClient{
		config: AnthropicConfig{
			EnableThinking: true,
			ThinkingBudget: 4096,
		},
	}

	parts := []*genai.Part{
		{Text: "Let me think about this.", Thought: true, ThoughtSignature: []byte("sig_xyz")},
		{FunctionCall: &genai.FunctionCall{
			ID:   "toolu_real",
			Name: "bash",
			Args: map[string]any{"command": "ls"},
		}},
	}

	msg := c.buildAssistantMessage(parts)
	blocks := msg["content"].([]map[string]interface{})

	if len(blocks) != 2 {
		t.Fatalf("expected exactly 2 blocks (thinking, tool_use), got %d: %#v", len(blocks), blocks)
	}
	thinking := blocks[0]
	if thinking["type"] != "thinking" {
		t.Errorf("expected thinking block, got %v", thinking["type"])
	}
	if thinking["thinking"] != "Let me think about this." {
		t.Errorf("thinking text not preserved: got %q", thinking["thinking"])
	}
	if thinking["signature"] != "sig_xyz" {
		t.Errorf("thinking signature not preserved: got %q", thinking["signature"])
	}
}

// Without thinking enabled, no stub should be injected (keeps GLM/other
// providers from seeing unwanted reasoning_content blocks).
func TestBuildAssistantMessage_NoStubWhenThinkingDisabled(t *testing.T) {
	c := &AnthropicClient{
		config: AnthropicConfig{
			EnableThinking: false,
		},
	}

	parts := []*genai.Part{
		{FunctionCall: &genai.FunctionCall{
			ID:   "toolu_x",
			Name: "bash",
			Args: map[string]any{"command": "pwd"},
		}},
	}

	msg := c.buildAssistantMessage(parts)
	blocks := msg["content"].([]map[string]interface{})

	for _, b := range blocks {
		if b["type"] == "thinking" {
			t.Errorf("thinking block injected when thinking was disabled: %#v", b)
		}
	}
}

// Regression guard for the tool_use_id mismatch bug. fc.ID must propagate
// to the assistant message's tool_use block as `id`, so the paired
// tool_result (which uses the same ID) lines up and Kimi doesn't reject
// with "tool_call_id is not found".
func TestBuildAssistantMessage_UsesFunctionCallID(t *testing.T) {
	c := &AnthropicClient{}

	parts := []*genai.Part{
		{FunctionCall: &genai.FunctionCall{
			ID:   "toolu_kimi_XYZ",
			Name: "bash",
			Args: map[string]any{"command": "ls"},
		}},
	}

	msg := c.buildAssistantMessage(parts)
	blocks := msg["content"].([]map[string]interface{})
	var toolUse map[string]interface{}
	for _, b := range blocks {
		if b["type"] == "tool_use" {
			toolUse = b
			break
		}
	}
	if toolUse == nil {
		t.Fatalf("no tool_use block emitted: %#v", blocks)
	}
	if toolUse["id"] != "toolu_kimi_XYZ" {
		t.Errorf("expected tool_use id=toolu_kimi_XYZ, got %v", toolUse["id"])
	}
	if strings.HasPrefix(toolUse["id"].(string), "fallback_") {
		t.Errorf("fallback id generated despite fc.ID being set: %v", toolUse["id"])
	}
}
