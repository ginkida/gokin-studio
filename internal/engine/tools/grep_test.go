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

type mockPredictor struct{}

func (m *mockPredictor) RecordAccess(path, accessType, fromFile string) {}

func TestGrepTool_Name(t *testing.T) {
	tool := NewGrepTool("/tmp")
	if tool.Name() != "grep" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "grep")
	}
}

func TestGrepTool_Description(t *testing.T) {
	tool := NewGrepTool("/tmp")
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
}

func TestGrepTool_Declaration(t *testing.T) {
	tool := NewGrepTool("/tmp")
	decl := tool.Declaration()
	if decl == nil {
		t.Fatal("Declaration() is nil")
	}
	if decl.Name != "grep" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "grep")
	}
}

func TestGrepTool_Validate(t *testing.T) {
	tool := NewGrepTool("/tmp")
	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"with pattern", map[string]any{"pattern": "func"}, false},
		{"with file", map[string]any{"pattern": "func", "file": "test.go"}, false},
		{"with case_insensitive", map[string]any{"pattern": "func", "case_insensitive": true}, false},
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

func TestGrepTool_NewGrepTool(t *testing.T) {
	tool := NewGrepTool("/home/user/project")
	if tool == nil {
		t.Fatal("NewGrepTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGrepTool_SetGitIgnore(t *testing.T) {
	tool := NewGrepTool("/tmp")
	gi := git.NewGitIgnore("/tmp")
	tool.SetGitIgnore(gi)
	if tool.gitIgnore != gi {
		t.Error("SetGitIgnore() did not set gitIgnore")
	}
}

func TestGrepTool_SetCache(t *testing.T) {
	tool := NewGrepTool("/tmp")
	c := cache.NewSearchCache(100, 5*time.Minute)
	tool.SetCache(c)
	if tool.cache != c {
		t.Error("SetCache() did not set cache")
	}
}

func TestGrepTool_SetAllowedDirs(t *testing.T) {
	tool := NewGrepTool("/tmp")
	tool.SetAllowedDirs([]string{"/another/dir"})
	if tool.pathValidator == nil {
		t.Error("SetAllowedDirs() did not set pathValidator")
	}
}

func TestGrepTool_SetPredictor(t *testing.T) {
	tool := NewGrepTool("/tmp")
	p := &mockPredictor{}
	tool.SetPredictor(p)
	if tool.predictor != p {
		t.Error("SetPredictor() did not set predictor")
	}
}

func TestGrepTool_Execute_SimplePattern(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGrepTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "func"})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
	if !strings.Contains(result.Content, "Actionable summary:") {
		t.Fatalf("grep output missing actionable summary:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "read the most relevant file") {
		t.Fatalf("grep output missing next-step read guidance:\n%s", result.Content)
	}
}

func TestGrepTool_Execute_SpecificFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "match.go"), []byte("package main\nfunc test() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "no_match.txt"), []byte("no match here"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGrepTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"pattern": "func",
		"file":    "match.go",
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGrepTool_Execute_CaseInsensitive(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main\n\nfunc Test() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGrepTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"pattern":          "TEST",
		"case_insensitive": true,
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGrepTool_Execute_NoMatches(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("package main\n\nfunc test() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGrepTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "nonexistent"})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	_ = result.Success
}

func TestGrepTool_Execute_CountOnlyIncludesActionableSummary(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("func a() {}\nfunc b() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("func c() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGrepTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"pattern":    "func",
		"count_only": true,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() success = false: %s", result.Error)
	}
	for _, needle := range []string{
		"Actionable summary:",
		"a.go (2)",
		"run grep without count_only or read the top file",
	} {
		if !strings.Contains(result.Content, needle) {
			t.Fatalf("count_only output missing %q:\n%s", needle, result.Content)
		}
	}
}

func TestGrepTool_Execute_MultipleFiles(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	for i := range 3 {
		name := filepath.Join(tmpDir, "file"+string(rune('0'+i))+".go")
		if err := os.WriteFile(name, []byte("package main\nfunc test() {}\n"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	tool := NewGrepTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "func"})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGrepTool_Execute_WithContext(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("line1\nline2\nline3\nline4\nline5\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGrepTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"pattern":       "line3",
		"context_lines": 1,
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGrepTool_Execute_InvalidRegex(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(tmpDir, "test.go"), []byte("content"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tool := NewGrepTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "[invalid("})
	if err != nil {
		t.Logf("Execute() returned expected error for invalid regex: %v", err)
	}
	_ = result.Success
}

func TestGrepTool_Execute_NonexistentFile(t *testing.T) {
	tool := NewGrepTool(resolvedTempDir(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"pattern": "test",
		"file":    "nonexistent.go",
	})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	_ = result.Success
}
