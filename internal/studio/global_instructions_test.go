package studio

import (
	"strings"
	"testing"
)

func TestComposeProjectSystemInstructionPrecedence(t *testing.T) {
	got := composeProjectSystemInstruction(
		"Use the repository release checklist.",
		"Keep answers concise.",
		"/tmp/project",
		"Project",
		projectSkillsDirective,
		manualApprovalDirective,
	)
	globalAt := strings.Index(got, "## Global user instructions\nKeep answers concise.")
	projectAt := strings.Index(got, "## Project instructions\nUse the repository release checklist.")
	runtimeAt := strings.Index(got, "## Permission mode: Manual")
	if globalAt < 0 || projectAt <= globalAt || runtimeAt <= projectAt {
		t.Fatalf("unexpected instruction precedence:\n%s", got)
	}
}

func TestComposeProjectSystemInstructionPreservesLegacyWithoutGlobal(t *testing.T) {
	const projectPrompt = "Use only checked-in evidence."
	got := composeProjectSystemInstruction(projectPrompt, "", "/tmp/project", "Project", manualApprovalDirective)
	if got != projectPrompt+manualApprovalDirective {
		t.Fatalf("prompt changed without global instructions:\n%s", got)
	}
}

func TestComposeProjectSystemInstructionAddsGlobalToDefault(t *testing.T) {
	got := composeProjectSystemInstruction("", "Prefer Russian.", "/tmp/project", "Project")
	if !strings.Contains(got, `working inside the project "Project"`) ||
		!strings.HasSuffix(got, "## Global user instructions\nPrefer Russian.") {
		t.Fatalf("default/global prompt composition failed:\n%s", got)
	}
}
