package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func TestRestrictedAgentRegistryCannotEscalateBeyondDeclaredTools(t *testing.T) {
	base := tools.DefaultRegistry(t.TempDir())

	for _, agentType := range []AgentType{AgentTypeExplore, AgentTypeBash, AgentTypePlan, AgentTypeGuide} {
		registry := createFilteredRegistry(agentType, base)
		allowed := make(map[string]bool)
		for _, name := range agentType.AllowedTools() {
			allowed[name] = true
		}
		for _, tool := range registry.List() {
			if !allowed[tool.Name()] {
				t.Fatalf("%s agent received undeclared tool %q", agentType, tool.Name())
			}
		}
		if _, exists := registry.Get("request_tool"); exists {
			t.Fatalf("%s agent can dynamically widen its capability set", agentType)
		}
		listTool, exists := registry.Get("tools_list")
		if !exists {
			t.Fatalf("%s agent is missing tools_list", agentType)
		}
		result, err := listTool.Execute(context.Background(), nil)
		if err != nil || !result.Success {
			t.Fatalf("%s tools_list failed: result=%+v err=%v", agentType, result, err)
		}
		for _, forbidden := range []string{"write", "delete", "git_commit"} {
			if !allowed[forbidden] && strings.Contains(result.Content, "**"+forbidden+"**") {
				t.Fatalf("%s tools_list advertised forbidden tool %q", agentType, forbidden)
			}
		}
	}

	if _, exists := base.Get("request_tool"); exists {
		t.Fatal("default registry still exposes retired capability-escalation tool")
	}
}

func TestDynamicAgentRegistryCannotGainUnlistedTools(t *testing.T) {
	base := tools.DefaultRegistry(t.TempDir())
	registry := createFilteredRegistryFromList([]string{"read", "glob", "request_tool"}, base)

	if _, exists := registry.Get("read"); !exists {
		t.Fatal("listed read tool is missing")
	}
	if _, exists := registry.Get("glob"); !exists {
		t.Fatal("listed glob tool is missing")
	}
	if _, exists := registry.Get("request_tool"); exists {
		t.Fatal("stale dynamic definition resurrected retired request_tool")
	}
	if _, exists := registry.Get("bash"); exists {
		t.Fatal("dynamic registry gained unlisted bash capability")
	}
}
