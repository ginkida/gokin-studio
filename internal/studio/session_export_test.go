package studio

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestExportSessionJSON_Basic dumps a session and confirms key fields.
func TestExportSessionJSON_Basic(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "ExportTest")

	// Inject a bit of history into the default session.
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	defaultSess := p.sessions["default"]
	p.mu.Unlock()
	defaultSess.mu.Lock()
	defaultSess.Name = "My Important Chat"
	defaultSess.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("Hello agent")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("Hello user")}},
	}
	defaultSess.mu.Unlock()

	out, err := s.ExportSessionJSON(info.ID, "default")
	if err != nil {
		t.Fatalf("ExportSessionJSON: %v", err)
	}
	var env SessionExportEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Version != sessionExportVersion {
		t.Errorf("version = %d, want %d", env.Version, sessionExportVersion)
	}
	if env.Name != "My Important Chat" {
		t.Errorf("name = %q, want 'My Important Chat'", env.Name)
	}
	if len(env.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(env.Entries))
	}
	if env.ExportedAt == 0 {
		t.Error("exportedAt should be populated")
	}
}

// TestExportSessionJSON_StripsThinkingParts confirms thinking/reasoning
// parts are excluded — they're not in the persisted format anyway.
func TestExportSessionJSON_StripsThinkingParts(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "ThinkingExport")

	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	sess := p.sessions["default"]
	p.mu.Unlock()
	sess.mu.Lock()
	sess.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("Hello")}},
		{Role: "model", Parts: []*genai.Part{
			{Text: "internal reasoning", Thought: true},
			genai.NewPartFromText("Hi back"),
		}},
	}
	sess.mu.Unlock()

	out, _ := s.ExportSessionJSON(info.ID, "default")
	if strings.Contains(out, "internal reasoning") {
		t.Error("thinking text leaked into export")
	}
	if !strings.Contains(out, "Hi back") {
		t.Error("non-thinking text missing from export")
	}
}

// TestExportSessionJSON_Validation covers reject paths.
func TestExportSessionJSON_Validation(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Validate")

	if _, err := s.ExportSessionJSON("", "default"); err == nil {
		t.Error("expected error for empty projectID")
	}
	if _, err := s.ExportSessionJSON("no-such-id", "default"); err == nil {
		t.Error("expected error for unknown project")
	}
	if _, err := s.ExportSessionJSON(info.ID, "no-such-session"); err == nil {
		t.Error("expected error for unknown session")
	}
	// Empty sessionID defaults to "default" — should succeed.
	if _, err := s.ExportSessionJSON(info.ID, ""); err != nil {
		t.Errorf("expected empty sessionID to default to 'default', got %v", err)
	}
}

// TestImportSessionJSON_RoundTrip exports then imports — confirms the
// imported session has the right entries + a fresh ID + "(imported)" suffix.
func TestImportSessionJSON_RoundTrip(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "RoundTrip")

	// Seed a session.
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	p.mu.Lock()
	defaultSess := p.sessions["default"]
	p.mu.Unlock()
	defaultSess.mu.Lock()
	defaultSess.Name = "Source Chat"
	defaultSess.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("Q1")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("A1")}},
	}
	defaultSess.mu.Unlock()

	exported, err := s.ExportSessionJSON(info.ID, "default")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Import into the same project. The new session must have a fresh ID.
	imported, err := s.ImportSessionJSON(info.ID, exported)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported.ID == "default" {
		t.Errorf("imported session got source ID 'default' — should have a fresh ID")
	}
	if !strings.Contains(strings.ToLower(imported.Name), "imported") {
		t.Errorf("imported name should contain 'imported', got %q", imported.Name)
	}
	// Verify the entries are preserved.
	p.mu.RLock()
	imp := p.sessions[imported.ID]
	p.mu.RUnlock()
	if imp == nil {
		t.Fatal("imported session not in project map")
	}
	imp.mu.RLock()
	histLen := len(imp.history)
	firstText := ""
	if histLen > 0 && len(imp.history[0].Parts) > 0 {
		firstText = imp.history[0].Parts[0].Text
	}
	imp.mu.RUnlock()
	if histLen != 2 {
		t.Errorf("imported history len = %d, want 2", histLen)
	}
	if firstText != "Q1" {
		t.Errorf("imported first text = %q, want 'Q1'", firstText)
	}
}

// TestImportSessionJSON_Validation covers reject paths.
func TestImportSessionJSON_Validation(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "ImportValidate")

	if _, err := s.ImportSessionJSON("", `{"version":1}`); err == nil {
		t.Error("expected error for empty projectID")
	}
	if _, err := s.ImportSessionJSON("no-such-id", `{"version":1}`); err == nil {
		t.Error("expected error for unknown project")
	}
	if _, err := s.ImportSessionJSON(info.ID, ""); err == nil {
		t.Error("expected error for empty payload")
	}
	if _, err := s.ImportSessionJSON(info.ID, "   "); err == nil {
		t.Error("expected error for whitespace-only payload")
	}
	if _, err := s.ImportSessionJSON(info.ID, "not json"); err == nil {
		t.Error("expected error for invalid JSON")
	}
	// Future-version rejection.
	future := `{"version":99,"name":"x","entries":[]}`
	if _, err := s.ImportSessionJSON(info.ID, future); err == nil {
		t.Error("expected error for future version")
	}
}

// TestImportSessionJSON_FreshDefaults handles a minimal valid payload —
// no name, no entries — should still produce a usable session.
func TestImportSessionJSON_FreshDefaults(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Defaults")

	min := `{"version":1,"entries":[]}`
	imp, err := s.ImportSessionJSON(info.ID, min)
	if err != nil {
		t.Fatalf("ImportSessionJSON: %v", err)
	}
	if imp.Name == "" {
		t.Error("imported session should have a fallback name")
	}
	if !strings.Contains(strings.ToLower(imp.Name), "imported") {
		t.Errorf("expected 'imported' in name, got %q", imp.Name)
	}
}

// TestImportSessionJSON_PersistsToDisk confirms the imported session
// survives a restart (i.e. is written to disk).
func TestImportSessionJSON_PersistsToDisk(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Persist")

	payload := `{"version":1,"name":"Persisted","entries":[{"role":"user","text":"hello"},{"role":"model","text":"world"}]}`
	imp, err := s.ImportSessionJSON(info.ID, payload)
	if err != nil {
		t.Fatalf("ImportSessionJSON: %v", err)
	}

	// Verify on disk.
	loaded, err := LoadHistory(info.ID + "_" + imp.ID)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("disk-loaded history len = %d, want 2", len(loaded))
	}
}
