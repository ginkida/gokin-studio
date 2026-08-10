package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginResourceToolConfinesReadsToEnabledPlugins(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "reporting", "skills", "daily", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("# Daily\nReviewed instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "disabled", "secret.md")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := NewPluginResourceTool(root, []string{"reporting"})
	list, err := tool.Execute(t.Context(), map[string]any{"action": "list", "plugin": "reporting"})
	if err != nil || !list.Success || !strings.Contains(list.Content, "reporting/skills/daily/SKILL.md") {
		t.Fatalf("list = %#v, %v", list, err)
	}
	read, err := tool.Execute(t.Context(), map[string]any{"action": "read", "path": "reporting/skills/daily/SKILL.md"})
	if err != nil || !read.Success || !strings.Contains(read.Content, "Reviewed instructions") {
		t.Fatalf("read = %#v, %v", read, err)
	}
	for _, path := range []string{"disabled/secret.md", "../outside", "/etc/passwd"} {
		result, _ := tool.Execute(t.Context(), map[string]any{"action": "read", "path": path})
		if result.Success {
			t.Errorf("unsafe read %q succeeded", path)
		}
	}
}

func TestPluginResourceToolRejectsSymlinkComponents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "reporting"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "reporting", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := NewPluginResourceTool(root, []string{"reporting"})
	result, _ := tool.Execute(t.Context(), map[string]any{
		"action": "read", "path": "reporting/linked/secret.md",
	})
	if result.Success {
		t.Fatal("symlink escape succeeded")
	}
}
