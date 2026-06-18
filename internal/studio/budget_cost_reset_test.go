package studio

import "testing"

// TestClearHistory_ResetsUsageAndCostCache is the regression for the reliability
// re-audit finding: the project cost cache is otherwise monotonic-increasing, so
// a strict-budget block would stay stuck even after the user clears a session.
// Clearing a session's history zeroes its usage, and the cost cache must
// re-derive so the cumulative cost reflects the reduction.
func TestClearHistory_ResetsUsageAndCostCache(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-cost-clear", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Give the default session recorded usage and prime the cost cache (as a
	// budget pre-flight check would).
	sess := p.GetSession("default")
	sess.mu.Lock()
	sess.usage = &SessionUsage{TotalCostUSD: 5.0, TurnCount: 3}
	sess.mu.Unlock()
	if got := p.totalCostUSD(); got != 5.0 {
		t.Fatalf("primed cost = %.4f, want 5.0", got)
	}

	if err := s.ClearHistory(p.ID, "default"); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}

	// Usage zeroed + cache invalidated → re-derives to 0.
	if got := p.totalCostUSD(); got != 0 {
		t.Errorf("cost after ClearHistory = %.4f, want 0 (cache must re-derive from zeroed usage)", got)
	}
	sess.mu.RLock()
	u := sess.usage
	sess.mu.RUnlock()
	if u != nil && u.TotalCostUSD != 0 {
		t.Errorf("session usage not cleared: %+v", u)
	}
}

// TestDeleteChatSession_LowersCostCache verifies that deleting an expensive
// session re-derives the project cost cache down (excluding the deleted
// session), so a strict-budget block reflects the removal.
func TestDeleteChatSession_LowersCostCache(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-cost-del", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// default session: $2; a second "expensive" session: $8. Total $10.
	def := p.GetSession("default")
	def.mu.Lock()
	def.usage = &SessionUsage{TotalCostUSD: 2.0}
	def.mu.Unlock()

	p.mu.Lock()
	exp := NewChatSession("Expensive")
	exp.usage = &SessionUsage{TotalCostUSD: 8.0}
	p.sessions["expensive"] = exp
	p.mu.Unlock()

	if got := p.totalCostUSD(); got != 10.0 {
		t.Fatalf("primed cost = %.4f, want 10.0", got)
	}

	if err := s.DeleteChatSession(p.ID, "expensive"); err != nil {
		t.Fatalf("DeleteChatSession: %v", err)
	}

	// Cache invalidated → re-derives to the remaining $2.
	if got := p.totalCostUSD(); got != 2.0 {
		t.Errorf("cost after delete = %.4f, want 2.0 (deleted session's $8 must drop out)", got)
	}
}

// TestInvalidateCostCache_ReseedsFromStats verifies the helper forces a re-seed
// rather than returning a stale cached value.
func TestInvalidateCostCache_ReseedsFromStats(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-cost-inval", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Stale cache says $99, but no session actually has usage.
	p.cachedTotalCostUSD = 99.0
	p.costSeeded = true
	if got := p.totalCostUSD(); got != 99.0 {
		t.Fatalf("stale cache = %.4f, want 99.0", got)
	}

	p.invalidateCostCache()

	if got := p.totalCostUSD(); got != 0 {
		t.Errorf("after invalidate, cost = %.4f, want 0 (must re-derive from real usage)", got)
	}
}
