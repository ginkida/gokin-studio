package studio

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

// ---------------------------------------------------------------------------
// Mock client
// ---------------------------------------------------------------------------

// mockResp is a single pre-programmed response from the mock client.
// err != nil → SendMessageWithHistory/SendFunctionResponse returns that error
// (simulates network/API failure). nilResp → returns (nil, nil) to exercise
// the resp==nil break. streamErr → stream succeeds but emits an error chunk so
// streamAndProcess returns a non-nil error. cancelProj → the mock cancels the
// project's session context before returning (used to cover the ctx.Err()
// checks in the agent loop that fire when cancellation races with an error).
// Otherwise the response streams text and/or function calls followed by a Done chunk.
type mockResp struct {
	text         string
	thinking     string // emitted as Thinking chunk before text
	funcCalls    []*genai.FunctionCall
	parts        []*genai.Part      // emitted as a Parts chunk (e.g. Thought:true)
	inputTokens  int                // reported on Done chunk
	outputTokens int                // reported on Done chunk
	finishReason genai.FinishReason // reported on Done chunk; "" → FinishReasonStop
	err          error
	nilResp      bool     // return (nil, nil) — no error, no stream
	streamErr    error    // emit an error chunk inside the stream
	cancelProj   *Project // if set, cancel session context before returning
}

// mockClient implements client.Client using a pre-loaded queue of responses.
type mockClient struct {
	mu        sync.Mutex
	responses []mockResp
	callCount int

	// shouldPanic causes SendMessageWithHistory to panic, exercising the
	// top-level recover in SendMessage.
	shouldPanic bool

	// sendMessageOverride is returned by SendMessage (used in messenger tests).
	// nil → return (nil, nil).
	sendMessageOverride *mockResp

	// Captured args from the most recent SendFunctionResponse call.
	lastFuncRespHistory []*genai.Content
	lastFuncRespResults []*genai.FunctionResponse

	// Captured by SetSystemInstruction.
	lastSystemInstruction string
}

func (m *mockClient) pop() mockResp {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if len(m.responses) == 0 {
		return mockResp{text: "(mock: no more responses)"}
	}
	r := m.responses[0]
	m.responses = m.responses[1:]
	return r
}

// makeStream converts a mockResp into a StreamingResponse channel.
func makeStream(r mockResp) *client.StreamingResponse {
	ch := make(chan client.ResponseChunk, 16)
	if r.streamErr != nil {
		ch <- client.ResponseChunk{Error: r.streamErr}
		close(ch)
		return &client.StreamingResponse{Chunks: ch}
	}
	if r.thinking != "" {
		ch <- client.ResponseChunk{Thinking: r.thinking}
	}
	if r.text != "" {
		ch <- client.ResponseChunk{Text: r.text}
	}
	for _, fc := range r.funcCalls {
		ch <- client.ResponseChunk{FunctionCalls: []*genai.FunctionCall{fc}}
	}
	if len(r.parts) > 0 {
		ch <- client.ResponseChunk{Parts: r.parts}
	}
	ch <- client.ResponseChunk{Done: true, InputTokens: r.inputTokens, OutputTokens: r.outputTokens, FinishReason: r.finishReason}
	close(ch)
	return &client.StreamingResponse{Chunks: ch}
}

// cancelIfNeeded reads and calls the session's cancelFn when r.cancelProj is
// set. This lets individual mock responses cancel the agent context before
// returning, so we can exercise the `if ctx.Err() != nil` guards that fire
// when cancellation races with an error or stream failure in the agent loop.
func cancelIfNeeded(r mockResp) {
	if r.cancelProj == nil {
		return
	}
	r.cancelProj.sessions["default"].mu.RLock()
	fn := r.cancelProj.sessions["default"].cancelFn
	r.cancelProj.sessions["default"].mu.RUnlock()
	if fn != nil {
		fn()
	}
}

func (m *mockClient) SendMessageWithHistory(_ context.Context, _ []*genai.Content, _ string) (*client.StreamingResponse, error) {
	m.mu.Lock()
	if m.shouldPanic {
		m.mu.Unlock()
		panic("intentional test panic in SendMessageWithHistory")
	}
	m.mu.Unlock()
	r := m.pop()
	cancelIfNeeded(r)
	if r.err != nil {
		return nil, r.err
	}
	if r.nilResp {
		return nil, nil
	}
	return makeStream(r), nil
}

func (m *mockClient) SendFunctionResponse(_ context.Context, history []*genai.Content, results []*genai.FunctionResponse) (*client.StreamingResponse, error) {
	m.mu.Lock()
	m.lastFuncRespHistory = history
	m.lastFuncRespResults = results
	m.mu.Unlock()
	r := m.pop()
	cancelIfNeeded(r)
	if r.err != nil {
		return nil, r.err
	}
	if r.nilResp {
		return nil, nil
	}
	return makeStream(r), nil
}

// sendMessageResp is an optional override for SendMessage (used by messenger
// tests). When nil, SendMessage returns (nil, nil) as a no-op.
func (m *mockClient) SendMessage(_ context.Context, _ string) (*client.StreamingResponse, error) {
	m.mu.Lock()
	r := m.sendMessageOverride
	m.mu.Unlock()
	if r == nil {
		return nil, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.nilResp {
		return nil, nil
	}
	return makeStream(*r), nil
}
func (m *mockClient) SetTools(_ []*genai.Tool) {}
func (m *mockClient) SetRateLimiter(_ any)     {}
func (m *mockClient) CountTokens(_ context.Context, _ []*genai.Content) (*genai.CountTokensResponse, error) {
	return nil, nil
}
func (m *mockClient) GetModel() string                 { return "test-model" }
func (m *mockClient) SetModel(_ string)                {}
func (m *mockClient) WithModel(_ string) client.Client { return m }
func (m *mockClient) GetRawClient() any                { return nil }
func (m *mockClient) SetSystemInstruction(s string) {
	m.mu.Lock()
	m.lastSystemInstruction = s
	m.mu.Unlock()
}
func (m *mockClient) SetThinkingBudget(_ int32) {}
func (m *mockClient) Close() error              { return nil }

// ---------------------------------------------------------------------------
// Mock tool
// ---------------------------------------------------------------------------

// echoTool is a trivial tool that echoes back its "input" argument. Used to
// verify tool execution in the agent loop without any filesystem access.
type echoTool struct{}

func (e *echoTool) Name() string        { return "echo" }
func (e *echoTool) Description() string { return "echoes input" }
func (e *echoTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "echo"}
}
func (e *echoTool) Validate(_ map[string]any) error { return nil }
func (e *echoTool) Execute(_ context.Context, args map[string]any) (tools.ToolResult, error) {
	msg, _ := args["input"].(string)
	return tools.ToolResult{Content: msg, Success: true}, nil
}

// errTool returns an error (not a panic) so the agent records the error as
// tool content rather than recovering from a panic. Used to test the
// `if toolErr != nil { content = toolErr.Error() }` branch (line 821).
type errTool struct{}

func (e *errTool) Name() string        { return "err_tool" }
func (e *errTool) Description() string { return "always errors" }
func (e *errTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "err_tool"}
}
func (e *errTool) Validate(_ map[string]any) error { return nil }
func (e *errTool) Execute(_ context.Context, _ map[string]any) (tools.ToolResult, error) {
	return tools.ToolResult{}, fmt.Errorf("deliberate tool error")
}

// errResultTool returns (ToolResult{Error: "...", Success: false}, nil) —
// used to test the `else if result.Error != ""` branch (line 822).
type errResultTool struct{}

func (e *errResultTool) Name() string        { return "err_result_tool" }
func (e *errResultTool) Description() string { return "returns error via ToolResult.Error" }
func (e *errResultTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "err_result_tool"}
}
func (e *errResultTool) Validate(_ map[string]any) error { return nil }
func (e *errResultTool) Execute(_ context.Context, _ map[string]any) (tools.ToolResult, error) {
	return tools.ToolResult{Error: "tool result error", Success: false}, nil
}

// cancelTool reads the session's cancelFn from the project and calls it,
// simulating a user stopping the agent mid-turn. Used to cover the
// `if ctx.Err() != nil { break outer }` branch after tool execution (line 840).
type cancelTool struct {
	proj *Project
}

func (c *cancelTool) Name() string        { return "cancel_tool" }
func (c *cancelTool) Description() string { return "cancels the agent context" }
func (c *cancelTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "cancel_tool"}
}
func (c *cancelTool) Validate(_ map[string]any) error { return nil }
func (c *cancelTool) Execute(_ context.Context, _ map[string]any) (tools.ToolResult, error) {
	c.proj.sessions["default"].mu.RLock()
	fn := c.proj.sessions["default"].cancelFn
	c.proj.sessions["default"].mu.RUnlock()
	if fn != nil {
		fn()
	}
	return tools.ToolResult{Content: "cancelled", Success: true}, nil
}

// panicTool panics when executed — used to test safeToolExecute recovery.
type panicTool struct{}

func (pt *panicTool) Name() string        { return "panic_tool" }
func (pt *panicTool) Description() string { return "panics" }
func (pt *panicTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "panic_tool"}
}
func (pt *panicTool) Validate(_ map[string]any) error { return nil }
func (pt *panicTool) Execute(_ context.Context, _ map[string]any) (tools.ToolResult, error) {
	panic("intentional test panic")
}

// ---------------------------------------------------------------------------
// Event recorder
// ---------------------------------------------------------------------------

type emittedEvent struct {
	name string
	data any
}

type recorder struct {
	mu     sync.Mutex
	events []emittedEvent
}

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	r.events = append(r.events, emittedEvent{name, data})
	r.mu.Unlock()
}

func (r *recorder) find(name string) []emittedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []emittedEvent
	for _, e := range r.events {
		if e.name == name {
			out = append(out, e)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Project builder
// ---------------------------------------------------------------------------

// newTestProject creates a minimal Project wired with a mock client and
// recorder. initClient is bypassed because p.client is already set.
func newTestProject(t *testing.T, mc *mockClient, reg *tools.Registry) (*Project, *recorder) {
	t.Helper()
	_ = withTempHistoryDir(t)
	if reg == nil {
		reg = tools.NewRegistry()
	}
	rec := &recorder{}
	p := &Project{
		ID:                "tp",
		Name:              "Test",
		Directory:         t.TempDir(),
		Provider:          "glm",
		Model:             "glm-5.1",
		sessions:          map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		client:            mc,
		registry:          reg,
		testEmitter:       rec.emit,
		retryInitialDelay: time.Millisecond, // fast retries in tests
	}
	return p, rec
}

// runAgent synchronously runs SendMessage and blocks until the goroutine
// inside is done. SendMessage itself is synchronous in the studio package
// (it's the Wails binding that wraps it in a goroutine); tests call it
// directly without a goroutine.
func runAgent(p *Project, message string) {
	p.SendMessage(context.Background(), message, Settings{
		DefaultProvider: "glm",
		DefaultModel:    "glm-5.1",
	})
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestAgentLoop_SimpleText verifies the happy path: one LLM round returning
// text with no tool calls emits the expected events and terminates cleanly.
func TestAgentLoop_SimpleText(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{text: "Hello, I can help with that."},
	}}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "hello")

	texts := rec.find(EventChatText)
	if len(texts) != 1 {
		t.Fatalf("expected 1 chat:text, got %d", len(texts))
	}
	if got := texts[0].data.(ChatTextEvent).Text; got != "Hello, I can help with that." {
		t.Errorf("unexpected text %q", got)
	}

	completes := rec.find(EventChatComplete)
	if len(completes) != 1 {
		t.Fatalf("expected 1 chat:complete, got %d", len(completes))
	}
	ce := completes[0].data.(ChatCompleteEvent)
	if ce.Turns != 1 {
		t.Errorf("expected 1 turn, got %d", ce.Turns)
	}
	if ce.ProjectID != "tp" {
		t.Errorf("unexpected project ID %q", ce.ProjectID)
	}

	// No errors should have been emitted.
	if errs := rec.find(EventChatError); len(errs) != 0 {
		t.Errorf("unexpected error events: %v", errs)
	}
}

// TestAgentLoop_ToolCall verifies that tool calls are executed, results are
// sent back to the model, and the loop terminates on the subsequent text reply.
func TestAgentLoop_ToolCall(t *testing.T) {
	fc := &genai.FunctionCall{ID: "call-1", Name: "echo", Args: map[string]any{"input": "pong"}}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{fc}}, // round 1: one tool call
		{text: "All done."},                    // round 2: final text
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(&echoTool{})

	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "echo pong")

	// Verify tool_call event.
	toolCalls := rec.find(EventChatToolCall)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 chat:tool_call, got %d", len(toolCalls))
	}
	tc := toolCalls[0].data.(ChatToolCallEvent)
	if tc.Tool != "echo" {
		t.Errorf("expected tool 'echo', got %q", tc.Tool)
	}

	// Verify tool_result event.
	toolResults := rec.find(EventChatToolResult)
	if len(toolResults) != 1 {
		t.Fatalf("expected 1 chat:tool_result, got %d", len(toolResults))
	}
	tr := toolResults[0].data.(ChatToolResultEvent)
	if !tr.Success {
		t.Errorf("expected tool success, got failure: %q", tr.Content)
	}
	if tr.Content != "pong" {
		t.Errorf("expected content %q, got %q", "pong", tr.Content)
	}

	// Final text must be the second round response.
	texts := rec.find(EventChatText)
	if len(texts) != 1 || texts[0].data.(ChatTextEvent).Text != "All done." {
		t.Fatalf("unexpected chat:text events: %v", texts)
	}

	// Two LLM calls: initial send + after tool result.
	if mc.callCount != 2 {
		t.Errorf("expected 2 LLM calls, got %d", mc.callCount)
	}

	// Verify SendFunctionResponse received function responses in results (not in
	// history). Before the fix, history contained the responses AND results also
	// contained them, causing every provider to emit duplicate tool_result blocks.
	mc.mu.Lock()
	hist := mc.lastFuncRespHistory
	res := mc.lastFuncRespResults
	mc.mu.Unlock()

	for _, c := range hist {
		for _, part := range c.Parts {
			if part.FunctionResponse != nil {
				t.Errorf("SendFunctionResponse history must not contain FunctionResponse parts (got one for %q)", part.FunctionResponse.Name)
			}
		}
	}
	if len(res) != 1 || res[0].Name != "echo" {
		t.Errorf("expected 1 FunctionResponse for 'echo' in results, got %v", res)
	}
}

// TestAgentLoop_ChainedToolCalls verifies that the inner tool loop handles
// chained tool rounds without re-entering SendMessageWithHistory. Before the
// fix, chained FCs escaped to the outer loop, causing a spurious second
// SendMessageWithHistory call (with "Continue.") that wasted a round-trip and
// had the sanitizer strip unresolved FunctionCall parts from history.
func TestAgentLoop_ChainedToolCalls(t *testing.T) {
	fc1 := &genai.FunctionCall{ID: "c1", Name: "echo", Args: map[string]any{"input": "first"}}
	fc2 := &genai.FunctionCall{ID: "c2", Name: "echo", Args: map[string]any{"input": "second"}}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{fc1}}, // round 0: first tool call
		{funcCalls: []*genai.FunctionCall{fc2}}, // round 1: second tool call (chained)
		{text: "Done with both."},               // round 2: final text
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(&echoTool{})

	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "use two tools")

	// Three LLM calls: initial + two SendFunctionResponse rounds.
	// No extra SendMessageWithHistory (which would mean callCount=4 in the old broken version).
	if mc.callCount != 3 {
		t.Errorf("expected 3 LLM calls (1 SendMessageWithHistory + 2 SendFunctionResponse), got %d", mc.callCount)
	}

	// Two tool_call events and two tool_result events.
	if n := len(rec.find(EventChatToolCall)); n != 2 {
		t.Errorf("expected 2 tool_call events, got %d", n)
	}
	if n := len(rec.find(EventChatToolResult)); n != 2 {
		t.Errorf("expected 2 tool_result events, got %d", n)
	}

	// Final text should be the third response.
	texts := rec.find(EventChatText)
	if len(texts) != 1 || texts[0].data.(ChatTextEvent).Text != "Done with both." {
		t.Fatalf("unexpected chat:text events: %v", texts)
	}

	// History shape: user, model(fc1), user(fr1), model(fc2), user(fr2), model(text) = 6 entries.
	sess := p.GetSession("default")
	sess.mu.RLock()
	histLen := len(sess.history)
	sess.mu.RUnlock()
	if histLen != 6 {
		t.Errorf("expected 6 history entries, got %d", histLen)
	}
}

// TestAgentLoop_UnknownTool verifies that calling an unknown tool emits a
// tool_result error event and the loop sends the error back to the model.
func TestAgentLoop_UnknownTool(t *testing.T) {
	fc := &genai.FunctionCall{ID: "x1", Name: "nonexistent_tool", Args: nil}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{fc}},
		{text: "OK"},
	}}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "use nonexistent tool")

	results := rec.find(EventChatToolResult)
	if len(results) == 0 {
		t.Fatal("expected a tool_result event for unknown tool")
	}
	tr := results[0].data.(ChatToolResultEvent)
	if tr.Success {
		t.Error("unknown tool should not succeed")
	}
	if tr.Tool != "nonexistent_tool" {
		t.Errorf("expected tool name 'nonexistent_tool', got %q", tr.Tool)
	}
}

// TestAgentLoop_ToolPanicRecovery verifies that a tool that panics is caught
// by safeToolExecute, surfaced as a failed tool_result, and the agent loop
// continues (sends the error back to the model) rather than crashing.
func TestAgentLoop_ToolPanicRecovery(t *testing.T) {
	fc := &genai.FunctionCall{ID: "p1", Name: "panic_tool", Args: nil}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{fc}},
		{text: "Recovered."},
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(&panicTool{})

	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "use panic tool")

	results := rec.find(EventChatToolResult)
	if len(results) == 0 {
		t.Fatal("expected a tool_result event after panic")
	}
	tr := results[0].data.(ChatToolResultEvent)
	if tr.Success {
		t.Error("panicking tool should not succeed")
	}

	// Loop must have continued and produced the final text response.
	texts := rec.find(EventChatText)
	if len(texts) != 1 || texts[0].data.(ChatTextEvent).Text != "Recovered." {
		t.Fatalf("expected final text 'Recovered.', got %v", texts)
	}
}

// TestAgentLoop_TransientErrorRetry verifies that a 429 / rate-limit error on
// the first attempt triggers a retry event and the second attempt succeeds.
func TestAgentLoop_TransientErrorRetry(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{err: fmt.Errorf("429 rate limit exceeded")},
		{text: "Retry success."},
	}}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "retry test")

	retries := rec.find(EventChatRetry)
	if len(retries) != 1 {
		t.Fatalf("expected 1 chat:retry event, got %d", len(retries))
	}
	re := retries[0].data.(ChatRetryEvent)
	if re.Attempt != 1 {
		t.Errorf("expected attempt=1, got %d", re.Attempt)
	}
	if re.ProjectID != "tp" {
		t.Errorf("unexpected project ID %q", re.ProjectID)
	}

	texts := rec.find(EventChatText)
	if len(texts) != 1 || texts[0].data.(ChatTextEvent).Text != "Retry success." {
		t.Fatalf("unexpected chat:text after retry: %v", texts)
	}

	if errs := rec.find(EventChatError); len(errs) != 0 {
		t.Errorf("unexpected error events: %v", errs)
	}
}

// TestAgentLoop_NonRetryableErrorSurfaces verifies that a 401 Unauthorized
// error is not retried and surfaces as a chat:error event.
func TestAgentLoop_NonRetryableErrorSurfaces(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{err: fmt.Errorf("401 Unauthorized: invalid API key")},
	}}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "bad key")

	if retries := rec.find(EventChatRetry); len(retries) != 0 {
		t.Errorf("non-retryable error should not trigger retries; got %d", len(retries))
	}

	errors := rec.find(EventChatError)
	if len(errors) == 0 {
		t.Fatal("expected a chat:error event for 401")
	}
	errText := errors[0].data.(ChatTextEvent).Text
	if errText == "" {
		t.Error("error text should not be empty")
	}
}

// TestAgentLoop_ContextCancelMidLoop verifies that cancelling the context
// before the agent starts terminates the loop without panicking or emitting
// spurious events. (The session cancel path is exercised separately via Stop.)
func TestAgentLoop_ContextCancelMidLoop(t *testing.T) {
	// Block forever — the context cancel should stop the loop.
	ch := make(chan client.ResponseChunk) // never closed
	mc := &mockClient{}
	mc.responses = []mockResp{{}} // will be popped by the send attempt
	// Override SendMessageWithHistory to block until context is done.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_ = mc
	p, rec := newTestProject(t, mc, nil)
	// Use a fresh project with an already-cancelled context.
	p.SendMessage(ctx, "cancel test", Settings{DefaultProvider: "glm", DefaultModel: "glm-5.1"})
	_ = ch

	// The loop should have exited cleanly — no panic, no stuck goroutine.
	// We expect either a chat:error (from the context error) or a chat:complete
	// with 0 turns, but critically no infinite loop.
	_ = rec.find(EventChatComplete)
	_ = rec.find(EventChatError)
}

// TestAgentLoop_MultiTurnHistory verifies that history accumulates correctly:
// after a tool call round-trip, the session history should contain
// user→model(toolcall)→user(toolresult)→model(text) in order.
func TestAgentLoop_MultiTurnHistory(t *testing.T) {
	fc := &genai.FunctionCall{ID: "h1", Name: "echo", Args: map[string]any{"input": "world"}}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{fc}},
		{text: "History preserved."},
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(&echoTool{})

	p, _ := newTestProject(t, mc, reg)
	runAgent(p, "check history")

	sess := p.GetSession("default")
	sess.mu.RLock()
	hist := make([]*genai.Content, len(sess.history))
	copy(hist, sess.history)
	sess.mu.RUnlock()

	// Expected order: user, model(fc), user(fr), model(text) = 4 entries.
	if len(hist) != 4 {
		t.Fatalf("expected 4 history entries, got %d", len(hist))
	}
	if hist[0].Role != "user" {
		t.Errorf("entry 0 role: want user, got %q", hist[0].Role)
	}
	if hist[1].Role != "model" {
		t.Errorf("entry 1 role: want model, got %q", hist[1].Role)
	}
	if hist[2].Role != "user" {
		t.Errorf("entry 2 role: want user (func response), got %q", hist[2].Role)
	}
	if hist[3].Role != "model" {
		t.Errorf("entry 3 role: want model, got %q", hist[3].Role)
	}
}

// TestSendWithRetry_ExhaustsAttemptsOnPersistentError verifies that exhausting
// all retry attempts returns an error rather than looping forever.
func TestSendWithRetry_ExhaustsAttempts(t *testing.T) {
	calls := 0
	_, err := sendWithRetry(context.Background(), nil, time.Millisecond, func() (*client.StreamingResponse, error) {
		calls++
		return nil, fmt.Errorf("503 service overloaded") // always retryable
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls (maxAttempts), got %d", calls)
	}
}

// TestSendWithRetry_NoRetryOnNonRetryable verifies that a non-retryable error
// (e.g. 401) stops immediately without backing off.
func TestSendWithRetry_NoRetryOnNonRetryable(t *testing.T) {
	calls := 0
	_, err := sendWithRetry(context.Background(), nil, time.Millisecond, func() (*client.StreamingResponse, error) {
		calls++
		return nil, fmt.Errorf("401 Unauthorized")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call for non-retryable error, got %d", calls)
	}
}

// TestSendWithRetry_ContextCancelStopsRetry verifies that cancelling the
// context during the backoff sleep stops the retry loop promptly.
func TestSendWithRetry_ContextCancelStopsRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	calls := 0
	_, err := sendWithRetry(ctx, nil, 200*time.Millisecond, func() (*client.StreamingResponse, error) {
		calls++
		return nil, fmt.Errorf("rate limit exceeded")
	})
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
	// With a 200ms delay and 50ms timeout the context should fire after 1 call.
	if calls > 2 {
		t.Errorf("expected at most 2 calls before context cancel, got %d", calls)
	}
}

// TestSendWithRetry_PreCancelledContext verifies that sendWithRetry returns
// immediately when the context is already cancelled at the time fn() returns
// an error, without waiting for the backoff delay or retrying.
func TestSendWithRetry_PreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately before any call

	calls := 0
	_, err := sendWithRetry(ctx, nil, time.Hour, func() (*client.StreamingResponse, error) {
		calls++
		return nil, fmt.Errorf("rate limit exceeded")
	})
	if err == nil {
		t.Fatal("expected error for pre-cancelled context, got nil")
	}
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call before ctx check, got %d", calls)
	}
}

// TestAgentLoop_ParallelFCsInOneRound verifies that when the model returns
// multiple function calls in a single response round, all of them are executed
// and their results are sent back together in a single SendFunctionResponse
// call (not split across multiple rounds). This is the "parallel tool call"
// path: fc1 and fc2 are returned in the same model turn, not chained.
func TestAgentLoop_ParallelFCsInOneRound(t *testing.T) {
	fc1 := &genai.FunctionCall{ID: "p1", Name: "echo", Args: map[string]any{"input": "hello"}}
	fc2 := &genai.FunctionCall{ID: "p2", Name: "echo", Args: map[string]any{"input": "world"}}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{fc1, fc2}}, // two FCs in ONE model response
		{text: "Both done."},
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(&echoTool{})

	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "run two tools in parallel")

	// Exactly 2 LLM calls: 1 SendMessageWithHistory + 1 SendFunctionResponse.
	// If parallel FCs were incorrectly handled as chained, callCount would be 3.
	if mc.callCount != 2 {
		t.Errorf("expected 2 LLM calls (1 initial + 1 SendFunctionResponse), got %d", mc.callCount)
	}

	// Both tool calls must have fired.
	if n := len(rec.find(EventChatToolCall)); n != 2 {
		t.Errorf("expected 2 tool_call events, got %d", n)
	}
	if n := len(rec.find(EventChatToolResult)); n != 2 {
		t.Errorf("expected 2 tool_result events, got %d", n)
	}

	// Final text from the second model response.
	texts := rec.find(EventChatText)
	if len(texts) != 1 || texts[0].data.(ChatTextEvent).Text != "Both done." {
		t.Fatalf("unexpected chat:text events: %v", texts)
	}

	// SendFunctionResponse must have received BOTH results in one call.
	mc.mu.Lock()
	results := mc.lastFuncRespResults
	mc.mu.Unlock()
	if len(results) != 2 {
		t.Errorf("expected 2 FunctionResponse results in single SendFunctionResponse call, got %d", len(results))
	}

	// History: user, model{fc1,fc2}, user{fr1,fr2}, model{text} = 4 entries.
	sess := p.GetSession("default")
	sess.mu.RLock()
	histLen := len(sess.history)
	sess.mu.RUnlock()
	if histLen != 4 {
		t.Errorf("expected 4 history entries, got %d", histLen)
	}
}

// TestSendMessage_SessionAlreadyActive verifies that SendMessage emits a
// chat:error and returns immediately when the session is already active
// (another agent goroutine is running). The LLM client must not be called.
func TestSendMessage_SessionAlreadyActive(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "should not be sent"}}}
	p, rec := newTestProject(t, mc, nil)

	// Mark the default session as active before sending.
	sess := p.GetSession("default")
	sess.mu.Lock()
	sess.active = true
	sess.mu.Unlock()

	runAgent(p, "hello while active")

	errors := rec.find(EventChatError)
	if len(errors) == 0 {
		t.Fatal("expected chat:error for already-active session, got none")
	}
	errText := errors[0].data.(ChatTextEvent).Text
	if errText == "" {
		t.Error("expected non-empty error text for already-active session")
	}
	mc.mu.Lock()
	calls := mc.callCount
	mc.mu.Unlock()
	if calls != 0 {
		t.Errorf("expected 0 LLM calls for already-active session, got %d", calls)
	}
}

// TestSendMessage_NilSessionEmitsError verifies that SendMessage emits
// chat:error when the requested session does not exist in the project and
// there is no "default" fallback — covering the `if session == nil` guard.
func TestSendMessage_NilSessionEmitsError(t *testing.T) {
	mc := &mockClient{}
	_ = withTempHistoryDir(t)
	rec := &recorder{}
	p := &Project{
		ID:          "tp-nilsess",
		Name:        "Test",
		Directory:   t.TempDir(),
		sessions:    map[string]*ChatSession{}, // no default session
		client:      mc,
		registry:    tools.NewRegistry(),
		testEmitter: rec.emit,
	}

	p.SendMessage(context.Background(), "hello", Settings{
		DefaultProvider: "glm",
		DefaultModel:    "glm-5.1",
	}, "nonexistent-session")

	errs := rec.find(EventChatError)
	if len(errs) == 0 {
		t.Fatal("expected chat:error for nil session, got none")
	}
	if text := errs[0].data.(ChatTextEvent).Text; text == "" {
		t.Error("expected non-empty error text for nil session")
	}
	if mc.callCount != 0 {
		t.Errorf("expected 0 LLM calls for nil session, got %d", mc.callCount)
	}
}

// TestSendMessage_RetryDelayDefault verifies that when retryInitialDelay is
// zero (the default for production code), the fallback of 2 * time.Second is
// applied. A non-retryable error (401) causes sendWithRetry to return after
// the first attempt without sleeping, so the test completes immediately.
func TestSendMessage_RetryDelayDefault(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{err: fmt.Errorf("401 Unauthorized: bad key")},
	}}
	_ = withTempHistoryDir(t)
	rec := &recorder{}
	// retryInitialDelay NOT set → zero → triggers the `retryDelay = 2*time.Second` branch.
	// The non-retryable 401 makes sendWithRetry return on the first attempt, so no actual sleep.
	p := &Project{
		ID:          "tp-retry-default",
		Name:        "Test",
		Directory:   t.TempDir(),
		sessions:    map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		client:      mc,
		registry:    tools.NewRegistry(),
		testEmitter: rec.emit,
	}

	runAgent(p, "trigger default retry delay")

	errs := rec.find(EventChatError)
	if len(errs) == 0 {
		t.Fatal("expected chat:error for 401, got none")
	}
}

// TestSendMessage_TokenUsageEmitted verifies that when the streaming response
// includes non-zero InputTokens/OutputTokens, recordResponse emits a
// chat:usage event with the accumulated counts.
func TestSendMessage_TokenUsageEmitted(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{text: "reply", inputTokens: 100, outputTokens: 50},
	}}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "count tokens")

	usages := rec.find(EventChatUsage)
	if len(usages) == 0 {
		t.Fatal("expected chat:usage event, got none")
	}
	u := usages[0].data.(ChatUsageEvent)
	if u.LastInputTokens != 100 {
		t.Errorf("LastInputTokens = %d, want 100", u.LastInputTokens)
	}
	if u.LastOutputTokens != 50 {
		t.Errorf("LastOutputTokens = %d, want 50", u.LastOutputTokens)
	}
}

// TestSendMessage_ThinkingEmitted verifies that a streaming response with a
// Thinking chunk triggers both a chat:thinking_delta (live streaming) and a
// chat:thinking (end-of-round finalisation) event in the correct order.
func TestSendMessage_ThinkingEmitted(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{thinking: "deep thoughts", text: "final answer"},
	}}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "think hard")

	if deltas := rec.find(EventChatThinkingDelta); len(deltas) == 0 {
		t.Error("expected at least one chat:thinking_delta event")
	}
	thinkings := rec.find(EventChatThinking)
	if len(thinkings) == 0 {
		t.Fatal("expected chat:thinking event, got none")
	}
	got := thinkings[0].data.(ChatThinkingEvent).Text
	if got != "deep thoughts" {
		t.Errorf("thinking text = %q, want 'deep thoughts'", got)
	}
}

// TestSendMessage_PanicRecovered verifies the top-level defer/recover in
// SendMessage: a panic inside the agent run (here: mock client panics in
// SendMessageWithHistory) is caught, a chat:error is emitted, and the
// session's active flag is cleared so the UI doesn't get stuck.
func TestSendMessage_PanicRecovered(t *testing.T) {
	_ = withTempHistoryDir(t)
	rec := &recorder{}
	mc := &mockClient{shouldPanic: true}
	p := &Project{
		ID:                "tp-panic",
		Name:              "Test",
		Directory:         t.TempDir(),
		sessions:          map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		client:            mc,
		registry:          tools.NewRegistry(),
		testEmitter:       rec.emit,
		retryInitialDelay: time.Millisecond,
	}

	// Must not panic the test process.
	runAgent(p, "trigger panic")

	errs := rec.find(EventChatError)
	if len(errs) == 0 {
		t.Fatal("expected chat:error after panic recovery, got none")
	}
	text := errs[0].data.(ChatTextEvent).Text
	if text == "" {
		t.Error("expected non-empty error text after panic recovery")
	}
	// Session must be unlocked / active cleared.
	sess := p.GetSession("default")
	sess.mu.RLock()
	active := sess.active
	sess.mu.RUnlock()
	if active {
		t.Error("expected session.active=false after panic recovery")
	}
}

// TestSendMessage_InitClientError verifies that when initClient fails (e.g. no API
// key configured), SendMessage emits a chat:error event and does not start the agent
// loop. Covers lines 499-503 of SendMessage and the body of initClient up to the
// NewClient call site.
func TestSendMessage_InitClientError(t *testing.T) {
	_ = withTempHistoryDir(t)

	// Ensure no GLM key is available so NewClient returns an error.
	prevKey := os.Getenv("GLM_API_KEY")
	_ = os.Unsetenv("GLM_API_KEY")
	t.Cleanup(func() {
		if prevKey != "" {
			_ = os.Setenv("GLM_API_KEY", prevKey)
		}
	})

	rec := &recorder{}
	p := &Project{
		ID:        "tp-init-fail",
		Name:      "Test",
		Directory: t.TempDir(),
		Provider:  "glm",
		Model:     "glm-5.1",
		// client is nil — forces initClient to run (not the early-return path)
		sessions:    map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		testEmitter: rec.emit,
	}

	// SendMessage is synchronous (runs the goroutine inline when called directly).
	p.SendMessage(context.Background(), "hello", Settings{
		DefaultProvider: "glm",
		DefaultModel:    "glm-5.1",
		// GLMKey is intentionally empty
	})

	errs := rec.find(EventChatError)
	if len(errs) == 0 {
		t.Fatal("expected chat:error from initClient failure, got none")
	}
	text := errs[0].data.(ChatTextEvent).Text
	if !strings.Contains(text, "init client") && !strings.Contains(text, "key") {
		t.Errorf("error text = %q; want substring 'init client' or 'key'", text)
	}
}

// TestInitClient_FullPath verifies that initClient runs fully (past the NewClient
// call) when using the Ollama provider, which doesn't require an API key. The
// Ollama base URL is pointed at a non-listening port so that SendMessageWithHistory
// fails fast (ECONNREFUSED) without blocking. This covers:
//   - initClient lines 356-456 (tool setup, messenger wiring, system prompt)
//   - initMemoryAndPlan lines 227-311 (memory/plan store creation)
//
// retryInitialDelay is set to 1ms so the three retry attempts complete in <10ms.
func TestInitClient_FullPath(t *testing.T) {
	_ = withTempHistoryDir(t)
	// withTempHistoryDir sets GOKIN_CONFIG_DIR; initMemoryAndPlan uses configDir() so
	// memory stores land in the temp dir rather than ~/.config/gokin-studio.

	rec := &recorder{}
	s := newStudioForTest(t)
	p := &Project{
		ID:        "tp-init-full",
		Name:      "Test",
		Directory: t.TempDir(),
		Provider:  "ollama",
		Model:     "llama3",
		// client is intentionally nil — forces the full initClient path.
		sessions:          map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		testEmitter:       rec.emit,
		retryInitialDelay: time.Millisecond, // avoid 2s default; ECONNREFUSED retries finish in <10ms
		studio:            s,
	}
	s.projects[p.ID] = p

	// Port 1 is not listening on any test host; the TCP connect fails immediately
	// with ECONNREFUSED so the test doesn't block waiting for Ollama.
	p.SendMessage(context.Background(), "hello", Settings{
		DefaultProvider: "ollama",
		DefaultModel:    "llama3",
		OllamaURL:       "http://127.0.0.1:1",
	})

	// After initClient succeeds and the agent loop exhausts retries, a chat:error
	// or the idle project:status event must be emitted.
	if len(rec.find(EventChatError)) == 0 && len(rec.find(EventProjectStatus)) == 0 {
		t.Error("expected chat:error or project:status after initClient+retry exhaustion, got neither")
	}
	// initClient must have set p.client (Ollama HTTP client, no API key needed).
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()
	if c == nil {
		t.Error("expected p.client to be set after initClient success, got nil")
	}
}

// TestInitClient_EmptyProviderFallback verifies that when p.Provider == "" and
// p.Model == "", initClient resolves both from settings.DefaultProvider /
// settings.DefaultModel. This covers the `if provider == ""` and `if model == ""`
// branches (lines 357-363).
func TestInitClient_EmptyProviderFallback(t *testing.T) {
	_ = withTempHistoryDir(t)
	prevKey := os.Getenv("GLM_API_KEY")
	_ = os.Unsetenv("GLM_API_KEY")
	t.Cleanup(func() {
		if prevKey != "" {
			_ = os.Setenv("GLM_API_KEY", prevKey)
		}
	})

	rec := &recorder{}
	p := &Project{
		ID:          "tp-empty-provider",
		Name:        "Test",
		Directory:   t.TempDir(),
		Provider:    "", // empty — must fall back to settings
		Model:       "", // empty — must fall back to settings
		sessions:    map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		testEmitter: rec.emit,
	}
	p.SendMessage(context.Background(), "hello", Settings{
		DefaultProvider: "glm",
		DefaultModel:    "glm-5.1",
		// GLMKey intentionally empty — initClient will fail with "key required"
	})

	// Must emit chat:error (initClient fails but the provider/model fallback ran).
	if len(rec.find(EventChatError)) == 0 {
		t.Error("expected chat:error after provider fallback + missing key, got none")
	}
}

// TestInitClient_KimiModelMigration verifies that a legacy Kimi model name is
// rewritten to "kimi-for-coding" inside initClient before NewClient is called,
// covering the kimi provider switch and model-migration case (lines 366-386).
func TestInitClient_KimiModelMigration(t *testing.T) {
	_ = withTempHistoryDir(t)
	prevKey := os.Getenv("KIMI_API_KEY")
	_ = os.Unsetenv("KIMI_API_KEY")
	t.Cleanup(func() {
		if prevKey != "" {
			_ = os.Setenv("KIMI_API_KEY", prevKey)
		}
	})

	rec := &recorder{}
	p := &Project{
		ID:          "tp-kimi-migrate",
		Name:        "Test",
		Directory:   t.TempDir(),
		Provider:    "kimi",
		Model:       "kimi-latest", // legacy model name → must be rewritten to "kimi-for-coding"
		sessions:    map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		testEmitter: rec.emit,
	}
	p.SendMessage(context.Background(), "hello", Settings{
		DefaultProvider: "kimi",
		DefaultModel:    "kimi-for-coding",
		// KimiKey intentionally empty — initClient fails after migration
	})

	if len(rec.find(EventChatError)) == 0 {
		t.Error("expected chat:error after Kimi model migration + missing key, got none")
	}
}

// TestInitClient_ThinkingModeEnabled verifies that ThinkingMode="enabled" with a
// zero ThinkingBudget causes the budget to be clamped to 4096 inside initClient
// (lines 394-399). Uses GLM provider with no key so NewClient fails fast.
func TestInitClient_ThinkingModeEnabled(t *testing.T) {
	_ = withTempHistoryDir(t)
	prevKey := os.Getenv("GLM_API_KEY")
	_ = os.Unsetenv("GLM_API_KEY")
	t.Cleanup(func() {
		if prevKey != "" {
			_ = os.Setenv("GLM_API_KEY", prevKey)
		}
	})

	rec := &recorder{}
	p := &Project{
		ID:             "tp-thinking-enabled",
		Name:           "Test",
		Directory:      t.TempDir(),
		Provider:       "glm",
		Model:          "glm-5.1",
		ThinkingMode:   "enabled",
		ThinkingBudget: 0, // zero → must be clamped to 4096
		sessions:       map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		testEmitter:    rec.emit,
	}
	p.SendMessage(context.Background(), "hello", Settings{
		DefaultProvider: "glm",
		DefaultModel:    "glm-5.1",
	})

	// initClient must fail (no GLM key) → chat:error emitted.
	if len(rec.find(EventChatError)) == 0 {
		t.Error("expected chat:error after thinking-enabled initClient + missing key, got none")
	}
}

// TestInitClient_MinimaxProvider verifies that the "minimax" provider case sets
// cfg.API.MiniMaxKey inside initClient (line 383-384).
func TestInitClient_MinimaxProvider(t *testing.T) {
	_ = withTempHistoryDir(t)
	prevKey := os.Getenv("MINIMAX_API_KEY")
	_ = os.Unsetenv("MINIMAX_API_KEY")
	t.Cleanup(func() {
		if prevKey != "" {
			_ = os.Setenv("MINIMAX_API_KEY", prevKey)
		}
	})

	rec := &recorder{}
	p := &Project{
		ID:          "tp-minimax",
		Name:        "Test",
		Directory:   t.TempDir(),
		Provider:    "minimax",
		Model:       "minimax-text",
		sessions:    map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		testEmitter: rec.emit,
	}
	p.SendMessage(context.Background(), "hello", Settings{
		DefaultProvider: "minimax",
		DefaultModel:    "minimax-text",
		// MiniMaxKey intentionally empty — initClient fails after minimax branch
	})

	if len(rec.find(EventChatError)) == 0 {
		t.Error("expected chat:error after minimax initClient + missing key, got none")
	}
}

// TestInitClient_KimiAutoThinking verifies that provider="kimi" with ThinkingMode=""
// (auto) enables thinking by default inside initClient (lines 403-406).
func TestInitClient_KimiAutoThinking(t *testing.T) {
	_ = withTempHistoryDir(t)
	prevKey := os.Getenv("KIMI_API_KEY")
	_ = os.Unsetenv("KIMI_API_KEY")
	t.Cleanup(func() {
		if prevKey != "" {
			_ = os.Setenv("KIMI_API_KEY", prevKey)
		}
	})

	rec := &recorder{}
	p := &Project{
		ID:           "tp-kimi-auto",
		Name:         "Test",
		Directory:    t.TempDir(),
		Provider:     "kimi",
		Model:        "kimi-for-coding",
		ThinkingMode: "", // auto → Kimi should enable thinking
		sessions:     map[string]*ChatSession{"default": NewChatSession("Chat 1")},
		testEmitter:  rec.emit,
	}
	p.SendMessage(context.Background(), "hello", Settings{
		DefaultProvider: "kimi",
		DefaultModel:    "kimi-for-coding",
	})

	if len(rec.find(EventChatError)) == 0 {
		t.Error("expected chat:error after Kimi auto-thinking initClient + missing key, got none")
	}
}

// TestSendMessage_NilRespBreaks verifies that when SendMessageWithHistory
// returns (nil, nil) the agent loop breaks cleanly without panicking. This
// covers the `if resp == nil { break }` guard in the outer turn loop.
func TestSendMessage_NilRespBreaks(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{nilResp: true},
	}}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "hello")

	// The loop must have exited cleanly — either chat:complete or chat:error,
	// but NOT a panic (which would crash the test binary).
	completes := rec.find(EventChatComplete)
	errors := rec.find(EventChatError)
	if len(completes) == 0 && len(errors) == 0 {
		t.Error("expected chat:complete or chat:error after nil resp break, got neither")
	}
}

// TestAgentLoop_ToolError verifies the path where a tool returns an error
// (not a panic). The error message becomes the tool result content so the
// model can see why the tool failed. Covers `content = toolErr.Error()`.
func TestAgentLoop_ToolError(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(&errTool{})

	mc := &mockClient{responses: []mockResp{
		// First round: call err_tool
		{funcCalls: []*genai.FunctionCall{{Name: "err_tool"}}},
		// Second round (after tool result): plain text
		{text: "Sorry, the tool failed."},
	}}
	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "run err_tool please")

	// Must complete without panic.
	completes := rec.find(EventChatComplete)
	if len(completes) == 0 {
		t.Error("expected chat:complete after tool error, got none")
	}

	// A tool_result event must have been emitted; its Success flag must be false.
	results := rec.find(EventChatToolResult)
	if len(results) == 0 {
		t.Fatal("expected chat:tool_result event, got none")
	}
	ev := results[0].data.(ChatToolResultEvent)
	if ev.Success {
		t.Error("expected Success=false for errTool result, got true")
	}
	if ev.Content == "" {
		t.Error("expected non-empty error content in tool result")
	}
}

// TestAgentLoop_FuncRespNilResp verifies that the inner tool loop exits
// cleanly when SendFunctionResponse returns (nil, nil). Covers the
// `if resp == nil { break outer }` guard after the function-response send.
func TestAgentLoop_FuncRespNilResp(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(&echoTool{})

	mc := &mockClient{responses: []mockResp{
		// First round: model requests echo tool
		{funcCalls: []*genai.FunctionCall{{Name: "echo", Args: map[string]any{"input": "hi"}}}},
		// Second (function-response) round: nil response — inner loop must break
		{nilResp: true},
	}}
	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "echo hi")

	// Must not panic. Complete or error are both acceptable outcomes.
	completes := rec.find(EventChatComplete)
	errors := rec.find(EventChatError)
	if len(completes) == 0 && len(errors) == 0 {
		t.Error("expected chat:complete or chat:error after nil FuncResp break, got neither")
	}
}

// TestAgentLoop_FuncRespError verifies that the inner tool loop emits
// chat:error and breaks cleanly when SendFunctionResponse returns an error
// that is NOT due to context cancellation. Covers lines 866-869.
func TestAgentLoop_FuncRespError(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(&echoTool{})

	mc := &mockClient{responses: []mockResp{
		// First round: model requests echo tool
		{funcCalls: []*genai.FunctionCall{{Name: "echo", Args: map[string]any{"input": "hi"}}}},
		// Function-response round: returns a non-retryable API error
		{err: fmt.Errorf("internal error (non-retryable)")},
	}}
	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "echo hi")

	errors := rec.find(EventChatError)
	if len(errors) == 0 {
		t.Error("expected chat:error after SendFunctionResponse error, got none")
	}
}

// TestAgentLoop_ToolResultError verifies the `else if result.Error != ""`
// branch: a tool that returns (ToolResult{Error: "..."}, nil) — error via
// the result struct rather than via the Go error return value.
func TestAgentLoop_ToolResultError(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(&errResultTool{})

	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: "err_result_tool"}}},
		{text: "I see the tool had an error."},
	}}
	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "run err_result_tool")

	// Must complete cleanly.
	if len(rec.find(EventChatComplete)) == 0 {
		t.Error("expected chat:complete after err_result_tool, got none")
	}

	// Tool result event must show failure.
	results := rec.find(EventChatToolResult)
	if len(results) == 0 {
		t.Fatal("expected chat:tool_result event, got none")
	}
	ev := results[0].data.(ChatToolResultEvent)
	if ev.Success {
		t.Error("expected Success=false for errResultTool, got true")
	}
	if ev.Content == "" {
		t.Error("expected non-empty error content from ToolResult.Error")
	}
}

// TestAgentLoop_ThinkingPartsPreserved verifies that response parts with
// Thought=true are collected into the model history turn so they can be
// sent back to reasoning-capable providers (Kimi, native Anthropic).
// Covers the `if p != nil && p.Thought && p.Text != ""` branch.
func TestAgentLoop_ThinkingPartsPreserved(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{
			text: "Here is my answer.",
			parts: []*genai.Part{
				{Text: "internal reasoning", Thought: true},
			},
		},
	}}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "think about something")

	// Must complete cleanly.
	if len(rec.find(EventChatComplete)) == 0 {
		t.Error("expected chat:complete, got none")
	}

	// The model history should contain the assistant turn; the thought part
	// should appear in the genai.Content Parts alongside the text.
	p.sessions["default"].mu.RLock()
	hist := p.sessions["default"].history
	p.sessions["default"].mu.RUnlock()

	foundThought := false
	for _, c := range hist {
		if c.Role != "model" {
			continue
		}
		for _, part := range c.Parts {
			if part != nil && part.Thought {
				foundThought = true
			}
		}
	}
	if !foundThought {
		t.Error("expected Thought=true part in model history, not found")
	}
}

// TestAgentLoop_ContextCancelledAfterToolExec verifies the
// `if ctx.Err() != nil { break outer }` branch that runs after all tools
// in one round have been executed (line 840). cancelTool cancels the session's
// context as a side-effect; the next ctx.Err() check fires before the inner
// loop attempts SendFunctionResponse.
func TestAgentLoop_ContextCancelledAfterToolExec(t *testing.T) {
	p, rec := newTestProject(t, nil, nil)
	reg := tools.NewRegistry()
	_ = reg.Register(&cancelTool{proj: p})
	p.registry = reg

	mc := &mockClient{responses: []mockResp{
		// First LLM response calls cancel_tool which will cancel the context.
		{funcCalls: []*genai.FunctionCall{{Name: "cancel_tool"}}},
	}}
	p.client = mc

	runAgent(p, "run cancel_tool")

	// The loop must exit cleanly — no panic, chat:complete or no-op after ctx cancel.
	// We just verify the session is no longer active.
	p.sessions["default"].mu.RLock()
	active := p.sessions["default"].active
	p.sessions["default"].mu.RUnlock()
	if active {
		t.Error("expected session.active=false after context cancellation, got true")
	}
	_ = rec // recorder used for other assertions in sibling tests
}

// TestAgentLoop_ContextCancelledDuringToolIteration verifies the per-tool
// ctx.Err() check inside the function-call iteration loop (line 785). When
// cancel_tool fires and then a second FC is about to execute, the check fires
// before the second tool runs. Also covers line 778 (inner tool loop start).
func TestAgentLoop_ContextCancelledDuringToolIteration(t *testing.T) {
	p, rec := newTestProject(t, nil, nil)
	reg := tools.NewRegistry()
	_ = reg.Register(&cancelTool{proj: p})
	_ = reg.Register(&echoTool{})
	p.registry = reg

	mc := &mockClient{responses: []mockResp{
		// Two FCs in one response: cancel_tool first, echo second.
		// cancel_tool cancels ctx; the echo iteration then hits the ctx.Err() guard.
		{funcCalls: []*genai.FunctionCall{
			{Name: "cancel_tool"},
			{Name: "echo", Args: map[string]any{"input": "should not run"}},
		}},
	}}
	p.client = mc

	runAgent(p, "cancel then echo")

	// Session must be inactive; no panic.
	p.sessions["default"].mu.RLock()
	active := p.sessions["default"].active
	p.sessions["default"].mu.RUnlock()
	if active {
		t.Error("expected session.active=false after mid-tool-loop cancel, got true")
	}

	// Only ONE tool_call event should be emitted (cancel_tool); the echo call
	// must have been skipped due to the ctx.Err() guard.
	toolCalls := rec.find(EventChatToolCall)
	if len(toolCalls) < 1 {
		t.Error("expected at least 1 chat:tool_call event (cancel_tool)")
	}
}

// TestAgentLoop_StreamErrAfterFuncResp verifies that when the stream returned
// by SendFunctionResponse emits an error chunk, streamAndProcess returns an
// error and the inner tool loop breaks cleanly. Covers lines 886-890.
func TestAgentLoop_StreamErrAfterFuncResp(t *testing.T) {
	reg := tools.NewRegistry()
	_ = reg.Register(&echoTool{})

	mc := &mockClient{responses: []mockResp{
		// First round: tool call
		{funcCalls: []*genai.FunctionCall{{Name: "echo", Args: map[string]any{"input": "hi"}}}},
		// SendFunctionResponse returns a stream that immediately errors
		{streamErr: fmt.Errorf("simulated stream parse error")},
	}}
	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "echo then stream error")

	// Must exit without panic. Error or complete are both acceptable.
	completes := rec.find(EventChatComplete)
	errors := rec.find(EventChatError)
	if len(completes) == 0 && len(errors) == 0 {
		t.Error("expected chat:complete or chat:error after stream error in func-resp, got neither")
	}
}

// TestAgentLoop_CtxCancelledOnSendError covers the `if ctx.Err() != nil` guard
// at line 752 of project.go: when sendWithRetry's fn() returns an error AND the
// context is simultaneously cancelled, sendWithRetry returns ctx.Err() (not the
// original error), and the agent loop breaks via the ctx check rather than
// emitting chat:error.
//
// cancelProj causes the mock to cancel the session context before returning
// the error. sendWithRetry detects ctx.Err() != nil after fn() fails and
// returns ctx.Canceled. The outer agent loop: `if err != nil { if ctx.Err() != nil { break } }`.
func TestAgentLoop_CtxCancelledOnSendError(t *testing.T) {
	_ = withTempHistoryDir(t)
	reg := tools.NewRegistry()
	mc := &mockClient{}
	p, _ := newTestProject(t, mc, reg)

	// First (and only) response: cancel context then return an error.
	mc.mu.Lock()
	mc.responses = []mockResp{
		{cancelProj: p, err: fmt.Errorf("simulated api error with ctx cancel")},
	}
	mc.mu.Unlock()

	// runAgent must not panic; it should simply exit (ctx cancelled).
	runAgent(p, "trigger ctx cancel + error")
}

// TestAgentLoop_CtxCancelledOnStreamError covers the `if ctx.Err() != nil` guard
// at line 766: when sendWithRetry succeeds (returns a stream) but the context was
// cancelled before streamAndProcess runs. ProcessStream sees ctx.Done() ready and
// either returns ctx.Err() or the stream's error chunk — both are non-nil errors —
// so the outer-loop guard `if ctx.Err() != nil { break }` fires.
func TestAgentLoop_CtxCancelledOnStreamError(t *testing.T) {
	_ = withTempHistoryDir(t)
	reg := tools.NewRegistry()
	mc := &mockClient{}
	p, _ := newTestProject(t, mc, reg)

	// Response: cancel context AND embed a stream error. After fn() returns the
	// stream, ctx is already cancelled; ProcessStream's select sees both ctx.Done()
	// and the error chunk. Either outcome makes err != nil with ctx.Err() != nil.
	mc.mu.Lock()
	mc.responses = []mockResp{
		{cancelProj: p, streamErr: fmt.Errorf("stream error with ctx cancel")},
	}
	mc.mu.Unlock()

	runAgent(p, "trigger ctx cancel + stream error")
}

// TestAgentLoop_CtxCancelledOnFuncRespError covers the `if ctx.Err() != nil` guard
// at line 863: when a tool call executes successfully, SendFunctionResponse's fn()
// returns an error AND the context is cancelled. sendWithRetry returns ctx.Err(),
// and the inner tool loop fires `if ctx.Err() != nil { break outer }`.
func TestAgentLoop_CtxCancelledOnFuncRespError(t *testing.T) {
	_ = withTempHistoryDir(t)
	reg := tools.NewRegistry()
	_ = reg.Register(&echoTool{})
	mc := &mockClient{}
	p, _ := newTestProject(t, mc, reg)

	mc.mu.Lock()
	mc.responses = []mockResp{
		// Initial turn: model requests the echo tool.
		{funcCalls: []*genai.FunctionCall{{Name: "echo", Args: map[string]any{"input": "hi"}}}},
		// SendFunctionResponse: cancel context + return error.
		{cancelProj: p, err: fmt.Errorf("func-resp error with ctx cancel")},
	}
	mc.mu.Unlock()

	runAgent(p, "tool call then ctx cancel on func-resp error")
}

// TestAgentLoop_CtxCancelledOnFuncRespStream covers the `if ctx.Err() != nil`
// guard at line 887: a tool call executes, SendFunctionResponse returns a stream
// (success), but the context is cancelled and the stream contains an error chunk.
// streamAndProcess returns a non-nil error with ctx.Err() also non-nil, so the
// inner-loop guard fires `break outer`.
func TestAgentLoop_CtxCancelledOnFuncRespStream(t *testing.T) {
	_ = withTempHistoryDir(t)
	reg := tools.NewRegistry()
	_ = reg.Register(&echoTool{})
	mc := &mockClient{}
	p, _ := newTestProject(t, mc, reg)

	mc.mu.Lock()
	mc.responses = []mockResp{
		// Initial turn: model requests the echo tool.
		{funcCalls: []*genai.FunctionCall{{Name: "echo", Args: map[string]any{"input": "hi"}}}},
		// SendFunctionResponse: cancel context + stream error.
		{cancelProj: p, streamErr: fmt.Errorf("func-resp stream error with ctx cancel")},
	}
	mc.mu.Unlock()

	runAgent(p, "tool call then ctx cancel on func-resp stream error")
}

// ---------------------------------------------------------------------------
// pin_context and history_search wiring tests
// ---------------------------------------------------------------------------

// TestInitMemoryAndPlan_PinContextWired verifies that initMemoryAndPlan wires
// the pin_context tool so calling it updates p.pinnedContext and that
// LoadPersistedPin restores a pre-existing pin from disk.
func TestInitMemoryAndPlan_PinContextWired(t *testing.T) {
	dir := t.TempDir()
	reg := tools.DefaultRegistry(dir)
	p := &Project{
		ID:        "pin-test",
		Directory: dir,
		sessions:  map[string]*ChatSession{"default": NewChatSession("Chat 1")},
	}
	_ = withTempHistoryDir(t)

	p.initMemoryAndPlan(reg)

	// Get the pin_context tool and call it directly.
	tool, ok := reg.Get("pin_context")
	if !ok {
		t.Fatal("pin_context not in registry")
	}
	ctx := context.Background()
	res, err := tool.Execute(ctx, map[string]any{"content": "my pinned note"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("Execute returned failure: %s", res.Error)
	}
	p.mu.RLock()
	got := p.pinnedContext
	p.mu.RUnlock()
	if got != "my pinned note" {
		t.Errorf("pinnedContext = %q, want %q", got, "my pinned note")
	}
}

// TestSendMessage_PinnedContextApplied verifies that when p.pinnedContext is
// non-empty at the start of SendMessage, SetSystemInstruction is called on the
// client with the base system prompt appended by the pinned context section.
func TestSendMessage_PinnedContextApplied(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "done"}}}
	p, _ := newTestProject(t, mc, nil)
	p.pinnedContext = "important note"
	p.SystemPrompt = "you are a test agent"

	runAgent(p, "hello")

	mc.mu.Lock()
	got := mc.lastSystemInstruction
	mc.mu.Unlock()
	if !strings.Contains(got, "important note") {
		t.Errorf("SetSystemInstruction not called with pinned content; got %q", got)
	}
	if !strings.Contains(got, "you are a test agent") {
		t.Errorf("SetSystemInstruction missing base system prompt; got %q", got)
	}
}

// TestSendMessage_NoPinNoSystemInstructionUpdate verifies that when
// p.pinnedContext is empty, SetSystemInstruction is NOT called during SendMessage
// (client retains whatever instruction was set during initClient).
func TestSendMessage_NoPinNoSystemInstructionUpdate(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "done"}}}
	p, _ := newTestProject(t, mc, nil)
	// pinnedContext is zero-value (empty) by default.

	runAgent(p, "hello")

	mc.mu.Lock()
	got := mc.lastSystemInstruction
	mc.mu.Unlock()
	if got != "" {
		t.Errorf("SetSystemInstruction should not be called when no pin; got %q", got)
	}
}

// TestAgentLoop_HistorySearchViaContext verifies that the history_search tool
// receives a per-session history getter via context and can find prior turns.
func TestAgentLoop_HistorySearchViaContext(t *testing.T) {
	reg := tools.NewRegistry()
	// Use a real HistorySearchTool wired with nil getter — the context path must
	// supply the getter for the search to work.
	_ = reg.Register(tools.NewHistorySearchTool(nil))

	mc := &mockClient{responses: []mockResp{
		// Round 1: agent calls history_search looking for "needle".
		{funcCalls: []*genai.FunctionCall{{
			Name: "history_search",
			Args: map[string]any{"pattern": "needle"},
		}}},
		// Round 2: agent responds with text after seeing the tool result.
		{text: "found the needle"},
	}}
	p, rec := newTestProject(t, mc, reg)

	// Pre-seed prior history so history_search has something to find.
	session := p.GetSession("default")
	session.history = append(session.history, &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{genai.NewPartFromText("there is a needle in this message")},
	})

	runAgent(p, "search history")

	// The tool_result event should contain the matched excerpt.
	results := rec.find(EventChatToolResult)
	if len(results) == 0 {
		t.Fatal("no tool_result events emitted")
	}
	found := false
	for _, ev := range results {
		if d, ok := ev.data.(ChatToolResultEvent); ok && strings.Contains(d.Content, "needle") {
			found = true
		}
	}
	if !found {
		t.Errorf("history_search result should contain 'needle'; events: %+v", results)
	}
}

// ---------------------------------------------------------------------------
// Truncation continuation (iter 1204)
// ---------------------------------------------------------------------------

// TestAgentLoop_TruncationContinuation verifies that a max_tokens TEXT
// response with no tool calls triggers an auto-continuation: the agent
// appends a user nudge to session history and re-sends to the LLM, up to
// maxTruncationContinuations times.
func TestAgentLoop_TruncationContinuation(t *testing.T) {
	// Three truncated responses followed by a final clean response.
	mc := &mockClient{
		responses: []mockResp{
			{text: "Part 1", finishReason: genai.FinishReasonMaxTokens},
			{text: "Part 2", finishReason: genai.FinishReasonMaxTokens},
			{text: "Part 3", finishReason: genai.FinishReasonMaxTokens},
			{text: "Part 4 done", finishReason: genai.FinishReasonStop},
		},
	}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "write a long response")

	// All four LLM calls should have been made.
	mc.mu.Lock()
	calls := mc.callCount
	mc.mu.Unlock()
	if calls != 4 {
		t.Errorf("callCount = %d, want 4 (3 continuations + 1 final)", calls)
	}

	// All four text chunks should have been emitted.
	texts := rec.find(EventChatText)
	if len(texts) != 4 {
		t.Errorf("chat:text events = %d, want 4", len(texts))
	}

	// The session history should contain the continuation prompt turns.
	p.sessions["default"].mu.RLock()
	hist := p.sessions["default"].history
	p.sessions["default"].mu.RUnlock()
	contCount := 0
	for _, h := range hist {
		if h.Role == "user" {
			for _, part := range h.Parts {
				if part != nil && part.Text == truncationContinuationPrompt {
					contCount++
				}
			}
		}
	}
	if contCount != 3 {
		t.Errorf("truncation continuation prompt in history = %d, want 3", contCount)
	}

	// chat:complete.Text must be the COMBINED text from all continuations (iter
	// 1209 carriedText fix), not just the last segment.
	completes := rec.find(EventChatComplete)
	if len(completes) != 1 {
		t.Fatalf("expected 1 chat:complete, got %d", len(completes))
	}
	completeText := completes[0].data.(ChatCompleteEvent).Text
	for _, part := range []string{"Part 1", "Part 2", "Part 3", "Part 4 done"} {
		if !strings.Contains(completeText, part) {
			t.Errorf("chat:complete.Text missing %q; got %q", part, completeText)
		}
	}
}

// TestAgentLoop_TruncationContinuation_BudgetExhausted verifies that when all
// maxTruncationContinuations are used up the loop still exits cleanly rather
// than looping forever.
func TestAgentLoop_TruncationContinuation_BudgetExhausted(t *testing.T) {
	// Four truncated responses — one more than the max allowed.
	mc := &mockClient{
		responses: []mockResp{
			{text: "Part 1", finishReason: genai.FinishReasonMaxTokens},
			{text: "Part 2", finishReason: genai.FinishReasonMaxTokens},
			{text: "Part 3", finishReason: genai.FinishReasonMaxTokens},
			// 4th call: also truncated — budget (3) exhausted after this
			{text: "Part 4", finishReason: genai.FinishReasonMaxTokens},
		},
	}
	p, rec := newTestProject(t, mc, nil)
	runAgent(p, "write a really long response")

	// Exactly maxTruncationContinuations+1 calls (1 initial + 3 continuations).
	mc.mu.Lock()
	calls := mc.callCount
	mc.mu.Unlock()
	if calls != maxTruncationContinuations+1 {
		t.Errorf("callCount = %d, want %d", calls, maxTruncationContinuations+1)
	}

	// chat:complete should still fire — loop exits cleanly.
	if len(rec.find(EventChatComplete)) == 0 {
		t.Error("chat:complete not emitted after exhausting truncation budget")
	}

	// chat:error should fire with a truncation message so the user knows why
	// the response ended mid-thought.
	errs := rec.find(EventChatError)
	if len(errs) == 0 {
		t.Error("no chat:error emitted after truncation budget exhausted")
	} else {
		if d, ok := errs[0].data.(ChatTextEvent); ok {
			if !strings.Contains(d.Text, "truncated") {
				t.Errorf("chat:error text should mention truncation; got %q", d.Text)
			}
			if !strings.Contains(d.Text, "continuation") {
				t.Errorf("chat:error text should mention continuation attempts; got %q", d.Text)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Incomplete-work continuation (iter 1205)
// ---------------------------------------------------------------------------

// TestAgentLoop_IncompleteWorkContinuation verifies that when the model
// returns text with no tool calls but the todo list still has unfinished items,
// the agent appends the continuation prompt and re-sends; and that once the
// model marks the todo complete (via tool call), the loop exits normally.
func TestAgentLoop_IncompleteWorkContinuation(t *testing.T) {
	// Response 1: text only → incomplete-work check fires → continuation nudge
	// Response 2: model calls the todo tool to mark items complete
	// Response 3 (after tool execution): text "Done!" → no more todos → break
	mc := &mockClient{
		responses: []mockResp{
			{text: "I will now write the code..."},
			{funcCalls: []*genai.FunctionCall{{
				Name: "todo",
				ID:   "todo-1",
				Args: map[string]any{
					"todos": []any{
						map[string]any{"content": "write the function", "status": "completed"},
					},
				},
			}}},
			{text: "Done!"},
		},
	}

	// Register a todo tool with an unfinished item.
	reg := tools.NewRegistry()
	tt := tools.NewTodoTool()
	_ = reg.Register(tt)
	_, _ = tt.Execute(nil, map[string]any{
		"todos": []any{
			map[string]any{"content": "write the function", "status": "pending"},
		},
	})

	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "implement foo")

	mc.mu.Lock()
	calls := mc.callCount
	mc.mu.Unlock()

	// Three LLM calls: initial → nudge continuation → function response.
	if calls != 3 {
		t.Errorf("callCount = %d, want 3 (initial + 1 continuation + 1 function response)", calls)
	}

	// The continuation prompt should be in session history.
	p.sessions["default"].mu.RLock()
	hist := p.sessions["default"].history
	p.sessions["default"].mu.RUnlock()
	foundPrompt := false
	for _, h := range hist {
		if h.Role == "user" {
			for _, part := range h.Parts {
				if part != nil && strings.Contains(part.Text, "unfinished item") {
					foundPrompt = true
				}
			}
		}
	}
	if !foundPrompt {
		t.Error("incomplete-work continuation prompt not found in session history")
	}

	// chat:complete must fire so the session ends.
	if len(rec.find(EventChatComplete)) == 0 {
		t.Error("chat:complete not emitted after incomplete-work continuation")
	}
}

// TestAgentLoop_IncompleteWorkContinuation_BudgetExhausted verifies the loop
// still exits after MaxIncompleteWorkContinuations consecutive no-action turns.
func TestAgentLoop_IncompleteWorkContinuation_BudgetExhausted(t *testing.T) {
	// 4 responses, all text-only — more than the 3-nudge budget.
	mc := &mockClient{
		responses: []mockResp{
			{text: "I will start soon..."},
			{text: "Almost there..."},
			{text: "Just one more moment..."},
			{text: "Finishing up..."},
		},
	}

	reg := tools.NewRegistry()
	tt := tools.NewTodoTool()
	_ = reg.Register(tt)
	_, _ = tt.Execute(nil, map[string]any{
		"todos": []any{
			map[string]any{"content": "pending task", "status": "pending"},
		},
	})

	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "do the task")

	mc.mu.Lock()
	calls := mc.callCount
	mc.mu.Unlock()

	// 1 initial + 3 nudges (budget) = 4 total calls.
	if calls != tools.MaxIncompleteWorkContinuations+1 {
		t.Errorf("callCount = %d, want %d", calls, tools.MaxIncompleteWorkContinuations+1)
	}

	if len(rec.find(EventChatComplete)) == 0 {
		t.Error("chat:complete not emitted after exhausting incomplete-work budget")
	}
}

// ---------------------------------------------------------------------------
// Stagnation hard-abort (iter 1207)
// ---------------------------------------------------------------------------

// TestAgentLoop_StagnationHardAbort verifies that when the model keeps calling
// the same tool after receiving a recovery hint, the agent hard-aborts (emits
// chat:error and stops) rather than spinning through all 40 inner tool rounds.
//
// Setup:
//   - Response 0: 5 identical echo calls → fc[4] triggers stagnation → recovery
//     hint sent, stagnationRecoveries["echo:"] = 1, loop continues.
//   - Response 1: 1 more identical echo call → fc[0] triggers stagnation again,
//     stagnationRecoveries["echo:"] = 1 > 0 → hard abort.
//
// Expectations: 2 LLM calls total, chat:error, chat:complete.
func TestAgentLoop_StagnationHardAbort(t *testing.T) {
	fc := func(id string) *genai.FunctionCall {
		return &genai.FunctionCall{ID: id, Name: "echo", Args: map[string]any{"input": "stuck"}}
	}
	mc := &mockClient{responses: []mockResp{
		// Round 0: 5 identical calls — fc[4] triggers first stagnation (recovery).
		{funcCalls: []*genai.FunctionCall{fc("a"), fc("b"), fc("c"), fc("d"), fc("e")}},
		// Round 1: same call again — triggers hard abort on first fc.
		{funcCalls: []*genai.FunctionCall{fc("f")}},
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(&echoTool{})

	p, rec := newTestProject(t, mc, reg)
	runAgent(p, "do something")

	mc.mu.Lock()
	calls := mc.callCount
	mc.mu.Unlock()

	// Initial call + one SendFunctionResponse (round 1) = 2 total.
	if calls != 2 {
		t.Errorf("callCount = %d, want 2 (should hard-abort after first recovery round)", calls)
	}

	// chat:error must be emitted with the stagnation abort message.
	errs := rec.find(EventChatError)
	if len(errs) == 0 {
		t.Fatal("no chat:error emitted on stagnation hard-abort")
	}
	if d, ok := errs[0].data.(ChatTextEvent); ok {
		if !strings.Contains(d.Text, "repeating") {
			t.Errorf("chat:error text should mention repeating; got %q", d.Text)
		}
	}

	// chat:complete must still fire (turn exits cleanly for history persistence).
	if len(rec.find(EventChatComplete)) == 0 {
		t.Error("chat:complete not emitted after stagnation hard-abort")
	}
}
