package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentStoreRejectsTraversalAndNilState(t *testing.T) {
	root := t.TempDir()
	store, err := NewAgentStore(root)
	if err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "escape.json")
	if err := store.SaveState(&AgentState{ID: "../escape"}); err == nil || !strings.Contains(err.Error(), "invalid agent ID") {
		t.Fatalf("SaveState traversal error=%v", err)
	}
	if err := store.SaveState(nil); err == nil {
		t.Fatal("SaveState(nil) succeeded")
	}
	if _, err := store.Load("../escape"); err == nil {
		t.Fatal("Load traversal succeeded")
	}
	if err := store.Delete("../escape"); err == nil {
		t.Fatal("Delete traversal succeeded")
	}
	if store.Exists("../escape") {
		t.Fatal("Exists accepted traversal")
	}
	if _, err := os.Stat(escape); !os.IsNotExist(err) {
		t.Fatalf("agent traversal created outside file: %v", err)
	}
}

func TestAgentStoreCheckpointValidationAndAtomicMode(t *testing.T) {
	root := t.TempDir()
	store, err := NewAgentStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCheckpoint(nil); err == nil {
		t.Fatal("SaveCheckpoint(nil) succeeded")
	}
	if err := store.SaveCheckpoint(&AgentCheckpoint{CheckpointID: "agent-1-123"}); err == nil {
		t.Fatal("checkpoint without agent state succeeded")
	}
	bad := &AgentCheckpoint{CheckpointID: "../escape"}
	if err := store.SaveCheckpoint(bad); err == nil || !strings.Contains(err.Error(), "invalid checkpoint ID") {
		t.Fatalf("SaveCheckpoint traversal error=%v", err)
	}
	if _, err := store.LoadCheckpoint("../escape"); err == nil {
		t.Fatal("LoadCheckpoint traversal succeeded")
	}
	if err := store.DeleteCheckpoint("../escape"); err == nil {
		t.Fatal("DeleteCheckpoint traversal succeeded")
	}
	if _, err := store.ListCheckpoints("../escape"); err == nil {
		t.Fatal("ListCheckpoints traversal succeeded")
	}
	if _, err := store.CleanupCheckpoints("agent-1", -1); err == nil {
		t.Fatal("CleanupCheckpoints negative keep count succeeded")
	}

	cp := &AgentCheckpoint{CheckpointID: "agent-1-123", AgentState: &AgentState{ID: "agent-1"}}
	if err := store.SaveCheckpoint(cp); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	path := filepath.Join(root, "agents", "checkpoints", cp.CheckpointID+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("checkpoint mode=%#o, want 0600", got)
	}
	loaded, err := store.LoadCheckpoint(cp.CheckpointID)
	if err != nil || loaded.CheckpointID != cp.CheckpointID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestAgentStoreRejectsSymlinkAndMismatchedRecords(t *testing.T) {
	root := t.TempDir()
	store, err := NewAgentStore(root)
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(&AgentState{ID: "linked"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "agents", "linked.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.Load("linked"); err == nil {
		t.Fatal("Load followed a symlinked agent state")
	}
	if store.Exists("linked") {
		t.Fatal("Exists accepted a symlinked agent state")
	}
	if ids, err := store.List(); err != nil || len(ids) != 0 {
		t.Fatalf("List included symlink: %v err=%v", ids, err)
	}

	data, err = json.Marshal(&AgentState{ID: "inside-id"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "requested-id.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("requested-id"); err == nil || !strings.Contains(err.Error(), "ID mismatch") {
		t.Fatalf("mismatched agent load error=%v", err)
	}

	checkpoints := filepath.Join(root, "agents", "checkpoints")
	if err := os.MkdirAll(checkpoints, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(&AgentCheckpoint{
		CheckpointID: "inside-checkpoint",
		AgentState:   &AgentState{ID: "inside"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkpoints, "requested-checkpoint.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadCheckpoint("requested-checkpoint"); err == nil || !strings.Contains(err.Error(), "ID mismatch") {
		t.Fatalf("mismatched checkpoint load error=%v", err)
	}
}

func TestAgentStoreCleanupCheckpointsKeepsNewest(t *testing.T) {
	root := t.TempDir()
	store, err := NewAgentStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"agent-1-100", "agent-1-300", "agent-1-200"} {
		if err := store.SaveCheckpoint(&AgentCheckpoint{
			CheckpointID: id,
			AgentState:   &AgentState{ID: "agent-1"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.CleanupCheckpoints("agent-1", 1)
	if err != nil || removed != 2 {
		t.Fatalf("CleanupCheckpoints removed=%d err=%v", removed, err)
	}
	ids, err := store.ListCheckpoints("agent-1")
	if err != nil || len(ids) != 1 || ids[0] != "agent-1-300" {
		t.Fatalf("remaining checkpoints=%v err=%v, want newest", ids, err)
	}
}

func TestAgentCheckpointFilteringUsesExactOwner(t *testing.T) {
	root := t.TempDir()
	store, err := NewAgentStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range []*AgentCheckpoint{
		{CheckpointID: "agent-1-100", AgentState: &AgentState{ID: "agent-1"}},
		{CheckpointID: "agent-1-child-200", AgentState: &AgentState{ID: "agent-1-child"}},
	} {
		if err := store.SaveCheckpoint(cp); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := store.ListCheckpoints("agent-1")
	if err != nil || len(ids) != 1 || ids[0] != "agent-1-100" {
		t.Fatalf("exact agent checkpoints=%v err=%v", ids, err)
	}
	removed, err := store.CleanupCheckpoints("agent-1", 0)
	if err != nil || removed != 1 {
		t.Fatalf("cleanup exact owner removed=%d err=%v", removed, err)
	}
	childIDs, err := store.ListCheckpoints("agent-1-child")
	if err != nil || len(childIDs) != 1 || childIDs[0] != "agent-1-child-200" {
		t.Fatalf("child checkpoint was captured by parent cleanup: %v err=%v", childIDs, err)
	}
}

func TestSetPinnedContextUsesAtomicPrivateFile(t *testing.T) {
	root := t.TempDir()
	agent := &Agent{workDir: root}
	agent.SetPinnedContext("private project context")
	path := filepath.Join(root, ".gokin", "pinned_context.md")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "private project context" {
		t.Fatalf("pinned context=%q err=%v", got, err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("pinned context mode=%#o, want 0600", mode)
	}
	agent.SetPinnedContext("")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleared pinned context remains: %v", err)
	}
}
