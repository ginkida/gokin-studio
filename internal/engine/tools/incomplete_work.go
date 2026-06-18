package tools

import (
	"fmt"
	"strings"
)

// MaxIncompleteWorkContinuations bounds consecutive nudges where the model
// narrates without running a tool (announced the next step but didn't take it).
// A model that completes todos between nudges resets the counter and runs free.
// Ported from upstream gokin (tools/incomplete_work.go).
const MaxIncompleteWorkContinuations = 3

// registryGetter is a minimal interface for registry lookups. Both Registry
// and LazyRegistry satisfy it, so IncompleteTodoSummary works with either.
type registryGetter interface {
	Get(name string) (Tool, bool)
}

// IncompleteTodoSummary inspects the registry's todo tool and returns the count
// of not-completed items (pending + in_progress) plus a short human summary.
// Returns (0, "") when there is no todo tool, no list, or all items are done —
// i.e. the model is genuinely finished and the loop should end normally.
// Safe on a nil registry or missing tool.
func IncompleteTodoSummary(registry registryGetter) (int, string) {
	if registry == nil {
		return 0, ""
	}
	tool, ok := registry.Get("todo")
	if !ok {
		return 0, ""
	}
	tt, ok := tool.(*TodoTool)
	if !ok || tt == nil {
		return 0, ""
	}

	items := tt.GetItems()
	count := 0
	var lines []string
	for _, it := range items {
		if it.Status == "completed" {
			continue
		}
		count++
		if len(lines) < 5 {
			marker := "•"
			if it.Status == "in_progress" {
				marker = "▶"
			}
			lines = append(lines, fmt.Sprintf("  %s %s", marker, it.Content))
		}
	}
	if count == 0 {
		return 0, ""
	}
	summary := strings.Join(lines, "\n")
	if count > len(lines) {
		summary += fmt.Sprintf("\n  … and %d more", count-len(lines))
	}
	return count, summary
}

// IncompleteWorkContinuationPrompt is the firm user-role nudge appended to
// history when the model stopped with unfinished todos. It insists the model
// ACT (call the next tool) rather than describe — the failure mode is the model
// announcing intent without taking the action.
func IncompleteWorkContinuationPrompt(count int, summary string) string {
	return fmt.Sprintf(
		"Your task list still has %d unfinished item(s):\n%s\n\nThe task is NOT complete — keep going. Do the next step NOW by calling the appropriate tool; do not just describe what you will do. When everything is genuinely finished, call todo to mark the items completed.",
		count, summary,
	)
}
