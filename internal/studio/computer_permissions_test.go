package studio

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func TestComputerAppPermissionPersistenceAndBlockPriority(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "P")

	if err := s.SetProjectComputerAppPermission(info.ID, ` C:\Apps\Editor.EXE `, "allow"); err != nil {
		t.Fatal(err)
	}
	perms, err := s.ListProjectComputerPermissions(info.ID)
	if err != nil || len(perms.Allowed) != 1 || perms.Allowed[0] != "c:/apps/editor.exe" {
		t.Fatalf("allowed permissions = %#v, %v", perms, err)
	}
	if err := s.SetProjectComputerAppPermission(info.ID, "c:/apps/editor.exe", "block"); err != nil {
		t.Fatal(err)
	}
	perms, _ = s.ListProjectComputerPermissions(info.ID)
	if len(perms.Allowed) != 0 || len(perms.Blocked) != 1 {
		t.Fatalf("block did not replace allow: %#v", perms)
	}
	cfg := s.projects[info.ID].ToConfig()
	if len(cfg.ComputerAllowedApps) != 0 || len(cfg.ComputerBlockedApps) != 1 {
		t.Fatalf("permission config = %#v", cfg)
	}
	if err := s.SetProjectComputerAppPermission(info.ID, "com.1password.1password", "allow"); err == nil {
		t.Fatal("allowed a built-in sensitive application")
	}
}

func TestComputerUseDirectivePrefersPreciseToolsAndPreservesProviderVision(t *testing.T) {
	for _, tc := range []struct {
		provider string
		want     string
	}{
		{provider: "glm", want: "enabled Z.AI Vision MCP tool"},
		{provider: "kimi", want: "receives screenshots directly as image tool results"},
	} {
		directive := computerUseDirective(true, tc.provider)
		for _, required := range []string{
			"enabled MCP connector or dedicated tool",
			"then bounded web/file tools",
			"only then screen interaction",
			"Always call computer_screenshot before computer_action",
			tc.want,
		} {
			if !strings.Contains(directive, required) {
				t.Errorf("%s directive missing %q:\n%s", tc.provider, required, directive)
			}
		}
	}
	if disabled := computerUseDirective(false, "kimi"); disabled != "" {
		t.Fatalf("disabled computer use emitted directive: %q", disabled)
	}
}

func TestComputerApprovalIsScopedPerObservedApplication(t *testing.T) {
	screen := &countedNamedTool{name: "computer_screenshot"}
	reg := tools.NewRegistry()
	if err := reg.Register(screen); err != nil {
		t.Fatal(err)
	}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: screen.Name()}, {Name: screen.Name()}}},
		{text: "done"},
	}}
	p, _ := newTestProject(t, mc, reg)
	p.PermissionMode = "auto"
	apps := []tools.ComputerApplication{
		{ID: "com.example.editor", Name: "Editor"},
		{ID: "com.example.editor", Name: "Editor"},
		{ID: "com.example.browser", Name: "Browser"},
		{ID: "com.example.browser", Name: "Browser"},
	}
	var observed atomic.Int32
	p.testForegroundApplication = func(context.Context) (tools.ComputerApplication, error) {
		index := int(observed.Add(1)) - 1
		if index >= len(apps) {
			return tools.ComputerApplication{}, fmt.Errorf("unexpected observation %d", index)
		}
		return apps[index], nil
	}
	var approvals atomic.Int32
	p.testToolApproval = func(context.Context, string) (bool, error) {
		approvals.Add(1)
		return true, nil
	}
	runAgent(p, "inspect editor and browser")
	if approvals.Load() != 2 || screen.calls.Load() != 2 {
		t.Fatalf("approvals=%d captures=%d, want one approval per app and both captures", approvals.Load(), screen.calls.Load())
	}
}

func TestComputerActionRevalidatesForegroundApplicationAfterApproval(t *testing.T) {
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
	apps := []tools.ComputerApplication{
		{ID: "com.example.editor", Name: "Editor"},
		{ID: "com.example.browser", Name: "Browser"},
	}
	var observed atomic.Int32
	p.testForegroundApplication = func(context.Context) (tools.ComputerApplication, error) {
		return apps[int(observed.Add(1))-1], nil
	}
	p.testToolApproval = func(context.Context, string) (bool, error) { return true, nil }
	runAgent(p, "inspect editor")

	if screen.calls.Load() != 0 {
		t.Fatal("computer tool ran after foreground app changed")
	}
	results := mc.lastFuncRespResults
	if len(results) != 1 || !strings.Contains(fmt.Sprint(results[0].Response["result"]), "foreground application changed") {
		t.Fatalf("provider result does not explain revalidation failure: %#v", results)
	}
}

func TestSensitiveComputerAppBypassesApprovalAndExecution(t *testing.T) {
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
	p.testForegroundApplication = func(context.Context) (tools.ComputerApplication, error) {
		return tools.ComputerApplication{ID: "com.1password.1password", Name: "1Password"}, nil
	}
	p.testToolApproval = func(context.Context, string) (bool, error) {
		t.Fatal("sensitive app reached user approval instead of fail-closed block")
		return true, nil
	}
	runAgent(p, "inspect passwords")
	if screen.calls.Load() != 0 {
		t.Fatal("computer tool executed against sensitive app")
	}
}

func TestComputerActionRequiresSingleActionReviewEvenForAllowedApp(t *testing.T) {
	action := &countedNamedTool{name: "computer_action"}
	reg := tools.NewRegistry()
	if err := reg.Register(action); err != nil {
		t.Fatal(err)
	}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{
			Name: action.Name(), Args: map[string]any{"action": "click", "x": 10.0, "y": 20.0},
		}}},
		{text: "done"},
	}}
	p, _ := newTestProject(t, mc, reg)
	app := tools.ComputerApplication{ID: "com.example.editor", Name: "Editor"}
	p.ComputerAllowedApps = []string{app.ID}
	p.testForegroundApplication = func(context.Context) (tools.ComputerApplication, error) { return app, nil }
	var reviews atomic.Int32
	p.testToolApproval = func(_ context.Context, toolName string) (bool, error) {
		if toolName != "computer_action" {
			t.Fatalf("unexpected approval for %q", toolName)
		}
		reviews.Add(1)
		return true, nil
	}
	runAgent(p, "click the button")
	if reviews.Load() != 1 || action.calls.Load() != 1 {
		t.Fatalf("reviews=%d actions=%d, want one exact-action review", reviews.Load(), action.calls.Load())
	}
}

func TestComputerActionDenialNeverExecutes(t *testing.T) {
	action := &countedNamedTool{name: "computer_action"}
	reg := tools.NewRegistry()
	if err := reg.Register(action); err != nil {
		t.Fatal(err)
	}
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{
			Name: action.Name(), Args: map[string]any{"action": "type", "text": "do not type this"},
		}}},
		{text: "done"},
	}}
	p, _ := newTestProject(t, mc, reg)
	app := tools.ComputerApplication{ID: "com.example.editor", Name: "Editor"}
	p.ComputerAllowedApps = []string{app.ID}
	p.testForegroundApplication = func(context.Context) (tools.ComputerApplication, error) { return app, nil }
	p.testToolApproval = func(context.Context, string) (bool, error) { return false, nil }
	runAgent(p, "type text")
	if action.calls.Load() != 0 {
		t.Fatal("denied computer action executed")
	}
}

func TestComputerActionApprovalDetailsExposeExactAction(t *testing.T) {
	app := tools.ComputerApplication{ID: "com.example.editor", Name: "Editor"}
	for _, tc := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"action": "click", "x": 12, "y": 34, "button": "double"}, []string{"Action: click", "Coordinates: (12, 34)", "Button: double"}},
		{map[string]any{"action": "type", "text": "publish draft"}, []string{"Action: type", "Text: publish draft"}},
		{map[string]any{"action": "key", "keys": "CTRL+S"}, []string{"Action: key", "Keys: CTRL+S"}},
	} {
		text := approvalDetailsText(computerActionApprovalDetails(tc.args, app))
		for _, want := range tc.want {
			if !strings.Contains(text, want) {
				t.Errorf("details %q missing %q", text, want)
			}
		}
	}
}
