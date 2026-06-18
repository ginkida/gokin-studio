package studio

import (
	"strings"
	"testing"
)

// GLM caches the system+tools PREFIX implicitly server-side (no cache_control
// markers — verified live against api.z.ai/api/anthropic). The cache hits only
// while that prefix stays byte-for-byte identical across turns; any per-turn
// mutation of the system instruction re-bills the whole prefix. These tests
// lock the agent loop so a future change can't silently reintroduce drift.

// TestGLMCachePrefix_NotMutatedPerTurnInAutoMode verifies that in the common
// case (auto permission mode, no pinned context) the agent loop never re-applies
// the system instruction per turn — the init-time prefix stands untouched, so
// the implicit GLM cache keeps hitting across a multi-turn session.
func TestGLMCachePrefix_NotMutatedPerTurnInAutoMode(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{text: "turn 1"}, {text: "turn 2"}, {text: "turn 3"},
	}}
	p, _ := newTestProject(t, mc, nil) // GLM provider, auto perm mode, no pin
	runAgent(p, "first")
	runAgent(p, "second")
	runAgent(p, "third")

	mc.mu.Lock()
	calls := append([]string(nil), mc.systemInstructionCalls...)
	mc.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("system instruction re-applied %d time(s) in auto mode — busts the GLM prefix cache; calls=%q",
			len(calls), calls)
	}
}

// TestGLMCachePrefix_StableAcrossTurnsInAskMode verifies that when the loop DOES
// re-apply the system instruction every turn (ask mode re-asserts the directive),
// the bytes are identical turn-to-turn, so the cached prefix is still stable.
func TestGLMCachePrefix_StableAcrossTurnsInAskMode(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{text: "turn 1"}, {text: "turn 2"}, {text: "turn 3"},
	}}
	p, _ := newTestProject(t, mc, nil)
	p.PermissionMode = "ask"
	p.SystemPrompt = "you are a test agent"

	runAgent(p, "first")
	runAgent(p, "second")
	runAgent(p, "third")

	mc.mu.Lock()
	calls := append([]string(nil), mc.systemInstructionCalls...)
	mc.mu.Unlock()
	if len(calls) < 2 {
		t.Fatalf("expected the system instruction re-applied each turn in ask mode, got %d call(s)", len(calls))
	}
	first := calls[0]
	for i, c := range calls {
		if c != first {
			t.Errorf("system instruction drifted at turn %d — busts the GLM prefix cache:\n  turn0=%q\n  turn%d=%q",
				i, first, i, c)
		}
	}
}

// TestGLMCachePrefix_PinnedContextNeverInSystemInstruction locks the batch-25
// invariant from the cache angle: pinned context must travel via SetTurnContext
// (appended to the last user message, OUTSIDE the cached prefix), never injected
// into the system instruction where it would re-bill the prefix on every change.
func TestGLMCachePrefix_PinnedContextNeverInSystemInstruction(t *testing.T) {
	mc := &mockClient{responses: []mockResp{
		{text: "turn 1"}, {text: "turn 2"},
	}}
	p, _ := newTestProject(t, mc, nil)
	p.SystemPrompt = "you are a test agent"

	// Turn 1 with no pin, then pin changes for turn 2 — the kind of change that
	// would bust the cache if it landed in the system prefix.
	runAgent(p, "first")
	p.pinnedContext = "deploy key is in vault"
	runAgent(p, "second")

	mc.mu.Lock()
	siCalls := append([]string(nil), mc.systemInstructionCalls...)
	tcCalls := append([]string(nil), mc.turnContextCalls...)
	mc.mu.Unlock()

	for i, c := range siCalls {
		if strings.Contains(c, "deploy key") {
			t.Errorf("pinned context leaked into system instruction (call %d) — busts the GLM cache: %q", i, c)
		}
	}
	// The pin must have reached turn context on turn 2.
	foundPin := false
	for _, c := range tcCalls {
		if strings.Contains(c, "deploy key") {
			foundPin = true
		}
	}
	if !foundPin {
		t.Errorf("pinned context never delivered via SetTurnContext; turnContextCalls=%q", tcCalls)
	}
}
