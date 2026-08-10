package studio

import (
	"path/filepath"
	"testing"
)

// TestAddProject_AppliesDefaultBudget verifies that a new project picks up
// the global DefaultBudgetUSD setting on creation. Symmetric with how
// DefaultThinkingMode/Budget are applied.
func TestAddProject_AppliesDefaultBudget(t *testing.T) {
	s := newStudioForTest(t)

	// Set the default before creating.
	s.config.Settings.DefaultBudgetUSD = 12.34

	dir := t.TempDir()
	info, err := s.AddProject("BudgetCascade", filepath.Join(dir, "."))
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	if info.BudgetUSD != 12.34 {
		t.Errorf("new project BudgetUSD = %.2f, want 12.34 (from DefaultBudgetUSD)", info.BudgetUSD)
	}
}

// TestAddProject_DefaultBudgetZero confirms the no-default case: new project
// gets BudgetUSD = 0 (which the frontend treats as "no cap").
func TestAddProject_DefaultBudgetZero(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()
	info, err := s.AddProject("NoDefaultBudget", dir)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if info.BudgetUSD != 0 {
		t.Errorf("new project BudgetUSD = %.2f, want 0 (no default set)", info.BudgetUSD)
	}
}

// TestUpdateSettings_ClampsDefaultBudget verifies the negative-zero clamp
// and the over-cap clamp ($100,000) so a malformed UI value can't poison
// the persisted config.
func TestUpdateSettings_ClampsDefaultBudget(t *testing.T) {
	s := newStudioForTest(t)

	// Negative → clamped to 0.
	cfg := StudioConfig{Settings: Settings{
		Theme:            "dark",
		DefaultProvider:  "glm",
		DefaultModel:     "glm-5.1",
		DefaultBudgetUSD: -50,
	}}
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatalf("UpdateSettings(-50): %v", err)
	}
	if got := s.config.Settings.DefaultBudgetUSD; got != 0 {
		t.Errorf("after UpdateSettings(-50) DefaultBudgetUSD = %.2f, want 0", got)
	}

	// Over $100,000 → clamped to 100,000.
	cfg.Settings.DefaultBudgetUSD = 999999
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatalf("UpdateSettings(999999): %v", err)
	}
	if got := s.config.Settings.DefaultBudgetUSD; got != 100000 {
		t.Errorf("after UpdateSettings(999999) DefaultBudgetUSD = %.2f, want 100000", got)
	}

	// Valid value passes through.
	cfg.Settings.DefaultBudgetUSD = 25.50
	if err := s.UpdateSettings(cfg); err != nil {
		t.Fatalf("UpdateSettings(25.50): %v", err)
	}
	if got := s.config.Settings.DefaultBudgetUSD; got != 25.50 {
		t.Errorf("after UpdateSettings(25.50) DefaultBudgetUSD = %.2f, want 25.50", got)
	}
}
