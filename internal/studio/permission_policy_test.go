package studio

import "testing"

func TestNormalizePermissionModeKeepsLegacyProjectsSafe(t *testing.T) {
	for input, want := range map[string]string{
		"": "auto", "auto": "auto", "ask": "manual",
		"manual": "manual", "acceptEdits": "accept_edits", "accept_edits": "accept_edits",
		"accept-edits": "accept_edits", "skip": "skip", "plan": "plan", "garbage": "auto",
	} {
		if got := normalizePermissionMode(input); got != want {
			t.Errorf("normalizePermissionMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPermissionPolicyManualAutoSkip(t *testing.T) {
	tests := []struct {
		name string
		mode string
		tool string
		args map[string]any
		want permissionDecision
	}{
		{"manual read", "manual", "read", nil, permissionAllow},
		{"manual write", "manual", "write", nil, permissionAskTurn},
		{"accept edits write", "accept_edits", "write", nil, permissionAllow},
		{"accept edits document", "acceptEdits", "document_create", map[string]any{"file_path": "report.docx"}, permissionAllow},
		{"accept edits mkdir", "accept_edits", "mkdir", nil, permissionAllow},
		{"accept edits shell still asks", "accept_edits", "bash", map[string]any{"command": "go test ./..."}, permissionAskTurn},
		{"accept edits git add still asks", "accept_edits", "git_add", nil, permissionAskTurn},
		{"accept edits git commit still asks", "accept_edits", "git_commit", nil, permissionAskTurn},
		{"accept edits document replace exact", "accept_edits", "document_create", map[string]any{"replace": true}, permissionAskAction},
		{"accept edits delete exact", "accept_edits", "delete", nil, permissionAskAction},
		{"accept edits MCP exact", "accept_edits", "mcp_calendar_create", nil, permissionAskAction},
		{"auto root-anchored edit", "auto", "edit", nil, permissionAllow},
		{"auto creates new document", "auto", "document_create", map[string]any{"file_path": "report.docx"}, permissionAllow},
		{"manual document asks for turn", "manual", "document_create", map[string]any{"file_path": "report.docx"}, permissionAskTurn},
		{"auto replace document exact", "auto", "document_create", map[string]any{"file_path": "report.docx", "replace": true}, permissionAskAction},
		{"skip replace document exact", "skip", "document_create", map[string]any{"file_path": "report.docx", "replace": true}, permissionAskAction},
		{"auto arbitrary shell", "auto", "bash", map[string]any{"command": "go test ./..."}, permissionAskTurn},
		{"skip arbitrary shell", "skip", "bash", nil, permissionAllow},
		{"skip destructive shell exact", "skip", "bash", map[string]any{"command": "rm -rf build"}, permissionAskAction},
		{"skip delete is still exact", "skip", "delete", nil, permissionAskAction},
		{"auto MCP is exact", "auto", "mcp_calendar_create", nil, permissionAskAction},
		{"skip MCP is exact", "skip", "mcp_calendar_create", nil, permissionAskAction},
		{"auto PR create is exact", "auto", "git_pr", map[string]any{"action": "create"}, permissionAskAction},
		{"skip bash network is exact", "skip", "bash", map[string]any{"command": "npm install", "network_access": true}, permissionAskAction},
		{"skip tests network is exact", "skip", "run_tests", map[string]any{"network_access": true}, permissionAskAction},
		{"skip verify network is exact", "skip", "verify_code", map[string]any{"network_access": true}, permissionAskAction},
		{"auto branch create", "auto", "git_branch", map[string]any{"action": "create"}, permissionAllow},
		{"skip branch delete exact", "skip", "git_branch", map[string]any{"action": "delete"}, permissionAskAction},
		{"manual external browser list is bounded", "manual", "external_browser", map[string]any{"action": "list"}, permissionAllow},
		{"auto external browser inspect exact", "auto", "external_browser", map[string]any{"action": "inspect"}, permissionAskAction},
		{"skip external browser click still exact", "skip", "external_browser", map[string]any{"action": "click"}, permissionAskAction},
		{"manual scheduled list is read only", "manual", "scheduled_task", map[string]any{"action": "list"}, permissionAllow},
		{"auto scheduled list is read only", "auto", "scheduled_task", map[string]any{"action": "list"}, permissionAllow},
		{"skip scheduled list is read only", "skip", "scheduled_task", map[string]any{"action": "list"}, permissionAllow},
		{"manual scheduled create exact", "manual", "scheduled_task", map[string]any{"action": "create"}, permissionAskAction},
		{"auto scheduled update exact", "auto", "scheduled_task", map[string]any{"action": "update"}, permissionAskAction},
		{"skip scheduled run now exact", "skip", "scheduled_task", map[string]any{"action": "run_now"}, permissionAskAction},
		{"skip scheduled delete exact", "skip", "scheduled_task", map[string]any{"action": "delete"}, permissionAskAction},
		{"manual session list is bounded", "manual", "session_agent", map[string]any{"action": "list"}, permissionAllow},
		{"auto session send is reviewed", "auto", "session_agent", map[string]any{"action": "send"}, permissionAskAction},
		{"auto session list stays promptless", "auto", "session_agent", map[string]any{"action": "list"}, permissionAllow},
		{"manual session archive exact", "manual", "session_agent", map[string]any{"action": "archive"}, permissionAskAction},
		{"auto session archive exact", "auto", "session_agent", map[string]any{"action": "archive"}, permissionAskAction},
		{"skip session archive exact", "skip", "session_agent", map[string]any{"action": "archive"}, permissionAskAction},
		{"plan read", "plan", "read", nil, permissionAllow},
		{"plan plugin resource", "plan", "plugin_resource", nil, permissionAllow},
		{"plan write denied without prompt", "plan", "write", nil, permissionDeny},
		{"plan shell denied without prompt", "plan", "bash", map[string]any{"command": "git status"}, permissionDeny},
		{"plan MCP denied without prompt", "plan", "mcp_calendar_list", nil, permissionDeny},
		{"plan screen denied without prompt", "plan", "computer_screenshot", nil, permissionDeny},
		{"plan external browser mixed schema denied", "plan", "external_browser", map[string]any{"action": "list"}, permissionDeny},
		{"plan mutating multi-action memory denied", "plan", "memory", map[string]any{"action": "list"}, permissionDeny},
		{"plan git branch schema denied", "plan", "git_branch", map[string]any{"action": "list"}, permissionDeny},
		{"plan cannot self-enter lifecycle", "plan", "enter_plan_mode", nil, permissionDeny},
		{"plan cannot self-update lifecycle", "plan", "update_plan_progress", nil, permissionDeny},
		{"plan cannot self-exit lifecycle", "plan", "exit_plan_mode", nil, permissionDeny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := permissionForTool(tt.mode, tt.tool, tt.args); got != tt.want {
				t.Fatalf("decision = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlanModeDisablesPluginHooks(t *testing.T) {
	handlers := []pluginHookHandler{{Plugin: "mutating-hook", Event: "PreToolUse", Command: "touch"}}
	if got := permissionHookHandlers("plan", handlers); len(got) != 0 {
		t.Fatalf("Plan mode retained %d executable plugin hook(s)", len(got))
	}
	if got := permissionHookHandlers("manual", handlers); len(got) != 1 {
		t.Fatalf("Manual mode unexpectedly removed plugin hooks: %#v", got)
	}
}
