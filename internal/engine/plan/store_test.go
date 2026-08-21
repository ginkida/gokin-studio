package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanStoreRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	store, err := NewPlanStore(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := NewPlan("unsafe", "unsafe")
	plan.ID = "../escape"
	if err := store.Save(plan); err == nil || !strings.Contains(err.Error(), "invalid plan ID") {
		t.Fatalf("Save traversal error=%v", err)
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
	if _, err := os.Stat(filepath.Join(root, "escape.json")); !os.IsNotExist(err) {
		t.Fatalf("plan traversal created outside file: %v", err)
	}
}

func TestPlanStoreUsesAtomicPrivateFile(t *testing.T) {
	root := t.TempDir()
	store, err := NewPlanStore(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := NewPlan("durable", "state")
	plan.ID = "plan-1"
	if err := store.Save(plan); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "plans", plan.ID+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("plan mode=%#o, want 0600", got)
	}
	loaded, err := store.Load(plan.ID)
	if err != nil || loaded.Title != plan.Title {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestPlanStoreSnapshotsAfterClaimingPublicationOrder(t *testing.T) {
	root := t.TempDir()
	store, err := NewPlanStore(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := NewPlan("old", "state")
	plan.ID = "plan-order"

	// Stop Save immediately before it claims the store lock, and independently
	// hold that lock. With the former snapshot-before-lock order, "old" had
	// already been captured when this hook ran; the fixed order observes "new".
	started := make(chan struct{})
	release := make(chan struct{})
	store.beforeSaveLock = func() {
		close(started)
		<-release
	}
	store.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- store.Save(plan) }()
	<-started
	plan.mu.Lock()
	plan.Title = "new"
	plan.mu.Unlock()
	close(release)
	store.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "new" {
		t.Fatalf("persisted title=%q, want state observed after publication order was claimed", loaded.Title)
	}
}

func TestPlanStoreRejectsSymlinkAndMismatchedRecord(t *testing.T) {
	root := t.TempDir()
	store, err := NewPlanStore(root)
	if err != nil {
		t.Fatal(err)
	}

	targetPlan := NewPlan("outside", "must not be followed")
	targetPlan.ID = "linked"
	targetPlan.UpdatedAt = time.Now().Add(-24 * time.Hour)
	data, err := json.Marshal(targetPlan)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "plans", "linked.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.Load("linked"); err == nil {
		t.Fatal("Load followed a symlinked plan")
	}
	if store.Exists("linked") {
		t.Fatal("Exists accepted a symlinked plan")
	}
	if plans, err := store.List(); err != nil || len(plans) != 0 {
		t.Fatalf("List included symlink: %+v err=%v", plans, err)
	}
	if cleaned, err := store.Cleanup(time.Hour); err != nil || cleaned != 0 {
		t.Fatalf("Cleanup followed symlink: cleaned=%d err=%v", cleaned, err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("outside symlink target changed: %v", err)
	}

	mismatch := NewPlan("mismatch", "identity")
	mismatch.ID = "inside-id"
	data, err = json.Marshal(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plans", "requested-id.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("requested-id"); err == nil || !strings.Contains(err.Error(), "ID mismatch") {
		t.Fatalf("mismatched plan load error=%v", err)
	}
}
