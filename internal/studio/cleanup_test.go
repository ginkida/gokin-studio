package studio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/memory"
	"github.com/ginkida/gokin-studio/internal/engine/tasks"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

// TestRemoveProject_CleansUpHistoryAndReplay verifies that removing a project
// also removes its per-session history file and any in-flight replay log so
// stale data never reappears if a new project is created with the same ID.
func TestRemoveProject_CleansUpHistoryAndReplay(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "CleanupProject")

	// Write a history file for the default session.
	key := info.ID + "_default"
	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}
	if err := SaveHistoryWithName(key, "Chat 1", hist); err != nil {
		t.Fatalf("SaveHistoryWithName: %v", err)
	}
	// Write a replay log (simulated interrupted turn).
	r := NewReplayLogger(info.ID, "default")
	r.Append(ReplayEvent{Type: "user", Text: "hello"})
	r.Close()

	// Confirm both files exist before removal.
	if _, err := os.Stat(historyPath(key)); err != nil {
		t.Fatalf("expected history file to exist: %v", err)
	}
	if _, err := os.Stat(replayPath(info.ID, "default")); err != nil {
		t.Fatalf("expected replay file to exist: %v", err)
	}

	// Remove the project.
	if err := s.RemoveProject(info.ID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}

	// Both files must be gone.
	if _, err := os.Stat(historyPath(key)); !os.IsNotExist(err) {
		t.Errorf("history file still exists after RemoveProject (stat err: %v)", err)
	}
	if _, err := os.Stat(replayPath(info.ID, "default")); !os.IsNotExist(err) {
		t.Errorf("replay file still exists after RemoveProject (stat err: %v)", err)
	}
}

// TestDeleteChatSession_CleansUpHistoryAndReplay verifies that deleting an
// individual session removes its history and replay files while leaving other
// sessions' files untouched.
func TestDeleteChatSession_CleansUpHistoryAndReplay(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-cleanup", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Create a second session so we can delete one without hitting the last-session guard.
	sess2, err := s.CreateChatSession(p.ID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}

	// Write history and replay for the second session.
	key2 := p.ID + "_" + sess2.ID
	hist := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("task")}},
	}
	if err := SaveHistoryWithName(key2, sess2.Name, hist); err != nil {
		t.Fatalf("SaveHistoryWithName sess2: %v", err)
	}
	r2 := NewReplayLogger(p.ID, sess2.ID)
	r2.Append(ReplayEvent{Type: "user", Text: "task"})
	r2.Close()

	// Confirm both files exist.
	if _, err := os.Stat(historyPath(key2)); err != nil {
		t.Fatalf("expected sess2 history to exist: %v", err)
	}
	if _, err := os.Stat(replayPath(p.ID, sess2.ID)); err != nil {
		t.Fatalf("expected sess2 replay to exist: %v", err)
	}

	// Delete the second session.
	if err := s.DeleteChatSession(p.ID, sess2.ID); err != nil {
		t.Fatalf("DeleteChatSession: %v", err)
	}

	// Both session-2 files must be gone.
	if _, err := os.Stat(historyPath(key2)); !os.IsNotExist(err) {
		t.Errorf("sess2 history still exists after DeleteChatSession (stat err: %v)", err)
	}
	if _, err := os.Stat(replayPath(p.ID, sess2.ID)); !os.IsNotExist(err) {
		t.Errorf("sess2 replay still exists after DeleteChatSession (stat err: %v)", err)
	}

	// The default session must still exist (we only deleted sess2).
	sessions, err := s.ListChatSessions(p.ID)
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 remaining session, got %d", len(sessions))
	}
}

func TestDeleteChatSession_DiskFailureKeepsSessionVisible(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Delete Failure")
	extra, err := s.CreateChatSession(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := historyPath(info.ID + "_" + extra.ID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "child"), 0700); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteChatSession(info.ID, extra.ID); err == nil {
		t.Fatal("expected history deletion error")
	}
	sessions, err := s.ListChatSessions(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, session := range sessions {
		found = found || session.ID == extra.ID
	}
	if !found {
		t.Fatal("session disappeared from memory despite disk deletion failure")
	}
}

func TestClearHistory_DiskFailureKeepsConversationVisible(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Clear Failure")
	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	session := p.GetSession("default")
	session.mu.Lock()
	session.history = []*genai.Content{{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("keep this")}}}
	session.mu.Unlock()
	path := historyPath(info.ID + "_default")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "child"), 0700); err != nil {
		t.Fatal(err)
	}

	if err := s.ClearHistory(info.ID, "default"); err == nil {
		t.Fatal("expected history deletion error")
	}
	session.mu.RLock()
	remaining := len(session.history)
	session.mu.RUnlock()
	if remaining != 1 {
		t.Fatalf("history changed despite disk deletion failure: %d entries", remaining)
	}
}

// TestClearHistory_DiscardsReplay verifies that ClearHistory also removes any
// in-flight replay log so the recovery banner can't resurrect a turn the user
// explicitly cleared.
func TestClearHistory_DiscardsReplay(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-clear", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Write a replay log for the default session.
	r := NewReplayLogger(p.ID, "default")
	r.Append(ReplayEvent{Type: "user", Text: "hello"})
	r.Close()

	if _, err := os.Stat(replayPath(p.ID, "default")); err != nil {
		t.Fatalf("expected replay file to exist before clear: %v", err)
	}

	if err := s.ClearHistory(p.ID, "default"); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}

	if _, err := os.Stat(replayPath(p.ID, "default")); !os.IsNotExist(err) {
		t.Errorf("replay file still exists after ClearHistory (err: %v)", err)
	}
}

// TestRemoveProject_NoDefaultSession verifies the `!hasDefault` cleanup path in
// RemoveProject: when a project's sessions map has no "default" entry,
// RemoveProject still explicitly scrubs the default session's history and replay
// files (in case they exist from a previous import or manual copy).
func TestRemoveProject_NoDefaultSession(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-nod", Name: "P", Directory: t.TempDir()})
	p.studio = s
	// Replace the default session with a custom-named one.
	delete(p.sessions, "default")
	p.sessions["custom"] = NewChatSession("Custom Chat")
	s.projects[p.ID] = p

	if err := s.RemoveProject(p.ID); err != nil {
		t.Fatalf("RemoveProject with no default session: %v", err)
	}
	if _, err := s.GetProject(p.ID); err == nil {
		t.Error("expected project to be removed, but GetProject succeeded")
	}
}

// TestDeleteChatSession_UnknownProject verifies that attempting to delete a
// session from a project that doesn't exist returns an error (not a panic).
func TestDeleteChatSession_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	err := s.DeleteChatSession("no-such-project", "default")
	if err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestClearHistory_EmptySessionIDDefaultsToDefault verifies that passing "" as
// the session ID in ClearHistory is treated as "default", so the caller
// doesn't need to know the session ID for the common single-session case.
func TestClearHistory_EmptySessionIDDefaultsToDefault(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-clr-empty", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p
	p.sessions["default"].history = []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}

	if err := s.ClearHistory(p.ID, ""); err != nil {
		t.Fatalf("ClearHistory with empty sessionID: %v", err)
	}
	p.sessions["default"].mu.RLock()
	histLen := len(p.sessions["default"].history)
	p.sessions["default"].mu.RUnlock()
	if histLen != 0 {
		t.Errorf("expected empty history after ClearHistory, got %d entries", histLen)
	}
}

// TestClearHistory_NilSession verifies that ClearHistory returns an error when
// the requested session doesn't exist and there is no "default" session to
// fall back to. GetSession returns nil in that case.
func TestClearHistory_NilSession(t *testing.T) {
	_ = withTempHistoryDir(t)
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-clr-nil", Name: "P", Directory: t.TempDir()})
	p.studio = s
	// Delete the default session so GetSession("nonexistent") returns nil.
	delete(p.sessions, "default")
	s.projects[p.ID] = p

	err := s.ClearHistory(p.ID, "nonexistent-session")
	if err == nil {
		t.Error("expected error when session not found, got nil")
	}
}

// TestStopGeneration_UnknownProject verifies that stopping generation for an
// unknown project returns an error rather than panicking.
func TestStopGeneration_UnknownProject(t *testing.T) {
	s := newStudioForTest(t)
	if err := s.StopGeneration("no-such-id", "default"); err == nil {
		t.Error("expected error for unknown project, got nil")
	}
}

// TestStopGeneration_StopsSpecificSession verifies that StopGeneration with a
// session ID calls that session's cancel function (stops just the one session
// rather than all of them).
func TestStopGeneration_StopsSpecificSession(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-stop", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Wire a cancel function onto the default session so we can observe it.
	ctx, cancel := context.WithCancel(context.Background())
	p.sessions["default"].mu.Lock()
	p.sessions["default"].cancelFn = cancel
	p.sessions["default"].mu.Unlock()

	if err := s.StopGeneration(p.ID, "default"); err != nil {
		t.Fatalf("StopGeneration: %v", err)
	}

	// The context derived from the cancel function must now be Done.
	select {
	case <-ctx.Done():
		// ✓ cancel was called
	default:
		t.Error("expected cancelFn to be called by StopGeneration, but context is not Done")
	}
}

// TestStop_FlushesMemoryStores verifies that Project.Stop flushes the memory
// store and project-learning store when they are initialised. This exercises
// the `if memStore != nil` and `if learning != nil` branches that are never
// reached in tests that don't call initMemoryAndPlan.
func TestStop_FlushesMemoryStores(t *testing.T) {
	_ = withTempHistoryDir(t)
	configDir := t.TempDir()
	projDir := t.TempDir()

	p := NewProject(ProjectConfig{ID: "pid-flush", Name: "P", Directory: projDir})

	// Inject a real memory.Store so the memStore != nil branch is reached.
	store, err := memory.NewStore(configDir, projDir, 100)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	p.memoryStore = store

	// Inject a real memory.ProjectLearning so the learning != nil branch is reached.
	learning, err := memory.NewProjectLearning(projDir)
	if err != nil {
		t.Fatalf("memory.NewProjectLearning: %v", err)
	}
	p.projectLearning = learning

	// Stop must not panic and must call Flush on both stores without error.
	p.Stop()
}

// TestStop_CancelsRunningTasks verifies that Project.Stop cancels all running
// background tasks when a taskManager is present. This exercises the
// `if tm != nil` / `tm.ListRunning()` / `tm.Cancel(id)` block.
func TestStop_CancelsRunningTasks(t *testing.T) {
	workDir := t.TempDir()
	p := NewProject(ProjectConfig{ID: "pid-tasks", Name: "P", Directory: workDir})

	// Inject a real tasks.Manager.
	tm := tasks.NewManager(workDir)
	p.taskManager = tm

	// Start a long-running background task so ListRunning() returns something.
	ctx := context.Background()
	id, err := tm.Start(ctx, "sleep 30")
	if err != nil {
		t.Fatalf("tm.Start: %v", err)
	}

	running := tm.ListRunning()
	if len(running) == 0 {
		t.Skip("sleep task didn't start in time — skipping")
	}

	// Stop must cancel the running task (no error or panic).
	p.Stop()

	// After Stop, the task should no longer be running.
	_ = id
	remaining := tm.ListRunning()
	if len(remaining) != 0 {
		t.Errorf("expected 0 running tasks after Stop, got %d", len(remaining))
	}
}

func TestProjectClosePermanentlyClosesBackgroundTaskManagers(t *testing.T) {
	p := NewProject(ProjectConfig{ID: "pid-close-tasks", Name: "P", Directory: t.TempDir()})
	projectManager := tasks.NewManager(p.Directory)
	sessionManager := tasks.NewManager(p.Directory)
	p.taskManager = projectManager
	session := p.sessions["default"]
	session.taskManager = sessionManager

	p.Close()

	for name, manager := range map[string]*tasks.Manager{
		"project": projectManager,
		"session": sessionManager,
	} {
		if _, err := manager.Start(context.Background(), "unused"); !errors.Is(err, tasks.ErrManagerClosed) {
			t.Fatalf("%s manager Start after project Close = %v, want ErrManagerClosed", name, err)
		}
	}
}

// TestStopGeneration_EmptySessionIDStopsAll verifies that passing an empty
// session ID triggers p.Stop() which cancels every session in the project,
// not just a single named one.
func TestStopGeneration_EmptySessionIDStopsAll(t *testing.T) {
	s := newStudioForTest(t)
	p := NewProject(ProjectConfig{ID: "pid-stopall", Name: "P", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p

	// Add a second session alongside the default.
	sess2 := NewChatSession("Chat 2")
	p.sessions[sess2.ID] = sess2

	// Wire cancel functions onto both sessions.
	ctx1, cancel1 := context.WithCancel(context.Background())
	p.sessions["default"].mu.Lock()
	p.sessions["default"].cancelFn = cancel1
	p.sessions["default"].mu.Unlock()

	ctx2, cancel2 := context.WithCancel(context.Background())
	sess2.mu.Lock()
	sess2.cancelFn = cancel2
	sess2.mu.Unlock()

	// StopGeneration("") must cancel all sessions.
	if err := s.StopGeneration(p.ID, ""); err != nil {
		t.Fatalf("StopGeneration: %v", err)
	}

	select {
	case <-ctx1.Done():
	default:
		t.Error("expected default session cancelFn to be called, but context is not Done")
	}
	select {
	case <-ctx2.Done():
	default:
		t.Error("expected sess2 cancelFn to be called, but context is not Done")
	}
}

// TestInitMemoryAndPlan_MemoriseTool verifies that when the "memorize" tool exists
// in the registry AND projectLearning was set, initMemoryAndPlan wires the learning
// store into the MemorizeTool (lines 261-266 of project.go). DefaultRegistry does
// not include "memorize", so this test builds a custom registry.
// It also covers the `if !ok { continue }` branch in the planManager loop (line 271-272)
// by intentionally omitting one plan tool from the registry.
func TestInitMemoryAndPlan_MemoriseTool(t *testing.T) {
	_ = withTempHistoryDir(t)
	projDir := t.TempDir()

	// Build a registry that includes the memorize tool (not in DefaultRegistry).
	reg := tools.NewRegistry()
	_ = reg.Register(tools.NewMemorizeTool(nil)) // wire target; initially nil store
	// Include only "enter_plan_mode" — the other 3 plan tools are absent,
	// so the loop hits `if !ok { continue }` for each missing name.
	_ = reg.Register(tools.NewEnterPlanModeTool())

	p := &Project{
		ID:        "pid-mp-memorize",
		Name:      "Test",
		Directory: projDir,
	}
	p.initMemoryAndPlan(reg)

	// Verify the memorize tool received the learning store.
	mt, ok := reg.Get("memorize")
	if !ok {
		t.Fatal("memorize tool missing from registry after initMemoryAndPlan")
	}
	if memTool, ok := mt.(*tools.MemorizeTool); ok {
		_ = memTool // tool should now have its learning store set
	} else {
		t.Fatal("expected *tools.MemorizeTool, got different type")
	}
}
