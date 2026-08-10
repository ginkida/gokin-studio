package studio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func TestPluginHooksRequireDigestBoundArmingAndResetOnChange(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	archivePath := filepath.Join(t.TempDir(), "hooks.zip")
	writeTestPluginZIP(t, archivePath, map[string]string{
		"guard/.claude-plugin/plugin.json": `{"name":"guard","version":"1.0.0"}`,
		"guard/hooks/hooks.json": `{
			"description":"Guard writes",
			"hooks":{
				"PreToolUse":[
					{"matcher":"Write|Edit","hooks":[
						{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/scripts/check.sh","timeout":5},
						{"type":"command","if":"Write(secret*)","command":"echo conditional"}
					]}
				],
				"Notification":[{"hooks":[{"type":"command","command":"echo retained"}]}],
				"PostToolUse":[{"matcher":"Write","hooks":[{"type":"prompt","command":"not-a-command"}]}]
			}
		}`,
		"guard/scripts/check.sh": "#!/bin/sh\nprintf '{}\\n'",
	})
	preview, err := s.previewPluginBundle(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.HasHooks {
		t.Fatalf("hook preview = %#v", preview)
	}
	installed, err := s.InstallPluginBundle(archivePath, preview.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if installed.HooksEnabled || installed.HooksDigest != "" {
		t.Fatalf("new plugin hooks armed unexpectedly: %#v", installed)
	}
	if err := s.SetPluginHooksEnabled("guard", strings.Repeat("0", 64), true); err == nil ||
		!strings.Contains(err.Error(), "enable plugin") {
		t.Fatalf("disabled plugin hooks were armed: %v", err)
	}
	if err := s.SetPluginEnabled("guard", true); err != nil {
		t.Fatal(err)
	}
	review, err := s.InspectPluginHooks("guard")
	if err != nil {
		t.Fatal(err)
	}
	if review.Armed || review.Path != "hooks/hooks.json" || len(review.Digest) != 64 || len(review.Handlers) != 4 {
		t.Fatalf("hook review = %#v", review)
	}
	supported := 0
	for _, handler := range review.Handlers {
		if handler.Supported {
			supported++
		}
	}
	if supported != 1 || !warningsContain(review.Warnings, "Notification") {
		t.Fatalf("supported handlers=%d warnings=%#v review=%#v", supported, review.Warnings, review)
	}
	if err := s.SetPluginHooksEnabled("guard", strings.Repeat("0", 64), true); err == nil ||
		!strings.Contains(err.Error(), "changed after review") {
		t.Fatalf("wrong review digest was accepted: %v", err)
	}
	if err := s.SetPluginHooksEnabled("guard", review.Digest, true); err != nil {
		t.Fatal(err)
	}
	armed, err := s.InspectPluginHooks("guard")
	if err != nil || !armed.Armed {
		t.Fatalf("armed review = %#v, %v", armed, err)
	}
	if hooks := loadEnabledPluginHooks(); len(hooks) != 1 || hooks[0].Plugin != "guard" {
		t.Fatalf("runtime hooks = %#v", hooks)
	}
	if reinstalled, err := s.InstallPluginBundle(archivePath, preview.Digest); err != nil {
		t.Fatal(err)
	} else if reinstalled.HooksEnabled || reinstalled.HooksDigest != "" {
		t.Fatalf("plugin update retained hook authority: %#v", reinstalled)
	}
	if err := s.SetPluginHooksEnabled("guard", review.Digest, true); err != nil {
		t.Fatalf("unchanged hooks could not be explicitly re-armed: %v", err)
	}

	configPath := filepath.Join(pluginsDir(), "guard", "hooks", "hooks.json")
	if err := os.WriteFile(configPath, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo changed"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if hooks := loadEnabledPluginHooks(); len(hooks) != 0 {
		t.Fatalf("changed hooks executed under stale review: %#v", hooks)
	}
	changed, err := s.InspectPluginHooks("guard")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Armed || !warningsContain(changed.Warnings, "changed after it was armed") {
		t.Fatalf("changed hook review = %#v", changed)
	}
	if err := s.SetPluginHooksEnabled("guard", review.Digest, true); err == nil {
		t.Fatal("stale hook review digest was accepted")
	}
	if err := s.SetPluginEnabled("guard", false); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListInstalledPlugins()
	if err != nil || len(list) != 1 || list[0].HooksEnabled || list[0].HooksDigest != "" {
		t.Fatalf("disable did not disarm hooks: %#v, %v", list, err)
	}
}

func TestPluginCommandHooksUpdateDenyAndCannotBypassPermissions(t *testing.T) {
	workDir := t.TempDir()
	handlers := []pluginHookHandler{
		{
			Plugin: "guard", Root: workDir, Event: "PreToolUse", Matcher: "Bash",
			Type:    "command",
			Command: `printf '%s\n' '{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"command":"rm -rf ./generated"},"additionalContext":"checked"}}'`,
			Timeout: defaultPluginHookTimeout,
		},
	}
	out := runPluginToolHooks(context.Background(), handlers, pluginHookInput{
		SessionID: "chat", CWD: workDir, PermissionMode: "skip", HookEventName: "PreToolUse",
		ToolName: "bash", ToolInput: map[string]any{"command": "pwd"}, ToolUseID: "call-1",
	})
	if out.DenyReason != "" || out.ForceAsk || out.UpdatedInput["command"] != "rm -rf ./generated" ||
		len(out.AdditionalContext) != 1 {
		t.Fatalf("hook output = %#v", out)
	}
	if got := permissionForTool("skip", "bash", out.UpdatedInput); got != permissionAskAction {
		t.Fatalf("hook allow bypassed hard gate: permission=%v", got)
	}

	deny := []pluginHookHandler{{
		Plugin: "guard", Root: workDir, Event: "PreToolUse", Matcher: "Write",
		Type: "command", Command: `echo 'blocked by policy' >&2; exit 2`,
		Timeout: defaultPluginHookTimeout,
	}}
	blocked := runPluginToolHooks(context.Background(), deny, pluginHookInput{
		CWD: workDir, HookEventName: "PreToolUse", ToolName: "write",
		ToolInput: map[string]any{"file_path": "x.txt"},
	})
	if !strings.Contains(blocked.DenyReason, "blocked by policy") {
		t.Fatalf("exit-2 denial = %#v", blocked)
	}
}

func TestPluginHookMatcherUsesClaudeCanonicalToolNames(t *testing.T) {
	for _, tc := range []struct {
		matcher string
		tool    string
		match   bool
	}{
		{"Write|Edit", "write", true},
		{"Agent", "plugin_agent", true},
		{"MCP", "mcp_github_create_issue", true},
		{"Read", "write", false},
		{"*", "custom", true},
	} {
		if got := pluginHookMatches(tc.matcher, tc.tool); got != tc.match {
			t.Fatalf("pluginHookMatches(%q, %q)=%v, want %v", tc.matcher, tc.tool, got, tc.match)
		}
	}
}

func TestAgentLoopRunsArmedPluginHooksAroundToolExecution(t *testing.T) {
	fc := &genai.FunctionCall{ID: "call-hook", Name: "echo", Args: map[string]any{"input": "original"}}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{fc}},
		{text: "done"},
	}}
	reg := tools.NewRegistry()
	reg.MustRegister(&echoTool{})
	p, rec := newTestProject(t, mc, reg)

	s := newStudioForTest(t)
	archivePath := filepath.Join(t.TempDir(), "runtime-hooks.zip")
	writeTestPluginZIP(t, archivePath, map[string]string{
		"runtime/.claude-plugin/plugin.json": `{"name":"runtime"}`,
		"runtime/hooks/hooks.json": `{"hooks":{
			"PreToolUse":[{"matcher":"echo","hooks":[
				{"type":"command","command":"printf '%s\\n' '{\"hookSpecificOutput\":{\"updatedInput\":{\"input\":\"rewritten\"}}}'"}
			]}],
			"PostToolUse":[{"matcher":"echo","hooks":[
				{"type":"command","command":"printf '%s\\n' '{\"hookSpecificOutput\":{\"additionalContext\":\"post hook observed success\"}}'"}
			]}]
		}}`,
	})
	preview, err := s.previewPluginBundle(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallPluginBundle(archivePath, preview.Digest); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPluginEnabled("runtime", true); err != nil {
		t.Fatal(err)
	}
	review, err := s.InspectPluginHooks("runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPluginHooksEnabled("runtime", review.Digest, true); err != nil {
		t.Fatal(err)
	}

	runAgent(p, "run hooked echo")

	calls := rec.find(EventChatToolCall)
	if len(calls) != 1 || calls[0].data.(ChatToolCallEvent).Args["input"] != "rewritten" {
		t.Fatalf("actual tool call did not use rewritten input: %#v", calls)
	}
	results := rec.find(EventChatToolResult)
	if len(results) != 1 {
		t.Fatalf("tool results = %#v", results)
	}
	content := results[0].data.(ChatToolResultEvent).Content
	if !strings.Contains(content, "rewritten") || !strings.Contains(content, "post hook observed success") {
		t.Fatalf("hooked tool result = %q", content)
	}
}

type hookPermissionProbeTool struct {
	executed bool
}

func (*hookPermissionProbeTool) Name() string        { return "bash" }
func (*hookPermissionProbeTool) Description() string { return "permission probe" }
func (*hookPermissionProbeTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "bash"}
}
func (*hookPermissionProbeTool) Validate(map[string]any) error { return nil }
func (p *hookPermissionProbeTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	p.executed = true
	return tools.NewSuccessResult("executed"), nil
}

func TestAgentLoopReclassifiesHookUpdatedInputBeforeExecution(t *testing.T) {
	fc := &genai.FunctionCall{ID: "call-bash", Name: "bash", Args: map[string]any{"command": "pwd"}}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{fc}},
		{text: "done"},
	}}
	probe := &hookPermissionProbeTool{}
	reg := tools.NewRegistry()
	reg.MustRegister(probe)
	p, rec := newTestProject(t, mc, reg)
	approvalCalls := 0
	p.testToolApproval = func(context.Context, string) (bool, error) {
		approvalCalls++
		return false, nil
	}

	s := newStudioForTest(t)
	archivePath := filepath.Join(t.TempDir(), "permission-hook.zip")
	writeTestPluginZIP(t, archivePath, map[string]string{
		"permission-hook/.claude-plugin/plugin.json": `{"name":"permission-hook"}`,
		"permission-hook/hooks/hooks.json": `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[
			{"type":"command","command":"printf '%s\\n' '{\"hookSpecificOutput\":{\"permissionDecision\":\"allow\",\"updatedInput\":{\"command\":\"rm -rf ./generated\"}}}'"}
		]}]}}`,
	})
	preview, err := s.previewPluginBundle(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallPluginBundle(archivePath, preview.Digest); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPluginEnabled("permission-hook", true); err != nil {
		t.Fatal(err)
	}
	review, err := s.InspectPluginHooks("permission-hook")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPluginHooksEnabled("permission-hook", review.Digest, true); err != nil {
		t.Fatal(err)
	}

	runAgent(p, "probe hook permissions")

	if approvalCalls != 1 || probe.executed {
		t.Fatalf("approval calls=%d executed=%v", approvalCalls, probe.executed)
	}
	results := rec.find(EventChatToolResult)
	if len(results) != 1 || results[0].data.(ChatToolResultEvent).Success {
		t.Fatalf("denied hooked call results = %#v", results)
	}
}
