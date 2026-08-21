package tools

import (
	"context"
	"math"
	"strings"
	"testing"
)

type validatingTaskRunner struct {
	calls          int
	agentType      string
	prompt         string
	maxTurns       int
	model          string
	thoroughness   Thoroughness
	outputStyle    OutputStyle
	asyncID        string
	resumeID       string
	resumeAsyncID  string
	foregroundID   string
	foregroundData AgentResult
}

func (r *validatingTaskRunner) capture(ctx context.Context, agentType, prompt string, maxTurns int, model string) {
	r.calls++
	r.agentType = agentType
	r.prompt = prompt
	r.maxTurns = maxTurns
	r.model = model
	r.thoroughness = ThoroughnessFromContext(ctx)
	r.outputStyle = OutputStyleFromContext(ctx)
}

func (r *validatingTaskRunner) Spawn(ctx context.Context, agentType, prompt string, maxTurns int, model string) (string, error) {
	r.capture(ctx, agentType, prompt, maxTurns, model)
	return r.foregroundID, nil
}

func (r *validatingTaskRunner) SpawnAsync(ctx context.Context, agentType, prompt string, maxTurns int, model string) string {
	r.capture(ctx, agentType, prompt, maxTurns, model)
	return r.asyncID
}

func (r *validatingTaskRunner) SpawnAsyncWithStreaming(ctx context.Context, agentType, prompt string, maxTurns int, model string, _ func(string), _ func(string, *AgentProgress)) string {
	r.capture(ctx, agentType, prompt, maxTurns, model)
	return r.asyncID
}

func (r *validatingTaskRunner) Resume(context.Context, string, string) (string, error) {
	r.calls++
	return r.resumeID, nil
}

func (r *validatingTaskRunner) ResumeAsync(context.Context, string, string) (string, error) {
	r.calls++
	return r.resumeAsyncID, nil
}

func (r *validatingTaskRunner) GetResult(agentID string) (AgentResult, bool) {
	if agentID != r.foregroundID && agentID != r.resumeID {
		return AgentResult{}, false
	}
	return r.foregroundData, true
}

func TestTaskToolExecuteEnforcesValidation(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "missing prompt", args: map[string]any{"subagent_type": "explore"}},
		{name: "blank prompt", args: map[string]any{"prompt": " \t", "subagent_type": "explore"}},
		{name: "missing type", args: map[string]any{"prompt": "inspect"}},
		{name: "unknown type", args: map[string]any{"prompt": "inspect", "subagent_type": "typo"}},
		{name: "zero turns", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "max_turns": 0}},
		{name: "negative turns", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "max_turns": -1}},
		{name: "excess turns", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "max_turns": MaxTaskTurns + 1}},
		{name: "fractional turns", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "max_turns": 1.5}},
		{name: "infinite turns", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "max_turns": math.Inf(1)}},
		{name: "string turns", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "max_turns": "5"}},
		{name: "unknown model", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "model": "ultra"}},
		{name: "blank model", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "model": " "}},
		{name: "unknown thoroughness", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "thoroughness": "extreme"}},
		{name: "unknown output style", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "output_style": "novel"}},
		{name: "non-boolean background", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "run_in_background": "yes"}},
		{name: "non-string description", args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "description": 42}},
		{name: "non-string resume", args: map[string]any{"prompt": "continue", "resume": 42}},
		{name: "unknown ignored type", args: map[string]any{"prompt": "continue", "resume": "agent-old", "subagent_type": "typo"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &validatingTaskRunner{}
			tool := NewTaskTool()
			tool.SetRunner(runner)

			result, err := tool.Execute(context.Background(), test.args)
			if err != nil || result.Success || !strings.Contains(result.Error, "validation error") {
				t.Fatalf("Execute = %+v, %v; want validation failure", result, err)
			}
			if runner.calls != 0 {
				t.Fatalf("runner called %d times for invalid arguments", runner.calls)
			}
		})
	}
}

func TestTaskToolExecutePropagatesValidatedOptions(t *testing.T) {
	runner := &validatingTaskRunner{
		foregroundID: "agent-1",
		foregroundData: AgentResult{
			AgentID: "agent-1", Type: "explore", Status: "completed", Completed: true,
		},
	}
	tool := NewTaskTool()
	tool.SetRunner(runner)

	result, err := tool.Execute(nil, map[string]any{
		"prompt":        "inspect repository",
		"subagent_type": " Explore ",
		"max_turns":     float64(MaxTaskTurns),
		"model":         " FLASH ",
		"thoroughness":  " QUICK ",
		"output_style":  " CONCISE ",
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute = %+v, %v", result, err)
	}
	if runner.calls != 1 || runner.agentType != "explore" || runner.prompt != "inspect repository" ||
		runner.maxTurns != MaxTaskTurns || runner.model != "flash" ||
		runner.thoroughness != ThoroughnessQuick || runner.outputStyle != OutputStyleConcise {
		t.Fatalf("runner capture = %+v", runner)
	}
}

func TestTaskToolRejectsEmptyBackgroundAgentIDs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: "spawn",
			args: map[string]any{"prompt": "inspect", "subagent_type": "explore", "run_in_background": true},
		},
		{
			name: "resume",
			args: map[string]any{"prompt": "continue", "resume": "agent-old", "run_in_background": true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &validatingTaskRunner{}
			tool := NewTaskTool()
			tool.SetRunner(runner)

			result, err := tool.Execute(context.Background(), test.args)
			if err != nil || result.Success || !strings.Contains(result.Error, "empty agent ID") {
				t.Fatalf("Execute = %+v, %v; want empty-ID failure", result, err)
			}
			if runner.calls != 1 {
				t.Fatalf("runner calls = %d, want 1", runner.calls)
			}
		})
	}
}

func TestTaskToolRejectsEmptyForegroundAgentIDs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: "spawn",
			args: map[string]any{"prompt": "inspect", "subagent_type": "explore"},
		},
		{
			name: "resume",
			args: map[string]any{"prompt": "continue", "resume": "agent-old"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &validatingTaskRunner{}
			tool := NewTaskTool()
			tool.SetRunner(runner)

			result, err := tool.Execute(context.Background(), test.args)
			if err != nil || result.Success || !strings.Contains(result.Error, "empty agent ID") {
				t.Fatalf("Execute = %+v, %v; want empty-ID failure", result, err)
			}
			if runner.calls != 1 {
				t.Fatalf("runner calls = %d, want 1", runner.calls)
			}
		})
	}
}
