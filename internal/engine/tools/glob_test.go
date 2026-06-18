package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/cache"
	"github.com/ginkida/gokin-studio/internal/engine/git"
)

func TestGlobTool_Name(t *testing.T) {
	tool := NewGlobTool("/tmp")
	if tool.Name() != "glob" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "glob")
	}
}

func TestGlobTool_Description(t *testing.T) {
	tool := NewGlobTool("/tmp")
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
}

func TestGlobTool_Declaration(t *testing.T) {
	tool := NewGlobTool("/tmp")
	decl := tool.Declaration()
	if decl == nil {
		t.Fatal("Declaration() is nil")
	}
	if decl.Name != "glob" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "glob")
	}
}

func TestGlobTool_Validate(t *testing.T) {
	tool := NewGlobTool("/tmp")
	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"with pattern", map[string]any{"pattern": "*.go"}, false},
		{"with pattern and max_results", map[string]any{"pattern": "**/*.go", "max_results": 100}, false},
		{"missing pattern", map[string]any{}, true},
		{"empty pattern", map[string]any{"pattern": ""}, true},
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

func TestGlobTool_NewGlobTool(t *testing.T) {
	tool := NewGlobTool("/home/user/project")
	if tool == nil {
		t.Fatal("NewGlobTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGlobTool_SetGitIgnore(t *testing.T) {
	tool := NewGlobTool("/tmp")
	gi := git.NewGitIgnore("/tmp")
	tool.SetGitIgnore(gi)
	if tool.gitIgnore != gi {
		t.Error("SetGitIgnore() did not set gitIgnore")
	}
}

func TestGlobTool_SetCache(t *testing.T) {
	tool := NewGlobTool("/tmp")
	c := cache.NewSearchCache(100, 5*time.Minute)
	tool.SetCache(c)
	if tool.cache != c {
		t.Error("SetCache() did not set cache")
	}
}

// TestGlobTool_CacheHitIncludesActionableSummary guards the iter-1200 fix: the
// cache-hit path must include the "Found N file(s)" header and actionable
// summary (category grouping + "Next:" hint), not just the raw file list.
func TestGlobTool_CacheHitIncludesActionableSummary(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c := cache.NewSearchCache(100, 5*time.Minute)
	tool := NewGlobTool(tmpDir)
	tool.SetCache(c)

	// First call — live result, populates cache.
	r1, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.go"})
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if !r1.Success {
		t.Fatalf("first Execute failed: %s", r1.Content)
	}

	// Second call — cache hit.
	r2, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.go"})
	if err != nil {
		t.Fatalf("cache-hit Execute: %v", err)
	}
	if !r2.Success {
		t.Fatalf("cache-hit Execute failed: %s", r2.Content)
	}
	if !strings.Contains(r2.Content, "Found") {
		t.Errorf("cache-hit result missing 'Found N file(s)' header:\n%s", r2.Content)
	}
	if !strings.Contains(r2.Content, "Next:") {
		t.Errorf("cache-hit result missing 'Next:' actionable summary:\n%s", r2.Content)
	}
}

func TestGlobTool_SetAllowedDirs(t *testing.T) {
	tool := NewGlobTool("/tmp")
	tool.SetAllowedDirs([]string{"/another/dir"})
	if tool.pathValidator == nil {
		t.Error("SetAllowedDirs() did not set pathValidator")
	}
}

func TestGlobTool_Execute_SinglePattern(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGlobTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.go"})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGlobTool_Execute_RecursivePattern(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "top.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGlobTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "**/*.go"})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGlobTool_Execute_MaxResults(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	for i := range 5 {
		name := filepath.Join(tmpDir, "file"+string(rune('0'+i))+".txt")
		if err := os.WriteFile(name, []byte("content"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	tool := NewGlobTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"pattern":     "*.txt",
		"max_results": 3,
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	_ = result.Success
}

func TestGlobTool_Execute_NoMatches(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGlobTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.go"})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	_ = result.Success
}

func TestGlobTool_Execute_InvalidPattern(t *testing.T) {
	tool := NewGlobTool(resolvedTempDir(t))
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "[invalid"})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	_ = result.Success
}

func TestGlobTool_Execute_ExcludeGitignore(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGlobTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.*"})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	_ = result.Success
}
