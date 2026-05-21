package studio

import (
	"strings"
	"testing"
)

// TestSetProjectEnforceBudget_RoundTrip verifies the flag persists through
// ToConfig + NewProject (restart-style round-trip) and is exposed via
// ProjectInfo.
func TestSetProjectEnforceBudget_RoundTrip(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "budget-enf")
	if info.EnforceBudget {
		t.Fatal("default EnforceBudget should be false")
	}
	if err := s.SetProjectEnforceBudget(info.ID, true); err != nil {
		t.Fatalf("SetProjectEnforceBudget(true): %v", err)
	}
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if !got.EnforceBudget {
		t.Error("expected EnforceBudget=true after Set")
	}
	// Restart round-trip via ToConfig + NewProject.
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	cfg := p.ToConfig()
	if !cfg.EnforceBudget {
		t.Error("ToConfig dropped EnforceBudget")
	}
	rebuilt := NewProject(cfg)
	if !rebuilt.EnforceBudget {
		t.Error("NewProject did not restore EnforceBudget from config")
	}
	// Flip off again.
	if err := s.SetProjectEnforceBudget(info.ID, false); err != nil {
		t.Fatalf("SetProjectEnforceBudget(false): %v", err)
	}
	got, _ = s.GetProject(info.ID)
	if got.EnforceBudget {
		t.Error("expected EnforceBudget=false after Set(false)")
	}
}

// TestSetProjectEnforceBudget_UnknownProject rejects with a clean error
// rather than silently no-op'ing.
func TestSetProjectEnforceBudget_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	err := s.SetProjectEnforceBudget("nope", true)
	if err == nil {
		t.Fatal("expected error for unknown project")
	}
}

// TestSendMessage_RespectsEnforceBudget is the central regression guard:
// once cumulative cost reaches the budget AND EnforceBudget is on,
// SendMessage refuses with a chat:error mentioning the budget. Without
// EnforceBudget OR without a positive BudgetUSD, the turn proceeds.
func TestSendMessage_RespectsEnforceBudget(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "hello"}}}
	p, rec := newTestProject(t, mc, nil)
	// Seed the cache directly so we don't need to actually run prior turns.
	p.cachedTotalCostUSD = 5.50
	p.costSeeded = true
	p.BudgetUSD = 5.00
	p.EnforceBudget = true

	runAgent(p, "spend more")

	errs := rec.find(EventChatError)
	if len(errs) == 0 {
		t.Fatal("expected chat:error when over budget with enforcement on")
	}
	te, _ := errs[0].data.(ChatTextEvent)
	if !strings.Contains(te.Text, "Budget reached") {
		t.Errorf("expected 'Budget reached' in error text, got %q", te.Text)
	}
	// The actual model call must NOT have happened: mock client tracks
	// callCount; verify it's still 0.
	if mc.callCount != 0 {
		t.Errorf("expected no LLM calls when over budget, got callCount=%d", mc.callCount)
	}
}

// TestSendMessage_AllowsWhenUnderBudget: with the cache below budget, the
// turn proceeds normally.
func TestSendMessage_AllowsWhenUnderBudget(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "ok"}}}
	p, rec := newTestProject(t, mc, nil)
	p.cachedTotalCostUSD = 2.00
	p.costSeeded = true
	p.BudgetUSD = 5.00
	p.EnforceBudget = true

	runAgent(p, "go")

	if len(rec.find(EventChatError)) != 0 {
		t.Error("did not expect chat:error when under budget")
	}
	if len(rec.find(EventChatText)) == 0 {
		t.Error("expected chat:text when under budget")
	}
}

// TestSendMessage_NoEnforceProceedsOverBudget: with EnforceBudget off, the
// turn still proceeds even if cost is already past budget. Verifies the
// opt-in semantics.
func TestSendMessage_NoEnforceProceedsOverBudget(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "ok"}}}
	p, rec := newTestProject(t, mc, nil)
	p.cachedTotalCostUSD = 999.99
	p.costSeeded = true
	p.BudgetUSD = 1.00
	p.EnforceBudget = false // explicit

	runAgent(p, "go")

	if len(rec.find(EventChatError)) != 0 {
		t.Error("did not expect chat:error with enforcement off")
	}
}

// TestSendMessage_NoBudgetSetEnforceIgnored: enforcement on but BudgetUSD=0
// (the default) should not block. Defends against a user toggling
// enforcement on before setting a budget.
func TestSendMessage_NoBudgetSetEnforceIgnored(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "ok"}}}
	p, rec := newTestProject(t, mc, nil)
	p.cachedTotalCostUSD = 100.00
	p.costSeeded = true
	p.BudgetUSD = 0 // no budget set
	p.EnforceBudget = true

	runAgent(p, "go")

	if len(rec.find(EventChatError)) != 0 {
		t.Error("did not expect chat:error without a budget set")
	}
}

// TestBumpTotalCostUSD_AccumulatesOverTurns: simulates two turns each
// adding $0.50; verifies the cache grows accordingly without needing the
// agent loop.
func TestBumpTotalCostUSD_AccumulatesOverTurns(t *testing.T) {
	p, _ := newTestProject(t, &mockClient{}, nil)
	p.costSeeded = true // skip the lazy disk-walk seed
	p.bumpTotalCostUSD(0.50)
	if p.cachedTotalCostUSD != 0.50 {
		t.Errorf("after first bump: got %.4f, want 0.50", p.cachedTotalCostUSD)
	}
	p.bumpTotalCostUSD(0.30)
	if p.cachedTotalCostUSD != 0.80 {
		t.Errorf("after second bump: got %.4f, want 0.80", p.cachedTotalCostUSD)
	}
	// Zero/negative deltas should be ignored (cheap defense against
	// accidental sign flips).
	p.bumpTotalCostUSD(0)
	p.bumpTotalCostUSD(-1.50)
	if p.cachedTotalCostUSD != 0.80 {
		t.Errorf("zero/negative bumps modified cache: got %.4f, want 0.80", p.cachedTotalCostUSD)
	}
}
