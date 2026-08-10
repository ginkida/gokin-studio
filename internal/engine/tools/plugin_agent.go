package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"google.golang.org/genai"
)

const maxPluginAgentTaskBytes = 256 << 10

type PluginAgentSpec struct {
	ID          string
	Description string
}

type PluginAgentRunResult struct {
	Agent     string
	SessionID string
	Response  string
}

type PluginAgentRunner interface {
	RunPluginAgent(ctx context.Context, agentID, task string) (PluginAgentRunResult, error)
}

// PluginAgentTool delegates one bounded task to a reviewed agent definition
// from an enabled plugin. The runner owns session isolation and permission
// enforcement; this adapter only exposes the catalog to the model.
type PluginAgentTool struct {
	specs  []PluginAgentSpec
	byID   map[string]PluginAgentSpec
	runner PluginAgentRunner
}

func NewPluginAgentTool(specs []PluginAgentSpec, runner PluginAgentRunner) *PluginAgentTool {
	copied := append([]PluginAgentSpec(nil), specs...)
	sort.SliceStable(copied, func(i, j int) bool { return copied[i].ID < copied[j].ID })
	byID := make(map[string]PluginAgentSpec, len(copied))
	for _, spec := range copied {
		byID[spec.ID] = spec
	}
	return &PluginAgentTool{specs: copied, byID: byID, runner: runner}
}

func (t *PluginAgentTool) Name() string { return "plugin_agent" }

func (t *PluginAgentTool) Description() string {
	var b strings.Builder
	b.WriteString("Delegate a focused task to a reviewed specialist agent from an enabled plugin. ")
	b.WriteString("The specialist runs in an inspectable child chat with the current GLM/Kimi model and the user's permission policy. Available agents:")
	for _, spec := range t.specs {
		line := "\n- " + spec.ID
		if description := strings.TrimSpace(spec.Description); description != "" {
			line += ": " + description
		}
		if b.Len()+len(line) > 6000 {
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

func (t *PluginAgentTool) Declaration() *genai.FunctionDeclaration {
	ids := make([]string, 0, len(t.specs))
	for _, spec := range t.specs {
		ids = append(ids, spec.ID)
	}
	return &genai.FunctionDeclaration{
		Name: t.Name(), Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"agent": {
					Type:        genai.TypeString,
					Enum:        ids,
					Description: "Exact plugin:agent identifier from the reviewed catalog",
				},
				"task": {
					Type:        genai.TypeString,
					Description: "Self-contained task for the specialist, including the expected deliverable",
				},
			},
			Required: []string{"agent", "task"},
		},
	}
}

func (t *PluginAgentTool) Validate(args map[string]any) error {
	agent, ok := GetString(args, "agent")
	if !ok || strings.TrimSpace(agent) == "" {
		return NewValidationError("agent", "is required")
	}
	if _, exists := t.byID[agent]; !exists {
		return NewValidationError("agent", "must identify an enabled reviewed plugin agent")
	}
	task, ok := GetString(args, "task")
	if !ok || strings.TrimSpace(task) == "" {
		return NewValidationError("task", "is required")
	}
	if len(task) > maxPluginAgentTaskBytes || !utf8.ValidString(task) || strings.ContainsRune(task, 0) {
		return NewValidationError("task", fmt.Sprintf("must be UTF-8 text up to %d KiB", maxPluginAgentTaskBytes>>10))
	}
	return nil
}

func (t *PluginAgentTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	if t.runner == nil {
		return NewErrorResult("plugin agent runner is unavailable"), nil
	}
	agent, _ := GetString(args, "agent")
	task, _ := GetString(args, "task")
	result, err := t.runner.RunPluginAgent(ctx, agent, task)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("plugin agent %s failed: %v", agent, err)), nil
	}
	return NewSuccessResultWithData(
		fmt.Sprintf("Response from plugin agent %s (child chat %s):\n\n%s", result.Agent, result.SessionID, result.Response),
		map[string]any{
			"agent":      result.Agent,
			"session_id": result.SessionID,
		},
	), nil
}
