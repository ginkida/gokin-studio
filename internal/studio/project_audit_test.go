package studio

import (
	"strings"
	"testing"
)

// findProjectAuditLog returns the first event-log entry with source="project"
// whose message contains the substring. Helper for assertion brevity.
func findProjectAuditLog(s *Studio, want string) *EventLogEntry {
	for _, l := range s.GetRecentLogs() {
		if l.Source == "project" && strings.Contains(l.Message, want) {
			return &l
		}
	}
	return nil
}

func TestAuditProjectProvider_Logged(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Change provider — should log.
	if err := s.SetProjectProvider(p.ID, "kimi", "kimi-for-coding"); err != nil {
		t.Fatal(err)
	}
	hit := findProjectAuditLog(s, "provider")
	if hit == nil {
		t.Errorf("expected project audit log for provider change; logs=%+v", s.GetRecentLogs())
	}
	if hit != nil && (!strings.Contains(hit.Message, "kimi") || !strings.Contains(hit.Message, "glm")) {
		t.Errorf("audit message should mention old + new provider; got %q", hit.Message)
	}
}

func TestAuditProjectProvider_NoOpDoesNotLog(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Clear logs so we only see entries from the next call.
	s.ClearLogs()
	// Setting the same provider+model — no audit log.
	if err := s.SetProjectProvider(p.ID, p.Provider, p.Model); err != nil {
		t.Fatal(err)
	}
	for _, l := range s.GetRecentLogs() {
		if l.Source == "project" && strings.Contains(l.Message, "provider") {
			t.Errorf("no-op SetProjectProvider should not log: %+v", l)
		}
	}
}

func TestAuditProjectBudget_Logged(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectBudget(p.ID, 25.50); err != nil {
		t.Fatal(err)
	}
	hit := findProjectAuditLog(s, "budget")
	if hit == nil {
		t.Errorf("expected budget audit log; logs=%+v", s.GetRecentLogs())
	}
	if hit != nil && !strings.Contains(hit.Message, "25.50") {
		t.Errorf("audit message should mention new budget; got %q", hit.Message)
	}
}

func TestAuditProjectThinking_Logged(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectThinking(p.ID, "enabled", 8192); err != nil {
		t.Fatal(err)
	}
	hit := findProjectAuditLog(s, "thinking mode")
	if hit == nil {
		t.Errorf("expected thinking mode audit; logs=%+v", s.GetRecentLogs())
	}
	hit = findProjectAuditLog(s, "thinking budget")
	if hit == nil {
		t.Errorf("expected thinking budget audit; logs=%+v", s.GetRecentLogs())
	}
}

func TestAuditProjectModelParams_Logged(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectModelParams(p.ID, 0.5, 8192); err != nil {
		t.Fatal(err)
	}
	if findProjectAuditLog(s, "temperature") == nil {
		t.Errorf("expected temperature audit; logs=%+v", s.GetRecentLogs())
	}
	if findProjectAuditLog(s, "max tokens") == nil {
		t.Errorf("expected max tokens audit; logs=%+v", s.GetRecentLogs())
	}
}

func TestAuditProjectSystemPrompt_NeverLogsContent(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const secret = "do-not-log-internal-api-docs-please-12345"
	if err := s.SetProjectSystemPrompt(p.ID, secret); err != nil {
		t.Fatal(err)
	}
	// Audit log exists.
	hit := findProjectAuditLog(s, "system prompt")
	if hit == nil {
		t.Errorf("expected system prompt audit; logs=%+v", s.GetRecentLogs())
	}
	// SECURITY: secret value MUST NOT appear in any log entry.
	for _, l := range s.GetRecentLogs() {
		if strings.Contains(l.Message, secret) {
			t.Errorf("SYSTEM PROMPT LEAK in event log: %q — message=%q", secret, l.Message)
		}
	}
	// Update to a new prompt — should log "updated" + char counts.
	const updated = "different-but-still-not-loggable"
	if err := s.SetProjectSystemPrompt(p.ID, updated); err != nil {
		t.Fatal(err)
	}
	for _, l := range s.GetRecentLogs() {
		if strings.Contains(l.Message, updated) || strings.Contains(l.Message, secret) {
			t.Errorf("prompt content leaked: msg=%q", l.Message)
		}
	}
	// Clear — should log "cleared".
	if err := s.SetProjectSystemPrompt(p.ID, ""); err != nil {
		t.Fatal(err)
	}
	hit = findProjectAuditLog(s, "cleared")
	if hit == nil {
		t.Errorf("expected 'cleared' audit when prompt removed; logs=%+v", s.GetRecentLogs())
	}
}

func TestAuditProjectPinned_TogglesLogged(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectPinned(p.ID, true); err != nil {
		t.Fatal(err)
	}
	if findProjectAuditLog(s, "pinned to top") == nil {
		t.Errorf("expected 'pinned to top' audit; logs=%+v", s.GetRecentLogs())
	}
	s.ClearLogs()
	if err := s.SetProjectPinned(p.ID, false); err != nil {
		t.Fatal(err)
	}
	if findProjectAuditLog(s, "unpinned") == nil {
		t.Errorf("expected 'unpinned' audit; logs=%+v", s.GetRecentLogs())
	}
}

func TestAuditProjectAdded_Logged(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	dir := t.TempDir()
	if _, err := s.AddProject("MyProject", dir); err != nil {
		t.Fatal(err)
	}
	hit := findProjectAuditLog(s, "added project")
	if hit == nil {
		t.Errorf("expected 'added project' audit; logs=%+v", s.GetRecentLogs())
	}
	if hit != nil && (!strings.Contains(hit.Message, "MyProject") || !strings.Contains(hit.Message, dir)) {
		t.Errorf("audit message should include name + dir; got %q", hit.Message)
	}
}

func TestAuditProjectRemoved_Logged(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("ToDelete", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.ClearLogs()
	if err := s.RemoveProject(p.ID); err != nil {
		t.Fatal(err)
	}
	hit := findProjectAuditLog(s, "removed project")
	if hit == nil {
		t.Errorf("expected 'removed project' audit; logs=%+v", s.GetRecentLogs())
	}
	if hit != nil && !strings.Contains(hit.Message, "ToDelete") {
		t.Errorf("audit message should include project name; got %q", hit.Message)
	}
}

func TestAuditProjectRenamed_Logged(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("OldName", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.ClearLogs()
	if err := s.RenameProject(p.ID, "NewName"); err != nil {
		t.Fatal(err)
	}
	hit := findProjectAuditLog(s, "renamed")
	if hit == nil {
		t.Errorf("expected rename audit; logs=%+v", s.GetRecentLogs())
	}
	if hit != nil && (!strings.Contains(hit.Message, "OldName") || !strings.Contains(hit.Message, "NewName")) {
		t.Errorf("audit message should include both names; got %q", hit.Message)
	}
}

func TestAuditProject_ConsistentSource(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := NewStudio()
	s.config = defaultConfig()
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetProjectBudget(p.ID, 10.0)
	_ = s.SetProjectThinking(p.ID, "enabled", 4096)
	_ = s.SetProjectPinned(p.ID, true)
	// Every audit entry should have Source="project" (Level might be "info").
	count := 0
	for _, l := range s.GetRecentLogs() {
		if l.Source == "project" {
			count++
			if l.Level != "info" {
				t.Errorf("project audit Level=%q, want 'info'", l.Level)
			}
		}
	}
	if count < 3 {
		t.Errorf("expected at least 3 project audit entries; got %d", count)
	}
}
