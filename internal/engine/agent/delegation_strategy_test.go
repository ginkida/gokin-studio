package agent

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func TestDelegationDepthHelpersNormalizeUnsafeInput(t *testing.T) {
	if depth := DelegationDepthFromContext(nil); depth != 0 {
		t.Fatalf("nil context depth = %d, want 0", depth)
	}
	tests := []struct {
		name  string
		depth int
		want  int
	}{
		{name: "negative", depth: -1, want: 0},
		{name: "valid", depth: 3, want: 3},
		{name: "excess", depth: MaxDelegationDepth + 100, want: MaxDelegationDepth},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := WithDelegationDepth(nil, test.depth)
			if got := DelegationDepthFromContext(ctx); got != test.want {
				t.Fatalf("normalized depth = %d, want %d", got, test.want)
			}
		})
	}

	for _, test := range tests {
		ctx := context.WithValue(context.Background(), delegationDepthKey{}, test.depth)
		if got := DelegationDepthFromContext(ctx); got != test.want {
			t.Fatalf("raw context depth %d normalized to %d, want %d", test.depth, got, test.want)
		}
	}
}

func TestDelegationStrategyDepthPolicyRespectsAbsoluteLimit(t *testing.T) {
	d := NewDelegationStrategy(AgentTypeExplore, nil)
	d.ApplyThoroughness(tools.ThoroughnessThorough)
	d.mu.RLock()
	maxDepth := d.maxDepth
	d.mu.RUnlock()
	if maxDepth != MaxDelegationDepth {
		t.Fatalf("thorough max depth = %d, want absolute limit %d", maxDepth, MaxDelegationDepth)
	}

	decision := d.Evaluate(&DelegationContext{
		AgentType:       AgentTypeExplore,
		LastToolName:    "bash",
		DelegationDepth: MaxDelegationDepth - 1,
	})
	if !decision.ShouldDelegate {
		t.Fatal("delegation was blocked before the absolute depth limit")
	}
	decision = d.Evaluate(&DelegationContext{
		AgentType:       AgentTypeExplore,
		LastToolName:    "bash",
		DelegationDepth: MaxDelegationDepth,
	})
	if decision.ShouldDelegate {
		t.Fatal("delegation was allowed at the absolute depth limit")
	}

	d.SetDepth(-10)
	if depth := d.GetDepth(); depth != 0 {
		t.Fatalf("negative strategy depth = %d, want 0", depth)
	}
	d.SetDepth(MaxDelegationDepth + 10)
	if depth := d.GetDepth(); depth != MaxDelegationDepth {
		t.Fatalf("excess strategy depth = %d, want %d", depth, MaxDelegationDepth)
	}
}

func TestDelegationStrategyEvaluateNilAndLoopAccounting(t *testing.T) {
	d := NewDelegationStrategy(AgentTypeExplore, nil)
	if decision := d.Evaluate(nil); decision == nil || decision.ShouldDelegate {
		t.Fatalf("Evaluate(nil) = %+v, want no delegation", decision)
	}
	ctx := &DelegationContext{AgentType: AgentTypeExplore, LastToolName: "bash"}
	for i := 0; i < 10; i++ {
		if decision := d.Evaluate(ctx); !decision.ShouldDelegate {
			t.Fatalf("evaluation %d consumed execution loop budget", i+1)
		}
	}
	if count := d.delegationHistory["explore:bash"]; count != 0 {
		t.Fatalf("evaluation-only history count = %d, want 0", count)
	}

	d.delegationHistory["explore:bash"] = 3
	if decision := d.Evaluate(ctx); decision.ShouldDelegate {
		t.Fatal("delegation loop was not blocked after three actual attempts")
	}
}

func TestDelegationStrategyAdaptiveTurnsNeverExpandsBudget(t *testing.T) {
	d := NewDelegationStrategy(AgentTypeGeneral, nil)
	d.SetDepth(2)
	tests := []struct {
		base int
		want int
	}{
		{base: 3, want: 1},
		{base: 10, want: 4},
		{base: 0, want: 9},
		{base: MaxTurnLimit + 50, want: MaxTurnLimit - 6},
	}
	for _, test := range tests {
		if got := d.AdaptiveMaxTurns(test.base); got != test.want {
			t.Fatalf("AdaptiveMaxTurns(%d) = %d, want %d", test.base, got, test.want)
		}
	}

	d.mu.Lock()
	d.depthPenalty = -100
	d.mu.Unlock()
	if got := d.AdaptiveMaxTurns(10); got != 10 {
		t.Fatalf("negative penalty expanded budget to %d, want 10", got)
	}
}

func TestDelegationStrategyProgressAndLoadInputsAreNormalized(t *testing.T) {
	d := NewDelegationStrategy(AgentTypeGeneral, nil)
	d.TrackProgress("same")
	if got := d.GetStuckCount(); got != 0 {
		t.Fatalf("first progress sample stuck count = %d, want 0", got)
	}
	for i := 0; i < 5; i++ {
		d.TrackProgress("same")
	}
	if got := d.GetStuckCount(); got != 5 || !d.IsStuck() {
		t.Fatalf("repeated progress = count %d, stuck %v; want 5, true", got, d.IsStuck())
	}
	d.TrackProgress("changed")
	if got := d.GetStuckCount(); got != 0 || d.IsStuck() {
		t.Fatalf("changed progress = count %d, stuck %v; want 0, false", got, d.IsStuck())
	}

	d.SetActiveAgents(-5)
	d.mu.RLock()
	active := d.activeAgents
	d.mu.RUnlock()
	if active != 0 {
		t.Fatalf("negative active-agent count = %d, want 0", active)
	}
}

func TestDelegationStrategySuppressionNormalizesAndReclaimsEntries(t *testing.T) {
	d := NewDelegationStrategy(AgentTypeExplore, nil)
	d.SuppressRule(" BASH ", time.Hour)
	d.mu.Lock()
	if !d.isRuleSuppressed("bash") {
		d.mu.Unlock()
		t.Fatal("normalized rule was not suppressed")
	}
	d.failedRules["explore:bash"] = time.Now().Add(-time.Second)
	if d.isRuleSuppressed("bash") {
		d.mu.Unlock()
		t.Fatal("expired rule remains suppressed")
	}
	_, exists := d.failedRules["explore:bash"]
	d.mu.Unlock()
	if exists {
		t.Fatal("expired suppression entry was not reclaimed")
	}

	d.SuppressRule("bash", time.Hour)
	d.SuppressRule("bash", 0)
	d.mu.RLock()
	_, exists = d.failedRules["explore:bash"]
	d.mu.RUnlock()
	if exists {
		t.Fatal("non-positive suppression duration did not clear the entry")
	}
}

func TestDelegationStrategyExecuteValidatesBeforeRegistration(t *testing.T) {
	d := NewDelegationStrategy(AgentTypeGeneral, nil)
	if _, err := d.ExecuteDelegation(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("nil decision error = %v", err)
	}
	if _, err := d.ExecuteDelegation(context.Background(), &DelegationDecision{}); err == nil || !strings.Contains(err.Error(), "does not authorize") {
		t.Fatalf("unauthorized decision error = %v", err)
	}
	valid := &DelegationDecision{ShouldDelegate: true, TargetType: "general", Query: "work"}
	if _, err := d.ExecuteDelegation(context.Background(), valid); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing messenger error = %v", err)
	}

	runner := NewRunner(context.Background(), nil, nil, "")
	messenger := NewAgentMessenger(context.Background(), runner, "source")
	d.SetMessenger(messenger)
	malformed := &DelegationDecision{ShouldDelegate: true, TargetType: "general", Query: " "}
	if _, err := d.ExecuteDelegation(context.Background(), malformed); err == nil || !strings.Contains(err.Error(), "must not be blank") {
		t.Fatalf("malformed decision error = %v", err)
	}
	if len(messenger.pending) != 0 || len(d.delegationHistory) != 0 {
		t.Fatalf("rejected execution mutated state: pending=%d history=%v", len(messenger.pending), d.delegationHistory)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.ExecuteDelegation(ctx, valid); err != context.Canceled {
		t.Fatalf("cancelled execution error = %v, want context.Canceled", err)
	}
	if len(messenger.pending) != 0 || len(d.delegationHistory) != 0 {
		t.Fatalf("cancelled execution mutated state: pending=%d history=%v", len(messenger.pending), d.delegationHistory)
	}
}

func TestDelegationStrategyExecuteAcceptsNilContextAndTracksAttempt(t *testing.T) {
	dir := t.TempDir()
	runner := NewRunner(context.Background(), &panicAgentClient{}, tools.DefaultRegistry(dir), dir)
	messenger := NewAgentMessenger(context.Background(), runner, "source")
	d := NewDelegationStrategy(AgentTypeGeneral, messenger)
	d.SetDepth(2)
	response, err := d.ExecuteDelegation(nil, &DelegationDecision{
		ShouldDelegate: true,
		TargetType:     " GENERAL ",
		Query:          "trigger provider panic",
	})
	if response != "" || err == nil || !strings.Contains(err.Error(), "delegation") || !strings.Contains(err.Error(), "provider exploded") {
		t.Fatalf("ExecuteDelegation = (%q, %v), want delivered failure error", response, err)
	}
	d.mu.RLock()
	count := d.delegationHistory["general:general"]
	d.mu.RUnlock()
	if count != 1 {
		t.Fatalf("actual delegation attempt count = %d, want 1", count)
	}
	runner.mu.RLock()
	var spawned *Agent
	for _, candidate := range runner.agents {
		spawned = candidate
		break
	}
	runner.mu.RUnlock()
	if spawned == nil || spawned.maxTurns != 9 {
		t.Fatalf("spawned delegated agent = %+v, want depth-adapted maxTurns 9", spawned)
	}
}

func TestDelegationStrategySuccessRateUsesExistingMetricsAndBoundaries(t *testing.T) {
	optimizer := &StrategyOptimizer{metrics: map[string]*StrategyMetrics{
		"bash": {StrategyName: "bash", FailureCount: 4},
	}}
	d := NewDelegationStrategy(AgentTypeExplore, nil)
	d.SetStrategyOptimizer(optimizer)
	if rate := d.getAgentSuccessRate("bash"); rate != 0 {
		t.Fatalf("direct fallback failure rate = %v, want 0", rate)
	}

	optimizer.mu.Lock()
	optimizer.metrics["delegate:bash"] = &StrategyMetrics{StrategyName: "delegate:bash", SuccessCount: 4}
	optimizer.mu.Unlock()
	if rate := d.getAgentSuccessRate("bash"); rate != 1 {
		t.Fatalf("delegation-specific success rate = %v, want 1", rate)
	}
	if rate := d.getAgentSuccessRate("missing"); rate != 0.5 {
		t.Fatalf("missing success rate = %v, want neutral 0.5", rate)
	}

	if got := boundedDelegationFloat(math.NaN(), 0, 1, 0.5); got != 0.5 {
		t.Fatalf("bounded NaN = %v, want fallback 0.5", got)
	}
	if got := boundedDelegationFloat(math.Inf(1), 0, 1, 0.5); got != 0.5 {
		t.Fatalf("bounded infinity = %v, want fallback 0.5", got)
	}
}
