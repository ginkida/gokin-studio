package tools

import (
	"context"
	"strings"
	"testing"
)

type fakePluginAgentRunner struct {
	agent string
	task  string
	err   error
}

func (r *fakePluginAgentRunner) RunPluginAgent(_ context.Context, agentID, task string) (PluginAgentRunResult, error) {
	r.agent, r.task = agentID, task
	if r.err != nil {
		return PluginAgentRunResult{}, r.err
	}
	return PluginAgentRunResult{Agent: agentID, SessionID: "child-1", Response: "review complete"}, nil
}

func TestPluginAgentToolCatalogValidationAndExecution(t *testing.T) {
	runner := &fakePluginAgentRunner{}
	tool := NewPluginAgentTool([]PluginAgentSpec{
		{ID: "review:security", Description: "Review security-sensitive changes"},
		{ID: "docs:writer", Description: "Draft documentation"},
	}, runner)
	declaration := tool.Declaration()
	if declaration.Name != "plugin_agent" ||
		!strings.Contains(declaration.Description, "review:security") ||
		len(declaration.Parameters.Properties["agent"].Enum) != 2 {
		t.Fatalf("declaration = %#v", declaration)
	}
	if err := tool.Validate(map[string]any{"agent": "missing:agent", "task": "work"}); err == nil {
		t.Fatal("unknown plugin agent was accepted")
	}
	if err := tool.Validate(map[string]any{"agent": "review:security", "task": ""}); err == nil {
		t.Fatal("empty plugin agent task was accepted")
	}
	result, err := tool.Execute(context.Background(), map[string]any{
		"agent": "review:security", "task": "Inspect the change",
	})
	if err != nil || !result.Success || runner.agent != "review:security" ||
		runner.task != "Inspect the change" ||
		!strings.Contains(result.Content, "child-1") {
		t.Fatalf("execute = %#v, %v; runner=%#v", result, err, runner)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["session_id"] != "child-1" {
		t.Fatalf("result data = %#v", result.Data)
	}
	if _, leaked := data["response"]; leaked {
		t.Fatal("full child response was duplicated into unbounded structured tool data")
	}
	if !IsWriteTool("plugin_agent") || !RequiresUserApproval("plugin_agent", map[string]any{}) {
		t.Fatal("plugin agent must serialize with writes and pass through the approval gate")
	}
}
