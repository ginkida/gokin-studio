package studio

import (
	"fmt"

	"github.com/ginkida/gokin-studio/internal/engine/plan"
)

// ProjectPlanStep is one step of the active plan, projected for the frontend.
type ProjectPlanStep struct {
	Title  string `json:"title"`
	Status string `json:"status"` // pending / in_progress / completed / failed / skipped / paused
}

// ProjectPlanInfo is the current plan-mode state for a project's right-hand
// context panel (Goal + Progress). Active=false means no plan is running.
type ProjectPlanInfo struct {
	Active      bool              `json:"active"`
	Title       string            `json:"title,omitempty"`
	Status      string            `json:"status,omitempty"` // in_progress / completed / failed
	CurrentStep int               `json:"currentStep"`      // completed step count
	TotalSteps  int               `json:"totalSteps"`
	Steps       []ProjectPlanStep `json:"steps,omitempty"`
}

// GetProjectPlan returns the active plan-mode plan for a project, projected
// for the context panel (Goal + Progress). The plan manager is created per
// project in initMemoryAndPlan and driven by the enter_plan_mode /
// update_plan_progress tools. Returns {Active:false} when no plan is running.
//
// Steps are read via the plan's deep-copying GetStepsSnapshot (which locks
// the plan), so this is safe to call from the Wails goroutine while the agent
// goroutine mutates the plan.
func (s *Studio) GetProjectPlan(projectID string) (*ProjectPlanInfo, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	p.mu.RLock()
	mgr := p.planManager
	p.mu.RUnlock()
	return projectPlanInfo(mgr), nil
}

// GetSessionPlan reports plan-mode progress for the selected conversation.
// Sessions own independent plan managers just as they own independent tools
// and worktrees.
func (s *Studio) GetSessionPlan(projectID, sessionID string) (*ProjectPlanInfo, error) {
	_, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	session.mu.RLock()
	mgr := session.planManager
	session.mu.RUnlock()
	return projectPlanInfo(mgr), nil
}

func projectPlanInfo(mgr *plan.Manager) *ProjectPlanInfo {
	if mgr == nil {
		return &ProjectPlanInfo{Active: false}
	}
	// Use GetCurrentPlan rather than IsActive: a fully-completed plan is no
	// longer "active" but we still want the panel to show its Goal + the
	// all-checkmarks Progress (matches the mockup's "Complete · 5/5"). The
	// plan goes away only when ClearPlan/exit_plan_mode nils currentPlan.
	pl := mgr.GetCurrentPlan()
	if pl == nil {
		return &ProjectPlanInfo{Active: false}
	}

	current, total, _ := mgr.GetProgress()
	steps := pl.GetStepsSnapshot()

	info := &ProjectPlanInfo{
		Active:      true,
		Title:       pl.Title, // set once at CreatePlan, stable for the plan instance
		CurrentStep: current,
		TotalSteps:  total,
	}
	failed := false
	for _, st := range steps {
		if st == nil {
			continue
		}
		status := st.Status.String()
		if status == "failed" {
			failed = true
		}
		info.Steps = append(info.Steps, ProjectPlanStep{Title: st.Title, Status: status})
	}
	switch {
	case failed:
		info.Status = "failed"
	case total > 0 && current >= total:
		info.Status = "completed"
	default:
		info.Status = "in_progress"
	}
	return info
}
