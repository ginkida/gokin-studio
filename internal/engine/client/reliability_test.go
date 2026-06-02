package client

import (
	"context"
	"testing"
)

// TestProcessStreamEvent_AttachesToolCallsOnNonToolUseStopReason guards the
// iter-1070+ fix: Anthropic-compatible shims (GLM/MiniMax/Kimi/DeepSeek)
// sometimes stream tool_use content blocks but report stop_reason "end_turn"
// (or "stop") instead of "tool_use". Because the SSE loop returns on the first
// Done chunk, the message_stop fallback is unreachable — so the accumulated
// tool calls must be attached on ANY terminating stop_reason, else the agent
// silently stops mid-task with no tool execution.
func TestProcessStreamEvent_AttachesToolCallsOnNonToolUseStopReason(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}

	// A tool_use content block: start → input delta → stop (accumulates the call).
	c.processStreamEvent(map[string]interface{}{
		"type": "content_block_start",
		"content_block": map[string]interface{}{
			"type": "tool_use",
			"name": "echo",
			"id":   "toolu_1",
		},
	}, acc)
	c.processStreamEvent(map[string]interface{}{
		"type": "content_block_delta",
		"delta": map[string]interface{}{
			"type":         "input_json_delta",
			"partial_json": `{"input":"hi"}`,
		},
	}, acc)
	c.processStreamEvent(map[string]interface{}{"type": "content_block_stop"}, acc)

	if len(acc.completedCalls) != 1 {
		t.Fatalf("expected 1 accumulated tool call, got %d", len(acc.completedCalls))
	}

	// message_delta reporting stop_reason "end_turn" (NOT "tool_use") — the bug
	// case. The returned chunk must still carry the tool calls.
	chunk := c.processStreamEvent(map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": "end_turn"},
	}, acc)

	if !chunk.Done {
		t.Error("expected Done=true on message_delta with stop_reason")
	}
	if len(chunk.FunctionCalls) != 1 {
		t.Fatalf("tool calls dropped on stop_reason=end_turn: got %d, want 1", len(chunk.FunctionCalls))
	}
	if chunk.FunctionCalls[0].Name != "echo" {
		t.Errorf("wrong tool name: %q", chunk.FunctionCalls[0].Name)
	}
	if !acc.callsEmitted {
		t.Error("expected callsEmitted=true to suppress a duplicate emit at message_stop")
	}

	// message_stop now must NOT re-emit (idempotency — duplicate tool_use blocks
	// are rejected by MiniMax with 400).
	stopChunk := c.processStreamEvent(map[string]interface{}{"type": "message_stop"}, acc)
	if len(stopChunk.FunctionCalls) != 0 {
		t.Errorf("message_stop re-emitted tool calls after message_delta already did: got %d", len(stopChunk.FunctionCalls))
	}
}

// TestProcessStream_AccumulatesCacheCreationTokens guards the iter-1070+ fix:
// ProcessStream (the path studio uses) dropped chunk.CacheCreationInputTokens,
// so cache-WRITE cost was always 0 — undercounting cost and weakening strict
// budget enforcement. It must mirror Collect() and carry both cache fields.
func TestProcessStream_AccumulatesCacheCreationTokens(t *testing.T) {
	ch := make(chan ResponseChunk, 4)
	ch <- ResponseChunk{Text: "hi"}
	ch <- ResponseChunk{
		Done:                     true,
		InputTokens:              100,
		OutputTokens:             20,
		CacheReadInputTokens:     50,
		CacheCreationInputTokens: 30,
	}
	close(ch)

	resp, err := ProcessStream(context.Background(), &StreamingResponse{Chunks: ch}, &StreamHandler{})
	if err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if resp.CacheCreationInputTokens != 30 {
		t.Errorf("cache-creation tokens dropped: got %d, want 30", resp.CacheCreationInputTokens)
	}
	if resp.CacheReadInputTokens != 50 {
		t.Errorf("cache-read tokens: got %d, want 50", resp.CacheReadInputTokens)
	}
}
