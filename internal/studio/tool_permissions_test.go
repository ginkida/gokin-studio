package studio

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func TestProjectToolPermissionPersistsListsAndRevokes(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Persistent permissions")

	if err := s.grantProjectToolPermission(info.ID, "write", map[string]any{"file_path": "notes.md"}); err != nil {
		t.Fatal(err)
	}
	permissions, err := s.ListProjectToolPermissions(info.ID)
	if err != nil || len(permissions) != 1 {
		t.Fatalf("permissions = %#v, err = %v", permissions, err)
	}
	if permissions[0].Tool != "write" || permissions[0].Scope != "This project" || permissions[0].CreatedAt <= 0 {
		t.Fatalf("permission info = %#v", permissions[0])
	}

	cfg := s.projects[info.ID].ToConfig()
	restarted := NewProject(cfg)
	if !restarted.hasPersistentToolPermission("write", map[string]any{"file_path": "other.md"}) {
		t.Fatal("permission did not survive a ProjectConfig round trip")
	}
	if err := s.RevokeProjectToolPermission(info.ID, "write"); err != nil {
		t.Fatal(err)
	}
	permissions, err = s.ListProjectToolPermissions(info.ID)
	if err != nil || len(permissions) != 0 || len(s.projects[info.ID].ToConfig().ToolPermissions) != 0 {
		t.Fatalf("revoked permissions = %#v, err = %v", permissions, err)
	}
}

func TestAlwaysAllowApprovalAnswerPersistsAtomically(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Approval answer")
	p := s.projects[info.ID]
	event, allowOption, persistOption := ordinaryToolApprovalEvent("edit", map[string]any{"file_path": "notes.md"})
	if event.Scope != "current_turn_or_project_tool" || len(event.Options) != 3 || persistOption == "" {
		t.Fatalf("approval event = %#v, persist option = %q", event, persistOption)
	}
	allowed, persisted, err := p.resolveToolApprovalAnswer(
		persistOption, allowOption, persistOption, "edit", map[string]any{"file_path": "notes.md"},
	)
	if err != nil || !allowed || !persisted || !p.hasPersistentToolPermission("edit", map[string]any{"file_path": "other.md"}) {
		t.Fatalf("answer result allowed=%v persisted=%v err=%v rules=%#v", allowed, persisted, err, p.ToConfig().ToolPermissions)
	}

	// A failed atomic config save must not publish the grant or allow the call.
	if err := os.Remove(configPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(configPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	event, allowOption, persistOption = ordinaryToolApprovalEvent("write", map[string]any{"file_path": "new.md"})
	allowed, persisted, err = p.resolveToolApprovalAnswer(
		persistOption, allowOption, persistOption, "write", map[string]any{"file_path": "new.md"},
	)
	if err == nil || allowed || persisted || p.hasPersistentToolPermission("write", map[string]any{"file_path": "new.md"}) {
		t.Fatalf("failed save result allowed=%v persisted=%v err=%v rules=%#v", allowed, persisted, err, p.ToConfig().ToolPermissions)
	}
}

func TestProjectToolPermissionEligibilityFailsClosed(t *testing.T) {
	allowed := []struct {
		name string
		args map[string]any
	}{
		{"write", map[string]any{"file_path": "a.txt"}},
		{"document_create", map[string]any{"file_path": "report.docx"}},
		{"git_branch", map[string]any{"action": "switch"}},
	}
	for _, test := range allowed {
		if !persistentToolPermissionEligible(test.name, test.args) {
			t.Errorf("ordinary %s was not eligible", test.name)
		}
	}
	denied := []struct {
		name string
		args map[string]any
	}{
		{"bash", map[string]any{"command": "go test ./..."}},
		{"delete", map[string]any{"path": "a.txt"}},
		{"ssh", map[string]any{}},
		{"mcp_calendar_create", map[string]any{}},
		{"computer_action", map[string]any{"action": "click"}},
		{"external_browser", map[string]any{"action": "click"}},
		{"document_create", map[string]any{"file_path": "report.docx", "replace": true}},
		{"git_branch", map[string]any{"action": "delete"}},
		{"future_unknown_tool", map[string]any{}},
	}
	for _, test := range denied {
		if persistentToolPermissionEligible(test.name, test.args) {
			t.Errorf("hard/unknown %s unexpectedly became eligible", test.name)
		}
	}
}

func TestPersistentToolPermissionBypassesOnlyOrdinaryManualGate(t *testing.T) {
	writeTool := &countedNamedTool{name: "write"}
	reg := tools.NewRegistry()
	reg.MustRegister(writeTool)
	mc := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: "write", Args: map[string]any{"file_path": "notes.md"}}}},
		{text: "done"},
	}}
	p, _ := newTestProject(t, mc, reg)
	p.PermissionMode = "manual"
	p.ToolPermissions = []ToolPermissionRule{{Tool: "write", CreatedAt: 1}}
	var approvals atomic.Int32
	p.testToolApproval = func(context.Context, string) (bool, error) {
		approvals.Add(1)
		return false, nil
	}
	runAgent(p, "write the note")
	if approvals.Load() != 0 || writeTool.calls.Load() != 1 {
		t.Fatalf("ordinary persistent gate approvals=%d executions=%d", approvals.Load(), writeTool.calls.Load())
	}

	documentTool := &countedNamedTool{name: "document_create"}
	reg2 := tools.NewRegistry()
	reg2.MustRegister(documentTool)
	mc2 := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{{Name: "document_create", Args: map[string]any{"file_path": "report.docx", "replace": true}}}},
		{text: "done"},
	}}
	p2, _ := newTestProject(t, mc2, reg2)
	p2.PermissionMode = "manual"
	p2.ToolPermissions = []ToolPermissionRule{{Tool: "document_create", CreatedAt: 1}}
	p2.testToolApproval = func(context.Context, string) (bool, error) {
		approvals.Add(1)
		return false, nil
	}
	runAgent(p2, "replace the report")
	if approvals.Load() != 1 || documentTool.calls.Load() != 0 {
		t.Fatalf("hard variant approvals=%d executions=%d", approvals.Load(), documentTool.calls.Load())
	}
}

func TestSanitizeToolPermissionRulesDropsUnknownDuplicatesAndFutureDates(t *testing.T) {
	rules := sanitizeToolPermissionRules([]ToolPermissionRule{
		{Tool: " WRITE ", CreatedAt: 10},
		{Tool: "write", CreatedAt: 20},
		{Tool: "bash", CreatedAt: 30},
		{Tool: "edit", CreatedAt: 1 << 62},
	})
	if len(rules) != 2 || rules[0].Tool != "write" || rules[0].CreatedAt != 10 || rules[1].Tool != "edit" || rules[1].CreatedAt != 0 {
		t.Fatalf("sanitized rules = %#v", rules)
	}
}
