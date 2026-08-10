package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestArchiveRestoreChatSessionPersistsAndPreservesHistory(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Session archive")
	extra, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	project := s.projects[info.ID]
	project.testEmitter = func(string, any) {}
	target := project.GetSession(extra.ID)
	history := []*genai.Content{{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("keep this transcript")}}}
	target.mu.Lock()
	target.history = history
	target.mu.Unlock()
	if err := SaveHistoryWithName(projectSessionStorageKey(info.ID, extra.ID), extra.Name, history); err != nil {
		t.Fatal(err)
	}

	if err := s.ArchiveChatSession(info.ID, extra.ID); err != nil {
		t.Fatal(err)
	}
	active, err := s.ListChatSessions(info.ID)
	if err != nil || len(active) != 1 || active[0].ID != "default" {
		t.Fatalf("active sessions = %#v, %v", active, err)
	}
	archived, err := s.ListArchivedChatSessions(info.ID)
	if err != nil || len(archived) != 1 || archived[0].ID != extra.ID || !archived[0].Archived || archived[0].ArchivedAt == 0 {
		t.Fatalf("archived sessions = %#v, %v", archived, err)
	}

	// Recreate the project from disk to prove archive state is durable and the
	// hidden chat still loads its complete conversation.
	reloaded := NewProject(project.ToConfig())
	reloaded.studio = s
	reloaded.testEmitter = func(string, any) {}
	s.projects[info.ID] = reloaded
	reloadedTarget := reloaded.GetSession(extra.ID)
	if reloadedTarget == nil {
		t.Fatal("archived session disappeared after reload")
	}
	reloadedTarget.mu.RLock()
	reloadedArchived := reloadedTarget.ArchivedAt > 0
	reloadedHistory := append([]*genai.Content(nil), reloadedTarget.history...)
	reloadedTarget.mu.RUnlock()
	if !reloadedArchived || !historyContainsText(reloadedHistory, "keep this transcript") {
		t.Fatalf("reloaded archived=%v history=%#v", reloadedArchived, reloadedHistory)
	}

	restored, err := s.RestoreChatSession(info.ID, extra.ID)
	if err != nil || restored.Archived {
		t.Fatalf("restore = %#v, %v", restored, err)
	}
	active, _ = s.ListChatSessions(info.ID)
	archived, _ = s.ListArchivedChatSessions(info.ID)
	if len(active) != 2 || len(archived) != 0 {
		t.Fatalf("post-restore active=%#v archived=%#v", active, archived)
	}
}

func TestArchiveChatSessionGuardsLastRunningAndArchivedDelivery(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Archive guards")
	s.projects[info.ID].testEmitter = func(string, any) {}
	if err := s.ArchiveChatSession(info.ID, "default"); err == nil || !strings.Contains(err.Error(), "last active") {
		t.Fatalf("last-active archive error = %v", err)
	}
	extra, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := s.projects[info.ID].GetSession(extra.ID)
	target.mu.Lock()
	target.active = true
	target.mu.Unlock()
	if err := s.ArchiveChatSession(info.ID, extra.ID); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("running archive error = %v", err)
	}
	target.mu.Lock()
	target.active = false
	target.mu.Unlock()
	if err := s.ArchiveChatSession(info.ID, extra.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SendMessage(info.ID, "should not run", extra.ID); err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("send to archived error = %v", err)
	}
	if err := s.QueueMessage(info.ID, "should not queue", extra.ID, "q1"); err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("queue to archived error = %v", err)
	}
}

func TestArchiveChatSessionDiskFailureIsNoOp(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Archive durability")
	extra, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "config-is-a-file")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOKIN_CONFIG_DIR", blocked)
	if err := s.ArchiveChatSession(info.ID, extra.ID); err == nil {
		t.Fatal("expected archive persistence failure")
	}
	target := s.projects[info.ID].GetSession(extra.ID)
	target.mu.RLock()
	archivedAt := target.ArchivedAt
	target.mu.RUnlock()
	if archivedAt != 0 {
		t.Fatalf("session was hidden despite failed persistence: %d", archivedAt)
	}
}

func TestDeleteArchivedSessionAndRemoveProjectCleanArchiveMetadata(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Archive cleanup")
	s.projects[info.ID].testEmitter = func(string, any) {}
	extra, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveChatSession(info.ID, extra.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChatSession(info.ID, extra.ID); err != nil {
		t.Fatal(err)
	}
	archived, err := s.ListArchivedChatSessions(info.ID)
	if err != nil || len(archived) != 0 {
		t.Fatalf("archived after permanent delete = %#v, %v", archived, err)
	}
	if _, err := os.Stat(sessionArchivesPath(info.ID)); !os.IsNotExist(err) {
		t.Fatalf("archive index survived permanent delete: %v", err)
	}

	extra, err = s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveChatSession(info.ID, extra.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveProject(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sessionArchivesPath(info.ID)); !os.IsNotExist(err) {
		t.Fatalf("archive index survived project removal: %v", err)
	}
}

func TestApplySessionArchivesRepairsImpossibleAllArchivedState(t *testing.T) {
	withTempConfigDir(t)
	sessions := map[string]*ChatSession{
		"default": NewChatSession("Chat 1"),
		"other":   NewChatSession("Chat 2"),
	}
	sessions["default"].ID = "default"
	sessions["other"].ID = "other"
	if err := saveArchivedSessions("repair", map[string]int64{"default": 1, "other": 2, "stale": 3}); err != nil {
		t.Fatal(err)
	}
	applySessionArchives("repair", sessions)
	if sessions["default"].ArchivedAt != 0 || sessions["other"].ArchivedAt == 0 {
		t.Fatalf("repaired archive flags: default=%d other=%d", sessions["default"].ArchivedAt, sessions["other"].ArchivedAt)
	}
	records, err := loadArchivedSessions("repair")
	if err != nil || len(records) != 1 || records["other"] != 2 {
		t.Fatalf("repaired records = %#v, %v", records, err)
	}
}
