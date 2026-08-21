package studio

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

// The whole point of `delegate list`: give the model real project IDs. The tool
// it replaces offered a fixed role enum that matched no project, so every call
// fell through to "the first other project" in map order.
func TestDelegateListReturnsRealProjectIdentities(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	if err := s.SetProjectProfile(to.ID, "Infrastructure and deploys", []string{"Deploys", "containers"}); err != nil {
		t.Fatalf("SetProjectProfile: %v", err)
	}

	result := s.delegateList(from.ID, nil)
	if !result.Success {
		t.Fatalf("list failed: %+v", result)
	}
	if !strings.Contains(result.Content, to.ID) {
		t.Fatalf("list does not name the target project ID:\n%s", result.Content)
	}
	if strings.Contains(result.Content, from.ID) {
		t.Fatal("list must not offer the caller as a delegation target")
	}
	data, _ := result.Data.(map[string]any)
	targets, _ := data["targets"].([]DelegationTargetInfo)
	if len(targets) != 1 {
		t.Fatalf("targets = %+v, want exactly the one other project", targets)
	}
	if targets[0].Description != "Infrastructure and deploys" {
		t.Fatalf("description = %q", targets[0].Description)
	}
	// Capabilities are normalised for prompt embedding.
	if len(targets[0].Capabilities) != 2 || targets[0].Capabilities[0] != "deploys" {
		t.Fatalf("capabilities = %+v", targets[0].Capabilities)
	}
	if !targets[0].Reachable {
		t.Fatalf("target unexpectedly unreachable: %s", targets[0].Reason)
	}
}

// A target the caller may not reach is listed with the reason, not hidden, so
// the model does not keep retrying a call that is structurally refused.
func TestDelegateListMarksUnreachableTargets(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	deep := &delegationStamp{ChainID: "c", Depth: maxDelegationDepth, Chain: []string{from.ID}}

	data, _ := s.delegateList(from.ID, deep).Data.(map[string]any)
	targets, _ := data["targets"].([]DelegationTargetInfo)
	if len(targets) != 1 || targets[0].ProjectID != to.ID {
		t.Fatalf("targets = %+v", targets)
	}
	if targets[0].Reachable || !strings.Contains(targets[0].Reason, "hops deep") {
		t.Fatalf("target should be marked unreachable at max depth: %+v", targets[0])
	}
}

// A caller mistake is a tool error carrying the valid targets; a target-side
// refusal is a successful result carrying error_type. The tool this replaces
// flattened both into one prose string beginning "error: ".
func TestDelegateUnknownTargetIsCallerMistakeWithTargetList(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	result := s.delegateStart("run", from.ID, "default", nil, map[string]any{
		"project_id": "not-a-project", "task": "do work",
	})
	if result.Success {
		t.Fatal("an unknown target must be reported as a caller mistake")
	}
	data, _ := result.Data.(map[string]any)
	if data["error_type"] != DelegationErrorUnknownTarget {
		t.Fatalf("error_type = %v", data["error_type"])
	}
	targets, _ := data["available_targets"].([]DelegationTargetInfo)
	if len(targets) != 1 || targets[0].ProjectID != to.ID {
		t.Fatalf("available_targets = %+v; the model needs the valid IDs to recover", targets)
	}
}

func TestDelegateTargetRefusalIsSuccessWithErrorType(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	target := s.projects[to.ID]
	s.mu.RUnlock()
	target.mu.Lock()
	target.EnforceBudget, target.BudgetUSD = true, 0.01
	target.mu.Unlock()
	target.bumpTotalCostUSD(5)

	result := s.delegateStart("run", from.ID, "default", nil, map[string]any{
		"project_id": to.ID, "task": "expensive",
	})
	if !result.Success {
		t.Fatalf("a well-formed call whose TARGET refused should still succeed: %+v", result)
	}
	data, _ := result.Data.(map[string]any)
	if data["error_type"] != DelegationErrorBudget {
		t.Fatalf("error_type = %v, want budget", data["error_type"])
	}
	if hint, _ := data["hint"].(string); hint == "" {
		t.Fatal("a refusal must carry an actionable hint")
	}
}

func TestDelegateHandlerRefusesTurnWithNoAddressableCaller(t *testing.T) {
	s, _, _, _ := delegationTestStudio(t)
	handler := s.makeDelegateHandler()
	result, err := handler(context.Background(), "list", map[string]any{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if result.Success {
		t.Fatal("a turn with no routing must not be able to delegate")
	}
}

// Run IDs are capabilities carried in model output and logs. Knowing another
// chat's ID must not let an agent read its answer or cancel its work; the user
// can still administer every run through the separate Wails bindings.
func TestDelegateRunActionsRequireExactCallerOwnership(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	now := time.Now().UnixMilli()
	for _, run := range []DelegationRun{
		{
			ID: "owned-answer", Kind: "run", FromProjectID: from.ID, FromSessionID: "default",
			ToProjectID: to.ID, Task: "answer", Status: "completed", Answer: "private result",
			AnswerBytes: len("private result"), StartedAt: now, CompletedAt: now,
		},
		{
			ID: "owned-live", Kind: "run", FromProjectID: from.ID, FromSessionID: "default",
			ToProjectID: to.ID, Task: "work", Status: "running", StartedAt: now - 1,
		},
	} {
		if _, err := appendDelegationRun(run); err != nil {
			t.Fatalf("appendDelegationRun(%s): %v", run.ID, err)
		}
	}

	handler := s.makeDelegateHandler()
	owner := withAskUserRouting(context.Background(), from.ID, "default")
	for _, call := range []struct {
		action string
		args   map[string]any
	}{
		{action: "status", args: map[string]any{"run_id": "owned-answer"}},
		{action: "fetch", args: map[string]any{"run_id": "owned-answer"}},
	} {
		result, err := handler(owner, call.action, call.args)
		if err != nil || !result.Success || !strings.Contains(result.Content, "private result") {
			t.Fatalf("owner %s = %+v, err=%v", call.action, result, err)
		}
	}

	foreignCallers := []context.Context{
		withAskUserRouting(context.Background(), from.ID, "another-chat"),
		withAskUserRouting(context.Background(), to.ID, "default"),
	}
	for _, foreign := range foreignCallers {
		for _, call := range []struct {
			action string
			runID  string
		}{
			{action: "status", runID: "owned-answer"},
			{action: "fetch", runID: "owned-answer"},
			{action: "cancel", runID: "owned-live"},
		} {
			result, err := handler(foreign, call.action, map[string]any{"run_id": call.runID})
			if err != nil || result.Success {
				t.Fatalf("foreign %s unexpectedly succeeded: %+v, err=%v", call.action, result, err)
			}
			if !strings.Contains(result.Error, "delegation run not found") || strings.Contains(result.Error, "private result") || result.Data != nil {
				t.Fatalf("foreign %s leaked run details: error=%q data=%#v", call.action, result.Error, result.Data)
			}
			missing, missingErr := handler(foreign, call.action, map[string]any{"run_id": "missing-run"})
			if missingErr != nil || missing.Success || missing.Error != "delegation run not found: missing-run" {
				t.Fatalf("missing %s result = %+v, err=%v", call.action, missing, missingErr)
			}
		}
	}
	if run, _ := mustLoadDelegationRun(t, "owned-live"); run.Status != "running" {
		t.Fatalf("foreign cancel changed status to %q", run.Status)
	}

	result, err := handler(owner, "cancel", map[string]any{"run_id": "owned-live"})
	if err != nil || !result.Success {
		t.Fatalf("owner cancel = %+v, err=%v", result, err)
	}
	if run, _ := mustLoadDelegationRun(t, "owned-live"); run.Status != "stopped" {
		t.Fatalf("owner cancel left status %q", run.Status)
	}
}

func TestDelegateDeclarationAndValidation(t *testing.T) {
	tool := tools.NewDelegateTool()
	if tool.Name() != "delegate" || tool.Declaration() == nil {
		t.Fatal("delegate declaration unavailable")
	}
	for _, args := range []map[string]any{
		{"action": "ask"},                    // missing project_id
		{"action": "run", "project_id": "p"}, // missing task
		{"action": "status"},                 // missing run_id
		{"action": "nonsense"},               // unknown action
		{},                                   // missing action
	} {
		if err := tool.Validate(args); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}
	for _, args := range []map[string]any{
		{"action": "list"},
		{"action": "run", "project_id": "p", "task": "t"},
		{"action": "fetch", "run_id": "r"},
	} {
		if err := tool.Validate(args); err != nil {
			t.Fatalf("valid args rejected: %#v: %v", args, err)
		}
	}
}

// Starting work elsewhere is reviewed exactly, every time, and can never be
// remembered. Reading a run back is inert.
func TestDelegatePermissionClassificationByAction(t *testing.T) {
	for _, action := range []string{"ask", "run", "batch"} {
		args := map[string]any{"action": action}
		if !tools.RequiresUserApproval("delegate", args) {
			t.Fatalf("%s does not require approval", action)
		}
		if !hardGatedTool("delegate", args) {
			t.Fatalf("%s is not hard-gated", action)
		}
		for _, mode := range []string{"manual", "accept_edits", "auto", "skip"} {
			if got := permissionForTool(mode, "delegate", args); got != permissionAskAction {
				t.Fatalf("%s in %s mode = %v, want exact-action review", action, mode, got)
			}
		}
	}
	for _, action := range []string{"list", "status", "fetch", "cancel"} {
		args := map[string]any{"action": action}
		if tools.RequiresUserApproval("delegate", args) {
			t.Fatalf("%s should be promptless", action)
		}
		if hardGatedTool("delegate", args) {
			t.Fatalf("%s should not be hard-gated", action)
		}
	}
	// Plan mode strips the tool entirely, like session_agent.
	if got := permissionForTool("plan", "delegate", map[string]any{"action": "list"}); got != permissionDeny {
		t.Fatalf("delegate in plan mode = %v, want deny", got)
	}
	if !tools.IsWriteTool("delegate") {
		t.Fatal("delegate must serialize; it must never join a parallel read group")
	}
}

// The legacy tools delegate replaces are gone: each one cost schema tokens on
// every request while teaching the model that a capability existed which did
// not. coordinate remains a valid generic-engine capability; Studio removes it
// separately in newStudioToolRegistry because its agents use session runs.
func TestRetiredDelegationToolsAreNotRegistered(t *testing.T) {
	registry := tools.DefaultRegistry(t.TempDir())
	for _, name := range []string{"ask_agent", "update_scratchpad"} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("%q is still registered", name)
		}
	}
	if _, ok := registry.Get("delegate"); !ok {
		t.Fatal("delegate is not registered")
	}
	if _, ok := registry.Get("coordinate"); !ok {
		t.Fatal("generic engine coordinate tool is not registered")
	}
}

func TestSetProjectProfileSanitizesAndPersists(t *testing.T) {
	s, _, to, _ := delegationTestStudio(t)
	long := strings.Repeat("д", 400) // multibyte: truncation must stay rune-safe
	err := s.SetProjectProfile(to.ID, "Line one\nline two\ttabbed", []string{
		"  Deploys  ", "DEPLOYS", "", long, "a", "b", "c", "d", "e", "f", "g", "h",
	})
	if err != nil {
		t.Fatalf("SetProjectProfile: %v", err)
	}
	s.mu.RLock()
	project := s.projects[to.ID]
	s.mu.RUnlock()
	project.mu.RLock()
	description, capabilities := project.Description, project.Capabilities
	project.mu.RUnlock()

	if strings.ContainsAny(description, "\n\t") {
		t.Fatalf("description keeps structure characters: %q", description)
	}
	if description != "Line one line two tabbed" {
		t.Fatalf("description = %q", description)
	}
	if len(capabilities) > projectMaxCapabilities {
		t.Fatalf("capabilities = %d, cap is %d", len(capabilities), projectMaxCapabilities)
	}
	if capabilities[0] != "deploys" {
		t.Fatalf("capabilities not normalised: %+v", capabilities)
	}
	for _, capability := range capabilities {
		if len(capability) > projectCapabilityMaxBytes {
			t.Fatalf("capability %q exceeds the byte cap", capability)
		}
		if !utf8ValidString(capability) {
			t.Fatalf("capability %q is not valid UTF-8 after truncation", capability)
		}
	}
	// Survives a config round-trip.
	if cfg := project.ToConfig(); cfg.Description != description || len(cfg.Capabilities) != len(capabilities) {
		t.Fatalf("profile lost in ToConfig: %+v", cfg)
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
