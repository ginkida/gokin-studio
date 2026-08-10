package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func (s *Studio) makeScheduledTaskHandler(projectID string) tools.ScheduledTaskHandler {
	return func(ctx context.Context, action string, args map[string]any) (tools.ToolResult, error) {
		routedProject, sessionID := askUserRouting(ctx)
		if routedProject == "" || routedProject != projectID {
			return tools.ToolResult{}, fmt.Errorf("scheduled-task routing does not match the current project")
		}
		if sessionID == "" {
			sessionID = "default"
		}

		switch action {
		case "list":
			tasks, err := s.ListScheduledTasks(projectID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			views := make([]map[string]any, 0, len(tasks))
			for _, task := range tasks {
				views = append(views, scheduledTaskToolView(task))
			}
			encoded, err := json.MarshalIndent(views, "", "  ")
			if err != nil {
				return tools.ToolResult{}, err
			}
			if len(tasks) == 0 {
				return tools.NewSuccessResultWithData("No scheduled routines exist for this project.", views), nil
			}
			return tools.NewSuccessResultWithData(
				fmt.Sprintf("%d scheduled routine(s):\n%s", len(tasks), encoded), views,
			), nil

		case "create":
			task := ScheduledTask{
				ProjectID: projectID,
				SessionID: sessionID,
				Name:      scheduledToolString(args, "name"),
				Prompt:    scheduledToolString(args, "prompt"),
				Schedule:  scheduledToolString(args, "schedule"),
				Enabled:   true,
				Provider:  scheduledToolString(args, "provider"),
				Model:     scheduledToolString(args, "model"),
				// Future unattended work starts in the safest mode unless the
				// user explicitly reviewed another mode in the tool call.
				ApprovalMode: "manual",
			}
			applyScheduledToolFields(&task, args)
			created, err := s.SaveScheduledTask(task)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return scheduledTaskMutationResult("Created", created), nil

		case "update":
			task, err := s.scheduledTaskForTool(projectID, scheduledToolString(args, "task_id"))
			if err != nil {
				return tools.ToolResult{}, err
			}
			applyScheduledToolFields(&task, args)
			updated, err := s.SaveScheduledTask(task)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return scheduledTaskMutationResult("Updated", updated), nil

		case "pause", "resume":
			task, err := s.scheduledTaskForTool(projectID, scheduledToolString(args, "task_id"))
			if err != nil {
				return tools.ToolResult{}, err
			}
			task.Enabled = action == "resume"
			updated, err := s.SaveScheduledTask(task)
			if err != nil {
				return tools.ToolResult{}, err
			}
			verb := "Paused"
			if updated.Enabled {
				verb = "Resumed"
			}
			return scheduledTaskMutationResult(verb, updated), nil

		case "run_now":
			taskID := scheduledToolString(args, "task_id")
			run, err := s.RunScheduledTaskNow(projectID, taskID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			return tools.NewSuccessResultWithData(
				fmt.Sprintf("Started scheduled routine in child chat %s.", run.SessionID), run,
			), nil

		case "delete":
			taskID := scheduledToolString(args, "task_id")
			task, err := s.scheduledTaskForTool(projectID, taskID)
			if err != nil {
				return tools.ToolResult{}, err
			}
			if err := s.DeleteScheduledTask(projectID, taskID); err != nil {
				return tools.ToolResult{}, err
			}
			return tools.NewSuccessResultWithData(
				fmt.Sprintf("Deleted scheduled routine %q.", task.Name),
				map[string]any{"id": task.ID, "name": task.Name},
			), nil
		default:
			return tools.ToolResult{}, fmt.Errorf("unsupported scheduled-task action %q", action)
		}
	}
}

func (s *Studio) scheduledTaskForTool(projectID, taskID string) (ScheduledTask, error) {
	tasks, err := s.ListScheduledTasks(projectID)
	if err != nil {
		return ScheduledTask{}, err
	}
	for _, task := range tasks {
		if task.ID == taskID {
			return task, nil
		}
	}
	return ScheduledTask{}, fmt.Errorf("scheduled task not found: %s", taskID)
}

func applyScheduledToolFields(task *ScheduledTask, args map[string]any) {
	for key, target := range map[string]*string{
		"name": &task.Name, "prompt": &task.Prompt, "schedule": &task.Schedule,
		"time_of_day": &task.TimeOfDay, "provider": &task.Provider,
		"model": &task.Model, "approval_mode": &task.ApprovalMode,
	} {
		if value, exists := args[key]; exists {
			if text, ok := value.(string); ok {
				*target = strings.TrimSpace(text)
			}
		}
	}
	if _, exists := args["interval_minutes"]; exists {
		task.IntervalMinutes = tools.GetIntDefault(args, "interval_minutes", task.IntervalMinutes)
	}
	if _, exists := args["weekday"]; exists {
		task.Weekday = tools.GetIntDefault(args, "weekday", task.Weekday)
	}
	if _, exists := args["enabled"]; exists {
		task.Enabled = tools.GetBoolDefault(args, "enabled", task.Enabled)
	}
}

func scheduledToolString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func scheduledTaskToolView(task ScheduledTask) map[string]any {
	view := map[string]any{
		"id": task.ID, "name": task.Name, "prompt": scheduledTaskTitle(task.Prompt, 240),
		"schedule": task.Schedule, "enabled": task.Enabled,
		"provider": task.Provider, "model": task.Model, "approval_mode": task.ApprovalMode,
		"next_run_at": task.NextRunAt, "last_status": task.LastStatus,
	}
	switch task.Schedule {
	case "interval":
		view["interval_minutes"] = task.IntervalMinutes
	case "daily", "weekdays":
		view["time_of_day"] = task.TimeOfDay
	case "weekly":
		view["time_of_day"] = task.TimeOfDay
		view["weekday"] = task.Weekday
	}
	if task.LastError != "" {
		view["last_error"] = scheduledTaskTitle(task.LastError, 240)
	}
	return view
}

func scheduledTaskMutationResult(verb string, task ScheduledTask) tools.ToolResult {
	view := scheduledTaskToolView(task)
	return tools.NewSuccessResultWithData(
		fmt.Sprintf("%s scheduled routine %q (%s).", verb, task.Name, task.ID), view,
	)
}
