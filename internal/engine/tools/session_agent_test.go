package tools

import (
	"context"
	"strings"
	"testing"
)

func TestSessionAgentToolContract(t *testing.T) {
	tool := NewSessionAgentTool()
	if tool.Name() != "session_agent" || tool.Declaration() == nil {
		t.Fatal("session_agent declaration is unavailable")
	}
	if err := tool.Validate(map[string]any{"action": "list"}); err != nil {
		t.Fatalf("list validation: %v", err)
	}
	for _, args := range []map[string]any{
		{"action": "read", "project_id": "p", "session_id": "s"},
		{"action": "send", "project_id": "p", "session_id": "s", "message": "schema changed"},
		{"action": "rename", "project_id": "p", "session_id": "s", "name": "Payments"},
		{"action": "archive", "project_id": "p", "session_id": "s"},
		{"action": "suggest", "name": "Fix flaky test", "message": "Investigate and fix the flaky checkout test."},
	} {
		if err := tool.Validate(args); err != nil {
			t.Fatalf("valid args rejected: %#v: %v", args, err)
		}
	}
	for _, args := range []map[string]any{
		{},
		{"action": "delete", "project_id": "p", "session_id": "s"},
		{"action": "suggest", "name": "Missing prompt"},
		{"action": "read", "project_id": "p"},
		{"action": "send", "project_id": "p", "session_id": "s", "message": "\x00"},
		{"action": "rename", "project_id": "p", "session_id": "s", "name": strings.Repeat("n", SessionAgentNameMaxBytes+1)},
	} {
		if err := tool.Validate(args); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}
}

func TestSessionAgentToolHandlerAndSafetyClassification(t *testing.T) {
	tool := NewSessionAgentTool()
	called := false
	tool.SetHandler(func(_ context.Context, action string, args map[string]any) (ToolResult, error) {
		called = action == "list" && args["action"] == " LIST "
		return NewSuccessResult("ok"), nil
	})
	result, err := tool.Execute(context.Background(), map[string]any{"action": " LIST "})
	if err != nil || !result.Success || !called {
		t.Fatalf("Execute() = %+v, %v, called=%v", result, err, called)
	}
	if !IsWriteTool("session_agent") {
		t.Fatal("session_agent must serialize around other state-changing calls")
	}
	// "send" starts a full tool-enabled turn in ANOTHER chat, under that
	// chat's permission mode. That is strictly more capable than ask_agent,
	// which is a one-shot query with no tool loop and is itself gated — so a
	// prompt-injected agent must not be able to drive a sibling project
	// without the user seeing an exact-action review.
	if !RequiresUserApproval("session_agent", map[string]any{"action": "send"}) {
		t.Fatal("cross-session send starts another agent loop and must be reviewed")
	}
	if !RequiresUserApproval("session_agent", map[string]any{"action": "rename"}) {
		t.Fatal("renaming another session mutates the user's catalog and must be reviewed")
	}
	if !RequiresUserApproval("session_agent", map[string]any{"action": "archive"}) {
		t.Fatal("archiving another session must require explicit approval")
	}
	for _, readOnly := range []string{"list", "read", "suggest"} {
		if RequiresUserApproval("session_agent", map[string]any{"action": readOnly}) {
			t.Fatalf("%s neither mutates nor executes and should stay promptless", readOnly)
		}
	}
	meta, ok := NewDefaultSafetyValidator().GetMetadata("session_agent")
	if !ok || meta.Category != "coordination" || meta.SafetyLevel != SafetyLevelCaution {
		t.Fatalf("session_agent metadata = %+v, %v", meta, ok)
	}
}

func TestSearchSessionTranscriptsToolContractAndReadOnlyClassification(t *testing.T) {
	tool := NewSearchSessionTranscriptsTool()
	if tool.Name() != "search_session_transcripts" || tool.Declaration() == nil {
		t.Fatal("search_session_transcripts declaration is unavailable")
	}
	for _, args := range []map[string]any{
		{"query": "invoice schema"},
		{"query": "кот", "project_id": "p", "include_archived": true},
	} {
		if err := tool.Validate(args); err != nil {
			t.Fatalf("valid args rejected: %#v: %v", args, err)
		}
	}
	for _, args := range []map[string]any{
		{},
		{"query": "   "},
		{"query": "bad\x00query"},
		{"query": strings.Repeat("q", SessionTranscriptSearchQueryMaxBytes+1)},
		{"query": "valid", "project_id": strings.Repeat("p", SessionAgentIDMaxBytes+1)},
	} {
		if err := tool.Validate(args); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}

	called := false
	tool.SetHandler(func(_ context.Context, action string, args map[string]any) (ToolResult, error) {
		called = action == "search" && args["query"] == "needle"
		return NewSuccessResult("found"), nil
	})
	result, err := tool.Execute(context.Background(), map[string]any{"query": "needle"})
	if err != nil || !result.Success || !called {
		t.Fatalf("Execute() = %+v, %v, called=%v", result, err, called)
	}
	if IsWriteTool(tool.Name()) || RequiresUserApproval(tool.Name(), map[string]any{"query": "needle"}) {
		t.Fatal("cross-session transcript search must remain read-only and approval-free")
	}
	if !IsReadOnlyForPlanMode(tool.Name()) {
		t.Fatal("cross-session transcript search must remain available in Plan mode")
	}
	meta, ok := NewDefaultSafetyValidator().GetMetadata(tool.Name())
	if !ok || meta.SafetyLevel != SafetyLevelSafe || meta.RequiresConfirmation {
		t.Fatalf("search metadata = %+v, %v", meta, ok)
	}
}
