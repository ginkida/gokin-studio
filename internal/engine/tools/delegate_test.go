package tools

import (
	"math"
	"strings"
	"testing"
)

func TestDelegateToolStrictArgumentContract(t *testing.T) {
	tool := NewDelegateTool()
	valid := []map[string]any{
		{"action": "list"},
		{"action": "ask", "project_id": "p", "task": "question", "goal": "why"},
		{"action": "run", "project_id": "p", "task": strings.Repeat("x", DelegateTaskMaxBytes)},
		{"action": "batch", "task": "shared", "targets": []any{
			map[string]any{"project_id": "a"},
			map[string]any{"project_id": "b", "task": "specific"},
		}},
		{"action": "fetch", "run_id": "r", "offset": float64(0), "max_bytes": float64(9000)},
	}
	for _, args := range valid {
		if err := tool.Validate(args); err != nil {
			t.Fatalf("valid args rejected: %#v: %v", args, err)
		}
	}

	invalid := []map[string]any{
		{"action": "run", "project_id": strings.Repeat("p", DelegateIDMaxBytes+1), "task": "t"},
		{"action": "run", "project_id": "p", "task": "bad\x00task"},
		{"action": "ask", "project_id": "p", "task": "t", "goal": strings.Repeat("g", DelegateGoalMaxBytes+1)},
		{"action": "status", "run_id": strings.Repeat("r", DelegateIDMaxBytes+1)},
		{"action": "fetch", "run_id": "r", "offset": -1},
		{"action": "fetch", "run_id": "r", "offset": 1.5},
		{"action": "fetch", "run_id": "r", "max_bytes": DelegateFetchMinBytes - 1},
		{"action": "fetch", "run_id": "r", "max_bytes": math.NaN()},
		{"action": "batch", "targets": []any{map[string]any{"project_id": "a"}}},
		{"action": "batch", "task": "shared", "targets": []any{
			map[string]any{"project_id": "a"}, map[string]any{"project_id": "a"},
		}},
		{"action": "batch", "task": "shared", "targets": []any{
			map[string]any{"project_id": "a"}, map[string]any{"project_id": "b"},
			map[string]any{"project_id": "c"}, map[string]any{"project_id": "d"},
			map[string]any{"project_id": "e"},
		}},
		{"action": "batch", "task": "shared", "targets": []any{"not-an-object"}},
	}
	for _, args := range invalid {
		if err := tool.Validate(args); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}
}
