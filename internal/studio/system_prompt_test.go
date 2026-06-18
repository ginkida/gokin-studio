package studio

import (
	"strings"
	"testing"
)

// TestDefaultSystemPrompt_MergedDisciplines guards the iter-1190 merge of the
// gokin upstream's prompt-engineering into the studio's default system prompt
// plus the GLM-5.2 tuning. It checks the newly-added disciplines are present
// AND that the studio-specific tooling guidance was preserved.
func TestDefaultSystemPrompt_MergedDisciplines(t *testing.T) {
	p := defaultSystemPrompt("/tmp/proj", "Proj")

	wantContains := []string{
		// Merged from gokin's baseSystemPrompt:
		"glob (NOT bash find",        // dedicated-tool-over-bash discipline
		"Evidence over assumption",   // repo-evidence-first section
		"Architecture-first for new", // architecture sketch for new features
		// GLM-5.2 tuning:
		"1M-token",
		// Studio-specific guidance that must NOT have been lost in the merge:
		"run_tests",
		"review_changes",
		"enter_plan_mode",
		"ask_user",
		"pin_context",
		"memorize",
	}
	for _, w := range wantContains {
		if !strings.Contains(p, w) {
			t.Errorf("default system prompt missing %q", w)
		}
	}
}
