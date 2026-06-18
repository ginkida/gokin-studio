package studio

import "testing"

// TestAgentLoop_RetriesEmptyResponse verifies the iter-1130 port: a transient
// empty model response (empty 200 — no text/tools/thinking) is retried rather
// than ending the turn silently, and the recovered text reaches the UI.
func TestAgentLoop_RetriesEmptyResponse(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{},                     // empty 200 → should be retried
		{text: "recovered ok"}, // retry succeeds
	}}
	p, rec := newTestProject(t, mc, nil)

	runAgent(p, "hello")

	if mc.callCount < 2 {
		t.Errorf("expected the empty response to be retried (callCount >= 2), got %d", mc.callCount)
	}
	texts := rec.find(EventChatText)
	if len(texts) != 1 || texts[0].data.(ChatTextEvent).Text != "recovered ok" {
		t.Errorf("recovered text not emitted; got %v", texts)
	}
	if len(rec.find(EventChatRetry)) == 0 {
		t.Error("expected a chat:retry event for the empty response")
	}
	if errs := rec.find(EventChatError); len(errs) != 0 {
		t.Errorf("should not surface a chat:error on recovery; got %v", errs)
	}
}

// TestAgentLoop_EmptyResponseExhaustedStillCompletes verifies that when every
// attempt returns empty, the turn still completes (the empty response is
// returned after exhausting retries) rather than erroring out — preserving the
// pre-port behavior for a persistently-empty model.
func TestAgentLoop_EmptyResponseExhaustedStillCompletes(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{}, {}, {}}} // always empty
	p, rec := newTestProject(t, mc, nil)

	runAgent(p, "hello")

	if mc.callCount != 3 {
		t.Errorf("expected exactly 3 attempts (maxStreamAttempts), got %d", mc.callCount)
	}
	if errs := rec.find(EventChatError); len(errs) != 0 {
		t.Errorf("exhausted empty retries should NOT surface a chat:error; got %v", errs)
	}
	if len(rec.find(EventChatComplete)) == 0 {
		t.Error("expected chat:complete after exhausting empty retries")
	}
}
