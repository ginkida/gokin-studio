package tools

// Tests for the read-before-edit safety invariant.
// Studio implements this via a *FileReadTracker injected through context
// (ReadTrackerCtxKey), unlike the gokin CLI which uses SetReadTracker/
// SetRequireReadBeforeEdit struct methods. Both enforce the same semantics:
// an existing file must be read in the current session before it can be edited.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// editCtxWithReadTracker returns a context with a fresh FileReadTracker
// seeded with a record for target (simulating a prior read).
func editCtxWithReadTracker(target string) context.Context {
	tracker := NewFileReadTracker()
	tracker.CheckAndRecord(target, 1, 100, 25)
	return context.WithValue(context.Background(), ReadTrackerCtxKey{}, tracker)
}

// editCtxNoRead returns a context with a tracker but WITHOUT recording target,
// simulating "file exists but wasn't read this session".
func editCtxNoRead() context.Context {
	tracker := NewFileReadTracker()
	return context.WithValue(context.Background(), ReadTrackerCtxKey{}, tracker)
}

// TestEditTool_BlocksWithoutPriorRead verifies that the guard fires for an
// existing file when the tracker has no record for it.
func TestEditTool_BlocksWithoutPriorRead(t *testing.T) {
	dir := resolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	et := NewEditTool(dir)

	result, err := et.Execute(editCtxNoRead(), map[string]any{
		"file_path":  target,
		"old_string": "Foo",
		"new_string": "Bar",
	})
	if err != nil {
		t.Fatalf("Execute returned transport error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure when file wasn't read first")
	}
	if !strings.Contains(result.Error, "read-before-edit") {
		t.Errorf("error must name the invariant, got: %q", result.Error)
	}

	// File must be untouched.
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "Foo") {
		t.Errorf("file modified despite blocked edit: %s", data)
	}
}

// TestEditTool_AllowsAfterPriorRead verifies that a recorded read unlocks the edit.
func TestEditTool_AllowsAfterPriorRead(t *testing.T) {
	dir := resolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	et := NewEditTool(dir)

	result, err := et.Execute(editCtxWithReadTracker(target), map[string]any{
		"file_path":  target,
		"old_string": "Foo",
		"new_string": "Bar",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success; got error: %s", result.Error)
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "Bar") {
		t.Errorf("edit didn't apply: %s", data)
	}
}

// TestEditTool_NoTrackerInContext verifies that when no tracker is in the context
// at all, edit goes through (backward-compat: stripped-down harnesses without
// a tracker must not be blocked or panic).
func TestEditTool_NoTrackerInContext(t *testing.T) {
	dir := resolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	et := NewEditTool(dir)

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "Foo",
		"new_string": "Bar",
	})
	if err != nil || !result.Success {
		t.Fatalf("no tracker in ctx must skip check silently; err=%v success=%v error=%s",
			err, result.Success, result.Error)
	}
}

// TestEditTool_InvalidatedFileRequiresReRead pins the "mutation resets
// read-knowledge" invariant: after InvalidateFile, a subsequent edit must fail
// because the tracker no longer considers the file known.
func TestEditTool_InvalidatedFileRequiresReRead(t *testing.T) {
	dir := resolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tracker := NewFileReadTracker()
	tracker.CheckAndRecord(target, 1, 100, 25)
	tracker.InvalidateFile(target)

	ctx := context.WithValue(context.Background(), ReadTrackerCtxKey{}, tracker)
	et := NewEditTool(dir)

	result, err := et.Execute(ctx, map[string]any{
		"file_path":  target,
		"old_string": "Foo",
		"new_string": "Bar",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("expected block after invalidation")
	}
	if !strings.Contains(result.Error, "read-before-edit") {
		t.Errorf("missing invariant marker: %q", result.Error)
	}
}

// TestEditTool_NonexistentFileBypassesCheck verifies that a missing file
// surfaces the "file not found" diagnostic, not the read-before-edit message.
func TestEditTool_NonexistentFileBypassesCheck(t *testing.T) {
	dir := resolvedTempDir(t)
	target := filepath.Join(dir, "never-created.go")

	et := NewEditTool(dir)

	result, err := et.Execute(editCtxNoRead(), map[string]any{
		"file_path":  target,
		"old_string": "a",
		"new_string": "b",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected an error for missing file")
	}
	if strings.Contains(result.Error, "read-before-edit") {
		t.Errorf("should surface file-not-found, not read-before-edit: %q", result.Error)
	}
	if !strings.Contains(result.Error, "file not found") {
		t.Errorf("expected 'file not found' diagnostic, got: %q", result.Error)
	}
}

// TestEditTool_MultiEditRespectsInvariant spot-checks that the multi-edit
// branch (edits=[]) also enforces the invariant, not only single string-replace.
func TestEditTool_MultiEditRespectsInvariant(t *testing.T) {
	dir := resolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	et := NewEditTool(dir)

	result, err := et.Execute(editCtxNoRead(), map[string]any{
		"file_path": target,
		"edits": []any{
			map[string]any{"old_string": "Foo", "new_string": "Bar"},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("multi-edit must also be blocked without prior read")
	}
	if !strings.Contains(result.Error, "read-before-edit") {
		t.Errorf("expected invariant marker in multi-edit path: %q", result.Error)
	}
}

// TestFileReadTracker_HasBeenReadSemantics exercises the tracker's full
// lifecycle: fresh tracker → not read; CheckAndRecord → read; InvalidateFile
// → not read; empty path → false.
func TestFileReadTracker_HasBeenReadSemantics(t *testing.T) {
	tracker := NewFileReadTracker()
	dir := resolvedTempDir(t)
	target := filepath.Join(dir, "x.go")
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if tracker.HasBeenRead(target) {
		t.Error("fresh tracker should not report read")
	}
	tracker.CheckAndRecord(target, 1, 100, 10)
	if !tracker.HasBeenRead(target) {
		t.Error("record not registered")
	}
	tracker.InvalidateFile(target)
	if tracker.HasBeenRead(target) {
		t.Error("invalidation should drop the record")
	}
	if tracker.HasBeenRead("") {
		t.Error("empty path must return false")
	}
}
