package tools

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tasks"
)

type opaqueAgentRunner struct {
	results   map[string]AgentResult
	cancelled []string
}

func (r *opaqueAgentRunner) Spawn(context.Context, string, string, int, string) (string, error) {
	return "", nil
}
func (r *opaqueAgentRunner) SpawnAsync(context.Context, string, string, int, string) string {
	return ""
}
func (r *opaqueAgentRunner) SpawnAsyncWithStreaming(context.Context, string, string, int, string, func(string), func(string, *AgentProgress)) string {
	return ""
}
func (r *opaqueAgentRunner) Resume(context.Context, string, string) (string, error) {
	return "", nil
}
func (r *opaqueAgentRunner) ResumeAsync(context.Context, string, string) (string, error) {
	return "", nil
}
func (r *opaqueAgentRunner) GetResult(id string) (AgentResult, bool) {
	result, ok := r.results[id]
	return result, ok
}
func (r *opaqueAgentRunner) ListAgents() []string {
	ids := make([]string, 0, len(r.results))
	for id := range r.results {
		ids = append(ids, id)
	}
	return ids
}
func (r *opaqueAgentRunner) Cancel(id string) error {
	r.cancelled = append(r.cancelled, id)
	return nil
}

func TestTaskToolsRouteOpaqueAgentIDsByRunnerMembership(t *testing.T) {
	const agentID = "0123456789abcdef" // Current engine format: no UUID dashes/prefix.
	runner := &opaqueAgentRunner{results: map[string]AgentResult{
		agentID: {
			AgentID: agentID, Type: "explore", Status: "running",
			Output: "live agent output",
		},
	}}

	output := NewTaskOutputTool()
	output.SetRunner(runner)
	result, err := output.Execute(context.Background(), map[string]any{
		"action": "get", "task_id": agentID,
	})
	if err != nil || !result.Success || !strings.Contains(result.Content, "live agent output") {
		t.Fatalf("opaque agent output = %+v, %v", result, err)
	}
	result, err = output.Execute(context.Background(), map[string]any{
		"action": "cancel", "task_id": agentID,
	})
	if err != nil || !result.Success || len(runner.cancelled) != 1 || runner.cancelled[0] != agentID {
		t.Fatalf("opaque agent cancel = %+v, %v; calls=%v", result, err, runner.cancelled)
	}

	stop := NewTaskStopTool()
	stop.SetRunner(runner)
	result, err = stop.Execute(context.Background(), map[string]any{"task_id": agentID})
	if err != nil || !result.Success || len(runner.cancelled) != 2 || runner.cancelled[1] != agentID {
		t.Fatalf("opaque agent stop = %+v, %v; calls=%v", result, err, runner.cancelled)
	}
}

func TestTaskStopRejectsAlreadyCompletedShellTask(t *testing.T) {
	manager := tasks.NewManager(t.TempDir())
	manager.SetWorkspaceSandboxEnabled(false)
	taskID, err := manager.StartWithArgs(context.Background(), "sh", []string{"-c", "exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := manager.Get(taskID)
	if !ok {
		t.Fatalf("started task %q is missing", taskID)
	}
	select {
	case <-task.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shell task did not complete")
	}

	stop := NewTaskStopTool()
	stop.SetManager(manager)
	result, err := stop.Execute(context.Background(), map[string]any{"task_id": taskID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "not running") {
		t.Fatalf("stop completed task = %+v", result)
	}
}

func TestTaskStopExecuteEnforcesValidation(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "missing id", args: map[string]any{}},
		{name: "blank id", args: map[string]any{"task_id": " \t"}},
		{name: "non-string id", args: map[string]any{"task_id": 42}},
		{name: "non-string reason", args: map[string]any{"task_id": "task-1", "reason": true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := NewTaskStopTool()
			if err := tool.Validate(test.args); err == nil {
				t.Fatal("Validate accepted malformed arguments")
			}
			result, err := tool.Execute(context.Background(), test.args)
			if err != nil || result.Success || !strings.Contains(result.Error, "validation error") {
				t.Fatalf("Execute = %+v, %v; want validation failure", result, err)
			}
		})
	}
}

func TestTaskOutputInfersActionWhenOmitted(t *testing.T) {
	tool := NewTaskOutputTool()
	if err := tool.Validate(map[string]any{}); err != nil {
		t.Fatalf("empty arguments should list tasks: %v", err)
	}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil || !result.Success || result.Content != "No background tasks" {
		t.Fatalf("implicit list = %+v, %v", result, err)
	}

	const agentID = "agent-implicit-get"
	runner := &opaqueAgentRunner{results: map[string]AgentResult{
		agentID: {AgentID: agentID, Type: "explore", Status: "completed", Completed: true, Output: "done"},
	}}
	tool.SetRunner(runner)
	result, err = tool.Execute(context.Background(), map[string]any{"task_id": agentID})
	if err != nil || !result.Success || !strings.Contains(result.Content, "done") {
		t.Fatalf("implicit get = %+v, %v", result, err)
	}

	result, err = tool.Execute(context.Background(), map[string]any{"action": " LIST "})
	if err != nil || !result.Success || !strings.Contains(result.Content, "Background Tasks") {
		t.Fatalf("normalized list = %+v, %v", result, err)
	}
}

func TestTaskOutputExecuteEnforcesArgumentTypesAndBounds(t *testing.T) {
	tests := []struct {
		name  string
		args  map[string]any
		field string
	}{
		{name: "non-string action", args: map[string]any{"action": 1}, field: "action"},
		{name: "blank action", args: map[string]any{"action": " "}, field: "action"},
		{name: "non-string id", args: map[string]any{"action": "get", "task_id": 1}, field: "task_id"},
		{name: "blank id", args: map[string]any{"action": "get", "task_id": " \t"}, field: "task_id"},
		{name: "non-boolean block", args: map[string]any{"action": "list", "block": "true"}, field: "block"},
		{name: "fractional timeout", args: map[string]any{"action": "list", "timeout_ms": 100.5}, field: "timeout_ms"},
		{name: "string timeout", args: map[string]any{"action": "list", "timeout_ms": "100"}, field: "timeout_ms"},
		{name: "nan timeout", args: map[string]any{"action": "list", "timeout_ms": math.NaN()}, field: "timeout_ms"},
		{name: "short timeout", args: map[string]any{"action": "list", "timeout_ms": minTaskOutputTimeout - 1}, field: "timeout_ms"},
		{name: "long timeout", args: map[string]any{"action": "list", "timeout_ms": maxTaskOutputTimeout + 1}, field: "timeout_ms"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := NewTaskOutputTool()
			validationErr := tool.Validate(test.args)
			if validationErr == nil || !strings.Contains(validationErr.Error(), test.field) {
				t.Fatalf("Validate error = %v; want field %q", validationErr, test.field)
			}
			result, err := tool.Execute(context.Background(), test.args)
			if err != nil || result.Success || !strings.Contains(result.Error, test.field) {
				t.Fatalf("Execute = %+v, %v; want %s failure", result, err, test.field)
			}
		})
	}

	for _, timeout := range []any{minTaskOutputTimeout, int64(maxTaskOutputTimeout), float64(1_000)} {
		if err := NewTaskOutputTool().Validate(map[string]any{"action": "list", "timeout_ms": timeout}); err != nil {
			t.Errorf("Validate rejected timeout %#v: %v", timeout, err)
		}
	}
}

func TestTaskOutputBlockingAcceptsNilContext(t *testing.T) {
	const agentID = "agent-completed"
	runner := &opaqueAgentRunner{results: map[string]AgentResult{
		agentID: {AgentID: agentID, Type: "explore", Status: "completed", Completed: true},
	}}
	tool := NewTaskOutputTool()
	tool.SetRunner(runner)

	result, err := tool.Execute(nil, map[string]any{
		"task_id": agentID, "block": true, "timeout_ms": minTaskOutputTimeout,
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute = %+v, %v", result, err)
	}
}

func TestTaskOutputRejectsNegativeOffset(t *testing.T) {
	tool := NewTaskOutputTool()
	if err := tool.Validate(map[string]any{"action": "get", "task_id": "agent-1", "offset": -1}); err == nil {
		t.Fatal("Validate accepted a negative offset")
	}
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "get",
		"task_id": "agent-12345678901234567890",
		"offset":  -1,
	})
	if err != nil || result.Success || !strings.Contains(result.Error, "negative") {
		t.Fatalf("Execute=%#v, %v; want negative-offset error result", result, err)
	}
}

func TestTaskOutputRejectsUnknownActionInValidationAndExecution(t *testing.T) {
	tool := NewTaskOutputTool()
	args := map[string]any{"action": "inspect"}
	if err := tool.Validate(args); err == nil || !strings.Contains(err.Error(), "action") {
		t.Fatalf("Validate error = %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil || result.Success || !strings.Contains(result.Error, "action") {
		t.Fatalf("Execute = %+v, %v", result, err)
	}
}

func TestTaskOutputDistinguishesCancelledWaitFromTimeout(t *testing.T) {
	const agentID = "agent-running"
	path := filepath.Join(t.TempDir(), "agent-running.log")
	if err := os.WriteFile(path, []byte("old:new-output"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &opaqueAgentRunner{results: map[string]AgentResult{
		agentID: {AgentID: agentID, Status: "running", Completed: false, OutputFile: path},
	}}
	tool := NewTaskOutputTool()
	tool.SetRunner(runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := tool.Execute(ctx, map[string]any{
		"action": "get", "task_id": agentID, "block": true, "timeout_ms": 1_000, "offset": 4,
	})
	if err != nil || !result.Success || !strings.Contains(result.Content, "Stopped waiting") {
		t.Fatalf("cancelled wait = %+v, %v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["timeout"] != false || data["wait_cancelled"] != true ||
		data["output"] != "new-output" || data["offset"] != int64(4) {
		t.Fatalf("cancelled wait data = %#v", result.Data)
	}
}

func TestTaskOutputBlockingAgentHonorsOffset(t *testing.T) {
	const agentID = "agent-complete"
	path := filepath.Join(t.TempDir(), "agent-complete.log")
	if err := os.WriteFile(path, []byte("old:new-output"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &opaqueAgentRunner{results: map[string]AgentResult{
		agentID: {
			AgentID: agentID, Status: "completed", Completed: true, OutputFile: path,
		},
	}}
	tool := NewTaskOutputTool()
	tool.SetRunner(runner)

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "get", "task_id": agentID, "block": true, "timeout_ms": 1_000, "offset": 4,
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute = %+v, %v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["output"] != "new-output" || data["offset"] != int64(4) ||
		data["next_offset"] != int64(len("old:new-output")) {
		t.Fatalf("blocking incremental data = %#v", result.Data)
	}
}

func TestTaskOutputRejectsNonIntegerOffset(t *testing.T) {
	tool := NewTaskOutputTool()
	for _, offset := range []any{1.5, "1", math.Inf(1)} {
		args := map[string]any{"action": "get", "task_id": "agent-1", "offset": offset}
		if err := tool.Validate(args); err == nil {
			t.Errorf("Validate accepted offset %#v", offset)
		}
		result, err := tool.Execute(context.Background(), args)
		if err != nil || result.Success || !strings.Contains(result.Error, "integer") {
			t.Errorf("Execute offset %#v = %#v, %v", offset, result, err)
		}
	}
}

func TestTaskOutputIncrementalReadIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	payload := bytes.Repeat([]byte("x"), maxTaskOutputReadBytes+23)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	tool := NewTaskOutputTool()
	result, err := tool.readAgentOutputFromFile(AgentResult{
		AgentID:    "agent-1",
		Status:     "running",
		OutputFile: path,
	}, 0)
	if err != nil || !result.Success {
		t.Fatalf("read=%#v, %v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("data=%#v", result.Data)
	}
	output, _ := data["output"].(string)
	if len(output) != maxTaskOutputReadBytes {
		t.Fatalf("output len=%d, want %d", len(output), maxTaskOutputReadBytes)
	}
	if got := data["next_offset"]; got != int64(maxTaskOutputReadBytes) {
		t.Fatalf("next_offset=%v", got)
	}
	if got := data["total_bytes"]; got != int64(len(payload)) {
		t.Fatalf("total_bytes=%v", got)
	}
}

func TestTaskOutputDoesNotFollowReplacedSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.log")
	link := filepath.Join(dir, "agent.log")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	tool := NewTaskOutputTool()
	result, err := tool.readAgentOutputFromFile(AgentResult{
		AgentID:    "agent-1",
		Status:     "completed",
		Completed:  true,
		Output:     "safe fallback",
		OutputFile: link,
	}, 0)
	if err != nil || !result.Success || !strings.Contains(result.Content, "safe fallback") {
		t.Fatalf("read=%#v, %v", result, err)
	}
	if strings.Contains(result.Content, "secret") {
		t.Fatal("incremental output followed a symlink")
	}
}

func TestTaskOutputExplicitZeroOffsetReadsShellStream(t *testing.T) {
	manager := tasks.NewManager(t.TempDir())
	manager.SetWorkspaceSandboxEnabled(false)
	id, err := manager.StartWithArgs(context.Background(), "sh", []string{"-c", "printf shell-stream"})
	if err != nil {
		t.Fatal(err)
	}
	task, ok := manager.Get(id)
	if !ok {
		t.Fatal("started task missing from manager")
	}
	select {
	case <-task.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shell task timed out")
	}

	tool := NewTaskOutputTool()
	tool.SetManager(manager)
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "get",
		"task_id": id,
		"offset":  0,
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute=%#v, %v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["output"] != "shell-stream" || data["next_offset"] != int64(len("shell-stream")) {
		t.Fatalf("incremental data=%#v", result.Data)
	}
}

func TestTaskOutputBlockingShellHonorsOffset(t *testing.T) {
	manager := tasks.NewManager(t.TempDir())
	manager.SetWorkspaceSandboxEnabled(false)
	id, err := manager.StartWithArgs(context.Background(), "sh", []string{"-c", "printf old:new-output"})
	if err != nil {
		t.Fatal(err)
	}

	tool := NewTaskOutputTool()
	tool.SetManager(manager)
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "get", "task_id": id, "block": true, "timeout_ms": 5_000, "offset": 4,
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute = %+v, %v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["output"] != "new-output" || data["offset"] != int64(4) ||
		data["next_offset"] != int64(len("old:new-output")) {
		t.Fatalf("blocking incremental data = %#v", result.Data)
	}
}

func TestTaskOutputShellIncrementalReadIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shell.log")
	payload := bytes.Repeat([]byte("s"), maxTaskOutputReadBytes+17)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewTaskOutputTool()
	result, err := tool.readShellOutputFromFile(tasks.Info{
		ID:         "task-1",
		Status:     "running",
		OutputFile: path,
	}, 0)
	if err != nil || !result.Success {
		t.Fatalf("read=%#v, %v", result, err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || len(data["output"].(string)) != maxTaskOutputReadBytes ||
		data["next_offset"] != int64(maxTaskOutputReadBytes) || data["total_bytes"] != int64(len(payload)) {
		t.Fatalf("bounded data=%#v", result.Data)
	}
}
