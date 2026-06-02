package studio

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

// blockUntilCancelTool blocks in Execute until its context is done. Used to
// hold the agent loop inside tool execution until the per-turn deadline fires,
// so we can exercise the DeadlineExceeded path deterministically.
type blockUntilCancelTool struct{}

func (b *blockUntilCancelTool) Name() string        { return "block_until_cancel" }
func (b *blockUntilCancelTool) Description() string { return "blocks until ctx is done" }
func (b *blockUntilCancelTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "block_until_cancel"}
}
func (b *blockUntilCancelTool) Validate(_ map[string]any) error { return nil }
func (b *blockUntilCancelTool) Execute(ctx context.Context, _ map[string]any) (tools.ToolResult, error) {
	<-ctx.Done()
	return tools.ToolResult{Content: "stopped", Success: true}, nil
}

// TestTruncateUTF8_DoesNotSplitRunes guards the iter-1070+ rune-safe draft/pin
// truncation: cutting at a raw byte boundary used to leave a half-encoded
// trailing rune for Cyrillic/CJK/emoji content. truncateUTF8 must always return
// valid UTF-8 within the byte cap.
func TestTruncateUTF8_DoesNotSplitRunes(t *testing.T) {
	// Each Cyrillic rune here is 2 bytes (12 bytes total) — every odd byte cap
	// would split a rune with a naive slice.
	s := "привет"
	for max := 0; max <= len(s); max++ {
		out := truncateUTF8(s, max)
		if !utf8.ValidString(out) {
			t.Errorf("truncateUTF8(%q, %d) = %q is not valid UTF-8", s, max, out)
		}
		if len(out) > max {
			t.Errorf("truncateUTF8(%q, %d) = %q exceeds %d bytes", s, max, out, max)
		}
	}

	// Under the limit → returned verbatim.
	if got := truncateUTF8("abc", 10); got != "abc" {
		t.Errorf("under-limit input changed: got %q", got)
	}

	// 4-byte emoji: 'a'(1) + 😀(4) + 'b'(1). A 3-byte cap lands inside the
	// emoji and must back off to just "a".
	emoji := "a😀b"
	if got := truncateUTF8(emoji, 3); got != "a" || !utf8.ValidString(got) {
		t.Errorf("emoji boundary: truncateUTF8(%q, 3) = %q, want %q", emoji, got, "a")
	}
}

// TestSessionFields_NoRaceWithConcurrentReaders is the regression guard for the
// iter-1070+ data races on ChatSession.Name / lastUsedAt. Those fields are
// owned by session.mu (the agent loop's first-turn auto-rename + lastUsedAt
// bump write them under session.mu), but ListChatSessions / SearchProjectHistory
// / CreateChatSession / ExportProjectAllSessions used to read them under only
// p.mu — a different lock. Run under `go test -race` to catch a regression.
//
// The writer mimics the agent loop / RenameChatSession at the field level
// (Name + lastUsedAt under session.mu); the readers are the real public APIs.
func TestSessionFields_NoRaceWithConcurrentReaders(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "race-sessions")
	// A couple of extra sessions so the reader loops have something to iterate.
	if _, err := s.CreateChatSession(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChatSession(info.ID); err != nil {
		t.Fatal(err)
	}

	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()

	def := p.sessions["default"]
	def.mu.Lock()
	def.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello world search target")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("a reply mentioning hello")}},
	}
	def.mu.Unlock()

	const iters = 150
	var wg sync.WaitGroup

	// Writer A: the real rename path (Name write under session.mu + persist).
	wg.Go(func() {
		for i := range iters {
			_ = s.RenameChatSession(info.ID, "default", fmt.Sprintf("Renamed %d", i))
		}
	})

	// Writer B: mimic the agent loop's per-turn Name + lastUsedAt writes under
	// session.mu, which is what actually races the readers in production.
	wg.Go(func() {
		for i := range iters {
			def.mu.Lock()
			def.Name = fmt.Sprintf("agent %d", i)
			def.lastUsedAt = int64(i + 1)
			def.mu.Unlock()
		}
	})

	// Readers: every public API the audit flagged for reading sess.Name /
	// lastUsedAt under the wrong lock.
	wg.Go(func() {
		for range iters {
			_, _ = s.ListChatSessions(info.ID)
		}
	})
	wg.Go(func() {
		for range iters {
			_, _ = s.SearchProjectHistory(info.ID, "hello")
		}
	})
	wg.Go(func() {
		for range iters {
			_, _ = s.ExportProjectAllSessions(info.ID)
		}
	})
	wg.Go(func() {
		for range iters {
			_, _ = s.CreateChatSession(info.ID)
		}
	})

	wg.Wait()
}

// TestAgentLoop_MidStreamRetryRecovers guards iter-1070+ F10: a transient
// mid-stream failure where nothing was emitted to the UI should be retried
// (sendWithRetry alone only covered the pre-stream call), so a single
// connection blip on a long turn no longer loses the whole turn.
func TestAgentLoop_MidStreamRetryRecovers(t *testing.T) {
	reg := tools.NewRegistry()
	mc := &mockClient{responses: []mockResp{
		// First attempt: stream emits a RETRYABLE error before any token.
		{streamErr: fmt.Errorf("connection reset: eof")},
		// Retry: succeeds with a real answer.
		{text: "recovered answer"},
	}}
	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "do the thing")

	completes := rec.find(EventChatComplete)
	if len(completes) == 0 {
		t.Fatal("expected chat:complete after mid-stream retry recovered")
	}
	ce, ok := completes[len(completes)-1].data.(ChatCompleteEvent)
	if !ok || ce.Text != "recovered answer" {
		t.Errorf("expected recovered text, got %#v", completes[len(completes)-1].data)
	}
	// A retry banner should have fired, and NO hard error (it recovered).
	if len(rec.find(EventChatRetry)) == 0 {
		t.Error("expected a chat:retry event during mid-stream recovery")
	}
	if errs := rec.find(EventChatError); len(errs) != 0 {
		t.Errorf("expected no chat:error after recovery, got %d", len(errs))
	}
	// Exactly two LLM calls: the failed attempt + the successful retry.
	mc.mu.Lock()
	calls := mc.callCount
	mc.mu.Unlock()
	if calls != 2 {
		t.Errorf("expected 2 LLM calls (fail + retry), got %d", calls)
	}
}

// TestAgentLoop_NonRetryableStreamErrorNotRetried confirms the conservative
// scope of F10: a NON-retryable mid-stream error is surfaced immediately
// (single attempt), not retried — preserving prior behaviour.
func TestAgentLoop_NonRetryableStreamErrorNotRetried(t *testing.T) {
	reg := tools.NewRegistry()
	mc := &mockClient{responses: []mockResp{
		{streamErr: fmt.Errorf("malformed SSE frame")}, // not retryable
		{text: "should never be reached"},
	}}
	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "go")

	if len(rec.find(EventChatError)) == 0 {
		t.Error("expected chat:error for a non-retryable stream error")
	}
	mc.mu.Lock()
	calls := mc.callCount
	mc.mu.Unlock()
	if calls != 1 {
		t.Errorf("non-retryable stream error should not retry: got %d calls, want 1", calls)
	}
}

// TestSendMessage_CancelledTurnSkipsFinalSave guards iter-1070+ F2: when a turn
// is cancelled (user Stop / /clear / session delete all cancel via cancelFn),
// the final SaveHistoryWithUsage must be SKIPPED so a concurrent ClearHistory /
// DeleteChatSession isn't overwritten and a cleared/deleted session isn't
// resurrected on next startup. We detect the skip by asserting the persisted
// usage was never bumped (TurnCount stays 0).
func TestSendMessage_CancelledTurnSkipsFinalSave(t *testing.T) {
	reg := tools.NewRegistry()
	mc := &mockClient{}
	p, _ := newTestProject(t, mc, reg)
	// The single response cancels the session context before returning, so the
	// agent loop reaches the tail with ctx.Err() != nil.
	mc.responses = []mockResp{{text: "partial", cancelProj: p}}
	runAgent(p, "a question")

	usage := LoadHistoryUsage(p.ID + "_default")
	if usage != nil && usage.TurnCount > 0 {
		t.Errorf("cancelled turn persisted usage (TurnCount=%d); the final save must be skipped", usage.TurnCount)
	}

	// The EARLY save (before the loop) still persisted the user turn, so the
	// conversation itself isn't lost — only the cancelled round's final save.
	hist, err := LoadHistory(p.ID + "_default")
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(hist) == 0 {
		t.Error("expected the user turn to remain persisted via the early save")
	}
}

// TestSendMessage_DeadlineExceededStillPersists guards the post-review fix to
// F2: the 30-minute per-turn DeadlineExceeded outcome produced real work and
// MUST be persisted (the pre-fix code saved unconditionally). Only an explicit
// cancel (Stop/clear/delete) should skip. A naive `ctx.Err() == nil` guard
// lumped DeadlineExceeded with cancel and silently lost the long run on
// restart.
func TestSendMessage_DeadlineExceededStillPersists(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(&blockUntilCancelTool{})
	mc := &mockClient{responses: []mockResp{
		// Round 1 completes (model tool-call recorded + tokens reported), then
		// the tool blocks until the (tiny stand-in for 30-min) deadline fires.
		{funcCalls: []*genai.FunctionCall{{Name: "block_until_cancel", Args: map[string]any{}}}, inputTokens: 800, outputTokens: 200},
	}}
	p, _ := newTestProject(t, mc, reg)
	p.testTurnTimeout = 30 * time.Millisecond
	runAgent(p, "a long task")

	usage := LoadHistoryUsage(p.ID + "_default")
	if usage == nil || usage.TurnCount == 0 {
		t.Error("DeadlineExceeded turn was not persisted (TurnCount=0); the final save must run on a 30-min timeout, not be skipped like a user cancel")
	}
}

// TestSendMessage_CancelledTurnKeepsBudgetCacheConsistent guards the post-review
// fix to F2/Reg2: the in-memory budget cache (bumpTotalCostUSD) must move in
// lockstep with the on-disk usage. A cancelled turn skips the disk save, so it
// must ALSO skip the in-memory bump — otherwise strict-budget enforcement
// counts cost that disk doesn't, and the cache silently drops on restart.
func TestSendMessage_CancelledTurnKeepsBudgetCacheConsistent(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(&echoTool{})
	mc := &mockClient{}
	p, _ := newTestProject(t, mc, reg)
	mc.mu.Lock()
	mc.responses = []mockResp{
		// Round 1 completes WITH reported tokens → estCost > 0 accumulates.
		{funcCalls: []*genai.FunctionCall{{Name: "echo", Args: map[string]any{"input": "x"}}}, inputTokens: 1000, outputTokens: 500},
		// Round 2 (func-response send) cancels — simulates a user Stop after a
		// completed round, the case where estCost is non-zero.
		{cancelProj: p, text: "won't persist"},
	}
	mc.mu.Unlock()
	runAgent(p, "go")

	// Disk: the cancelled turn skipped the save.
	if usage := LoadHistoryUsage(p.ID + "_default"); usage != nil && usage.TurnCount > 0 {
		t.Errorf("cancelled turn persisted usage: TurnCount=%d", usage.TurnCount)
	}
	// In-memory budget cache must agree with disk (both 0). totalCostUSD seeds
	// from disk on first read, so a value > 0 here means the cancelled-turn cost
	// was bumped in memory but never written to disk — the desync the fix closes.
	if got := p.totalCostUSD(); got != 0 {
		t.Errorf("cancelled turn desynced the budget cache: totalCostUSD()=%v, want 0 to match disk", got)
	}
}
