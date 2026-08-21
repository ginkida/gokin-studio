package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/fileutil"
	"github.com/ginkida/gokin-studio/internal/engine/tasks"

	"google.golang.org/genai"
)

const (
	maxTaskOutputReadBytes   = 1 * 1024 * 1024
	defaultTaskOutputTimeout = 120_000
	minTaskOutputTimeout     = 100
	maxTaskOutputTimeout     = 600_000
)

// TaskOutputTool retrieves output from background tasks (both shell and agent tasks).
type TaskOutputTool struct {
	manager *tasks.Manager
	runner  AgentRunner // For agent tasks
}

// NewTaskOutputTool creates a new task output tool.
func NewTaskOutputTool() *TaskOutputTool {
	return &TaskOutputTool{}
}

// SetManager sets the task manager for shell tasks.
func (t *TaskOutputTool) SetManager(manager *tasks.Manager) {
	t.manager = manager
}

// SetRunner sets the agent runner for agent tasks.
func (t *TaskOutputTool) SetRunner(runner AgentRunner) {
	t.runner = runner
}

func (t *TaskOutputTool) Name() string {
	return "task_output"
}

func (t *TaskOutputTool) Description() string {
	return "Get output from a background task or list all tasks"
}

func (t *TaskOutputTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"task_id": {
					Type:        genai.TypeString,
					Description: "Opaque shell-task or agent ID returned by bash/task. If omitted, lists all tasks.",
				},
				"action": {
					Type:        genai.TypeString,
					Description: "Action to perform: 'get', 'list', or 'cancel'. If omitted, gets task_id when present or lists all tasks.",
					Enum:        []string{"get", "list", "cancel"},
				},
				"block": {
					Type:        genai.TypeBoolean,
					Description: "If true, wait for task completion before returning. Default: false",
				},
				"timeout_ms": {
					Type:        genai.TypeInteger,
					Description: "Timeout in milliseconds when blocking. Default: 120000 (2 minutes). Max: 600000 (10 minutes).",
				},
				"offset": {
					Type:        genai.TypeInteger,
					Description: "Byte offset to read output from. Use this for incremental reads of long-running tasks. Returns only new output since the offset.",
				},
			},
		},
	}
}

func (t *TaskOutputTool) Validate(args map[string]any) error {
	action, err := taskOutputAction(args)
	if err != nil {
		return err
	}

	taskID, hasTaskID := GetString(args, "task_id")
	if _, exists := args["task_id"]; exists && (!hasTaskID || strings.TrimSpace(taskID) == "") {
		return NewValidationError("task_id", "must be a non-empty string")
	}
	if action == "get" || action == "cancel" {
		if !hasTaskID {
			return NewValidationError("task_id", "task_id is required for this action")
		}
	}
	if _, exists := args["block"]; exists {
		if _, ok := GetBool(args, "block"); !ok {
			return NewValidationError("block", "must be a boolean")
		}
	}
	if _, err := taskOutputTimeout(args); err != nil {
		return err
	}
	if _, _, err := parseTaskOutputOffset(args); err != nil {
		return NewValidationError("offset", err.Error())
	}

	return nil
}

func (t *TaskOutputTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult("validation error: " + err.Error()), nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	action, _ := taskOutputAction(args)
	taskID := strings.TrimSpace(GetStringDefault(args, "task_id", ""))
	block := GetBoolDefault(args, "block", false)
	timeoutMs, _ := taskOutputTimeout(args)
	offset, offsetProvided, _ := parseTaskOutputOffset(args)
	timeout := time.Duration(timeoutMs) * time.Millisecond

	switch action {
	case "list":
		return t.listTasks()
	case "cancel":
		return t.cancelTask(ctx, taskID)
	default:
		return t.getTaskOutput(ctx, taskID, block, timeout, offset, offsetProvided)
	}
}

func taskOutputAction(args map[string]any) (string, error) {
	raw, exists := args["action"]
	if !exists {
		if taskID, ok := GetString(args, "task_id"); ok && strings.TrimSpace(taskID) != "" {
			return "get", nil
		}
		return "list", nil
	}
	action, ok := raw.(string)
	if !ok {
		return "", NewValidationError("action", "must be one of: get, list, cancel")
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "get", "list", "cancel":
		return action, nil
	default:
		return "", NewValidationError("action", "must be one of: get, list, cancel")
	}
}

func taskOutputTimeout(args map[string]any) (int, error) {
	raw, exists := args["timeout_ms"]
	if !exists {
		return defaultTaskOutputTimeout, nil
	}

	var value int64
	switch number := raw.(type) {
	case int:
		value = int64(number)
	case int64:
		value = number
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number ||
			number < minTaskOutputTimeout || number > maxTaskOutputTimeout {
			return 0, NewValidationError("timeout_ms", fmt.Sprintf("must be an integer between %d and %d", minTaskOutputTimeout, maxTaskOutputTimeout))
		}
		value = int64(number)
	default:
		return 0, NewValidationError("timeout_ms", fmt.Sprintf("must be an integer between %d and %d", minTaskOutputTimeout, maxTaskOutputTimeout))
	}
	if value < minTaskOutputTimeout || value > maxTaskOutputTimeout {
		return 0, NewValidationError("timeout_ms", fmt.Sprintf("must be an integer between %d and %d", minTaskOutputTimeout, maxTaskOutputTimeout))
	}
	return int(value), nil
}

func parseTaskOutputOffset(args map[string]any) (int64, bool, error) {
	raw, provided := args["offset"]
	if !provided {
		return 0, false, nil
	}
	var value int64
	switch number := raw.(type) {
	case int:
		value = int64(number)
	case int64:
		value = number
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number >= float64(math.MaxInt64) {
			return 0, true, fmt.Errorf("must be an integer in the supported range")
		}
		value = int64(number)
	default:
		return 0, true, fmt.Errorf("must be an integer")
	}
	if value < 0 {
		return 0, true, fmt.Errorf("must not be negative")
	}
	return value, true, nil
}

func (t *TaskOutputTool) getTaskOutput(ctx context.Context, taskID string, block bool, timeout time.Duration, offset int64, offsetProvided bool) (ToolResult, error) {
	// Resolve against the runner instead of guessing from ID syntax. Engine
	// agent IDs are opaque and older stores may contain multiple formats.
	if t.hasAgentTask(taskID) {
		return t.getAgentOutput(ctx, taskID, block, timeout, offset, offsetProvided)
	}

	// Fall back to shell task manager
	if t.manager == nil {
		return NewErrorResult("task manager not configured"), nil
	}

	// If blocking, wait for completion
	if block {
		return t.waitForShellTask(ctx, taskID, timeout, offset, offsetProvided)
	}

	info, ok := t.manager.GetInfo(taskID)
	if !ok {
		return NewErrorResult(fmt.Sprintf("task not found: %s", taskID)), nil
	}
	return t.shellTaskSnapshot(info, offset, offsetProvided)
}

func (t *TaskOutputTool) hasAgentTask(taskID string) bool {
	if t.runner == nil {
		return false
	}
	_, ok := t.runner.GetResult(taskID)
	return ok
}

// getAgentOutput retrieves output from an agent task
func (t *TaskOutputTool) getAgentOutput(ctx context.Context, agentID string, block bool, timeout time.Duration, offset int64, offsetProvided bool) (ToolResult, error) {
	// If blocking, wait for completion with timeout
	if block {
		return t.waitForAgentTask(ctx, agentID, timeout, offset, offsetProvided)
	}

	// Non-blocking: just get current status
	result, ok := t.runner.GetResult(agentID)
	if !ok {
		return NewErrorResult(fmt.Sprintf("agent not found: %s", agentID)), nil
	}

	return t.agentTaskSnapshot(result, offset, offsetProvided)
}

// waitForAgentTask waits for an agent to complete with timeout
func (t *TaskOutputTool) waitForAgentTask(
	ctx context.Context,
	agentID string,
	timeout time.Duration,
	offset int64,
	offsetProvided bool,
) (ToolResult, error) {
	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Poll for completion
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		result, ok := t.runner.GetResult(agentID)
		if !ok {
			return NewErrorResult(fmt.Sprintf("agent not found: %s", agentID)), nil
		}
		// Check before sleeping so an already-completed task has no artificial
		// 100ms delay and wins over a simultaneously cancelled wait context.
		if result.Completed {
			return t.agentTaskSnapshot(result, offset, offsetProvided)
		}
		select {
		case <-ctx.Done():
			result, ok = t.runner.GetResult(agentID)
			if !ok {
				return NewErrorResult(fmt.Sprintf("agent not found: %s", agentID)), nil
			}
			if result.Completed {
				return t.agentTaskSnapshot(result, offset, offsetProvided)
			}
			timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
			snapshot, err := t.agentTaskSnapshot(result, offset, offsetProvided)
			if err != nil {
				return ToolResult{}, err
			}
			header := "**Stopped waiting for agent completion**"
			if timedOut {
				header = "**Timeout waiting for agent completion**"
			}
			return decorateTaskWaitResult(snapshot, header, map[string]any{
				"agent_id":       agentID,
				"status":         string(result.Status),
				"completed":      result.Completed,
				"timeout":        timedOut,
				"wait_cancelled": !timedOut,
			}), nil

		case <-ticker.C:
		}
	}
}

// waitForShellTask waits for a shell task to complete with timeout
func (t *TaskOutputTool) waitForShellTask(
	ctx context.Context,
	taskID string,
	timeout time.Duration,
	offset int64,
	offsetProvided bool,
) (ToolResult, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	info, err := t.manager.Wait(waitCtx, taskID)
	if err == nil {
		return t.shellTaskSnapshot(info, offset, offsetProvided)
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return NewErrorResult(err.Error()), nil
	}

	timedOut := errors.Is(err, context.DeadlineExceeded)
	// Return a bounded partial snapshot when the caller stops waiting. The task
	// itself remains owned by the manager and may continue in the background.
	snapshot, snapshotErr := t.shellTaskSnapshot(info, offset, offsetProvided)
	if snapshotErr != nil {
		return ToolResult{}, snapshotErr
	}
	header := "**Stopped waiting for task completion**"
	if timedOut {
		header = "**Timeout waiting for task completion**"
	}
	return decorateTaskWaitResult(snapshot, header, map[string]any{
		"task_id":        taskID,
		"status":         info.Status,
		"running":        info.Status == "running",
		"timeout":        timedOut,
		"wait_cancelled": !timedOut,
	}), nil
}

func (t *TaskOutputTool) shellTaskSnapshot(info tasks.Info, offset int64, offsetProvided bool) (ToolResult, error) {
	if offsetProvided && info.OutputFile != "" {
		return t.readShellOutputFromFile(info, offset)
	}
	return t.formatShellTaskResult(info), nil
}

func (t *TaskOutputTool) agentTaskSnapshot(result AgentResult, offset int64, offsetProvided bool) (ToolResult, error) {
	if offsetProvided && result.OutputFile != "" {
		return t.readAgentOutputFromFile(result, offset)
	}
	return t.formatAgentResult(result), nil
}

func decorateTaskWaitResult(snapshot ToolResult, header string, waitData map[string]any) ToolResult {
	data := make(map[string]any, len(waitData)+8)
	if snapshotData, ok := snapshot.Data.(map[string]any); ok {
		for key, value := range snapshotData {
			data[key] = value
		}
	}
	for key, value := range waitData {
		data[key] = value
	}
	if snapshot.Content == "" {
		snapshot.Content = header
	} else {
		snapshot.Content = header + "\n\n" + snapshot.Content
	}
	snapshot.Data = data
	return snapshot
}

// formatShellTaskResult formats a shell task result
func (t *TaskOutputTool) formatShellTaskResult(info tasks.Info) ToolResult {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Task: %s\n", info.ID))
	builder.WriteString(fmt.Sprintf("Status: %s\n", info.Status))
	builder.WriteString(fmt.Sprintf("Command: %s\n", info.Command))
	builder.WriteString(fmt.Sprintf("Duration: %s\n", info.Duration))

	if info.Error != "" {
		builder.WriteString(fmt.Sprintf("Error: %s\n", info.Error))
	}
	if info.ExitCode != 0 {
		builder.WriteString(fmt.Sprintf("Exit Code: %d\n", info.ExitCode))
	}

	if info.Output != "" {
		builder.WriteString("\nOutput:\n")
		builder.WriteString(info.Output)
	}

	data := map[string]any{
		"task_id":   info.ID,
		"status":    info.Status,
		"command":   info.Command,
		"output":    info.Output,
		"error":     info.Error,
		"exit_code": info.ExitCode,
		"running":   info.Status == "running",
	}
	if info.OutputFile != "" {
		data["output_file"] = info.OutputFile
		data["total_bytes"] = info.TotalBytes
		data["next_offset"] = info.TotalBytes
	}
	return NewSuccessResultWithData(builder.String(), data)
}

// readShellOutputFromFile is the shell-task counterpart to agent incremental
// reads. Both paths share the same regular-file and allocation boundary.
func (t *TaskOutputTool) readShellOutputFromFile(info tasks.Info, offset int64) (ToolResult, error) {
	data, nextOffset, totalBytes, err := fileutil.ReadRegularFileRange(
		info.OutputFile,
		offset,
		maxTaskOutputReadBytes,
	)
	if err != nil {
		return t.formatShellTaskResult(info), nil
	}
	if len(data) == 0 {
		return NewSuccessResultWithData("No new output since last read.", map[string]any{
			"task_id":     info.ID,
			"status":      info.Status,
			"offset":      offset,
			"next_offset": offset,
			"total_bytes": totalBytes,
			"running":     info.Status == "running",
		}), nil
	}

	newOutput := string(data)
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Task: %s (incremental read)\n", info.ID))
	builder.WriteString(fmt.Sprintf("Status: %s\n", info.Status))
	builder.WriteString(fmt.Sprintf("Bytes: %d-%d of %d\n", offset, nextOffset, totalBytes))
	builder.WriteString("\nNew output:\n")
	builder.WriteString(newOutput)
	return NewSuccessResultWithData(builder.String(), map[string]any{
		"task_id":     info.ID,
		"status":      info.Status,
		"offset":      offset,
		"next_offset": nextOffset,
		"total_bytes": totalBytes,
		"output":      newOutput,
		"running":     info.Status == "running",
	}), nil
}

// readAgentOutputFromFile reads agent output from file starting at offset.
// This enables incremental reads for long-running agents without loading
// the entire output into memory.
func (t *TaskOutputTool) readAgentOutputFromFile(result AgentResult, offset int64) (ToolResult, error) {
	data, nextOffset, totalBytes, err := fileutil.ReadRegularFileRange(
		result.OutputFile,
		offset,
		maxTaskOutputReadBytes,
	)
	if err != nil {
		// Fall back to the bounded in-memory portion when the stream vanished or
		// was replaced by a non-regular path.
		return t.formatAgentResult(result), nil
	}

	if len(data) == 0 {
		// No new output
		return NewSuccessResultWithData("No new output since last read.", map[string]any{
			"agent_id":    result.AgentID,
			"status":      result.Status,
			"completed":   result.Completed,
			"offset":      offset,
			"next_offset": offset,
			"total_bytes": totalBytes,
		}), nil
	}

	newOutput := string(data)

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Agent: %s (incremental read)\n", result.AgentID))
	builder.WriteString(fmt.Sprintf("Status: %s\n", result.Status))
	builder.WriteString(fmt.Sprintf("Bytes: %d-%d of %d\n", offset, nextOffset, totalBytes))
	builder.WriteString("\nNew output:\n")
	builder.WriteString(newOutput)

	return NewSuccessResultWithData(builder.String(), map[string]any{
		"agent_id":    result.AgentID,
		"status":      result.Status,
		"completed":   result.Completed,
		"offset":      offset,
		"next_offset": nextOffset,
		"total_bytes": totalBytes,
		"output":      newOutput,
	}), nil
}

// formatAgentResult formats an agent result
func (t *TaskOutputTool) formatAgentResult(result AgentResult) ToolResult {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Agent: %s\n", result.AgentID))
	builder.WriteString(fmt.Sprintf("Type: %s\n", result.Type))
	builder.WriteString(fmt.Sprintf("Status: %s\n", result.Status))
	builder.WriteString(fmt.Sprintf("Duration: %s\n", result.Duration))

	if result.Error != "" {
		builder.WriteString(fmt.Sprintf("Error: %s\n", result.Error))
	}

	if result.Output != "" {
		builder.WriteString("\nOutput:\n")
		builder.WriteString(result.Output)
	}

	data := map[string]any{
		"agent_id":  result.AgentID,
		"type":      result.Type,
		"status":    result.Status,
		"output":    result.Output,
		"error":     result.Error,
		"duration":  result.Duration.String(),
		"completed": result.Completed,
		"running":   result.Status == "running",
	}

	// Include output file info for incremental reading
	if result.OutputFile != "" {
		data["output_file"] = result.OutputFile
		if fi, err := os.Stat(result.OutputFile); err == nil {
			data["total_bytes"] = fi.Size()
			data["next_offset"] = fi.Size()
		}
	}

	return NewSuccessResultWithData(builder.String(), data)
}

func (t *TaskOutputTool) listTasks() (ToolResult, error) {
	var builder strings.Builder
	totalCount := 0

	// List shell tasks
	var shellTasks []tasks.Info
	if t.manager != nil {
		shellTasks = t.manager.List()
	}

	// List agent tasks
	var agentTasks []AgentResult
	if t.runner != nil {
		// Get all agent IDs and their results
		if lister, ok := t.runner.(AgentLister); ok {
			for _, agentID := range lister.ListAgents() {
				if result, ok := t.runner.GetResult(agentID); ok {
					agentTasks = append(agentTasks, result)
				}
			}
		}
	}

	totalCount = len(shellTasks) + len(agentTasks)

	if totalCount == 0 {
		return NewSuccessResult("No background tasks"), nil
	}

	builder.WriteString(fmt.Sprintf("Background Tasks (%d total):\n\n", totalCount))

	// Shell tasks
	if len(shellTasks) > 0 {
		builder.WriteString("**Shell Tasks:**\n")
		for _, info := range shellTasks {
			status := info.Status
			if status == "completed" {
				status = "done"
			}

			// Truncate command if too long
			cmd := info.Command
			if len(cmd) > 50 {
				cmd = cmd[:47] + "..."
			}

			builder.WriteString(fmt.Sprintf("  [%s] %s - %s (%s)\n", status, info.ID, cmd, info.Duration))
		}
		builder.WriteString("\n")
	}

	// Agent tasks
	if len(agentTasks) > 0 {
		builder.WriteString("**Agent Tasks:**\n")
		for _, result := range agentTasks {
			status := string(result.Status)
			if status == "completed" {
				status = "done"
			}

			builder.WriteString(fmt.Sprintf("  [%s] %s - %s (%s)\n", status, result.AgentID, result.Type, result.Duration.Round(time.Millisecond)))
		}
	}

	// JSON data for structured access
	shellData, err := json.Marshal(shellTasks)
	if err != nil {
		shellData = []byte("[]")
	}
	agentData, err := json.Marshal(agentTasks)
	if err != nil {
		agentData = []byte("[]")
	}

	return NewSuccessResultWithData(builder.String(), map[string]any{
		"shell_tasks": string(shellData),
		"agent_tasks": string(agentData),
		"count":       totalCount,
	}), nil
}

func (t *TaskOutputTool) cancelTask(ctx context.Context, taskID string) (ToolResult, error) {
	if t.hasAgentTask(taskID) {
		if canceller, ok := t.runner.(AgentCanceller); ok {
			if err := canceller.Cancel(taskID); err != nil {
				return NewErrorResult(err.Error()), nil
			}
			return NewSuccessResult(fmt.Sprintf("Agent %s cancelled", taskID)), nil
		}
		return NewErrorResult("agent cancellation not supported"), nil
	}

	// Fall back to shell task manager
	if t.manager == nil {
		return NewErrorResult("task manager not configured"), nil
	}

	if err := t.manager.Cancel(taskID); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	return NewSuccessResult(fmt.Sprintf("Task %s cancelled", taskID)), nil
}

// AgentLister is an interface for listing agents.
type AgentLister interface {
	ListAgents() []string
}

// AgentCanceller is an interface for cancelling agents.
type AgentCanceller interface {
	Cancel(agentID string) error
}
