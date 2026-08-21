package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func TestParseAgentTypeFailsClosed(t *testing.T) {
	if got := ParseAgentType(" Explore "); got != AgentTypeExplore {
		t.Fatalf("normalized built-in type = %q", got)
	}
	if got := ParseAgentType("typo"); got != "" {
		t.Fatalf("unknown type = %q, want empty", got)
	}
	if tools := ParseAgentType("typo").AllowedTools(); len(tools) != 0 {
		t.Fatalf("unknown type received tools: %v", tools)
	}
}

func TestNormalizeAgentSpawnRequest(t *testing.T) {
	registry := NewAgentTypeRegistry()
	if err := registry.RegisterDynamic("reviewer", "Reviews code", []string{"read"}, "Review carefully"); err != nil {
		t.Fatal(err)
	}
	deps := runnerAgentDeps{typeRegistry: registry, baseRegistry: tools.DefaultRegistry(t.TempDir())}

	agentType, model, err := normalizeAgentSpawnRequest(deps, " General ", "work", 0, " flash ")
	if err != nil || agentType != "general" || model != "flash" {
		t.Fatalf("normalized request = (%q, %q, %v)", agentType, model, err)
	}
	agentType, _, err = normalizeAgentSpawnRequest(deps, "reviewer", "work", tools.MaxTaskTurns, "")
	if err != nil || agentType != "reviewer" {
		t.Fatalf("dynamic request = (%q, %v)", agentType, err)
	}

	tests := []struct {
		name     string
		agent    string
		prompt   string
		maxTurns int
	}{
		{name: "unknown type", agent: "typo", prompt: "work", maxTurns: 1},
		{name: "blank type", agent: " ", prompt: "work", maxTurns: 1},
		{name: "blank prompt", agent: "explore", prompt: " \t", maxTurns: 1},
		{name: "negative turns", agent: "explore", prompt: "work", maxTurns: -1},
		{name: "excess turns", agent: "explore", prompt: "work", maxTurns: tools.MaxTaskTurns + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := normalizeAgentSpawnRequest(deps, test.agent, test.prompt, test.maxTurns, ""); err == nil {
				t.Fatal("malformed request was accepted")
			}
		})
	}
}

func TestValidateRestoredAgentState(t *testing.T) {
	deps := runnerAgentDeps{}
	valid := &AgentState{ID: "agent-1", Type: AgentTypeExplore, MaxTurns: 0}
	if err := validateRestoredAgentState(deps, valid); err != nil {
		t.Fatalf("legacy default state rejected: %v", err)
	}
	for _, state := range []*AgentState{
		nil,
		{ID: " ", Type: AgentTypeExplore, MaxTurns: 1},
		{ID: "agent-1", Type: AgentType("typo"), MaxTurns: 1},
		{ID: "agent-1", Type: AgentTypeExplore, MaxTurns: -1},
		{ID: "agent-1", Type: AgentTypeExplore, MaxTurns: tools.MaxTaskTurns + 1},
	} {
		if err := validateRestoredAgentState(deps, state); err == nil {
			t.Errorf("invalid state accepted: %+v", state)
		}
	}
}

func TestRunnerRejectsInvalidPersistedStateWithoutConsumingCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store, err := NewAgentStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := &AgentState{ID: "agent-invalid", Type: AgentType("typo"), MaxTurns: 1}
	if err := store.SaveState(state); err != nil {
		t.Fatal(err)
	}
	checkpoint := &AgentCheckpoint{
		CheckpointID: "agent-invalid-999",
		AgentState:   state,
		Timestamp:    time.Now(),
	}
	if err := store.SaveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(nil, nil, tools.NewRegistry(), dir)
	runner.SetStore(store)
	if id, err := runner.Resume(nil, state.ID, "continue"); err == nil || id != "" || !strings.Contains(err.Error(), "unknown agent type") {
		t.Fatalf("Resume = (%q, %v), want invalid-state error", id, err)
	}
	if id, err := runner.ResumeLastCheckpoint(nil); err == nil || id != "" || !strings.Contains(err.Error(), "unknown agent type") {
		t.Fatalf("ResumeLastCheckpoint = (%q, %v), want invalid-state error", id, err)
	}
	if _, err := store.LoadCheckpoint(checkpoint.CheckpointID); err != nil {
		t.Fatalf("validation failure consumed checkpoint: %v", err)
	}
}

func TestRunnerRejectsInvalidSpawnBeforePublishingState(t *testing.T) {
	runner := NewRunner(nil, nil, nil, t.TempDir())

	if id, err := runner.Spawn(nil, "typo", "work", 1, ""); err == nil || id != "" {
		t.Fatalf("Spawn = (%q, %v), want rejection", id, err)
	}
	if id, result, err := runner.SpawnWithContext(nil, "explore", " ", 1, "", "", nil, false, nil); err == nil || id != "" || result != nil {
		t.Fatalf("SpawnWithContext = (%q, %+v, %v), want rejection", id, result, err)
	}
	if id := runner.SpawnAsync(nil, "explore", "work", tools.MaxTaskTurns+1, ""); id != "" {
		t.Fatalf("SpawnAsync invalid ID = %q", id)
	}
	if id := runner.SpawnAsyncWithStreaming(nil, "typo", "work", 1, "", nil, nil); id != "" {
		t.Fatalf("SpawnAsyncWithStreaming invalid ID = %q", id)
	}
	ids, err := runner.SpawnMultiple(nil, []AgentTask{
		{Type: AgentTypeExplore, Prompt: "valid", MaxTurns: 1},
		{Type: AgentType("typo"), Prompt: "invalid", MaxTurns: 1},
	})
	if err == nil || ids != nil {
		t.Fatalf("SpawnMultiple = (%v, %v), want atomic rejection", ids, err)
	}
	ids, err = runner.SpawnMultiple(nil, []AgentTask{{
		Type: AgentTypeExplore, Prompt: "valid", MaxTurns: 1, Thoroughness: "extreme",
	}})
	if err == nil || ids != nil {
		t.Fatalf("SpawnMultiple invalid thoroughness = (%v, %v)", ids, err)
	}

	runner.mu.RLock()
	agents, results, active := len(runner.agents), len(runner.results), len(runner.activeExecutions)
	runner.mu.RUnlock()
	if agents != 0 || results != 0 || active != 0 {
		t.Fatalf("rejected spawns published state: agents=%d results=%d active=%d", agents, results, active)
	}
}

func TestRunnerAsyncSpawnAcceptsNilContext(t *testing.T) {
	dir := t.TempDir()
	provider := &blockingAgentClient{entered: make(chan struct{})}
	runner := NewRunner(nil, provider, tools.DefaultRegistry(dir), dir)

	agentID := runner.SpawnAsync(nil, "explore", "wait", 1, "")
	if agentID == "" {
		t.Fatal("valid nil-context spawn was rejected")
	}
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("agent did not reach provider")
	}
	if err := runner.Cancel(agentID); err != nil {
		t.Fatal(err)
	}
	result, err := runner.WaitWithTimeout(agentID, 2*time.Second)
	if err != nil || result == nil || result.Status != AgentStatusCancelled {
		t.Fatalf("cancelled result = %+v, %v", result, err)
	}
}

func TestApplyThoroughnessKeepsConfiguredTurnCeiling(t *testing.T) {
	tests := []struct {
		name         string
		agentType    AgentType
		thoroughness tools.Thoroughness
		configured   int
		want         int
	}{
		{name: "explore quick profile", agentType: AgentTypeExplore, thoroughness: tools.ThoroughnessQuick, configured: 30, want: 8},
		{name: "explore quick hard ceiling", agentType: AgentTypeExplore, thoroughness: tools.ThoroughnessQuick, configured: 1, want: 1},
		{name: "explore thorough profile", agentType: AgentTypeExplore, thoroughness: tools.ThoroughnessThorough, configured: 100, want: 50},
		{name: "explore thorough hard ceiling", agentType: AgentTypeExplore, thoroughness: tools.ThoroughnessThorough, configured: 1, want: 1},
		{name: "bash thorough profile", agentType: AgentTypeBash, thoroughness: tools.ThoroughnessThorough, configured: 100, want: 20},
		{name: "general unchanged", agentType: AgentTypeGeneral, thoroughness: tools.ThoroughnessThorough, configured: 100, want: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := &Agent{Type: test.agentType, maxTurns: test.configured}
			agent.ApplyThoroughness(test.thoroughness)
			if agent.maxTurns != test.want {
				t.Fatalf("max turns = %d, want %d", agent.maxTurns, test.want)
			}
		})
	}
}

func TestAgentConstructorsCapTurnBudget(t *testing.T) {
	static := NewAgent(AgentTypeExplore, nil, tools.NewRegistry(), "", tools.MaxTaskTurns+50, "", nil, nil)
	if static.maxTurns != tools.MaxTaskTurns {
		t.Fatalf("static max turns = %d", static.maxTurns)
	}
	dynamic := NewAgentWithDynamicType(
		&DynamicAgentType{Name: "reviewer"}, nil, tools.NewRegistry(), "", tools.MaxTaskTurns+50, "", nil, nil,
	)
	if dynamic.maxTurns != tools.MaxTaskTurns {
		t.Fatalf("dynamic max turns = %d", dynamic.maxTurns)
	}
}

func TestPlanParserDefaultsUnknownTypeExplicitly(t *testing.T) {
	actions := (&TreePlanner{}).parsePlanResponse("STEP: typo | inspect the repository", "")
	if len(actions) == 0 || actions[0].AgentType != AgentTypeGeneral {
		t.Fatalf("parsed actions = %+v", actions)
	}
}

func TestCoordinatorRejectsMalformedTasksAndFailsWithoutRunner(t *testing.T) {
	coordinator := NewCoordinator(nil, nil, &CoordinatorConfig{MaxParallel: 1})
	if id := coordinator.AddTask(" ", AgentTypeExplore, PriorityNormal, nil); id != "" {
		t.Fatalf("blank task ID = %q", id)
	}
	if id := coordinator.AddTask("work", AgentType("typo"), PriorityNormal, nil); id != "" {
		t.Fatalf("unknown-type task ID = %q", id)
	}
	if id := coordinator.AddTask("work", AgentTypeExplore, 0, nil); id != "" {
		t.Fatalf("invalid-priority task ID = %q", id)
	}

	taskID := coordinator.AddTask("work", AgentTypeExplore, PriorityNormal, nil)
	if taskID == "" {
		t.Fatal("valid task was rejected")
	}
	coordinator.Start()
	results, err := coordinator.WaitWithTimeout(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result := results[taskID]
	if result == nil || result.Status != AgentStatusFailed || !result.Completed ||
		!strings.Contains(result.Error, "runner") {
		t.Fatalf("start failure result = %+v", result)
	}
}
