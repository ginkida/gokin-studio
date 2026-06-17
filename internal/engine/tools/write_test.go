package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/undo"
)

func TestWriteTool_Name(t *testing.T) {
	tool := NewWriteTool("/tmp")
	if tool.Name() != "write" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "write")
	}
}

func TestWriteTool_Description(t *testing.T) {
	tool := NewWriteTool("/tmp")
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
}

func TestWriteTool_Declaration(t *testing.T) {
	tool := NewWriteTool("/tmp")
	decl := tool.Declaration()
	if decl == nil {
		t.Fatal("Declaration() is nil")
	}
	if decl.Name != "write" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "write")
	}
}

func TestWriteTool_Validate(t *testing.T) {
	tool := NewWriteTool("/tmp")
	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"valid args", map[string]any{"file_path": "/tmp/test.txt", "content": "hello"}, false},
		{"missing file_path", map[string]any{"content": "hello"}, true},
		{"missing content", map[string]any{"file_path": "/tmp/test.txt"}, true},
		{"empty file_path", map[string]any{"file_path": "", "content": "hello"}, true},
		{"empty content", map[string]any{"file_path": "/tmp/test.txt", "content": ""}, true},
		{"empty content append", map[string]any{"file_path": "/tmp/test.txt", "content": "", "append": true}, false},
		{"nil args", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tool.Validate(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWriteTool_NewWriteTool(t *testing.T) {
	tool := NewWriteTool("/tmp")
	if tool == nil {
		t.Fatal("NewWriteTool() returned nil")
	}
	if tool.workDir != "/tmp" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/tmp")
	}
	if tool.pathValidator == nil {
		t.Error("pathValidator is nil")
	}
}

func TestWriteTool_SetUndoManager(t *testing.T) {
	tool := NewWriteTool("/tmp")
	manager := undo.NewManager()
	tool.SetUndoManager(manager)
	if tool.undoManager == nil {
		t.Error("undoManager is nil after SetUndoManager")
	}
}

// writeMockDiffHandler implements DiffHandler for write_test.go.
// Named distinctly from any other mock in the package.
type writeMockDiffHandler struct{ approve bool }

func (m *writeMockDiffHandler) PromptDiff(_ context.Context, _, _, _, _ string, _ bool) (bool, error) {
	return m.approve, nil
}

func TestWriteTool_SetDiffHandler(t *testing.T) {
	tool := NewWriteTool("/tmp")
	handler := &writeMockDiffHandler{approve: true}
	tool.SetDiffHandler(handler)
	if tool.diffHandler != handler {
		t.Error("diffHandler not set correctly")
	}
}

func TestWriteTool_SetDiffEnabled(t *testing.T) {
	tool := NewWriteTool("/tmp")
	tool.SetDiffEnabled(true)
	if !tool.diffEnabled {
		t.Error("diffEnabled = false, want true")
	}
	tool.SetDiffEnabled(false)
	if tool.diffEnabled {
		t.Error("diffEnabled = true, want false")
	}
}

func TestWriteTool_SetAllowedDirs(t *testing.T) {
	tool := NewWriteTool("/tmp")
	tool.SetAllowedDirs([]string{"/var", "/opt"})
	if tool.pathValidator == nil {
		t.Error("pathValidator is nil after SetAllowedDirs")
	}
}

func TestWriteTool_Execute_CreateNewFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "new_file.txt")
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "hello world",
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read created file: %v", err)
	}
	if string(content) != "hello world" {
		t.Errorf("File content = %v, want %v", string(content), "hello world")
	}
}

// TestWriteTool_Execute_OverwriteFile verifies that overwriting an existing
// file succeeds when it has been read in the same session (read-before-overwrite
// guard). Studio requires a prior read; use ReadTrackerCtxKey to simulate one.
func TestWriteTool_Execute_OverwriteFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "existing_file.txt")
	if err := os.WriteFile(filePath, []byte("old content"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Simulate a prior read so the guard allows the overwrite.
	tracker := NewFileReadTracker()
	tracker.CheckAndRecord(filePath, 1, 100, 11)
	ctx := context.WithValue(context.Background(), ReadTrackerCtxKey{}, tracker)

	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "new content",
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	if string(content) != "new content" {
		t.Errorf("File content = %v, want %v", string(content), "new content")
	}
}

func TestWriteTool_Execute_AppendMode(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "append_file.txt")
	if err := os.WriteFile(filePath, []byte("line 1\n"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// append=true is exempt from the read-before-overwrite guard.
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "line 2\n",
		"append":    true,
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	if string(content) != "line 1\nline 2\n" {
		t.Errorf("File content = %v, want %v", string(content), "line 1\nline 2\n")
	}
}

func TestWriteTool_Execute_CreateParentDirs(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "subdir1", "subdir2", "nested_file.txt")
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "nested content",
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file in nested directory: %v", err)
	}
	if string(content) != "nested content" {
		t.Errorf("File content = %v, want %v", string(content), "nested content")
	}
}

func TestWriteTool_Execute_EmptyContent(t *testing.T) {
	tool := NewWriteTool("/tmp")
	// Empty content should be rejected at validation.
	if err := tool.Validate(map[string]any{
		"file_path": "/tmp/empty_file.txt",
		"content":   "",
	}); err == nil {
		t.Error("Validate() should reject empty content")
	}
	// Empty content in append mode is fine (no-op).
	if err := tool.Validate(map[string]any{
		"file_path": "/tmp/empty_file.txt",
		"content":   "",
		"append":    true,
	}); err != nil {
		t.Errorf("Validate() should allow empty content in append mode: %v", err)
	}
}

func TestWriteTool_Execute_DiffEnabled_Approved(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)
	tool.SetDiffEnabled(true)
	tool.SetDiffHandler(&writeMockDiffHandler{approve: true})

	filePath := filepath.Join(tmpDir, "diff_file.txt")
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "approved content",
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() should succeed when diff is approved: %s", result.Error)
	}
}

func TestWriteTool_Execute_DiffEnabled_Rejected(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)
	tool.SetDiffEnabled(true)
	tool.SetDiffHandler(&writeMockDiffHandler{approve: false})

	filePath := filepath.Join(tmpDir, "diff_file.txt")
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "rejected content",
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when diff is rejected")
	}
	if result.Error != "changes rejected by user" {
		t.Errorf("Error = %v, want %v", result.Error, "changes rejected by user")
	}
}

func TestWriteTool_Execute_DiffEnabled_FileCreated(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)
	tool.SetDiffEnabled(true)
	tool.SetDiffHandler(&writeMockDiffHandler{approve: true})

	filePath := filepath.Join(tmpDir, "new_file.txt")
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "new file content",
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() should succeed for new file: %s", result.Error)
	}
}

func TestWriteTool_Execute_AppendToNewFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "append_to_new.txt")
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"content":   "content",
		"append":    true,
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() should succeed: %s", result.Error)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	if string(content) != "content" {
		t.Errorf("Content = %v, want %v", string(content), "content")
	}
}

func TestWriteTool_Execute_PathValidation(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": "/etc/passwd",
		"content":   "hacking",
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail for path outside workDir")
	}
}

// TestWriteTool_Execute_ReadBeforeOverwriteGuard pins the v0.86.0 fix:
// a full overwrite of an existing file the model never read is blocked.
// New files and append=true are exempt.
func TestWriteTool_Execute_ReadBeforeOverwriteGuard(t *testing.T) {
	dir := resolvedTempDir(t)
	tool := NewWriteTool(dir)
	existing := filepath.Join(dir, "existing.go")
	if err := os.WriteFile(existing, []byte("package main\n// important\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracker := NewFileReadTracker()
	ctx := context.WithValue(context.Background(), ReadTrackerCtxKey{}, tracker)

	// Overwriting without prior read is blocked.
	res, _ := tool.Execute(ctx, map[string]any{
		"file_path": existing,
		"content":   "package main\n",
	})
	if res.Success {
		t.Fatal("overwrite of unread existing file was allowed; guard didn't fire")
	}
	if b, _ := os.ReadFile(existing); string(b) != "package main\n// important\n" {
		t.Error("blocked write still modified the file")
	}

	// A brand-new file is always allowed.
	newFile := filepath.Join(dir, "fresh.go")
	if res, _ := tool.Execute(ctx, map[string]any{
		"file_path": newFile, "content": "package main\n",
	}); !res.Success {
		t.Errorf("writing a NEW file was blocked: %s", res.Error)
	}

	// append=true on an existing unread file is allowed.
	if res, _ := tool.Execute(ctx, map[string]any{
		"file_path": existing, "content": "// more\n", "append": true,
	}); !res.Success {
		t.Errorf("append to existing file was blocked: %s", res.Error)
	}

	// After recording a read, overwrite goes through.
	tracker.CheckAndRecord(existing, 0, 0, 100)
	if res, _ := tool.Execute(ctx, map[string]any{
		"file_path": existing, "content": "package main\n// rewritten\n",
	}); !res.Success {
		t.Errorf("overwrite after read was blocked: %s", res.Error)
	}
}

// Note: TestGetBoolDefault is defined in tools_test.go and covers GetBoolDefault.
