package tools

import (
	"context"
	"strings"
	"testing"
)

func TestScheduledTaskToolValidation(t *testing.T) {
	tool := NewScheduledTaskTool()
	valid := []map[string]any{
		{"action": "list"},
		{"action": "create", "prompt": "Review risks", "schedule": "interval", "interval_minutes": float64(30)},
		{"action": "create", "prompt": "Daily brief", "schedule": "daily", "time_of_day": "09:15", "provider": "glm", "model": "glm-5.2"},
		{"action": "create", "prompt": "Weekday brief", "schedule": "weekdays", "time_of_day": "18:00", "provider": "kimi", "model": "k3"},
		{"action": "create", "prompt": "Weekly brief", "schedule": "weekly", "time_of_day": "08:30", "weekday": float64(1)},
		{"action": "create", "prompt": "On demand", "schedule": "manual", "enabled": false, "approval_mode": "manual"},
		{"action": "create", "prompt": "Edit files", "schedule": "manual", "approval_mode": "accept_edits"},
		{"action": "update", "task_id": "task-1", "time_of_day": "10:00"},
		{"action": "update", "task_id": "task-1", "schedule": "interval", "interval_minutes": 60},
		{"action": "pause", "task_id": "task-1"},
		{"action": "resume", "task_id": "task-1"},
		{"action": "run_now", "task_id": "task-1"},
		{"action": "delete", "task_id": "task-1"},
	}
	for i, args := range valid {
		if err := tool.Validate(args); err != nil {
			t.Errorf("valid case %d (%v): %v", i, args, err)
		}
	}

	invalid := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing action", nil, "action"},
		{"unknown action", map[string]any{"action": "enable"}, "action"},
		{"unknown field", map[string]any{"action": "list", "project_id": "other"}, "project_id"},
		{"list mutation", map[string]any{"action": "list", "task_id": "x"}, "list does not accept"},
		{"create missing prompt", map[string]any{"action": "create", "schedule": "manual"}, "prompt"},
		{"create missing schedule", map[string]any{"action": "create", "prompt": "x"}, "schedule"},
		{"short interval", map[string]any{"action": "create", "prompt": "x", "schedule": "interval", "interval_minutes": 14}, "interval_minutes"},
		{"fractional interval", map[string]any{"action": "create", "prompt": "x", "schedule": "interval", "interval_minutes": 30.5}, "integer"},
		{"bad local time", map[string]any{"action": "update", "task_id": "x", "time_of_day": "9am"}, "time_of_day"},
		{"bad weekday", map[string]any{"action": "update", "task_id": "x", "weekday": 7}, "weekday"},
		{"provider without model", map[string]any{"action": "update", "task_id": "x", "provider": "glm"}, "provider and model"},
		{"unsupported provider", map[string]any{"action": "create", "prompt": "x", "schedule": "manual", "provider": "openai", "model": "gpt"}, "GLM or Kimi"},
		{"update no changes", map[string]any{"action": "update", "task_id": "x"}, "at least one"},
		{"pause extra field", map[string]any{"action": "pause", "task_id": "x", "enabled": false}, "only task_id"},
		{"bad approval", map[string]any{"action": "create", "prompt": "x", "schedule": "manual", "approval_mode": "never"}, "approval_mode"},
		{"bad enabled", map[string]any{"action": "create", "prompt": "x", "schedule": "manual", "enabled": "yes"}, "enabled"},
		{"blank id", map[string]any{"action": "delete", "task_id": " "}, "task_id"},
		{"nul prompt", map[string]any{"action": "create", "prompt": "bad\x00prompt", "schedule": "manual"}, "NUL"},
		{"long name", map[string]any{"action": "create", "prompt": "x", "name": strings.Repeat("a", 121), "schedule": "manual"}, "120 characters"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			err := tool.Validate(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate(%v) = %v, want error containing %q", tc.args, err, tc.want)
			}
		})
	}
}

func TestScheduledTaskToolExecuteUsesHandler(t *testing.T) {
	tool := NewScheduledTaskTool()
	result, err := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if err != nil || result.Success || !strings.Contains(result.Error, "not configured") {
		t.Fatalf("unconfigured Execute = %#v, %v", result, err)
	}

	var gotAction string
	tool.SetHandler(func(_ context.Context, action string, args map[string]any) (ToolResult, error) {
		gotAction = action
		return NewSuccessResultWithData("ok", args["task_id"]), nil
	})
	result, err = tool.Execute(context.Background(), map[string]any{"action": " RUN_NOW ", "task_id": "task-1"})
	if err != nil || !result.Success || result.Data != "task-1" || gotAction != "run_now" {
		t.Fatalf("configured Execute = %#v, %v; action=%q", result, err, gotAction)
	}
}

func TestScheduledTaskClassificationAndSafety(t *testing.T) {
	if !IsWriteTool("scheduled_task") {
		t.Fatal("scheduled_task must serialize with writes")
	}
	if RequiresUserApproval("scheduled_task", map[string]any{"action": "list"}) {
		t.Fatal("list must remain read-only")
	}
	for _, action := range []string{"create", "update", "pause", "resume", "run_now", "delete"} {
		if !RequiresUserApproval("scheduled_task", map[string]any{"action": action}) {
			t.Errorf("%s must require approval", action)
		}
	}
	meta, ok := NewDefaultSafetyValidator().GetMetadata("scheduled_task")
	if !ok || meta.SafetyLevel != SafetyLevelCaution || !meta.RequiresConfirmation {
		t.Fatalf("scheduled-task safety metadata = %#v, %v", meta, ok)
	}
}
