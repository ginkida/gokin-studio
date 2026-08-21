package tools

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"google.golang.org/genai"
)

const (
	DelegateIDMaxBytes      = 128
	DelegateTaskMaxBytes    = 256 << 10
	DelegateGoalMaxBytes    = 4 << 10
	DelegateBatchMaxTargets = 4
	DelegateFetchMinBytes   = utf8.UTFMax
	DelegateFetchMaxBytes   = 8 << 10
)

// DelegateHandler is implemented by the studio layer, which owns the project
// registry, the run store and the permission model.
type DelegateHandler func(ctx context.Context, action string, args map[string]any) (ToolResult, error)

// DelegateTool lets an agent hand work to another project and address the
// result afterwards.
//
// It replaces ask_agent, whose declaration advertised a fixed role enum
// ("explore", "bash", "general", "plan") that the studio layer then matched
// against PROJECT NAMES. No project is called "explore", so the match always
// failed and routing silently fell through to "the first other project" in map
// order. `list` exists so the model can name a real target instead.
type DelegateTool struct {
	handler DelegateHandler
}

func NewDelegateTool() *DelegateTool { return &DelegateTool{} }

func (t *DelegateTool) SetHandler(h DelegateHandler) { t.handler = h }

func (t *DelegateTool) Name() string { return "delegate" }

func (t *DelegateTool) Description() string {
	return "Hand work to another Gokin Studio project and collect the result. " +
		"Call action=\"list\" first to see the real project IDs, what each project is for, " +
		"and whether it is currently reachable. Use action=\"ask\" for a bounded question " +
		"answered without tools, and action=\"run\" when the other project must actually do " +
		"work in its own checkout. Batch reserves every target atomically before any work starts. " +
		"Each returned run_id belongs only to this exact project and chat; poll it with " +
		"action=\"status\", read long answers in slices with action=\"fetch\", and stop work " +
		"with action=\"cancel\"."
}

func (t *DelegateTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type:        genai.TypeString,
					Description: "list | ask | run | batch | status | fetch | cancel",
					Enum:        []string{"list", "ask", "run", "batch", "status", "fetch", "cancel"},
				},
				"project_id": {
					Type:        genai.TypeString,
					Description: "Exact project ID from action=\"list\". Required for ask and run.",
				},
				"task": {
					Type:        genai.TypeString,
					Description: "What the other project should answer or do. Required for ask and run.",
				},
				"goal": {
					Type:        genai.TypeString,
					Description: "Why you need it. Shown to the user on the approval card and to the target agent.",
				},
				"targets": {
					Type:        genai.TypeArray,
					Description: "Fan-out targets (batch only): up to 4 entries of {project_id, task}. One invalid entry rejects the whole batch.",
					Items: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"project_id": {Type: genai.TypeString},
							"task":       {Type: genai.TypeString},
						},
					},
				},
				"run_id": {
					Type:        genai.TypeString,
					Description: "Run to address. Required for status, fetch and cancel.",
				},
				"offset": {
					Type:        genai.TypeInteger,
					Description: "Byte offset into the stored answer (fetch only).",
				},
				"max_bytes": {
					Type:        genai.TypeInteger,
					Description: "Requested page size in bytes, from 4 to 8192 (fetch only).",
				},
			},
			Required: []string{"action"},
		},
	}
}

func (t *DelegateTool) Validate(args map[string]any) error {
	action := strings.ToLower(strings.TrimSpace(GetStringDefault(args, "action", "")))
	switch action {
	case "list":
		return nil
	case "ask", "run":
		if err := delegateRequiredText(args, "project_id", DelegateIDMaxBytes); err != nil {
			return fmt.Errorf("%w; call action=\"list\" to get the exact IDs", err)
		}
		if err := delegateRequiredText(args, "task", DelegateTaskMaxBytes); err != nil {
			return err
		}
		return delegateOptionalText(args, "goal", DelegateGoalMaxBytes)
	case "batch":
		raw, ok := args["targets"].([]any)
		if !ok || len(raw) == 0 {
			return fmt.Errorf("targets is required for batch; call action=\"list\" to get the exact IDs")
		}
		if len(raw) > DelegateBatchMaxTargets {
			return NewValidationError("targets", fmt.Sprintf("must contain at most %d projects", DelegateBatchMaxTargets))
		}
		if err := delegateOptionalText(args, "goal", DelegateGoalMaxBytes); err != nil {
			return err
		}
		if err := delegateOptionalText(args, "task", DelegateTaskMaxBytes); err != nil {
			return err
		}
		sharedTask := strings.TrimSpace(GetStringDefault(args, "task", ""))
		seen := make(map[string]struct{}, len(raw))
		for index, item := range raw {
			entry, ok := item.(map[string]any)
			if !ok {
				return NewValidationError("targets", fmt.Sprintf("entry %d must be an object", index+1))
			}
			if err := delegateRequiredText(entry, "project_id", DelegateIDMaxBytes); err != nil {
				return NewValidationError("targets", fmt.Sprintf("entry %d: %v", index+1, err))
			}
			projectID := strings.TrimSpace(GetStringDefault(entry, "project_id", ""))
			if _, duplicate := seen[projectID]; duplicate {
				return NewValidationError("targets", fmt.Sprintf("entry %d duplicates project_id %q", index+1, projectID))
			}
			seen[projectID] = struct{}{}
			if err := delegateOptionalText(entry, "task", DelegateTaskMaxBytes); err != nil {
				return NewValidationError("targets", fmt.Sprintf("entry %d: %v", index+1, err))
			}
			if strings.TrimSpace(GetStringDefault(entry, "task", "")) == "" && sharedTask == "" {
				return NewValidationError("targets", fmt.Sprintf("entry %d needs task or a shared task", index+1))
			}
		}
		return nil
	case "status", "fetch", "cancel":
		if err := delegateRequiredText(args, "run_id", DelegateIDMaxBytes); err != nil {
			return err
		}
		if action == "fetch" {
			if err := delegateOptionalInt(args, "offset", 0); err != nil {
				return err
			}
			if err := delegateOptionalInt(args, "max_bytes", DelegateFetchMinBytes); err != nil {
				return err
			}
		}
		return nil
	case "":
		return fmt.Errorf("action is required")
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

func delegateRequiredText(args map[string]any, key string, maxBytes int) error {
	value, ok := GetString(args, key)
	if !ok || strings.TrimSpace(value) == "" {
		return NewValidationError(key, "is required")
	}
	return delegateTextValue(key, value, maxBytes)
}

func delegateOptionalText(args map[string]any, key string, maxBytes int) error {
	value, exists := args[key]
	if !exists {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return NewValidationError(key, "must be text")
	}
	return delegateTextValue(key, text, maxBytes)
}

func delegateTextValue(key, value string, maxBytes int) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > maxBytes {
		return NewValidationError(key, fmt.Sprintf("must be valid UTF-8 text up to %d bytes", maxBytes))
	}
	return nil
}

func delegateOptionalInt(args map[string]any, key string, minimum int) error {
	raw, exists := args[key]
	if !exists {
		return nil
	}
	value, ok := GetInt(args, key)
	if !ok {
		return NewValidationError(key, "must be an integer")
	}
	switch number := raw.(type) {
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || float64(value) != number {
			return NewValidationError(key, "must be an integer")
		}
	case int64:
		if int64(value) != number {
			return NewValidationError(key, "is outside the supported integer range")
		}
	}
	if value < minimum {
		return NewValidationError(key, fmt.Sprintf("must be at least %d", minimum))
	}
	return nil
}

func (t *DelegateTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.handler == nil {
		return NewErrorResult("delegation is unavailable in this session"), nil
	}
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	action := strings.ToLower(strings.TrimSpace(GetStringDefault(args, "action", "")))
	return t.handler(ctx, action, args)
}
