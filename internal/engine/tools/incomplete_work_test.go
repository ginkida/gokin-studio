package tools

import (
	"strings"
	"testing"
)

func TestIncompleteTodoSummary_NilRegistry(t *testing.T) {
	n, summary := IncompleteTodoSummary(nil)
	if n != 0 || summary != "" {
		t.Errorf("nil registry: got (%d, %q), want (0, \"\")", n, summary)
	}
}

func TestIncompleteTodoSummary_NoTodoTool(t *testing.T) {
	reg := NewRegistry()
	n, summary := IncompleteTodoSummary(reg)
	if n != 0 || summary != "" {
		t.Errorf("registry without todo tool: got (%d, %q), want (0, \"\")", n, summary)
	}
}

func TestIncompleteTodoSummary_AllCompleted(t *testing.T) {
	reg := NewRegistry()
	tt := NewTodoTool()
	_ = reg.Register(tt)
	_, _ = tt.Execute(nil, map[string]any{
		"todos": []any{
			map[string]any{"content": "done task", "status": "completed"},
		},
	})

	n, summary := IncompleteTodoSummary(reg)
	if n != 0 || summary != "" {
		t.Errorf("all completed: got (%d, %q), want (0, \"\")", n, summary)
	}
}

func TestIncompleteTodoSummary_HasIncomplete(t *testing.T) {
	reg := NewRegistry()
	tt := NewTodoTool()
	_ = reg.Register(tt)
	_, _ = tt.Execute(nil, map[string]any{
		"todos": []any{
			map[string]any{"content": "write tests", "status": "in_progress"},
			map[string]any{"content": "update docs", "status": "pending"},
		},
	})

	n, summary := IncompleteTodoSummary(reg)
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	if !strings.Contains(summary, "write tests") {
		t.Errorf("summary should contain 'write tests'; got %q", summary)
	}
	if !strings.Contains(summary, "update docs") {
		t.Errorf("summary should contain 'update docs'; got %q", summary)
	}
	// in_progress items use the ▶ marker.
	if !strings.Contains(summary, "▶") {
		t.Errorf("summary should contain ▶ marker for in_progress item; got %q", summary)
	}
}

func TestIncompleteTodoSummary_MixedStatuses(t *testing.T) {
	reg := NewRegistry()
	tt := NewTodoTool()
	_ = reg.Register(tt)
	_, _ = tt.Execute(nil, map[string]any{
		"todos": []any{
			map[string]any{"content": "finished step", "status": "completed"},
			map[string]any{"content": "current step", "status": "in_progress"},
			map[string]any{"content": "future step", "status": "pending"},
		},
	})

	n, summary := IncompleteTodoSummary(reg)
	if n != 2 {
		t.Errorf("count = %d, want 2 (completed items excluded)", n)
	}
	if strings.Contains(summary, "finished step") {
		t.Errorf("summary must NOT contain completed item 'finished step'; got %q", summary)
	}
}

func TestIncompleteTodoSummary_CapsAt5Lines(t *testing.T) {
	reg := NewRegistry()
	tt := NewTodoTool()
	_ = reg.Register(tt)
	todos := make([]any, 7)
	for i := range todos {
		todos[i] = map[string]any{
			"content": strings.Repeat("x", i+1) + " task",
			"status":  "pending",
		}
	}
	_, _ = tt.Execute(nil, map[string]any{"todos": todos})

	n, summary := IncompleteTodoSummary(reg)
	if n != 7 {
		t.Errorf("count = %d, want 7", n)
	}
	if !strings.Contains(summary, "and 2 more") {
		t.Errorf("summary should contain 'and 2 more'; got %q", summary)
	}
}

func TestIncompleteWorkContinuationPrompt(t *testing.T) {
	p := IncompleteWorkContinuationPrompt(3, "  • task A\n  • task B\n  • task C")
	if !strings.Contains(p, "3 unfinished item(s)") {
		t.Errorf("prompt should mention count; got %q", p)
	}
	if !strings.Contains(p, "task A") {
		t.Errorf("prompt should include summary; got %q", p)
	}
	if !strings.Contains(p, "calling the appropriate tool") {
		t.Errorf("prompt should instruct to call a tool; got %q", p)
	}
}

func TestMaxIncompleteWorkContinuationsConstant(t *testing.T) {
	if MaxIncompleteWorkContinuations != 3 {
		t.Errorf("MaxIncompleteWorkContinuations = %d, want 3", MaxIncompleteWorkContinuations)
	}
}
