package tools

import (
	"testing"

	"google.golang.org/genai"
)

func TestGeminiToolsExcludingPlanMode(t *testing.T) {
	names := func(ts []*genai.Tool) map[string]bool {
		m := map[string]bool{}
		for _, tl := range ts {
			for _, d := range tl.FunctionDeclarations {
				m[d.Name] = true
			}
		}
		return m
	}

	reg := DefaultRegistry(t.TempDir())
	full := names(reg.GeminiTools())
	excl := names(reg.GeminiToolsExcludingPlanMode())

	// These are the interactive plan-mode control tools that must be excluded.
	planControl := []string{
		"enter_plan_mode", "exit_plan_mode", "update_plan_progress",
		"get_plan_status",
	}
	dropped := 0
	for _, n := range planControl {
		if full[n] {
			dropped++
		}
		if excl[n] {
			t.Errorf("%q must be excluded by GeminiToolsExcludingPlanMode", n)
		}
	}
	if dropped == 0 {
		t.Fatal("precondition: full tool set should contain plan-mode control tools")
	}

	// task*, editing tools, and core tools must REMAIN — only the interactive
	// plan-mode CONTROL tools are dropped.
	for _, n := range []string{"task", "task_output", "coordinate", "edit", "write", "read", "bash"} {
		if full[n] && !excl[n] {
			t.Errorf("%q must NOT be excluded — only plan-mode control tools are dropped", n)
		}
	}

	if len(excl) != len(full)-dropped {
		t.Errorf("excluded set size = %d, want %d (full %d minus %d plan-mode tools)",
			len(excl), len(full)-dropped, len(full), dropped)
	}
}

// TestGeminiToolsExcluding_Custom verifies that GeminiToolsExcluding handles
// ad-hoc exclusion sets correctly — useful for feature-gated tools.
func TestGeminiToolsExcluding_Custom(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(stubTool{name: "alpha"})
	reg.MustRegister(stubTool{name: "beta"})
	reg.MustRegister(stubTool{name: "gamma"})

	excluded := reg.GeminiToolsExcluding(map[string]bool{"beta": true})
	if len(excluded) != 1 {
		t.Fatalf("expected 1 Tool envelope, got %d", len(excluded))
	}
	got := map[string]bool{}
	for _, d := range excluded[0].FunctionDeclarations {
		got[d.Name] = true
	}
	if got["beta"] {
		t.Error("beta should be excluded")
	}
	if !got["alpha"] || !got["gamma"] {
		t.Errorf("alpha and gamma must remain: %v", got)
	}
}

// TestPlanModeControlToolNames_NoTaskTools guards that the control-tool set
// does NOT include task/task_output/task_stop — those must remain available
// even when plan mode is disabled.
func TestPlanModeControlToolNames_NoTaskTools(t *testing.T) {
	for _, name := range []string{"task", "task_output", "task_stop"} {
		if PlanModeControlToolNames[name] {
			t.Errorf("%q must NOT be in PlanModeControlToolNames — it is a background-agent tool, not plan-mode control", name)
		}
	}
}
