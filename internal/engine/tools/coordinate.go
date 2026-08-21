package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
	"google.golang.org/genai"
)

// CoordinatedTaskDef defines a task for coordination.
type CoordinatedTaskDef struct {
	ID           string   `json:"id"`
	Prompt       string   `json:"prompt"`
	AgentType    string   `json:"agent_type"`
	Priority     int      `json:"priority"`
	Dependencies []string `json:"depends_on,omitempty"`
}

// CoordinationResult is the import-cycle-free result shape returned by a
// runner-backed coordinate executor.
type CoordinationResult struct {
	AgentID   string        `json:"agent_id"`
	Type      string        `json:"type"`
	Status    string        `json:"status"`
	Output    string        `json:"output"`
	Error     string        `json:"error,omitempty"`
	Duration  time.Duration `json:"duration"`
	Completed bool          `json:"completed"`
}

// CoordinateExecutor bridges CoordinateTool to an agent runtime without
// importing that runtime into the tools package.
type CoordinateExecutor func(
	ctx context.Context,
	tasks []CoordinatedTaskDef,
	maxParallel int,
	timeout time.Duration,
	callback CoordinateCallback,
) (map[string]CoordinationResult, error)

// CoordinateCallback is called when coordination events occur.
type CoordinateCallback interface {
	OnTaskStart(taskID string, agentType string, prompt string)
	OnTaskComplete(taskID string, success bool, output string)
	OnTaskProgress(taskID string, progress float64, currentStep string) // Phase 2: Progress updates
	OnTaskText(taskID string, text string)                              // Phase 2: Streaming text
	OnAllComplete(results map[string]string)
}

// CoordinateTool manages parallel agent execution with dependencies.
type CoordinateTool struct {
	coordinatorFactory func() any // Returns *agent.Coordinator
	executor           CoordinateExecutor
	callback           CoordinateCallback
}

// NewCoordinateTool creates a new coordinate tool.
func NewCoordinateTool() *CoordinateTool {
	return &CoordinateTool{}
}

// SetCoordinatorFactory sets the factory function for creating coordinators.
func (t *CoordinateTool) SetCoordinatorFactory(factory func() any) {
	t.coordinatorFactory = factory
}

// SetExecutor installs the runtime-backed coordination path. The legacy
// factory remains supported for embedders that still configure it directly.
func (t *CoordinateTool) SetExecutor(executor CoordinateExecutor) {
	t.executor = executor
}

// SetCallback sets the callback for coordination events.
func (t *CoordinateTool) SetCallback(cb CoordinateCallback) {
	t.callback = cb
}

func (t *CoordinateTool) Name() string {
	return "coordinate"
}

func (t *CoordinateTool) Description() string {
	return `Coordinates multiple agents to work in parallel on related tasks. Use this when you need to:
1. Run multiple independent tasks in parallel (e.g., explore code AND run tests)
2. Run tasks with dependencies (e.g., explore first, THEN refactor)
3. Split a complex task into subtasks with proper orchestration

Each task can specify an agent type (explore, bash, general, plan) and dependencies on other tasks.
Tasks without dependencies run in parallel. Tasks with dependencies wait for their prerequisites.`
}

func (t *CoordinateTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"tasks": {
					Type:        genai.TypeArray,
					Description: "List of tasks to coordinate",
					Items: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"id": {
								Type:        genai.TypeString,
								Description: "Unique identifier for this task (used for dependencies)",
							},
							"prompt": {
								Type:        genai.TypeString,
								Description: "The task description/prompt for the agent",
							},
							"agent_type": {
								Type:        genai.TypeString,
								Description: "Type of agent: 'explore', 'bash', 'general', or 'plan'",
								Enum:        []string{"explore", "bash", "general", "plan"},
							},
							"priority": {
								Type:        genai.TypeInteger,
								Description: "Priority (1-10, higher runs first). Default: 5",
							},
							"depends_on": {
								Type:        genai.TypeArray,
								Description: "Task IDs that must complete before this task starts",
								Items: &genai.Schema{
									Type: genai.TypeString,
								},
							},
						},
						Required: []string{"id", "prompt", "agent_type"},
					},
				},
				"max_parallel": {
					Type:        genai.TypeInteger,
					Description: "Maximum number of agents to run in parallel. Default: 3",
				},
				"timeout_minutes": {
					Type:        genai.TypeInteger,
					Description: "Maximum time to wait for all tasks (in minutes). Default: 10",
				},
			},
			Required: []string{"tasks"},
		},
	}
}

func (t *CoordinateTool) Validate(args map[string]any) error {
	tasks, ok := args["tasks"].([]any)
	if !ok || len(tasks) == 0 {
		return NewValidationError("tasks", "must be a non-empty array")
	}
	if len(tasks) > 50 {
		return NewValidationError("tasks", "must contain at most 50 tasks")
	}

	// Validate each task
	ids := make(map[string]bool)
	for i, taskAny := range tasks {
		task, ok := taskAny.(map[string]any)
		if !ok {
			return NewValidationError("tasks", fmt.Sprintf("task %d must be an object", i))
		}

		id, _ := task["id"].(string)
		if id == "" {
			return NewValidationError("tasks", fmt.Sprintf("task %d must have an id", i))
		}
		if ids[id] {
			return NewValidationError("tasks", fmt.Sprintf("duplicate task id: %s", id))
		}
		ids[id] = true

		prompt, _ := task["prompt"].(string)
		if prompt == "" {
			return NewValidationError("tasks", fmt.Sprintf("task %s must have a prompt", id))
		}

		agentType, _ := task["agent_type"].(string)
		switch agentType {
		case "explore", "bash", "general", "plan":
		case "":
			return NewValidationError("tasks", fmt.Sprintf("task %s must have an agent_type", id))
		default:
			return NewValidationError("tasks", fmt.Sprintf("task %s has unsupported agent_type: %s", id, agentType))
		}
		if _, exists := task["priority"]; exists {
			priority, valid := coordinateIntArg(task, "priority", 5)
			if !valid || priority < 1 || priority > 10 {
				return NewValidationError("tasks", fmt.Sprintf("task %s priority must be an integer from 1 to 10", id))
			}
		}
	}

	// Validate dependencies exist
	graph := make(map[string][]string, len(tasks))
	for _, taskAny := range tasks {
		task := taskAny.(map[string]any)
		id := task["id"].(string)
		if rawDeps, exists := task["depends_on"]; exists {
			deps, valid := coordinateDependencies(rawDeps)
			if !valid {
				return NewValidationError("tasks", fmt.Sprintf("task %s depends_on must be an array of task IDs", id))
			}
			seenDeps := make(map[string]bool, len(deps))
			for _, dep := range deps {
				if !ids[dep] {
					return NewValidationError("tasks", fmt.Sprintf("task %s depends on unknown task: %s", id, dep))
				}
				if dep == id {
					return NewValidationError("tasks", fmt.Sprintf("task %s cannot depend on itself", id))
				}
				if seenDeps[dep] {
					return NewValidationError("tasks", fmt.Sprintf("task %s repeats dependency: %s", id, dep))
				}
				seenDeps[dep] = true
			}
			graph[id] = deps
		}
	}
	if cycle := coordinateDependencyCycle(graph); len(cycle) > 0 {
		return NewValidationError("tasks", fmt.Sprintf("dependency cycle detected: %s", strings.Join(cycle, " -> ")))
	}

	if _, exists := args["max_parallel"]; exists {
		maxParallel, valid := coordinateIntArg(args, "max_parallel", 3)
		if !valid || maxParallel < 1 || maxParallel > 10 {
			return NewValidationError("max_parallel", "must be an integer from 1 to 10")
		}
	}
	if _, exists := args["timeout_minutes"]; exists {
		timeoutMinutes, valid := coordinateIntArg(args, "timeout_minutes", 10)
		if !valid || timeoutMinutes < 1 || timeoutMinutes > 120 {
			return NewValidationError("timeout_minutes", "must be an integer from 1 to 120")
		}
	}

	return nil
}

func (t *CoordinateTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	// Parse arguments first so the graceful-fallback path can use them.
	tasksAny := args["tasks"].([]any)
	timeoutMinutes, _ := coordinateIntArg(args, "timeout_minutes", 10)
	maxParallel, _ := coordinateIntArg(args, "max_parallel", 3)
	definitions := make([]CoordinatedTaskDef, 0, len(tasksAny))
	for _, taskAny := range tasksAny {
		task := taskAny.(map[string]any)
		priority, _ := coordinateIntArg(task, "priority", 5)
		dependencies, _ := coordinateDependencies(task["depends_on"])
		definitions = append(definitions, CoordinatedTaskDef{
			ID:           task["id"].(string),
			Prompt:       task["prompt"].(string),
			AgentType:    task["agent_type"].(string),
			Priority:     priority,
			Dependencies: dependencies,
		})
	}

	if t.executor != nil {
		results, err := t.executor(
			ctx,
			definitions,
			maxParallel,
			time.Duration(timeoutMinutes)*time.Minute,
			t.callback,
		)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("coordination failed: %v", err)), nil
		}
		return formatCoordinationResults(definitions, results), nil
	}

	// When no coordinator factory is wired, fall back to a structured task
	// plan so agents get actionable guidance instead of a bare error.
	if t.coordinatorFactory == nil {
		return t.executeSimple(ctx, tasksAny)
	}

	// Create coordinator via factory
	coordAny := t.coordinatorFactory()
	if coordAny == nil {
		return NewErrorResult("failed to create coordinator"), nil
	}

	// We use interface methods to avoid import cycle
	type coordinatorInterface interface {
		AddTask(prompt string, agentType any, priority any, deps []string) string
		Start()
		WaitWithTimeout(timeout time.Duration) (map[string]any, error)
		GetStatus() any
	}

	coord, ok := coordAny.(coordinatorInterface)
	if !ok {
		// Fall back to simplified execution
		return t.executeSimple(ctx, tasksAny)
	}
	if stopper, ok := coordAny.(interface{ Stop() }); ok {
		defer stopper.Stop()
	}

	// Build task ID mapping (user IDs -> internal IDs)
	taskIDMap := make(map[string]string)

	// Add tasks to coordinator
	for _, task := range topologicallySortedCoordinationTasks(definitions) {
		// Map dependencies to internal IDs
		deps := make([]string, 0, len(task.Dependencies))
		for _, dependencyID := range task.Dependencies {
			deps = append(deps, taskIDMap[dependencyID])
		}

		internalID := coord.AddTask(task.Prompt, task.AgentType, task.Priority, deps)
		taskIDMap[task.ID] = internalID
	}

	// Start coordination
	coord.Start()

	// Wait for completion
	timeout := time.Duration(timeoutMinutes) * time.Minute
	results, err := coord.WaitWithTimeout(timeout)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("coordination failed: %v", err)), nil
	}

	// Build result summary
	var sb strings.Builder
	sb.WriteString("## Coordination Complete\n\n")

	// Reverse map internal IDs to user IDs
	reverseMap := make(map[string]string)
	for userID, internalID := range taskIDMap {
		reverseMap[internalID] = userID
	}

	succeeded := 0
	failed := 0

	for internalID, resultAny := range results {
		userID := reverseMap[internalID]
		if userID == "" {
			userID = internalID
		}

		sb.WriteString(fmt.Sprintf("### Task: %s\n", userID))

		if resultAny == nil {
			sb.WriteString("Status: No result\n\n")
			failed++
			continue
		}

		// Extract result fields via type assertion or JSON
		resultJSON, err := json.Marshal(resultAny)
		if err != nil {
			logging.Debug("failed to marshal task result", "error", err)
			resultJSON = []byte("{}")
		}
		var result struct {
			Status string `json:"status"`
			Output string `json:"output"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(resultJSON, &result); err != nil {
			logging.Debug("failed to unmarshal task result", "error", err, "taskID", userID)
		}

		if result.Status == "completed" {
			sb.WriteString("Status: **Completed**\n")
			succeeded++
		} else {
			sb.WriteString(fmt.Sprintf("Status: **Failed** - %s\n", result.Error))
			failed++
		}

		if result.Output != "" {
			// Truncate long outputs
			output := result.Output
			if len(output) > 500 {
				output = output[:500] + "...[truncated]"
			}
			sb.WriteString(fmt.Sprintf("Output:\n```\n%s\n```\n", output))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("---\n**Summary:** %d succeeded, %d failed out of %d tasks\n",
		succeeded, failed, len(tasksAny)))

	return NewSuccessResult(sb.String()), nil
}

func formatCoordinationResults(tasks []CoordinatedTaskDef, results map[string]CoordinationResult) ToolResult {
	var sb strings.Builder
	sb.WriteString("## Coordination Complete\n\n")
	succeeded := 0
	failed := 0
	for _, task := range tasks {
		result, exists := results[task.ID]
		sb.WriteString(fmt.Sprintf("### Task: %s\n", task.ID))
		if !exists {
			sb.WriteString("Status: No result\n\n")
			failed++
			continue
		}
		if result.Status == "completed" && result.Completed {
			sb.WriteString("Status: **Completed**\n")
			succeeded++
		} else {
			errorMessage := result.Error
			if errorMessage == "" {
				errorMessage = result.Status
			}
			sb.WriteString(fmt.Sprintf("Status: **Failed** - %s\n", errorMessage))
			failed++
		}
		if result.Output != "" {
			output := result.Output
			if len(output) > 500 {
				output = output[:500] + "...[truncated]"
			}
			sb.WriteString(fmt.Sprintf("Output:\n```\n%s\n```\n", output))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("---\n**Summary:** %d succeeded, %d failed out of %d tasks\n",
		succeeded, failed, len(tasks)))
	return NewSuccessResult(sb.String())
}

// executeSimple is a fallback when coordinator interface isn't available.
func (t *CoordinateTool) executeSimple(ctx context.Context, tasksAny []any) (ToolResult, error) {
	var sb strings.Builder
	sb.WriteString("## Task Plan\n\n")
	sb.WriteString("Coordinator not available. Tasks to execute:\n\n")

	for i, taskAny := range tasksAny {
		task := taskAny.(map[string]any)
		sb.WriteString(fmt.Sprintf("%d. **%s** (%s)\n", i+1, task["id"], task["agent_type"]))
		sb.WriteString(fmt.Sprintf("   Prompt: %s\n", task["prompt"]))
		if deps, ok := task["depends_on"].([]any); ok && len(deps) > 0 {
			sb.WriteString(fmt.Sprintf("   Depends on: %v\n", deps))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Execute each task sequentially using the tools available to you (bash, read, write, edit, git, web, etc.).\n")

	return NewSuccessResult(sb.String()), nil
}

func coordinateIntArg(args map[string]any, key string, defaultValue int) (int, bool) {
	value, exists := args[key]
	if !exists || value == nil {
		return defaultValue, true
	}
	switch number := value.(type) {
	case int:
		return number, true
	case int32:
		return int(number), true
	case int64:
		return int(number), int64(int(number)) == number
	case float32:
		converted := int(number)
		return converted, float32(converted) == number
	case float64:
		converted := int(number)
		return converted, float64(converted) == number
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil && int64(int(parsed)) == parsed
	default:
		return 0, false
	}
}

func coordinateDependencies(value any) ([]string, bool) {
	if value == nil {
		return nil, true
	}
	switch dependencies := value.(type) {
	case []string:
		clone := append([]string(nil), dependencies...)
		for _, dependency := range clone {
			if strings.TrimSpace(dependency) == "" {
				return nil, false
			}
		}
		return clone, true
	case []any:
		result := make([]string, 0, len(dependencies))
		for _, rawDependency := range dependencies {
			dependency, ok := rawDependency.(string)
			if !ok || strings.TrimSpace(dependency) == "" {
				return nil, false
			}
			result = append(result, dependency)
		}
		return result, true
	default:
		return nil, false
	}
}

func coordinateDependencyCycle(graph map[string][]string) []string {
	const (
		visiting = 1
		visited  = 2
	)
	state := make(map[string]int, len(graph))
	path := make([]string, 0, len(graph))
	var visit func(string) []string
	visit = func(taskID string) []string {
		switch state[taskID] {
		case visited:
			return nil
		case visiting:
			start := 0
			for i, pathID := range path {
				if pathID == taskID {
					start = i
					break
				}
			}
			cycle := append([]string(nil), path[start:]...)
			return append(cycle, taskID)
		}
		state[taskID] = visiting
		path = append(path, taskID)
		for _, dependency := range graph[taskID] {
			if cycle := visit(dependency); len(cycle) > 0 {
				return cycle
			}
		}
		path = path[:len(path)-1]
		state[taskID] = visited
		return nil
	}
	for taskID := range graph {
		if cycle := visit(taskID); len(cycle) > 0 {
			return cycle
		}
	}
	return nil
}

func topologicallySortedCoordinationTasks(tasks []CoordinatedTaskDef) []CoordinatedTaskDef {
	remaining := append([]CoordinatedTaskDef(nil), tasks...)
	ordered := make([]CoordinatedTaskDef, 0, len(tasks))
	added := make(map[string]bool, len(tasks))
	for len(remaining) > 0 {
		next := make([]CoordinatedTaskDef, 0, len(remaining))
		for _, task := range remaining {
			ready := true
			for _, dependency := range task.Dependencies {
				if !added[dependency] {
					ready = false
					break
				}
			}
			if !ready {
				next = append(next, task)
				continue
			}
			ordered = append(ordered, task)
			added[task.ID] = true
		}
		if len(next) == len(remaining) {
			// Validate rejects this state; retaining the remaining definitions
			// makes this helper total for direct package callers as well.
			return append(ordered, next...)
		}
		remaining = next
	}
	return ordered
}
