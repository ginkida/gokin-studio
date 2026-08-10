package studio

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/security"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

type countedIsolationTool struct {
	name      string
	isolation security.WorkspaceIsolationStatus
	calls     atomic.Int32
}

func (t *countedIsolationTool) Name() string        { return t.name }
func (t *countedIsolationTool) Description() string { return t.name }
func (t *countedIsolationTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name}
}
func (t *countedIsolationTool) Validate(map[string]any) error { return nil }
func (t *countedIsolationTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.calls.Add(1)
	return tools.NewSuccessResult("executed"), nil
}
func (t *countedIsolationTool) WorkspaceIsolationStatus() security.WorkspaceIsolationStatus {
	return t.isolation
}

func TestSkipModeStillExactGatesUnisolatedHostExecution(t *testing.T) {
	tool := &countedIsolationTool{
		name: "run_tests",
		isolation: security.WorkspaceIsolationStatus{
			Mode: "host", Detail: "not available", Enforced: false,
		},
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tool)
	model := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: tool.name, Args: map[string]any{"path": "."}}}},
		{text: "done"},
	}}
	project, _ := newTestProject(t, model, registry)
	project.PermissionMode = "skip"
	var approvals atomic.Int32
	project.testToolApproval = func(_ context.Context, got string) (bool, error) {
		approvals.Add(1)
		if got != tool.name {
			t.Fatalf("approval tool = %q", got)
		}
		return false, nil
	}

	runAgent(project, "run tests")

	if approvals.Load() != 1 || tool.calls.Load() != 0 {
		t.Fatalf("approvals=%d calls=%d", approvals.Load(), tool.calls.Load())
	}
	if got, _ := model.lastFuncRespResults[0].Response["error"].(string); !strings.Contains(got, "denied by the user") {
		t.Fatalf("provider result = %#v", model.lastFuncRespResults[0].Response)
	}
}

func TestSkipModeAllowsSandboxedReadOnlyValidation(t *testing.T) {
	tool := &countedIsolationTool{
		name: "run_tests",
		isolation: security.WorkspaceIsolationStatus{
			Available: true, Enforced: true, Mode: "test-sandbox", Detail: "isolated",
		},
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tool)
	model := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: tool.name, Args: map[string]any{"path": "."}}}},
		{text: "done"},
	}}
	project, _ := newTestProject(t, model, registry)
	project.PermissionMode = "skip"
	project.testToolApproval = func(context.Context, string) (bool, error) {
		t.Fatal("sandboxed run_tests unexpectedly requested approval")
		return false, nil
	}

	runAgent(project, "run tests")

	if tool.calls.Load() != 1 {
		t.Fatalf("sandboxed calls = %d", tool.calls.Load())
	}
}

func TestWorkspaceIsolationApprovalDetailsAreExplicit(t *testing.T) {
	for _, tc := range []struct {
		status security.WorkspaceIsolationStatus
		want   string
	}{
		{security.WorkspaceIsolationStatus{Enforced: true, Mode: "macos-sandbox", Detail: "workspace only"}, "enforced"},
		{security.WorkspaceIsolationStatus{Mode: "host", Detail: "not available"}, "HOST ACCESS"},
	} {
		details := toolApprovalDetails("bash", map[string]any{
			"command":              "go test ./...",
			"_workspace_isolation": tc.status,
		})
		text := approvalDetailsText(details)
		if !strings.Contains(text, "Isolation:") || !strings.Contains(text, tc.want) {
			t.Errorf("details = %q, want %q", text, tc.want)
		}
	}

	details := toolApprovalDetails("bash", map[string]any{
		"command":        "npm install",
		"network_access": true,
	})
	text := approvalDetailsText(details)
	if !strings.Contains(text, "FULL HOST NETWORK") || !strings.Contains(text, "LAN/private services") {
		t.Fatalf("network approval details = %q", text)
	}
}

func TestGetWorkspaceIsolationStatusMatchesRuntime(t *testing.T) {
	got := newStudioForTest(t).GetWorkspaceIsolationStatus()
	want := security.DetectWorkspaceIsolation()
	if got != want {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
}
