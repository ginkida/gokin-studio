package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/plan"
)

// TestExitPlanMode_ResultDirectsModelToImplement pins the headless plan-trap
// fix: exiting plan mode must tell the model it can edit NOW and not wait for
// write access. Without it, models in single-shot runs propose a change, exit
// plan mode, then ask "is there a way to grant write access?" instead of
// implementing.
func TestExitPlanMode_ResultDirectsModelToImplement(t *testing.T) {
	mgr := plan.NewManager(true, false)
	tool := NewExitPlanModeTool()
	tool.SetManager(mgr)

	res, err := tool.Execute(context.Background(), map[string]any{"reason": "completed"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.Success {
		t.Fatalf("exit_plan_mode should succeed, got: %q", res.Content)
	}
	for _, want := range []string{"edit now", "Implement", "write access"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("exit_plan_mode result should mention %q; got: %q", want, res.Content)
		}
	}
}
