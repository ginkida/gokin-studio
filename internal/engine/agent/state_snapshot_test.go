package agent

import (
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/genai"
)

func agentWithActivePlanSnapshotFixture(t *testing.T) (*Agent, string) {
	t.Helper()
	goal := &PlanGoal{
		Description: "finish the project", SuccessCriteria: []string{"tests pass"}, MaxDepth: 4, MaxNodes: 20,
	}
	tree := NewPlanTree(goal)
	tree.mu.Lock()
	child := tree.AddNode(tree.Root.ID, &PlannedAction{
		Type: ActionToolCall, ToolName: "read",
		ToolArgs: map[string]any{"nested": map[string]any{"path": "original.go"}},
	})
	if child == nil {
		t.Fatal("failed to add plan child")
	}
	child.Status = PlanNodeExecuting
	tree.CurrentNode = child
	tree.BestPath = []*PlanNode{tree.Root, child}
	tree.CurrentDepth = child.Depth
	tree.mu.Unlock()

	functionArgs := map[string]any{
		"nested": map[string]any{"value": "original"},
		"list":   []any{map[string]any{"item": "original"}},
	}
	response := map[string]any{"details": map[string]any{"status": "original"}}
	agent := &Agent{
		ID: "snapshot-agent", Type: AgentTypeGeneral, Model: "test-model",
		status: AgentStatusRunning, startTime: time.Now(), maxTurns: 7,
		originalPrompt: "original task",
		activePlan:     tree,
		history: []*genai.Content{{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "read", Args: functionArgs}},
				{FunctionResponse: &genai.FunctionResponse{ID: "call-1", Name: "read", Response: response}},
			},
		}},
	}
	return agent, child.ID
}

func TestAgentStateIsDeepSnapshot(t *testing.T) {
	agent, childID := agentWithActivePlanSnapshotFixture(t)
	state := agent.GetState()

	// Mutate every live source after taking the snapshot.
	agent.stateMu.Lock()
	agent.history[0].Parts[0].FunctionCall.Args["nested"].(map[string]any)["value"] = "live-mutated"
	agent.history[0].Parts[0].FunctionCall.Args["list"].([]any)[0].(map[string]any)["item"] = "live-mutated"
	agent.history[0].Parts[1].FunctionResponse.Response["details"].(map[string]any)["status"] = "live-mutated"
	agent.originalPrompt = "live-mutated"
	agent.stateMu.Unlock()
	agent.activePlan.mu.Lock()
	agent.activePlan.Goal.SuccessCriteria[0] = "live-mutated"
	agent.activePlan.nodeIndex[childID].Action.ToolArgs["nested"].(map[string]any)["path"] = "live-mutated.go"
	agent.activePlan.mu.Unlock()

	call := state.History[0].Parts[0].FunctionCall
	if call.Args["nested"].(map[string]any)["value"] != "original" ||
		call.Args["list"].([]any)[0].(map[string]any)["item"] != "original" {
		t.Fatalf("function-call snapshot was aliased: %#v", call.Args)
	}
	response := state.History[0].Parts[1].FunctionResp.Response
	if response["details"].(map[string]any)["status"] != "original" {
		t.Fatalf("function-response snapshot was aliased: %#v", response)
	}
	if state.LastPrompt != "original task" || state.ActivePlan.Goal.SuccessCriteria[0] != "tests pass" {
		t.Fatalf("state scalars/goal were aliased: prompt=%q goal=%v", state.LastPrompt, state.ActivePlan.Goal)
	}
	if got := state.ActivePlan.nodeIndex[childID].Action.ToolArgs["nested"].(map[string]any)["path"]; got != "original.go" {
		t.Fatalf("plan action snapshot was aliased: %v", got)
	}

	// Mutating the returned state must not modify the live agent either.
	state.ActivePlan.Goal.SuccessCriteria[0] = "snapshot-mutated"
	state.History[0].Parts[0].FunctionCall.Args["nested"].(map[string]any)["value"] = "snapshot-mutated"
	agent.stateMu.RLock()
	liveValue := agent.history[0].Parts[0].FunctionCall.Args["nested"].(map[string]any)["value"]
	agent.stateMu.RUnlock()
	agent.activePlan.mu.RLock()
	liveCriterion := agent.activePlan.Goal.SuccessCriteria[0]
	agent.activePlan.mu.RUnlock()
	if liveValue != "live-mutated" || liveCriterion != "live-mutated" {
		t.Fatalf("snapshot mutation leaked into agent: value=%v criterion=%v", liveValue, liveCriterion)
	}
}

func TestAgentStateJSONRoundTripPreservesPlanNavigation(t *testing.T) {
	agent, childID := agentWithActivePlanSnapshotFixture(t)
	state := agent.GetState()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AgentState
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.LastPrompt != "original task" || decoded.ActivePlan == nil {
		t.Fatalf("decoded state = %+v", decoded)
	}
	if decoded.ActivePlan.CurrentNode == nil || decoded.ActivePlan.CurrentNode.ID != childID {
		t.Fatalf("decoded current node = %+v", decoded.ActivePlan.CurrentNode)
	}
	if len(decoded.ActivePlan.BestPath) != 2 || decoded.ActivePlan.BestPath[1].ID != childID {
		t.Fatalf("decoded best path = %+v", decoded.ActivePlan.BestPath)
	}
	if node, ok := decoded.ActivePlan.nodeIndex[childID]; !ok || node.Action.ToolName != "read" {
		t.Fatalf("decoded node index = %+v, ok=%v", node, ok)
	}

	restored := &Agent{}
	if err := restored.RestoreHistory(&decoded); err != nil {
		t.Fatal(err)
	}
	decoded.ActivePlan.Goal.Description = "decoded-mutated"
	first := restored.GetActivePlan()
	if first == nil || first.Goal.Description != "finish the project" || first.CurrentNode.ID != childID {
		t.Fatalf("restored plan = %+v", first)
	}
	first.Goal.Description = "returned-snapshot-mutated"
	if second := restored.GetActivePlan(); second.Goal.Description != "finish the project" {
		t.Fatalf("GetActivePlan exposed mutable state: %+v", second.Goal)
	}
}

func TestRestoreCheckpointPrefersLosslessAgentStatePlan(t *testing.T) {
	source, childID := agentWithActivePlanSnapshotFixture(t)
	checkpoint, err := source.SaveCheckpoint("manual")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.PlanTreeSnapshot = &SerializedPlanTree{
		RootID: "legacy-root",
		Nodes: map[string]*SerializedPNode{
			"legacy-root": {ID: "legacy-root", Status: string(PlanNodePending)},
		},
		TotalNodes: 1,
	}

	restored := &Agent{}
	if err := restored.RestoreFromCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	plan := restored.GetActivePlan()
	if plan == nil || plan.CurrentNode == nil || plan.CurrentNode.ID != childID {
		t.Fatalf("lossless checkpoint plan was replaced by legacy snapshot: %+v", plan)
	}
	if err := restored.RestoreHistory(nil); err == nil {
		t.Fatal("RestoreHistory(nil) succeeded")
	}
	if err := restored.RestoreFromCheckpoint(nil); err == nil {
		t.Fatal("RestoreFromCheckpoint(nil) succeeded")
	}
}
