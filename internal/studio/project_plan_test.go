package studio

import (
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/plan"
)

func TestGetProjectPlan_UnknownProject(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	if _, err := s.GetProjectPlan("nope"); err == nil {
		t.Fatal("expected error for unknown project")
	}
}

func TestGetProjectPlan_NoActivePlan(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "noplan")
	pi, err := s.GetProjectPlan(info.ID)
	if err != nil {
		t.Fatalf("GetProjectPlan: %v", err)
	}
	if pi.Active {
		t.Errorf("expected Active=false with no plan, got %+v", pi)
	}
	if len(pi.Steps) != 0 {
		t.Errorf("expected no steps, got %d", len(pi.Steps))
	}
}

func TestGetProjectPlan_ActivePlanInProgress(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "withplan")

	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()

	mgr := plan.NewManager(true, false)
	pl := mgr.CreatePlan("Build the context panel", "", "req")
	pl.AddStep("Wire git stats", "")
	pl.AddStep("Render Goal + Progress", "")
	pl.AddStep("Mount in App", "")
	p.mu.Lock()
	p.planManager = mgr
	p.mu.Unlock()

	pi, err := s.GetProjectPlan(info.ID)
	if err != nil {
		t.Fatalf("GetProjectPlan: %v", err)
	}
	if !pi.Active {
		t.Fatal("expected Active=true with a plan present")
	}
	if pi.Title != "Build the context panel" {
		t.Errorf("Title = %q", pi.Title)
	}
	if pi.TotalSteps != 3 || len(pi.Steps) != 3 {
		t.Errorf("steps: TotalSteps=%d len(Steps)=%d, want 3/3", pi.TotalSteps, len(pi.Steps))
	}
	if pi.CurrentStep != 0 {
		t.Errorf("CurrentStep = %d, want 0", pi.CurrentStep)
	}
	if pi.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress", pi.Status)
	}
	if pi.Steps[0].Status != "pending" {
		t.Errorf("step[0].Status = %q, want pending", pi.Steps[0].Status)
	}
}

func TestGetProjectPlan_CompletedPlanStillShows(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "donelan")

	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()

	mgr := plan.NewManager(true, false)
	pl := mgr.CreatePlan("Ship it", "", "req")
	s1 := pl.AddStep("step one", "")
	s2 := pl.AddStep("step two", "")
	p.mu.Lock()
	p.planManager = mgr
	p.mu.Unlock()

	// Complete every step. The plan is then no longer "active" (IsActive
	// would be false) but the panel must still show it as completed.
	mgr.CompleteStep(s1.ID, "done")
	mgr.CompleteStep(s2.ID, "done")

	pi, err := s.GetProjectPlan(info.ID)
	if err != nil {
		t.Fatalf("GetProjectPlan: %v", err)
	}
	if !pi.Active {
		t.Fatal("a completed plan should still be returned (Active=true)")
	}
	if pi.Status != "completed" {
		t.Errorf("Status = %q, want completed", pi.Status)
	}
	if pi.CurrentStep != 2 || pi.TotalSteps != 2 {
		t.Errorf("progress = %d/%d, want 2/2", pi.CurrentStep, pi.TotalSteps)
	}
}
