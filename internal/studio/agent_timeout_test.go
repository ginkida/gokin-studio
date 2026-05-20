package studio

import (
	"strings"
	"testing"
	"time"
)

// TestAgentLoop_DeadlineExceededEmitsExplanation is the iter 980+
// regression guard for Bug 4: when the 30-minute per-turn ceiling fired,
// the loop broke silently and chat:complete emitted with empty text. The
// frontend's "Generating…" spinner cleared but no message explained why
// the agent stopped — user reported "it just stopped doing anything" on
// long refactors.
//
// We test the path by injecting `testTurnTimeout = 1ns`, which forces the
// internal context to be DeadlineExceeded by the time the outer loop's
// first guard (`if ctx.Err() != nil { break }`) checks it. The post-loop
// chat:error emit must fire with text mentioning the per-turn limit.
func TestAgentLoop_DeadlineExceededEmitsExplanation(t *testing.T) {
	mc := &mockClient{responses: []mockResp{}}
	p, rec := newTestProject(t, mc, nil)
	// 1 nanosecond → DeadlineExceeded fires before we ever reach the
	// mock client. Pure-Go context machinery, no IO involved.
	p.testTurnTimeout = 1 * time.Nanosecond

	runAgent(p, "long task that times out")

	errs := rec.find(EventChatError)
	if len(errs) == 0 {
		t.Fatal("expected chat:error to be emitted when per-turn deadline fires")
	}
	// At least one of the error events must mention the per-turn limit.
	matched := false
	for _, e := range errs {
		te, ok := e.data.(ChatTextEvent)
		if !ok {
			continue
		}
		if strings.Contains(te.Text, "30-minute") || strings.Contains(te.Text, "per-turn") {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("chat:error events did not mention the per-turn timeout; got %+v", errs)
	}

	// chat:complete must STILL fire so the frontend's spinner clears.
	// (Pre-fix this was the only event — now we emit error before it.)
	if completes := rec.find(EventChatComplete); len(completes) != 1 {
		t.Errorf("expected exactly 1 chat:complete event after timeout, got %d", len(completes))
	}
}
