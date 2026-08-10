package studio

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

type countedMCPTool struct{ calls atomic.Int32 }

func (t *countedMCPTool) Name() string        { return "mcp_external_mutate" }
func (t *countedMCPTool) Description() string { return "external tool with unknown effects" }
func (t *countedMCPTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name()}
}

type countedNamedTool struct {
	name  string
	calls atomic.Int32
}

func (t *countedNamedTool) Name() string        { return t.name }
func (t *countedNamedTool) Description() string { return t.name }
func (t *countedNamedTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name}
}
func (t *countedNamedTool) Validate(map[string]any) error { return nil }
func (t *countedNamedTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.calls.Add(1)
	return tools.NewSuccessResult("executed"), nil
}
func (t *countedMCPTool) Validate(map[string]any) error { return nil }
func (t *countedMCPTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.calls.Add(1)
	return tools.NewSuccessResult("executed"), nil
}

func mcpPermissionProject(t *testing.T) (*Project, *countedMCPTool) {
	t.Helper()
	tool := &countedMCPTool{}
	reg := tools.NewRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{
			{Name: tool.Name(), Args: map[string]any{"item": "one"}},
			{Name: tool.Name(), Args: map[string]any{"item": "two"}},
		}},
		{text: "done"},
	}}
	p, _ := newTestProject(t, mc, reg)
	return p, tool
}

func TestManualModeApprovesEachSensitiveMCPAction(t *testing.T) {
	p, tool := mcpPermissionProject(t)
	p.PermissionMode = "ask"
	var approvals atomic.Int32
	p.testToolApproval = func(context.Context, string) (bool, error) {
		approvals.Add(1)
		return true, nil
	}
	runAgent(p, "use external connector")
	if approvals.Load() != 2 || tool.calls.Load() != 2 {
		t.Fatalf("approval=%d executions=%d, want 2 exact approvals and 2 executions", approvals.Load(), tool.calls.Load())
	}
}

func TestManualModeDenialBlocksEachSensitiveMCPAction(t *testing.T) {
	p, tool := mcpPermissionProject(t)
	p.PermissionMode = "ask"
	var approvals atomic.Int32
	p.testToolApproval = func(context.Context, string) (bool, error) {
		approvals.Add(1)
		return false, nil
	}
	runAgent(p, "use external connector")
	if approvals.Load() != 2 || tool.calls.Load() != 0 {
		t.Fatalf("approval=%d executions=%d, want two exact denials and no execution", approvals.Load(), tool.calls.Load())
	}
	if len(p.client.(*mockClient).lastFuncRespResults) != 2 {
		t.Fatalf("provider received %d tool results, want both denials", len(p.client.(*mockClient).lastFuncRespResults))
	}
	for _, result := range p.client.(*mockClient).lastFuncRespResults {
		if !strings.Contains(result.Response["error"].(string), "denied by the user") {
			t.Errorf("provider denial result = %#v", result.Response)
		}
	}
}

func TestAutoModeStillHardGatesMCPTools(t *testing.T) {
	p, tool := mcpPermissionProject(t)
	p.PermissionMode = "auto"
	var approvals atomic.Int32
	p.testToolApproval = func(context.Context, string) (bool, error) {
		approvals.Add(1)
		return true, nil
	}
	runAgent(p, "use external connector")
	if approvals.Load() != 2 || tool.calls.Load() != 2 {
		t.Fatalf("auto MCP approvals=%d executions=%d, want 2 and 2", approvals.Load(), tool.calls.Load())
	}
}

func TestAutoModeAllowsBoundedEditButReviewsArbitraryShell(t *testing.T) {
	writeTool := &countedNamedTool{name: "write"}
	bashTool := &countedNamedTool{name: "bash"}
	reg := tools.NewRegistry()
	reg.MustRegister(writeTool)
	reg.MustRegister(bashTool)
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: "write"}, {Name: "bash", Args: map[string]any{"command": "go test ./..."}}}},
		{text: "done"},
	}}
	p, _ := newTestProject(t, mc, reg)
	p.PermissionMode = "auto"
	var approvals atomic.Int32
	p.testToolApproval = func(context.Context, string) (bool, error) {
		approvals.Add(1)
		return false, nil
	}
	runAgent(p, "edit then run a command")
	if approvals.Load() != 1 || writeTool.calls.Load() != 1 || bashTool.calls.Load() != 0 {
		t.Fatalf("approvals=%d writes=%d bash=%d", approvals.Load(), writeTool.calls.Load(), bashTool.calls.Load())
	}
}

func TestSkipModeBypassesOrdinaryButNotExactDeleteApproval(t *testing.T) {
	writeTool := &countedNamedTool{name: "write"}
	bashTool := &countedNamedTool{name: "bash"}
	deleteTool := &countedNamedTool{name: "delete"}
	reg := tools.NewRegistry()
	reg.MustRegister(writeTool)
	reg.MustRegister(bashTool)
	reg.MustRegister(deleteTool)
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: "write"}, {Name: "bash"}, {Name: "delete"}}},
		{text: "done"},
	}}
	p, _ := newTestProject(t, mc, reg)
	p.PermissionMode = "skip"
	var approvals atomic.Int32
	p.testToolApproval = func(_ context.Context, toolName string) (bool, error) {
		approvals.Add(1)
		if toolName != "delete" {
			t.Fatalf("unexpected approval for %q", toolName)
		}
		return false, nil
	}
	runAgent(p, "change and then delete")
	if approvals.Load() != 1 || writeTool.calls.Load() != 1 || bashTool.calls.Load() != 1 || deleteTool.calls.Load() != 0 {
		t.Fatalf("approvals=%d writes=%d bash=%d deletes=%d", approvals.Load(), writeTool.calls.Load(), bashTool.calls.Load(), deleteTool.calls.Load())
	}
}

func TestComputerUseAlwaysPromptsEvenInAutoMode(t *testing.T) {
	screen := &countedNamedTool{name: "computer_screenshot"}
	reg := tools.NewRegistry()
	if err := reg.Register(screen); err != nil {
		t.Fatal(err)
	}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: screen.Name()}}},
		{text: "done"},
	}}
	p, _ := newTestProject(t, mc, reg)
	p.PermissionMode = "auto"
	p.testForegroundApplication = func(context.Context) (tools.ComputerApplication, error) {
		return tools.ComputerApplication{ID: "com.example.editor", Name: "Editor"}, nil
	}
	var approvals atomic.Int32
	p.testToolApproval = func(context.Context, string) (bool, error) {
		approvals.Add(1)
		return false, nil
	}
	runAgent(p, "inspect my screen")
	if approvals.Load() != 1 || screen.calls.Load() != 0 {
		t.Fatalf("approvals=%d screen captures=%d, want one prompt and no capture", approvals.Load(), screen.calls.Load())
	}
}

func TestAskModeAllowsReadsButHardGatesBuiltInWrites(t *testing.T) {
	readTool := &countedNamedTool{name: "read"}
	writeTool := &countedNamedTool{name: "write"}
	reg := tools.NewRegistry()
	if err := reg.Register(readTool); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(writeTool); err != nil {
		t.Fatal(err)
	}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: "read"}, {Name: "write"}, {Name: "read"}, {Name: "write"}}},
		{text: "done"},
	}}
	p, _ := newTestProject(t, mc, reg)
	p.PermissionMode = "ask"
	var approvals atomic.Int32
	p.testToolApproval = func(_ context.Context, toolName string) (bool, error) {
		approvals.Add(1)
		if toolName != "write" {
			t.Fatalf("approval requested for %q, want first mutating write", toolName)
		}
		return false, nil
	}
	runAgent(p, "read and then attempt changes")
	if approvals.Load() != 1 || readTool.calls.Load() != 2 || writeTool.calls.Load() != 0 {
		t.Fatalf("approval=%d reads=%d writes=%d", approvals.Load(), readTool.calls.Load(), writeTool.calls.Load())
	}
}

func TestPlanModeExecutesOnlyAdvertisedReadOnlyToolsWithoutApprovalBypass(t *testing.T) {
	readTool := &countedNamedTool{name: "read"}
	writeTool := &countedNamedTool{name: "write"}
	mcpTool := &countedNamedTool{name: "mcp_external_mutate"}
	pluginAgent := &countedNamedTool{name: "plugin_agent"}
	computer := &countedNamedTool{name: "computer_screenshot"}
	reg := tools.NewRegistry()
	for _, tool := range []tools.Tool{readTool, writeTool, mcpTool, pluginAgent, computer} {
		reg.MustRegister(tool)
	}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{
			{Name: "read"},
			{Name: "write"},
			{Name: "mcp_external_mutate"},
			{Name: "plugin_agent"},
			{Name: "computer_screenshot"},
		}},
		{text: "plan ready"},
	}}
	p, _ := newTestProject(t, mc, reg)
	p.PermissionMode = "skip"
	p.GetSession("default").mu.Lock()
	p.GetSession("default").permissionMode = "plan"
	p.GetSession("default").mu.Unlock()
	var approvals atomic.Int32
	p.testToolApproval = func(context.Context, string) (bool, error) {
		approvals.Add(1)
		return true, nil
	}
	runAgent(p, "inspect and plan")

	if approvals.Load() != 0 {
		t.Fatalf("Plan offered %d approval bypass(es)", approvals.Load())
	}
	if readTool.calls.Load() != 1 || writeTool.calls.Load() != 0 || mcpTool.calls.Load() != 0 || pluginAgent.calls.Load() != 0 || computer.calls.Load() != 0 {
		t.Fatalf("executions read=%d write=%d mcp=%d plugin=%d computer=%d",
			readTool.calls.Load(), writeTool.calls.Load(), mcpTool.calls.Load(), pluginAgent.calls.Load(), computer.calls.Load())
	}
	mc.mu.Lock()
	advertised := append([]*genai.Tool(nil), mc.lastTools...)
	systemInstruction := mc.lastSystemInstruction
	mc.mu.Unlock()
	var names []string
	for _, envelope := range advertised {
		for _, declaration := range envelope.FunctionDeclarations {
			names = append(names, declaration.Name)
		}
	}
	if len(names) != 1 || names[0] != "read" {
		t.Fatalf("Plan advertised tools = %v, want only read", names)
	}
	if !strings.Contains(systemInstruction, "Permission mode: Plan") || !strings.Contains(systemInstruction, "read-only") {
		t.Fatalf("Plan directive missing: %q", systemInstruction)
	}
	for _, response := range mc.lastFuncRespResults {
		if response.Name != "read" && !strings.Contains(response.Response["error"].(string), "unavailable in Plan mode") {
			t.Fatalf("Plan denial for %s = %#v", response.Name, response.Response)
		}
	}
}

func TestToolApprovalDetailsShowAllowlistedOperationWithoutPayloads(t *testing.T) {
	details := toolApprovalDetails("write", map[string]any{
		"file_path": "/workspace/report.md",
		"content":   "PRIVATE FILE BODY",
		"env":       map[string]any{"API_TOKEN": "secret-token"},
		"headers":   map[string]any{"Authorization": "Bearer secret"},
		"password":  "hidden-password",
	})
	joined := approvalDetailsText(details)
	for _, want := range []string{"Tool: write", "File: /workspace/report.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("approval details %q do not contain %q", joined, want)
		}
	}
	for _, secret := range []string{"PRIVATE FILE BODY", "secret-token", "Bearer secret", "hidden-password"} {
		if strings.Contains(joined, secret) {
			t.Errorf("approval details leaked %q: %q", secret, joined)
		}
	}
}

func TestToolApprovalDetailsMCPShowsFieldNamesNotValues(t *testing.T) {
	details := toolApprovalDetails("mcp_calendar_create", map[string]any{
		"title":        "Confidential acquisition",
		"access_token": "very-secret",
	})
	joined := approvalDetailsText(details)
	if !strings.Contains(joined, "Argument fields: access_token, title") {
		t.Fatalf("MCP approval details do not identify provided fields: %q", joined)
	}
	for _, secret := range []string{"Confidential acquisition", "very-secret"} {
		if strings.Contains(joined, secret) {
			t.Errorf("MCP approval details leaked value %q: %q", secret, joined)
		}
	}
}

func TestPreviewApprovalTextTruncatesByRune(t *testing.T) {
	if got := previewApprovalText(" абвгд ", 3); got != "абв…" {
		t.Fatalf("previewApprovalText = %q, want %q", got, "абв…")
	}
}

func TestToolApprovalOnlyAcceptsExplicitTurnGrant(t *testing.T) {
	for _, answer := range []string{"yes", "allow", "Allow once", "Deny", "", "Allow changes for this turn please"} {
		if isToolApprovalGranted(answer, "Allow changes for this turn") {
			t.Errorf("ambiguous answer %q granted tool approval", answer)
		}
	}
	if !isToolApprovalGranted("  ALLOW CHANGES FOR THIS TURN  ", "Allow changes for this turn") {
		t.Fatal("explicit option did not grant tool approval")
	}
	if !isToolApprovalGranted("Allow computer access for this turn", "Allow computer access for this turn") {
		t.Fatal("explicit computer access option did not grant approval")
	}
}

func TestSensitiveToolApprovalEventIsSingleActionAndDenyByDefault(t *testing.T) {
	event := sensitiveToolApprovalEvent("delete", map[string]any{"path": "notes.txt"}, "Allow this action")
	if event.Scope != "single_action" {
		t.Fatalf("scope = %q, want single_action so the UI never describes an exact grant as turn-wide", event.Scope)
	}
	if event.Default != "Deny" {
		t.Fatalf("default = %q, want Deny", event.Default)
	}
	if len(event.Options) != 2 || event.Options[0] != "Allow this action" || event.Options[1] != "Deny" {
		t.Fatalf("options = %#v", event.Options)
	}
}

func approvalDetailsText(details []ToolApprovalDetail) string {
	var lines []string
	for _, detail := range details {
		lines = append(lines, detail.Label+": "+detail.Value)
	}
	return strings.Join(lines, "\n")
}
