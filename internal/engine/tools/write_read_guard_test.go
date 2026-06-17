package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteReadBeforeOverwriteGuard pins the read-before-overwrite fix: a full
// overwrite of an existing file the model never read this session is blocked (it
// would blindly discard unseen content). New files and append=true are exempt.
// Studio injects the tracker via ReadTrackerCtxKey rather than SetReadTracker.
func TestWriteReadBeforeOverwriteGuard(t *testing.T) {
	dir := resolvedTempDir(t)
	tool := NewWriteTool(dir)
	tracker := NewFileReadTracker()

	existing := filepath.Join(dir, "existing.go")
	if err := os.WriteFile(existing, []byte("package main\n// important\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Context without tracker — no guard, overwrite proceeds.
	res, _ := tool.Execute(context.Background(), map[string]any{
		"file_path": existing,
		"content":   "package main\n",
	})
	if !res.Success {
		t.Fatalf("overwrite without tracker should succeed (no guard), got: %s", res.Error)
	}

	// Restore the file.
	if err := os.WriteFile(existing, []byte("package main\n// important\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Context WITH tracker, file NOT read → guard fires.
	ctx := context.WithValue(context.Background(), ReadTrackerCtxKey{}, tracker)
	res, _ = tool.Execute(ctx, map[string]any{
		"file_path": existing,
		"content":   "package main\n",
	})
	if res.Success {
		t.Fatal("overwrite of unread existing file was allowed; guard didn't fire")
	}
	if !strings.Contains(res.Error, "read-before-overwrite") {
		t.Errorf("error = %q, want read-before-overwrite guidance", res.Error)
	}
	// The file must be untouched.
	if b, _ := os.ReadFile(existing); !strings.Contains(string(b), "important") {
		t.Error("blocked write still modified the file")
	}

	// A brand-new file is always allowed (nothing to discard).
	newFile := filepath.Join(dir, "fresh.go")
	if res, _ := tool.Execute(ctx, map[string]any{
		"file_path": newFile, "content": "package main\n",
	}); !res.Success {
		t.Errorf("writing a NEW file was blocked: %s", res.Error)
	}

	// append=true on an existing unread file is allowed (no content lost).
	if res, _ := tool.Execute(ctx, map[string]any{
		"file_path": existing, "content": "// more\n", "append": true,
	}); !res.Success {
		t.Errorf("append to existing file was blocked: %s", res.Error)
	}

	// After the file is recorded as read, the overwrite goes through.
	tracker.CheckAndRecord(existing, 0, 0, 100)
	if res, _ := tool.Execute(ctx, map[string]any{
		"file_path": existing, "content": "package main\n// rewritten\n",
	}); !res.Success {
		t.Errorf("overwrite after read was blocked: %s", res.Error)
	}
}
