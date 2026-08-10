package client

import (
	"encoding/base64"
	"testing"

	"google.golang.org/genai"
)

func TestKimiK3ReasoningEffortMapping(t *testing.T) {
	for _, tc := range []struct {
		budget int32
		want   string
	}{
		{1, "low"},
		{4096, "low"},
		{8192, "high"},
		{16384, "high"},
		{32768, "max"},
	} {
		if got := kimiK3ReasoningEffort(tc.budget); got != tc.want {
			t.Errorf("kimiK3ReasoningEffort(%d) = %q, want %q", tc.budget, got, tc.want)
		}
	}
}

func TestApplyAnthropicThinking_KimiK3UsesReasoningEffort(t *testing.T) {
	body := map[string]interface{}{}
	applyAnthropicThinking(body, "kimi", "k3", true, 8192, 32768, 0)
	if body["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", body["reasoning_effort"])
	}
	if _, exists := body["thinking"]; exists {
		t.Fatalf("K3 request unexpectedly contains token-budget thinking: %#v", body["thinking"])
	}
}

func TestApplyAnthropicThinking_FutureKimiUsesReasoningEffort(t *testing.T) {
	body := map[string]interface{}{}
	applyAnthropicThinking(body, "kimi", "k4", true, 32768, 131072, 0)
	if body["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %#v, want max", body["reasoning_effort"])
	}
	if _, exists := body["thinking"]; exists {
		t.Fatalf("future K family unexpectedly contains token-budget thinking: %#v", body)
	}
}

func TestApplyAnthropicThinking_KimiK3OffStaysOnK3AtLowEffort(t *testing.T) {
	body := map[string]interface{}{}
	applyAnthropicThinking(body, "kimi", "k3-256k", false, 0, 32768, 0)
	if body["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort = %#v, want low", body["reasoning_effort"])
	}
	if _, exists := body["thinking"]; exists {
		t.Fatalf("K3 request unexpectedly contains a thinking block: %#v", body["thinking"])
	}
}

func TestApplyAnthropicThinking_KimiCodingKeepsBudgetProtocol(t *testing.T) {
	body := map[string]interface{}{}
	applyAnthropicThinking(body, "kimi", "kimi-for-coding", true, 8192, 32768, 0)
	thinking, ok := body["thinking"].(map[string]interface{})
	if !ok || thinking["type"] != "enabled" || thinking["budget_tokens"] != int32(8192) {
		t.Fatalf("thinking = %#v, want enabled token-budget block", body["thinking"])
	}
	if _, exists := body["reasoning_effort"]; exists {
		t.Fatal("K2.7 request unexpectedly contains K3 reasoning_effort")
	}
}

func TestBuildUserMessage_StandaloneImageBlock(t *testing.T) {
	client := &AnthropicClient{}
	msg := client.buildUserMessage([]*genai.Part{
		genai.NewPartFromText("inspect"),
		{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte{1, 2, 3}}},
	})
	content, ok := msg["content"].([]map[string]interface{})
	if !ok || len(content) != 2 {
		t.Fatalf("content = %#v", msg["content"])
	}
	if content[1]["type"] != "image" {
		t.Fatalf("image block = %#v", content[1])
	}
	source, ok := content[1]["source"].(map[string]interface{})
	if !ok || source["media_type"] != "image/png" || source["data"] != base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) {
		t.Fatalf("image source = %#v", content[1]["source"])
	}
}

func TestConvertHistoryPreservesImageInsideToolResult(t *testing.T) {
	client := &AnthropicClient{}
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "screen-call", Name: "computer_screenshot",
		}}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{
				ID: "screen-call", Name: "computer_screenshot", Response: map[string]any{"result": "captured"},
			}},
			{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte{4, 5, 6}}},
		}},
	}
	messages := client.convertHistoryWithResults(history, nil)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	content, ok := messages[1]["content"].([]map[string]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("tool result content = %#v", messages[1]["content"])
	}
	blocks, ok := content[0]["content"].([]map[string]interface{})
	if !ok || len(blocks) != 2 || blocks[1]["type"] != "image" {
		t.Fatalf("multimodal tool_result = %#v", content[0])
	}
	source := blocks[1]["source"].(map[string]interface{})
	if source["data"] != base64.StdEncoding.EncodeToString([]byte{4, 5, 6}) {
		t.Fatalf("image source = %#v", source)
	}
}
