package studio

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestExportProjectJSON_BasicStructure dumps a project and confirms the
// envelope shape: version, name, system prompt, provider, sessions[].
func TestExportProjectJSON_BasicStructure(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "ExportProj")

	// Configure the project a bit so the export has interesting fields.
	if err := s.SetProjectSystemPrompt(info.ID, "be terse"); err != nil {
		t.Fatalf("SetProjectSystemPrompt: %v", err)
	}
	// Inject a session with some history.
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	defaultSess := p.sessions["default"]
	p.mu.Unlock()
	defaultSess.mu.Lock()
	defaultSess.Name = "Main"
	defaultSess.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}
	defaultSess.mu.Unlock()

	out, err := s.ExportProjectJSON(info.ID)
	if err != nil {
		t.Fatalf("ExportProjectJSON: %v", err)
	}
	var env ProjectExportEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Version != projectExportVersion {
		t.Errorf("version = %d, want %d", env.Version, projectExportVersion)
	}
	if env.Name != "ExportProj" {
		t.Errorf("name = %q, want 'ExportProj'", env.Name)
	}
	if env.SystemPrompt != "be terse" {
		t.Errorf("systemPrompt = %q, want 'be terse'", env.SystemPrompt)
	}
	if len(env.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(env.Sessions))
	}
	if env.Sessions[0].Name != "Main" {
		t.Errorf("session[0].Name = %q, want 'Main'", env.Sessions[0].Name)
	}
	if len(env.Sessions[0].Entries) != 2 {
		t.Errorf("session[0].Entries len = %d, want 2", len(env.Sessions[0].Entries))
	}
	if env.ExportedAt == 0 {
		t.Error("exportedAt should be populated")
	}
}

// TestExportProjectJSON_Validation covers reject paths.
func TestExportProjectJSON_Validation(t *testing.T) {
	s := newStudioForTest(t)
	if _, err := s.ExportProjectJSON(""); err == nil {
		t.Error("expected error for empty projectID")
	}
	if _, err := s.ExportProjectJSON("no-such-id"); err == nil {
		t.Error("expected error for unknown project")
	}
}

// TestImportProjectJSON_RoundTrip exports a project, imports it into a
// NEW directory, confirms the new project has all the sessions + the
// expected (imported) name suffix.
func TestImportProjectJSON_RoundTrip(t *testing.T) {
	s := newStudioForTest(t)
	src := addTestProject(t, s, "Source")

	// Configure source so we have something to verify after import.
	if err := s.SetProjectSystemPrompt(src.ID, "system-from-export"); err != nil {
		t.Fatalf("SetProjectSystemPrompt: %v", err)
	}
	s.mu.RLock()
	p := s.projects[src.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	sess := p.sessions["default"]
	p.mu.Unlock()
	sess.mu.Lock()
	sess.Name = "Conversation A"
	sess.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("Q1")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("A1")}},
	}
	sess.mu.Unlock()

	exported, err := s.ExportProjectJSON(src.ID)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Import to a fresh directory.
	newDir := t.TempDir()
	imp, err := s.ImportProjectJSON(exported, newDir)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imp.ID == src.ID {
		t.Errorf("imported project got source ID — should have a fresh ID")
	}
	if !strings.Contains(strings.ToLower(imp.Name), "imported") {
		t.Errorf("imported name should contain 'imported', got %q", imp.Name)
	}
	if imp.SystemPrompt != "system-from-export" {
		t.Errorf("system prompt not preserved on import: got %q", imp.SystemPrompt)
	}
	if filepath.Clean(imp.Directory) != filepath.Clean(newDir) {
		t.Errorf("imported directory = %q, want %q", imp.Directory, newDir)
	}

	// Verify sessions transferred. Iter 590+ deletes the empty default
	// after import when at least one envelope session was imported, so we
	// expect exactly the imported count (1 here).
	sessions, err := s.ListChatSessions(imp.ID)
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected exactly 1 session after import (default deleted), got %d: %v", len(sessions), sessions)
	}
	// At least one session should have the imported name suffix.
	foundImported := false
	for _, ss := range sessions {
		if strings.Contains(strings.ToLower(ss.Name), "imported") {
			foundImported = true
			break
		}
	}
	if !foundImported {
		t.Errorf("no session has 'imported' suffix: %v", sessions)
	}
	// And the default session should be gone (no session with ID "default").
	for _, ss := range sessions {
		if ss.ID == "default" {
			t.Errorf("default session should have been deleted after import, but still present")
		}
	}
}

// TestImportProjectJSON_Validation covers reject paths.
func TestImportProjectJSON_Validation(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()

	if _, err := s.ImportProjectJSON("", dir); err == nil {
		t.Error("expected error for empty payload")
	}
	if _, err := s.ImportProjectJSON("   ", dir); err == nil {
		t.Error("expected error for whitespace-only payload")
	}
	if _, err := s.ImportProjectJSON("not json", dir); err == nil {
		t.Error("expected error for invalid JSON")
	}
	if _, err := s.ImportProjectJSON(`{"version":1,"name":"x","sessions":[]}`, ""); err == nil {
		t.Error("expected error for empty directory")
	}
	if _, err := s.ImportProjectJSON(`{"version":99,"name":"x","sessions":[]}`, dir); err == nil {
		t.Error("expected error for future version")
	}
	// Oversize payload rejected.
	huge := strings.Repeat("x", ImportPayloadMaxBytes+1)
	if _, err := s.ImportProjectJSON(huge, dir); err == nil {
		t.Error("expected error for oversize payload")
	}
}

// TestImportProjectJSON_DuplicateDirectory rejects when the target dir is
// already registered as another project.
func TestImportProjectJSON_DuplicateDirectory(t *testing.T) {
	s := newStudioForTest(t)
	existing := addTestProject(t, s, "Existing")

	payload := `{"version":1,"name":"NewProj","sessions":[]}`
	if _, err := s.ImportProjectJSON(payload, existing.Directory); err == nil {
		t.Error("expected error when importing to a directory already registered to another project")
	}
}

// TestImportProjectJSON_FreshDefaults handles a minimal envelope.
func TestImportProjectJSON_FreshDefaults(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()
	min := `{"version":1,"sessions":[]}`
	imp, err := s.ImportProjectJSON(min, dir)
	if err != nil {
		t.Fatalf("ImportProjectJSON: %v", err)
	}
	if imp.Name == "" {
		t.Error("imported project should have a fallback name")
	}
	if !strings.Contains(strings.ToLower(imp.Name), "imported") {
		t.Errorf("expected 'imported' suffix, got %q", imp.Name)
	}
	// With zero envelope sessions, the AddProject-created default session
	// must remain — otherwise the project would have NO sessions and the
	// user can't open chat. This is the safe fallback path for the iter
	// 590+ default-cleanup logic.
	sessions, _ := s.ListChatSessions(imp.ID)
	if len(sessions) != 1 || sessions[0].ID != "default" {
		t.Errorf("expected exactly 1 default session when import has 0 sessions, got %d: %v", len(sessions), sessions)
	}
}

// TestImportProjectJSON_ProviderBoundary ensures an old export cannot bypass
// the GLM/Kimi-only runtime contract. Unsupported providers keep the fresh
// project's safe GLM default, while a valid K3 selection is preserved.
func TestImportProjectJSON_ProviderBoundary(t *testing.T) {
	s := newStudioForTest(t)
	// Make the creation-time default Kimi so the test proves import migration
	// is based on the source provider, not whichever default happens to be set.
	s.mu.Lock()
	s.config.Settings.DefaultProvider = "kimi"
	s.config.Settings.DefaultModel = "k3"
	s.mu.Unlock()

	legacy, err := s.ImportProjectJSON(
		`{"version":1,"name":"Legacy","provider":"deepseek","model":"deepseek-v4-pro","sessions":[]}`,
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("import legacy project: %v", err)
	}
	if legacy.Provider != defaultStudioProvider || legacy.Model != defaultStudioModel {
		t.Errorf("legacy provider escaped boundary: got %s/%s, want %s/%s",
			legacy.Provider, legacy.Model, defaultStudioProvider, defaultStudioModel)
	}

	legacyGLM, err := s.ImportProjectJSON(
		`{"version":1,"name":"Old GLM","provider":"glm","model":"glm-4-flash","sessions":[]}`,
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("import legacy GLM project: %v", err)
	}
	if legacyGLM.Provider != "glm" || legacyGLM.Model != defaultModelForProvider("glm") {
		t.Errorf("legacy GLM did not stay on GLM: got %s/%s, want glm/%s",
			legacyGLM.Provider, legacyGLM.Model, defaultModelForProvider("glm"))
	}

	k3, err := s.ImportProjectJSON(
		`{"version":1,"name":"K3","provider":"kimi","model":"k3","sessions":[]}`,
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("import K3 project: %v", err)
	}
	if k3.Provider != "kimi" || k3.Model != "k3" {
		t.Errorf("valid K3 selection was not preserved: got %s/%s", k3.Provider, k3.Model)
	}
}

// TestImportProjectJSON_DeletesDefaultAfterImport pins down the iter 590+
// fix: after importing real sessions, the empty default is removed.
func TestImportProjectJSON_DeletesDefaultAfterImport(t *testing.T) {
	s := newStudioForTest(t)
	dir := t.TempDir()
	payload := `{"version":1,"name":"P","sessions":[{"version":1,"name":"X","entries":[{"role":"user","text":"hi"}]}]}`
	imp, err := s.ImportProjectJSON(payload, dir)
	if err != nil {
		t.Fatalf("ImportProjectJSON: %v", err)
	}
	sessions, _ := s.ListChatSessions(imp.ID)
	if len(sessions) != 1 {
		t.Errorf("expected exactly 1 session after import (default deleted), got %d: %v", len(sessions), sessions)
	}
	for _, ss := range sessions {
		if ss.ID == "default" {
			t.Error("default session should have been deleted post-import")
		}
	}
}
