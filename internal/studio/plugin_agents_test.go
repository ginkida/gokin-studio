package studio

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func TestParsePluginAgentDefinition(t *testing.T) {
	def, err := parsePluginAgentDefinition(`---
name: security-reviewer
description: Review risky changes with evidence.
tools:
  - Read
  - Grep
  - Bash
model: sonnet
permissionMode: bypassPermissions
---
# Security reviewer

Inspect the delegated change and report concrete findings.`, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "security-reviewer" || def.Description != "Review risky changes with evidence." ||
		len(def.Tools) != 3 || def.Model != "sonnet" || def.PermissionMode != "bypassPermissions" ||
		!strings.Contains(def.Prompt, "concrete findings") {
		t.Fatalf("definition = %#v", def)
	}
	prompt := buildPluginAgentSystemPrompt("review:security-reviewer", def, []string{"UnknownTool"})
	for _, required := range []string{
		"separate inspectable child chat",
		"Do not attempt to spawn another plugin agent",
		"model hint is ignored",
		"cannot weaken",
		"UnknownTool",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("specialist prompt missing %q: %s", required, prompt)
		}
	}
}

func TestParsePluginAgentDefinitionRejectsUnsafeMetadata(t *testing.T) {
	for name, content := range map[string]string{
		"unterminated": "---\nname: agent\n# no end",
		"bad name":     "---\nname: Bad Agent\n---\nDo work",
		"empty body":   "---\nname: agent\n---\n",
		"bad tools":    "---\nname: agent\ntools: {read: true}\n---\nDo work",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePluginAgentDefinition(content, "fallback"); err == nil {
				t.Fatal("unsafe agent definition was accepted")
			}
		})
	}
}

func TestResolvePluginAgentToolsIsAllowlistAndBlocksRecursion(t *testing.T) {
	registry := tools.DefaultRegistry(t.TempDir())
	registry.MustRegister(tools.NewPluginAgentTool([]tools.PluginAgentSpec{{
		ID: "plugin:agent",
	}}, nil))
	allowed, unavailable := resolvePluginAgentTools(
		[]string{"Read", "WebFetch", "plugin_agent", "UnknownTool"}, registry,
	)
	if !allowed["read"] || !allowed["web_fetch"] || allowed["plugin_agent"] {
		t.Fatalf("allowed tools = %#v", allowed)
	}
	if len(unavailable) != 2 || unavailable[0] != "plugin_agent" || unavailable[1] != "UnknownTool" {
		t.Fatalf("unavailable tools = %#v", unavailable)
	}
	if inherited, missing := resolvePluginAgentTools(nil, registry); inherited != nil || missing != nil {
		t.Fatalf("empty tool list should inherit: %#v, %#v", inherited, missing)
	}

	childRegistry := buildExecutionRegistry(
		registry, t.TempDir(), "kimi", true,
		map[string]bool{"read": true, "computer_screenshot": true}, true,
	)
	for _, expected := range []string{"read", "computer_screenshot"} {
		if _, ok := childRegistry.Get(expected); !ok {
			t.Fatalf("allowed child tool %q was not registered", expected)
		}
	}
	for _, blocked := range []string{"write", "computer_action", "plugin_agent"} {
		if _, ok := childRegistry.Get(blocked); ok {
			t.Fatalf("blocked child tool %q was registered", blocked)
		}
	}
}

func TestPluginAgentChildSessionIsInspectableAndNonRecursive(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Agent child")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	allowed := map[string]bool{"read": true}
	session, err := createPluginAgentSession(
		project, "default", "review:security", "glm", "glm-5.2", "manual",
		"specialist prompt", allowed,
	)
	if err != nil {
		t.Fatal(err)
	}
	if session.ParentID != "default" || !session.pluginAgentChild ||
		session.executionProvider != "glm" || session.executionModel != "glm-5.2" ||
		session.executionPermissionMode != "manual" ||
		session.executionSystemPrompt != "specialist prompt" ||
		!session.executionAllowedTools["read"] {
		t.Fatalf("child session = %#v", session)
	}
	allowed["write"] = true
	if session.executionAllowedTools["write"] {
		t.Fatal("child session retained caller-owned allowlist map")
	}
	if loaded, err := LoadHistory(projectSessionStorageKey(project.ID, session.ID)); err != nil || loaded == nil {
		t.Fatalf("child history was not persisted: %#v, %v", loaded, err)
	}
}

func TestWaitForPluginAgentReturnsFinalModelTextAndCancels(t *testing.T) {
	session := NewChatSession("Agent")
	session.queueWorker = true
	go func() {
		time.Sleep(15 * time.Millisecond)
		session.mu.Lock()
		session.history = append(session.history, &genai.Content{
			Role: "model",
			Parts: []*genai.Part{
				{Text: "private reasoning", Thought: true},
				genai.NewPartFromText("final specialist result"),
			},
		})
		session.queueWorker = false
		session.mu.Unlock()
	}()
	result, err := waitForPluginAgent(context.Background(), session)
	if err != nil || result != "final specialist result" {
		t.Fatalf("wait result = %q, %v", result, err)
	}

	cancelled := NewChatSession("Cancelled")
	cancelled.queueWorker = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForPluginAgent(ctx, cancelled); err == nil {
		t.Fatal("cancelled parent did not stop child wait")
	}
	cancelled.mu.RLock()
	halted := cancelled.queueHalt
	cancelled.mu.RUnlock()
	if !halted {
		t.Fatal("cancelled parent did not signal child stop")
	}
}

func TestPluginAgentUsesOrdinaryApprovalPolicy(t *testing.T) {
	args := map[string]any{"agent": "plugin:specialist", "task": "Review this"}
	if got := permissionForTool("manual", "plugin_agent", args); got != permissionAskTurn {
		t.Fatalf("manual plugin agent permission = %v", got)
	}
	if got := permissionForTool("auto", "plugin_agent", args); got != permissionAskTurn {
		t.Fatalf("auto plugin agent permission = %v", got)
	}
	if got := permissionForTool("skip", "plugin_agent", args); got != permissionAllow {
		t.Fatalf("skip plugin agent permission = %v", got)
	}
}

func TestStudioPluginAgentRunnerUsesEffectiveParentExecutionPolicy(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Agent runner")
	archivePath := filepath.Join(t.TempDir(), "agents.zip")
	writeTestPluginZIP(t, archivePath, map[string]string{
		"agents/.claude-plugin/plugin.json": `{"name":"agents"}`,
		"agents/agents/reviewer.md": `---
name: reviewer
description: Review the delegated work.
tools: [Read]
model: opus
permissionMode: bypassPermissions
---
Read the relevant evidence and return a focused review.`,
	})
	preview, err := s.previewPluginBundle(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallPluginBundle(archivePath, preview.Digest); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPluginEnabled("agents", true); err != nil {
		t.Fatal(err)
	}

	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	mainClient := &mockClient{}
	childClient := &mockClient{responses: []mockResp{{text: "specialist evidence"}}}
	registry := tools.DefaultRegistry(project.Directory)
	project.mu.Lock()
	project.client = mainClient
	project.registry = registry
	project.Provider = "glm"
	project.Model = "glm-5.2"
	project.PermissionMode = "manual"
	project.testEmitter = func(string, any) {}
	project.mu.Unlock()
	parent := project.GetSession("default")
	parent.mu.Lock()
	parent.executionProvider = "kimi"
	parent.executionModel = "k3"
	parent.executionPermissionMode = "skip"
	parent.mu.Unlock()

	var captured struct {
		provider, model, permission, prompt string
		allowed                             map[string]bool
		disable                             bool
	}
	project.testExecutionClientFactory = func(
		_ Settings, provider, model, permissionMode, systemPrompt string, _ string,
		allowedTools map[string]bool, disablePluginAgents bool,
	) (client.Client, *tools.Registry, error) {
		captured.provider, captured.model = provider, model
		captured.permission, captured.prompt = permissionMode, systemPrompt
		captured.allowed, captured.disable = allowedTools, disablePluginAgents
		return childClient, buildExecutionRegistry(
			registry, project.Directory, provider, false, allowedTools, disablePluginAgents,
		), nil
	}

	runner := &studioPluginAgentRunner{studio: s, projectID: project.ID}
	result, err := runner.RunPluginAgent(
		withAskUserRouting(context.Background(), project.ID, "default"),
		"agents:reviewer", "Review the current implementation.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Agent != "agents:reviewer" || result.Response != "specialist evidence" ||
		result.SessionID == "" {
		t.Fatalf("runner result = %#v", result)
	}
	if captured.provider != "kimi" || captured.model != "k3" || captured.permission != "skip" {
		t.Fatalf("effective execution policy = %#v", captured)
	}
	if !captured.allowed["read"] || len(captured.allowed) != 1 || !captured.disable {
		t.Fatalf("child tool policy = %#v, recursive=%v", captured.allowed, captured.disable)
	}
	for _, required := range []string{"Active reviewed plugin specialist", "model hint is ignored", "cannot weaken"} {
		if !strings.Contains(captured.prompt, required) {
			t.Fatalf("captured specialist prompt missing %q: %s", required, captured.prompt)
		}
	}
	child := project.GetSession(result.SessionID)
	if child == nil || child.ParentID != "default" || !child.pluginAgentChild {
		t.Fatalf("inspectable child session = %#v", child)
	}
	parent.mu.Lock()
	parent.pluginAgentChild = true
	parent.mu.Unlock()
	if _, err := runner.RunPluginAgent(
		withAskUserRouting(context.Background(), project.ID, "default"),
		"agents:reviewer", "Try to recurse.",
	); err == nil || !strings.Contains(err.Error(), "recursive") {
		t.Fatalf("recursive plugin agent run was accepted: %v", err)
	}
}
