package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProcessStreamEvent_SignatureDeltaGuardedOnThinkingBlock is the regression
// for the audit's signature-leak finding. A signature_delta arriving while the
// current block is NOT a thinking block (malformed / out-of-order stream) must
// be IGNORED, so it can't leak a stale ThoughtSignature into the next thinking
// block — which a strict provider (GLM/Kimi, both signed-thinking on replay)
// rejects with a 400.
func TestProcessStreamEvent_SignatureDeltaGuardedOnThinkingBlock(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}

	// A text block is active, and a stray signature_delta arrives — must be dropped.
	c.processStreamEvent(map[string]interface{}{
		"type":          "content_block_start",
		"content_block": map[string]interface{}{"type": "text"},
	}, acc)
	c.processStreamEvent(map[string]interface{}{
		"type":  "content_block_delta",
		"delta": map[string]interface{}{"type": "signature_delta", "signature": "STALE-SIG"},
	}, acc)
	c.processStreamEvent(map[string]interface{}{"type": "content_block_stop"}, acc)

	// Now a legitimate thinking block with its own signature.
	c.processStreamEvent(map[string]interface{}{
		"type":          "content_block_start",
		"content_block": map[string]interface{}{"type": "thinking"},
	}, acc)
	c.processStreamEvent(map[string]interface{}{
		"type":  "content_block_delta",
		"delta": map[string]interface{}{"type": "thinking_delta", "thinking": "reasoning"},
	}, acc)
	c.processStreamEvent(map[string]interface{}{
		"type":  "content_block_delta",
		"delta": map[string]interface{}{"type": "signature_delta", "signature": "REAL-SIG"},
	}, acc)
	chunk := c.processStreamEvent(map[string]interface{}{"type": "content_block_stop"}, acc)

	// The emitted thinking Part must carry ONLY the real signature, never the stale one.
	if len(chunk.Parts) != 1 {
		t.Fatalf("expected 1 thinking part, got %d", len(chunk.Parts))
	}
	gotSig := string(chunk.Parts[0].ThoughtSignature)
	if gotSig != "REAL-SIG" {
		t.Errorf("ThoughtSignature = %q, want %q (stale signature leaked)", gotSig, "REAL-SIG")
	}
	if strings.Contains(gotSig, "STALE") {
		t.Errorf("stale signature leaked into thinking block: %q", gotSig)
	}
}

// TestProcessStreamEvent_SignatureResetBetweenThinkingBlocks verifies the
// content_block_start reset: two thinking blocks where only the first carries a
// signature must NOT bleed the first signature into the second.
func TestProcessStreamEvent_SignatureResetBetweenThinkingBlocks(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}

	// First thinking block WITH a signature.
	c.processStreamEvent(map[string]interface{}{
		"type":          "content_block_start",
		"content_block": map[string]interface{}{"type": "thinking"},
	}, acc)
	c.processStreamEvent(map[string]interface{}{
		"type":  "content_block_delta",
		"delta": map[string]interface{}{"type": "thinking_delta", "thinking": "first"},
	}, acc)
	c.processStreamEvent(map[string]interface{}{
		"type":  "content_block_delta",
		"delta": map[string]interface{}{"type": "signature_delta", "signature": "SIG-1"},
	}, acc)
	c.processStreamEvent(map[string]interface{}{"type": "content_block_stop"}, acc)

	// Second thinking block WITHOUT a signature.
	c.processStreamEvent(map[string]interface{}{
		"type":          "content_block_start",
		"content_block": map[string]interface{}{"type": "thinking"},
	}, acc)
	c.processStreamEvent(map[string]interface{}{
		"type":  "content_block_delta",
		"delta": map[string]interface{}{"type": "thinking_delta", "thinking": "second"},
	}, acc)
	chunk := c.processStreamEvent(map[string]interface{}{"type": "content_block_stop"}, acc)

	if len(chunk.Parts) != 1 {
		t.Fatalf("expected 1 thinking part, got %d", len(chunk.Parts))
	}
	if sig := string(chunk.Parts[0].ThoughtSignature); sig != "" {
		t.Errorf("second thinking block should have no signature, got %q (leaked from block 1)", sig)
	}
}

// TestProcessStreamEvent_MessageStopFlushesThinkTagParser verifies that a
// partial inline-tag fragment buffered at end of stream is flushed at
// message_stop rather than silently dropped. The ThinkTagParser buffers a
// partial "<thi"/"</thi" straddling a chunk boundary, waiting for the rest of
// the tag; if the stream ends there, those bytes live only in tagBuffer. The
// SSE loop returns on the first Done chunk, so without the message_stop flush
// they are lost (truncating the model's output for inline-<think> providers).
func TestProcessStreamEvent_MessageStopFlushesThinkTagParser(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}

	c.processStreamEvent(map[string]interface{}{
		"type":          "content_block_start",
		"content_block": map[string]interface{}{"type": "text"},
	}, acc)
	// Text ends with "<thi" — an incomplete <think> tag. The parser emits
	// "result" now and buffers "<thi" (it can't yet tell tag from literal text).
	deltaChunk := c.processStreamEvent(map[string]interface{}{
		"type":  "content_block_delta",
		"delta": map[string]interface{}{"type": "text_delta", "text": "result<thi"},
	}, acc)
	if deltaChunk.Text != "result" {
		t.Fatalf("pre-flush emitted text = %q, want %q (the rest is buffered)", deltaChunk.Text, "result")
	}

	// The stream ends here. message_stop must flush the buffered "<thi" fragment
	// into the chunk instead of dropping it.
	chunk := c.processStreamEvent(map[string]interface{}{"type": "message_stop"}, acc)
	if !chunk.Done {
		t.Error("message_stop chunk must be Done")
	}
	if !strings.Contains(chunk.Text, "<thi") {
		t.Errorf("flushed text missing buffered fragment; got Text=%q (would be dropped without the flush)", chunk.Text)
	}
}

// TestErrorBodyReadIsBounded verifies the doStreamRequest error path caps the
// response body it reads — a hostile/oversized gateway error page (common on
// GLM/Z.AI 5xx) must not be read unbounded into memory. We send a >64KB error
// body and assert the surfaced message is bounded near the 64KB cap.
func TestErrorBodyReadIsBounded(t *testing.T) {
	huge := strings.Repeat("A", 256<<10) // 256KB error page
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 400 is non-retryable (no "model_not_found"), so the request returns
		// immediately without the retry loop — keeps the test fast + deterministic.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(huge))
	}))
	defer srv.Close()

	c, err := NewAnthropicClient(AnthropicConfig{
		APIKey:     "test-key-1234567890",
		BaseURL:    srv.URL,
		Model:      "glm-5.2",
		MaxRetries: 0,
	})
	if err != nil {
		t.Fatalf("NewAnthropicClient: %v", err)
	}

	_, sErr := c.streamRequest(context.Background(), map[string]interface{}{
		"model":    "glm-5.2",
		"messages": []map[string]interface{}{{"role": "user", "content": "hi"}},
	})
	if sErr == nil {
		t.Fatal("expected an error for a 502 response")
	}
	// The error message embeds the (capped) body. With the 64KB LimitReader the
	// surfaced body can't be the full 256KB; allow generous slack for the wrapper.
	if len(sErr.Error()) > 128<<10 {
		t.Errorf("error message length %d suggests the body was read unbounded (cap is 64KB)", len(sErr.Error()))
	}
}
