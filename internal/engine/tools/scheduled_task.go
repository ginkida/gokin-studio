package tools

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"
)

const (
	ScheduledTaskNameMaxBytes   = 480
	ScheduledTaskPromptMaxBytes = 1 << 20
	ScheduledTaskIDMaxBytes     = 128
)

// ScheduledTaskHandler is supplied by the desktop runtime. Keeping storage and
// project/session routing outside the engine package lets the tool share the
// exact same scheduler validation and persistence path as the manual UI.
type ScheduledTaskHandler func(ctx context.Context, action string, args map[string]any) (ToolResult, error)

type ScheduledTaskTool struct {
	handler ScheduledTaskHandler
}

func NewScheduledTaskTool() *ScheduledTaskTool {
	return &ScheduledTaskTool{}
}

func (t *ScheduledTaskTool) SetHandler(handler ScheduledTaskHandler) {
	t.handler = handler
}

func (t *ScheduledTaskTool) Name() string {
	return "scheduled_task"
}

func (t *ScheduledTaskTool) Description() string {
	return "List or explicitly manage this project's local scheduled routines. Mutations require exact user approval."
}

func (t *ScheduledTaskTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type:        genai.TypeString,
					Enum:        []string{"list", "create", "update", "pause", "resume", "run_now", "delete"},
					Description: "Action. List before changing an existing task. Every action except list asks the user to confirm the exact operation.",
				},
				"task_id": {
					Type:        genai.TypeString,
					Description: "Existing task ID for update, pause, resume, run_now, or delete.",
				},
				"name": {
					Type:        genai.TypeString,
					Description: "Short routine name. Optional for create; the prompt's first line is used by default.",
				},
				"prompt": {
					Type:        genai.TypeString,
					Description: "Instructions executed in each new child chat. Required for create.",
				},
				"schedule": {
					Type:        genai.TypeString,
					Enum:        []string{"interval", "daily", "weekdays", "weekly", "manual"},
					Description: "Cadence. Manual creates an on-demand routine that never runs automatically.",
				},
				"interval_minutes": {
					Type:        genai.TypeInteger,
					Description: "For interval schedules: 15-10080 minutes.",
				},
				"time_of_day": {
					Type:        genai.TypeString,
					Description: "For daily, weekdays, or weekly schedules: HH:MM in the computer's local timezone.",
				},
				"weekday": {
					Type:        genai.TypeInteger,
					Description: "For weekly schedules: 0=Sunday through 6=Saturday.",
				},
				"provider": {
					Type:        genai.TypeString,
					Enum:        []string{"glm", "kimi"},
					Description: "Optional execution provider. Omit with model to inherit the current project.",
				},
				"model": {
					Type:        genai.TypeString,
					Description: "Optional GLM/Kimi model ID. Must be supplied together with provider.",
				},
				"approval_mode": {
					Type:        genai.TypeString,
					Enum:        []string{"manual", "accept_edits", "auto", "skip"},
					Description: "Permission mode inside each future child run. Defaults to manual.",
				},
				"enabled": {
					Type:        genai.TypeBoolean,
					Description: "Whether an automatic schedule is enabled. Create defaults to true. Manual routines remain on-demand.",
				},
			},
			Required: []string{"action"},
		},
	}
}

func (t *ScheduledTaskTool) Validate(args map[string]any) error {
	action, ok := GetString(args, "action")
	action = strings.ToLower(strings.TrimSpace(action))
	if !ok || action == "" {
		return NewValidationError("action", "action is required")
	}
	allowedKeys := map[string]bool{
		"action": true, "task_id": true, "name": true, "prompt": true, "schedule": true,
		"interval_minutes": true, "time_of_day": true, "weekday": true,
		"provider": true, "model": true, "approval_mode": true, "enabled": true,
	}
	for key := range args {
		if !allowedKeys[key] {
			return NewValidationError(key, "unknown scheduled_task field")
		}
	}

	switch action {
	case "list":
		if len(args) != 1 {
			return NewValidationError("action", "list does not accept mutation fields")
		}
	case "create":
		if err := scheduledTaskRequiredText(args, "prompt", ScheduledTaskPromptMaxBytes); err != nil {
			return err
		}
		schedule, ok := GetString(args, "schedule")
		if !ok || strings.TrimSpace(schedule) == "" {
			return NewValidationError("schedule", "schedule is required for create")
		}
	case "update":
		if err := scheduledTaskRequiredText(args, "task_id", ScheduledTaskIDMaxBytes); err != nil {
			return err
		}
		if len(args) <= 2 {
			return NewValidationError("action", "update requires at least one field to change")
		}
	case "pause", "resume", "run_now", "delete":
		if err := scheduledTaskRequiredText(args, "task_id", ScheduledTaskIDMaxBytes); err != nil {
			return err
		}
		if len(args) != 2 {
			return NewValidationError("action", action+" accepts only task_id")
		}
	default:
		return NewValidationError("action", "must be list, create, update, pause, resume, run_now, or delete")
	}

	for key, limit := range map[string]int{
		"task_id": ScheduledTaskIDMaxBytes,
		"name":    ScheduledTaskNameMaxBytes,
		"prompt":  ScheduledTaskPromptMaxBytes,
		"model":   256,
	} {
		if value, exists := args[key]; exists {
			text, ok := value.(string)
			if !ok || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || len(text) > limit {
				return NewValidationError(key, fmt.Sprintf("must be valid UTF-8 without NUL and at most %d bytes", limit))
			}
			if (key == "task_id" || key == "prompt") && strings.TrimSpace(text) == "" {
				return NewValidationError(key, key+" cannot be blank")
			}
			if key == "name" && len([]rune(strings.TrimSpace(text))) > 120 {
				return NewValidationError(key, "must contain at most 120 characters")
			}
		}
	}

	schedule := strings.ToLower(strings.TrimSpace(GetStringDefault(args, "schedule", "")))
	if schedule != "" {
		switch schedule {
		case "interval":
			minutes, ok := scheduledTaskInt(args, "interval_minutes")
			if !ok {
				return NewValidationError("interval_minutes", "interval schedule requires an integer number of minutes")
			}
			if minutes < 15 || minutes > 10080 {
				return NewValidationError("interval_minutes", "interval schedule requires 15-10080 minutes")
			}
		case "daily", "weekdays":
			if err := validateScheduledLocalTime(args); err != nil {
				return err
			}
		case "weekly":
			if err := validateScheduledLocalTime(args); err != nil {
				return err
			}
			weekday, ok := scheduledTaskInt(args, "weekday")
			if !ok {
				return NewValidationError("weekday", "weekly schedule requires an integer weekday")
			}
			if weekday < 0 || weekday > 6 {
				return NewValidationError("weekday", "weekly schedule requires weekday 0-6")
			}
		case "manual":
		default:
			return NewValidationError("schedule", "must be interval, daily, weekdays, weekly, or manual")
		}
	}
	if _, exists := args["time_of_day"]; exists {
		if err := validateScheduledLocalTime(args); err != nil {
			return err
		}
	}
	if _, exists := args["interval_minutes"]; exists {
		minutes, ok := scheduledTaskInt(args, "interval_minutes")
		if !ok || minutes < 15 || minutes > 10080 {
			return NewValidationError("interval_minutes", "must be an integer between 15 and 10080")
		}
	}
	if _, exists := args["weekday"]; exists {
		weekday, ok := scheduledTaskInt(args, "weekday")
		if !ok || weekday < 0 || weekday > 6 {
			return NewValidationError("weekday", "must be an integer between 0 and 6")
		}
	}

	_, hasProvider := args["provider"]
	_, hasModel := args["model"]
	if hasProvider != hasModel {
		return NewValidationError("provider", "provider and model must be supplied together")
	}
	if hasProvider {
		provider, providerOK := GetString(args, "provider")
		model, modelOK := GetString(args, "model")
		provider = strings.ToLower(strings.TrimSpace(provider))
		if !providerOK || !modelOK || (provider != "glm" && provider != "kimi") || strings.TrimSpace(model) == "" {
			return NewValidationError("provider", "provider/model must select GLM or Kimi and a non-empty model")
		}
	}
	if raw, exists := args["approval_mode"]; exists {
		mode, ok := raw.(string)
		mode = strings.ToLower(strings.TrimSpace(mode))
		if !ok || (mode != "manual" && mode != "accept_edits" && mode != "auto" && mode != "skip") {
			return NewValidationError("approval_mode", "must be manual, accept_edits, auto, or skip")
		}
	}
	if _, exists := args["enabled"]; exists {
		if _, ok := GetBool(args, "enabled"); !ok {
			return NewValidationError("enabled", "must be a boolean")
		}
	}
	return nil
}

func scheduledTaskInt(args map[string]any, key string) (int, bool) {
	raw, exists := args[key]
	if !exists {
		return 0, false
	}
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		converted := int(value)
		return converted, int64(converted) == value
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
			return 0, false
		}
		converted := int(value)
		return converted, float64(converted) == value
	default:
		return 0, false
	}
}

func scheduledTaskRequiredText(args map[string]any, key string, limit int) error {
	value, ok := GetString(args, key)
	if !ok || strings.TrimSpace(value) == "" {
		return NewValidationError(key, key+" is required")
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > limit {
		return NewValidationError(key, fmt.Sprintf("must be valid UTF-8 without NUL and at most %d bytes", limit))
	}
	return nil
}

func validateScheduledLocalTime(args map[string]any) error {
	value, ok := GetString(args, "time_of_day")
	if !ok {
		return NewValidationError("time_of_day", "time_of_day is required")
	}
	if _, err := time.Parse("15:04", value); err != nil {
		return NewValidationError("time_of_day", "must use HH:MM in 24-hour local time")
	}
	return nil
}

func (t *ScheduledTaskTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.handler == nil {
		return NewErrorResult("scheduled-task handler not configured"), nil
	}
	action, _ := GetString(args, "action")
	result, err := t.handler(ctx, strings.ToLower(strings.TrimSpace(action)), args)
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	return result, nil
}
