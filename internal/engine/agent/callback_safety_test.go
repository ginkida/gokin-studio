package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

type panicPlannedTool struct{}

func (*panicPlannedTool) Name() string        { return "panic_planned" }
func (*panicPlannedTool) Description() string { return "panics during a planned action" }
func (*panicPlannedTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "panic_planned", Description: "panic test"}
}
func (*panicPlannedTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	panic("planned tool exploded")
}
func (*panicPlannedTool) Validate(map[string]any) error { return nil }

func TestAgentObserverPanicsAreIsolated(t *testing.T) {
	agent := &Agent{ID: "agent-1", Type: AgentTypeGeneral, status: AgentStatusRunning}
	panicking := func() { panic("observer exploded") }

	agent.SetOnText(func(string) { panicking() })
	agent.safeOnText("text")
	agent.SetOnThinking(func(string) { panicking() })
	agent.safeOnThinking("thought")
	agent.SetOnOutputFile(func(string) { panicking() })
	agent.publishOutputFile("output.log")
	agent.SetProgressCallback(func(*AgentProgress) { panicking() })
	agent.SetProgress(1, 2, "working")
	callAgentObserver(agent.ID, "direct", panicking)

	if agent.Thought != "thought" {
		t.Fatalf("thinking state was not retained after observer panic: %q", agent.Thought)
	}
}

func TestRequiredCallbackPanicsBecomeErrors(t *testing.T) {
	_, err := callAgentInputCallback("agent-1", func(string) (string, error) {
		panic("input exploded")
	}, "prompt")
	if err == nil || !strings.Contains(err.Error(), "input exploded") {
		t.Fatalf("input panic error = %v", err)
	}

	approved, err := callWorkspaceReviewCallback(
		"agent-1",
		func(context.Context, []WorkspaceChangePreview) (bool, error) {
			panic("review exploded")
		},
		context.Background(),
		nil,
	)
	if approved || err == nil || !strings.Contains(err.Error(), "review exploded") {
		t.Fatalf("review panic = approved %v, error %v", approved, err)
	}
}

func TestPlannedActionPanicBecomesFailedResult(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&panicPlannedTool{})
	agent := &Agent{
		ID: "agent-1", Type: AgentTypeGeneral, status: AgentStatusRunning, registry: registry,
	}

	result := agent.executePlannedActionSafely(context.Background(), &PlannedAction{
		Type: ActionToolCall, ToolName: "panic_planned",
	})
	if result == nil || result.Status != AgentStatusFailed || !result.Completed ||
		!strings.Contains(result.Error, "planned tool exploded") {
		t.Fatalf("planned panic result = %+v", result)
	}
}
