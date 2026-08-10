package studio

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/genai"
)

func TestRenameChatSessionPersistenceFailureLeavesNameUntouched(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Sessions")
	p := s.projects[info.ID]
	session := p.sessions["default"]
	before := session.Info().Name

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOKIN_CONFIG_DIR", blocked)
	if err := s.RenameChatSession(info.ID, "default", "Durable name"); err == nil {
		t.Fatal("expected persistence failure")
	}
	if got := session.Info().Name; got != before {
		t.Fatalf("session name changed after failed persistence: got %q, want %q", got, before)
	}
}

func TestForkChatSessionPersistenceFailureDoesNotPublish(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Sessions")
	p := s.projects[info.ID]
	source := p.sessions["default"]
	source.mu.Lock()
	source.history = []*genai.Content{{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("branch here")}}}
	source.mu.Unlock()
	p.mu.RLock()
	before := len(p.sessions)
	p.mu.RUnlock()

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOKIN_CONFIG_DIR", blocked)
	created, err := s.ForkChatSession(info.ID, "default", 0, "Branch")
	if err == nil || created != nil {
		t.Fatalf("ForkChatSession = %#v, %v; want persistence failure", created, err)
	}
	p.mu.RLock()
	after := len(p.sessions)
	p.mu.RUnlock()
	if after != before {
		t.Fatalf("failed fork published a session: before=%d after=%d", before, after)
	}
}

func TestEditUserMessagePersistenceFailureDoesNotTrimOrResend(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Sessions")
	p := s.projects[info.ID]
	session := p.sessions["default"]
	session.mu.Lock()
	session.history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("first")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("answer")}},
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("second")}},
	}
	session.mu.Unlock()

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOKIN_CONFIG_DIR", blocked)
	if err := s.EditUserMessage(info.ID, "default", 0, "replacement"); err == nil {
		t.Fatal("expected persistence failure")
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	if len(session.history) != 3 || session.history[2].Parts[0].Text != "second" || session.active {
		t.Fatalf("failed edit mutated or resent session: history=%v active=%v", session.history, session.active)
	}
}

func TestRenameHistoryPreservesEntriesLineageAndUsage(t *testing.T) {
	withTempHistoryDir(t)
	key := projectSessionStorageKey("rename-project", "branch")
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("question")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("answer")}},
	}
	usage := &SessionUsage{TotalCostUSD: 1.25, TurnCount: 2}
	if err := SaveHistoryWithUsage(key, "Old", "parent", usage, history); err != nil {
		t.Fatal(err)
	}
	if err := RenameHistory(key, "New", "ignored", nil, nil); err != nil {
		t.Fatal(err)
	}
	loaded, name, parent, gotUsage, err := loadHistoryRaw(key)
	if err != nil {
		t.Fatal(err)
	}
	if name != "New" || parent != "parent" || len(loaded) != 2 || gotUsage == nil || gotUsage.TotalCostUSD != 1.25 || gotUsage.TurnCount != 2 {
		t.Fatalf("metadata-only rename lost data: name=%q parent=%q entries=%d usage=%+v", name, parent, len(loaded), gotUsage)
	}
}

func TestSaveNewHistoryRefusesToOverwrite(t *testing.T) {
	withTempHistoryDir(t)
	key := projectSessionStorageKey("collision-project", "same")
	if err := SaveHistoryWithName(key, "Original", nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveNewHistoryWithMetadata(key, "Replacement", "", nil); err == nil {
		t.Fatal("expected create-only history write to reject collision")
	}
	if got := LoadHistoryName(key); got != "Original" {
		t.Fatalf("collision overwrote history name: got %q", got)
	}
}
