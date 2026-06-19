package studio

import (
	"fmt"
	"strings"
	"testing"
)

// TestSendMessage_PreservesPartialTextOnStreamError verifies that when a stream
// fails AFTER emitting partial text (the GLM/Kimi mid-response stall that dies
// past the idle extension), the partial text the user already saw is appended to
// history — so the model remembers its partial work on the next message instead
// of it vanishing. Without this, sendAndStream dropped the partial entirely.
func TestSendMessage_PreservesPartialTextOnStreamError(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		// Streams "partial answer before stall" then errors mid-stream.
		{text: "partial answer before stall", streamErr: fmt.Errorf("stream_idle_timeout after partial response")},
	}}
	p, rec := newTestProject(t, mc, nil)

	runAgent(p, "do the thing")

	// The turn must have surfaced a chat:error (the stream failed).
	if len(rec.find(EventChatError)) == 0 {
		t.Error("expected chat:error after the stream failed")
	}

	// History must now contain a model turn carrying the partial text, AFTER the
	// user message — so the next turn's request includes it and the model can
	// continue rather than starting blank.
	sess := p.GetSession("default")
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	var modelTexts []string
	for _, c := range sess.history {
		if c.Role == "model" {
			for _, part := range c.Parts {
				if part.Text != "" {
					modelTexts = append(modelTexts, part.Text)
				}
			}
		}
	}
	joined := strings.Join(modelTexts, "|")
	if !strings.Contains(joined, "partial answer before stall") {
		t.Errorf("partial text not preserved in history; model turns = %q", joined)
	}
}

// TestSendMessage_NoPartialPreservedWhenNoText verifies the no-op case: a stream
// that errors with NO text emitted leaves no empty/junk model turn in history
// (and is retried, per the existing streamEmitted==false path).
func TestSendMessage_NoPartialPreservedWhenNoText(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{streamErr: fmt.Errorf("malformed SSE frame")}, // no text → retryable, no preserve
		{text: "recovered on retry"},
	}}
	p, _ := newTestProject(t, mc, nil)

	runAgent(p, "go")

	sess := p.GetSession("default")
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	// No empty-text model turn should have been inserted by the error path.
	for _, c := range sess.history {
		if c.Role == "model" {
			for _, part := range c.Parts {
				if part.Text == "" && part.FunctionCall == nil {
					t.Errorf("found an empty model part — preserve should be a no-op with no partial text")
				}
			}
		}
	}
}
