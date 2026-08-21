package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestHistoryManager(t *testing.T) (*HistoryManager, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	manager, err := NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	return manager, root
}

func TestHistoryManagerFullSessionAtomicPrivateRoundTrip(t *testing.T) {
	manager, root := newTestHistoryManager(t)
	session := NewSession()
	session.ID = "session-1"
	session.AddUserMessage("private question")
	if err := manager.SaveFull(session); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gokin", "sessions", "session-1.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("session mode=%#o, want 0600", got)
	}
	state, err := manager.LoadFull("session-1")
	if err != nil || state.ID != "session-1" || len(state.History) != 1 {
		t.Fatalf("loaded=%+v err=%v", state, err)
	}
	if temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".gokin-*.tmp")); err != nil || len(temps) != 0 {
		t.Fatalf("session save leaked temps=%v err=%v", temps, err)
	}
}

func TestHistoryManagerRejectsUnsafeIDs(t *testing.T) {
	manager, root := newTestHistoryManager(t)
	session := NewSession()
	session.ID = "../escape"
	if err := manager.SaveFull(session); err == nil || !strings.Contains(err.Error(), "invalid session ID") {
		t.Fatalf("SaveFull traversal error=%v", err)
	}
	if _, err := manager.LoadFull("../escape"); err == nil {
		t.Fatal("LoadFull traversal succeeded")
	}
	if err := manager.DeleteSession("../escape"); err == nil {
		t.Fatal("DeleteSession traversal succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "gokin", "escape.json")); !os.IsNotExist(err) {
		t.Fatalf("session traversal reached outside store: %v", err)
	}
}

func TestHistoryManagerRejectsSymlinkAndMismatchedSession(t *testing.T) {
	manager, root := newTestHistoryManager(t)
	sessionsDir := filepath.Join(root, "gokin", "sessions")
	data, err := json.Marshal(&SessionState{ID: "linked"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(sessionsDir, "linked.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := manager.LoadFull("linked"); err == nil {
		t.Fatal("LoadFull followed a symlink")
	}
	if sessions, err := manager.ListSessions(); err != nil || len(sessions) != 0 {
		t.Fatalf("ListSessions included symlink: %+v err=%v", sessions, err)
	}

	data, err = json.Marshal(&SessionState{ID: "inside-id"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "requested-id.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.LoadFull("requested-id"); err == nil || !strings.Contains(err.Error(), "ID mismatch") {
		t.Fatalf("mismatched session load error=%v", err)
	}
}

func TestHistoryManagerSnapshotsAfterClaimingPublicationOrder(t *testing.T) {
	manager, _ := newTestHistoryManager(t)
	session := NewSession()
	session.ID = "session-order"
	session.SetScratchpad("old")

	started := make(chan struct{})
	release := make(chan struct{})
	manager.beforeFullSaveLock = func() {
		close(started)
		<-release
	}
	manager.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- manager.SaveFull(session) }()
	<-started
	session.SetScratchpad("new")
	close(release)
	manager.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state, err := manager.LoadFull(session.ID)
	if err != nil || state.Scratchpad != "new" {
		t.Fatalf("persisted scratchpad=%q err=%v, want new", state.Scratchpad, err)
	}
}

func TestForkKeepsDisplayNameOutOfPersistentID(t *testing.T) {
	session := NewSession()
	branch := session.Fork("review ../../ with spaces")
	if !strings.Contains(strings.Join(session.ListBranches(), "\n"), "review ../../ with spaces") {
		t.Fatal("branch display name was not preserved")
	}
	if strings.Contains(branch.ID, "review") || strings.ContainsAny(branch.ID, `/\\ `) {
		t.Fatalf("unsafe display name leaked into branch ID %q", branch.ID)
	}
}

func TestSessionManagerStopDrainsSaverAndPreventsLaterWrites(t *testing.T) {
	_, root := newTestHistoryManager(t)
	session := NewSession()
	session.ID = "managed-session"
	manager, err := NewSessionManager(session, SessionManagerConfig{
		Enabled:      true,
		SaveInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.Start(context.Background())
	session.SetScratchpad("queued")
	if err := manager.SaveAfterMessage(); err != nil {
		t.Fatal(err)
	}
	session.SetScratchpad("final")
	manager.Stop()
	manager.Stop() // idempotent

	session.SetScratchpad("must-not-persist")
	if err := manager.SaveAfterMessage(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	path := filepath.Join(root, "gokin", "sessions", session.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Scratchpad != "final" {
		t.Fatalf("persisted scratchpad=%q after Stop, want final", state.Scratchpad)
	}
}

func TestConcurrentSessionManagerStopsShareCompletionBarrier(t *testing.T) {
	_, root := newTestHistoryManager(t)
	session := NewSession()
	session.ID = "concurrent-stop"
	session.SetScratchpad("final-state")
	manager, err := NewSessionManager(session, SessionManagerConfig{Enabled: true, SaveInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	manager.Start(context.Background())
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			<-start
			manager.Stop()
			done <- struct{}{}
		}()
	}
	close(start)
	for range 2 {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Stop did not complete")
		}
	}
	path := filepath.Join(root, "gokin", "sessions", session.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil || state.Scratchpad != "final-state" {
		t.Fatalf("final session state=%+v err=%v", state, err)
	}
}

func TestNewSessionManagerRejectsNilSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if _, err := NewSessionManager(nil, DefaultSessionManagerConfig()); err == nil {
		t.Fatal("nil session manager construction succeeded")
	}
}
