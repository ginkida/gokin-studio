package studio

import (
	"strings"
	"testing"
)

// TestSetProjectBudget validates input checks and that the field round-trips
// through Info() and ToConfig() so it survives a save/load cycle.
func TestSetProjectBudget(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "BudgetProj")

	// Negative budget rejected.
	if err := s.SetProjectBudget(info.ID, -1); err == nil {
		t.Error("expected error for negative budget, got nil")
	} else if !strings.Contains(err.Error(), "must be >= 0") {
		t.Errorf("error message = %q, want substring 'must be >= 0'", err.Error())
	}

	// Over-cap rejected (defends against typos like 100000000 instead of 100).
	if err := s.SetProjectBudget(info.ID, 100001); err == nil {
		t.Error("expected error for budget over $100,000, got nil")
	} else if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("error message = %q, want substring 'exceeds maximum'", err.Error())
	}

	// Unknown project rejected.
	if err := s.SetProjectBudget("no-such-id", 50); err == nil {
		t.Error("expected error for unknown project, got nil")
	}

	// Default budget is 0 (no cap set).
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.BudgetUSD != 0 {
		t.Errorf("default budget = %.2f, want 0", got.BudgetUSD)
	}

	// Valid budget round-trips through Info().
	if err := s.SetProjectBudget(info.ID, 25.50); err != nil {
		t.Fatalf("SetProjectBudget: %v", err)
	}
	got, err = s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.BudgetUSD != 25.50 {
		t.Errorf("budget after set = %.2f, want 25.50", got.BudgetUSD)
	}

	// Setting back to 0 removes the cap.
	if err := s.SetProjectBudget(info.ID, 0); err != nil {
		t.Fatalf("SetProjectBudget(0): %v", err)
	}
	got, _ = s.GetProject(info.ID)
	if got.BudgetUSD != 0 {
		t.Errorf("budget after clear = %.2f, want 0", got.BudgetUSD)
	}
}

// TestSetProjectBudget_UpperBoundary verifies that exactly $100,000 is accepted
// (off-by-one defense for the cap check).
func TestSetProjectBudget_UpperBoundary(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "MaxBudget")

	if err := s.SetProjectBudget(info.ID, 100000); err != nil {
		t.Errorf("SetProjectBudget(100000) errored: %v — boundary value should be allowed", err)
	}
	got, _ := s.GetProject(info.ID)
	if got.BudgetUSD != 100000 {
		t.Errorf("budget = %.2f, want 100000", got.BudgetUSD)
	}
}

// TestSetProjectBudget_PersistsToConfig verifies the budget is included in
// ToConfig() so it survives a save and reload.
func TestSetProjectBudget_PersistsToConfig(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "PersistBudget")

	if err := s.SetProjectBudget(info.ID, 42.75); err != nil {
		t.Fatalf("SetProjectBudget: %v", err)
	}

	// Read the in-memory project and convert to config.
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	cfg := p.ToConfig()

	if cfg.BudgetUSD != 42.75 {
		t.Errorf("ToConfig.BudgetUSD = %.2f, want 42.75", cfg.BudgetUSD)
	}

	// Build a new project from the same config and confirm it loads with the
	// budget intact — this is the round-trip that matters for restart survival.
	p2 := NewProject(cfg)
	if p2.BudgetUSD != 42.75 {
		t.Errorf("NewProject.BudgetUSD = %.2f, want 42.75 (lost on round-trip)", p2.BudgetUSD)
	}
}
