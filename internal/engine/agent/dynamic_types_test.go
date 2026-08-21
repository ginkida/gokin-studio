package agent

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func TestDynamicAgentTypeRegistrationValidation(t *testing.T) {
	tests := []struct {
		name        string
		typeName    string
		description string
		tools       []string
		prompt      string
	}{
		{name: "blank name", typeName: " "},
		{name: "numeric first character", typeName: "1reviewer"},
		{name: "invalid name character", typeName: "review/agent"},
		{name: "built-in collision", typeName: " GENERAL "},
		{name: "invalid description", typeName: "reviewer", description: "bad\x00description"},
		{name: "invalid prompt", typeName: "reviewer", prompt: "bad\x00prompt"},
		{name: "blank tool", typeName: "reviewer", tools: []string{" "}},
		{name: "invalid tool", typeName: "reviewer", tools: []string{"3bad"}},
		{name: "duplicate tool", typeName: "reviewer", tools: []string{"Read", "read"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewAgentTypeRegistry()
			if err := registry.RegisterDynamic(test.typeName, test.description, test.tools, test.prompt); err == nil {
				t.Fatal("invalid dynamic type was accepted")
			}
		})
	}

	registry := NewAgentTypeRegistry()
	if err := registry.RegisterDynamic(" Reviewer ", "Reviews", []string{" Read ", "glob"}, "Be careful"); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterDynamic("reviewer", "duplicate", nil, ""); err == nil {
		t.Fatal("duplicate dynamic type replaced the existing definition")
	}
}

func TestDynamicAgentTypeRegistryReturnsImmutableDeterministicSnapshots(t *testing.T) {
	registry := NewAgentTypeRegistry()
	inputTools := []string{"read", "glob"}
	if err := registry.RegisterDynamic("zeta", "Z", nil, "z prompt"); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterDynamic(" Reviewer ", "Reviews", inputTools, "review prompt"); err != nil {
		t.Fatal(err)
	}
	inputTools[0] = "bash"

	first, ok := registry.GetDynamic(" REVIEWER ")
	if !ok || first.Name != "reviewer" || !reflect.DeepEqual(first.AllowedTools, []string{"read", "glob"}) {
		t.Fatalf("first snapshot = %+v, ok=%v", first, ok)
	}
	first.Name = "mutated"
	first.Description = "mutated"
	first.AllowedTools[0] = "bash"
	second, ok := registry.GetDynamic("reviewer")
	if !ok || second.Name != "reviewer" || second.Description != "Reviews" || second.AllowedTools[0] != "read" {
		t.Fatalf("registry was mutated through GetDynamic: %+v", second)
	}

	listed := registry.ListDynamic()
	if len(listed) != 2 || listed[0].Name != "reviewer" || listed[1].Name != "zeta" {
		t.Fatalf("dynamic list order = %+v", listed)
	}
	listed[0].AllowedTools[0] = "bash"
	if got := registry.GetToolsForType(" Reviewer "); !reflect.DeepEqual(got, []string{"read", "glob"}) {
		t.Fatalf("tool snapshot = %v", got)
	}
	toolsSnapshot := registry.GetToolsForType("reviewer")
	toolsSnapshot[0] = "bash"
	if got := registry.GetToolsForType("reviewer"); got[0] != "read" {
		t.Fatalf("registry was mutated through GetToolsForType: %v", got)
	}

	wantAll := []string{"bash", "claude-code-guide", "explore", "general", "plan", "reviewer", "zeta"}
	if got := registry.ListAll(); !reflect.DeepEqual(got, wantAll) {
		t.Fatalf("all type names = %v, want %v", got, wantAll)
	}
	if !registry.IsBuiltin(" PLAN ") || !registry.Exists(" Reviewer ") {
		t.Fatal("normalized type lookup failed")
	}
}

func TestDynamicAgentWithNoDeclaredToolsGetsNoCapabilities(t *testing.T) {
	base := tools.DefaultRegistry(t.TempDir())
	filtered := createFilteredRegistryFromList(nil, base)
	if names := filtered.Names(); len(names) != 0 {
		t.Fatalf("empty dynamic allowlist received tools: %v", names)
	}

	runner := NewRunner(nil, nil, base, t.TempDir())
	registry := runner.GetTypeRegistry()
	if registry == nil {
		t.Fatal("Runner did not initialize its type registry")
	}
	if err := registry.RegisterDynamic("reviewer", "Reviews", nil, "Review carefully"); err != nil {
		t.Fatal(err)
	}
	deps := runner.snapshotAgentDeps()
	agentType, _, err := normalizeAgentSpawnRequest(deps, "reviewer", "work", 1, "")
	if err != nil || agentType != "reviewer" {
		t.Fatalf("dynamic request = (%q, %v)", agentType, err)
	}
	agent := runner.newConfiguredAgent(nil, deps, "reviewer", 1, "", nil)
	if names := agent.registry.Names(); len(names) != 0 {
		t.Fatalf("dynamic agent received undeclared tools: %v", names)
	}

	runner.SetTypeRegistry(nil)
	if runner.GetTypeRegistry() == nil {
		t.Fatal("SetTypeRegistry(nil) disabled the registry invariant")
	}
}

func TestDynamicAgentSpawnRejectsUnavailableDeclaredTool(t *testing.T) {
	registry := NewAgentTypeRegistry()
	if err := registry.RegisterDynamic("reviewer", "Reviews", []string{"missing_tool"}, ""); err != nil {
		t.Fatal(err)
	}
	deps := runnerAgentDeps{typeRegistry: registry, baseRegistry: tools.NewRegistry()}
	if _, _, err := normalizeAgentSpawnRequest(deps, "reviewer", "work", 1, ""); err == nil ||
		!strings.Contains(err.Error(), "missing_tool") {
		t.Fatalf("unavailable tool validation error = %v", err)
	}
}

func TestDynamicAgentTypeRegistryConcurrentSnapshots(t *testing.T) {
	registry := NewAgentTypeRegistry()
	if err := registry.RegisterDynamic("stable", "Stable", []string{"read"}, ""); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(5)
	go func() {
		defer wg.Done()
		for index := 0; index < 200; index++ {
			name := fmt.Sprintf("temp%d", index)
			if err := registry.RegisterDynamic(name, "", nil, ""); err == nil {
				_ = registry.UnregisterDynamic(name)
			}
		}
	}()
	for range 4 {
		go func() {
			defer wg.Done()
			for range 500 {
				if snapshot, ok := registry.GetDynamic("stable"); ok {
					snapshot.AllowedTools[0] = "bash"
				}
				_ = registry.ListDynamic()
				_ = registry.ListAll()
				_ = registry.GetToolsForType("stable")
			}
		}()
	}
	wg.Wait()

	if got := registry.GetToolsForType("stable"); !reflect.DeepEqual(got, []string{"read"}) {
		t.Fatalf("concurrent snapshots mutated registry: %v", got)
	}
}
