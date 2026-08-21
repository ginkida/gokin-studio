package tools

import (
	"context"
	"testing"

	"google.golang.org/genai"
)

// stubTool is a minimal Tool implementation for registry population in tests,
// without pulling in real tool dependencies.
type stubTool struct{ name string }

func (t stubTool) Name() string        { return t.name }
func (t stubTool) Description() string { return "stub " + t.name }
func (t stubTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: t.Description()}
}
func (t stubTool) Validate(_ map[string]any) error { return nil }
func (t stubTool) Execute(_ context.Context, _ map[string]any) (ToolResult, error) {
	return NewSuccessResult("stub"), nil
}

// namesFromDeclarations extracts the Name field from a slice of function declarations.
func namesFromDeclarations(decls []*genai.FunctionDeclaration) []string {
	names := make([]string, 0, len(decls))
	for _, d := range decls {
		if d != nil {
			names = append(names, d.Name)
		}
	}
	return names
}

// containsToolName reports whether list contains target.
func containsToolName(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

// TestIsReadOnlyForPlanMode pins the allow-list used by plan mode. Any change
// here shifts what the model can physically call while exploring — strict scrutiny.
func TestIsReadOnlyForPlanMode(t *testing.T) {
	// Must allow: read/search + metadata + non-mutating git + plan lifecycle.
	allowed := []string{
		"read", "glob", "grep", "list_dir", "tree", "diff",
		"web_fetch", "web_search",
		"env", "tools_list",
		"git_status", "git_diff", "git_log", "git_blame",
		"ask_user", "task_output", "get_plan_status",
		"history_search", "plugin_resource",
	}
	for _, name := range allowed {
		if !IsReadOnlyForPlanMode(name) {
			t.Errorf("%q must be allowed in plan mode (read/meta/plan-lifecycle tool)", name)
		}
	}

	// Must BLOCK: anything that mutates state (files, git, shell, subagents).
	blocked := []string{
		"write", "edit", "delete", "move", "copy", "mkdir",
		"bash", "ssh", "kill_shell", "run_tests",
		"git_add", "git_commit", "git_pr",
		"git_branch", "todo", "memory", "pin_context",
		"enter_plan_mode", "exit_plan_mode", "update_plan_progress",
		"ask_agent", "coordinate", "shared_memory", "update_scratchpad",
		"task", "task_stop",
		"memorize", "batch", "refactor", "verify_code", "check_impact",
		"undo_plan", "redo_plan",
	}
	for _, name := range blocked {
		if IsReadOnlyForPlanMode(name) {
			t.Errorf("%q must be BLOCKED in plan mode (write/exec/delegation tool)", name)
		}
	}

	// Unknown tools default to blocked — conservative for MCP and custom tools.
	unknown := []string{"", "some_mcp_tool", "custom_scraper", "unknown"}
	for _, name := range unknown {
		if IsReadOnlyForPlanMode(name) {
			t.Errorf("%q (unknown) must be BLOCKED — conservative default", name)
		}
	}
}

// TestPlanModeDeclarations_FiltersRegistry proves that plan-mode filtering at
// the registry level only surfaces tools in the allow-list.
func TestPlanModeDeclarations_FiltersRegistry(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(stubTool{name: "read"})
	reg.MustRegister(stubTool{name: "write"})
	reg.MustRegister(stubTool{name: "edit"})
	reg.MustRegister(stubTool{name: "bash"})
	reg.MustRegister(stubTool{name: "grep"})
	reg.MustRegister(stubTool{name: "get_plan_status"})

	fullNames := namesFromDeclarations(reg.Declarations())
	if !containsToolName(fullNames, "write") || !containsToolName(fullNames, "read") {
		t.Fatalf("Declarations() should list everything registered, got %v", fullNames)
	}

	planNames := namesFromDeclarations(reg.PlanModeDeclarations())
	wantAllowed := []string{"read", "grep", "get_plan_status"}
	wantBlocked := []string{"write", "edit", "bash"}

	for _, name := range wantAllowed {
		if !containsToolName(planNames, name) {
			t.Errorf("PlanModeDeclarations must include read-only %q, got %v", name, planNames)
		}
	}
	for _, name := range wantBlocked {
		if containsToolName(planNames, name) {
			t.Errorf("PlanModeDeclarations must EXCLUDE mutating %q, got %v", name, planNames)
		}
	}
}

// TestPlanModeTools_Wraps verifies the Gemini-style single-envelope shape
// expected by the client schema-push API.
func TestPlanModeTools_Wraps(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(stubTool{name: "read"})
	reg.MustRegister(stubTool{name: "bash"})

	// Studio uses PlanModeTools (gokin uses PlanModeGeminiTools — same thing).
	tools := reg.PlanModeTools()
	if len(tools) != 1 {
		t.Fatalf("expected single Tool envelope, got %d", len(tools))
	}
	if len(tools[0].FunctionDeclarations) != 1 {
		t.Errorf("expected 1 filtered declaration (read only), got %d", len(tools[0].FunctionDeclarations))
	}
	if tools[0].FunctionDeclarations[0].Name != "read" {
		t.Errorf("expected `read` in plan-mode envelope, got %q", tools[0].FunctionDeclarations[0].Name)
	}
}
