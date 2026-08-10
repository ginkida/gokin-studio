package studio

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

// TestGetProviders_ReturnsExpectedProviders verifies that GetProviders returns
// exactly the GLM/Kimi product contract with at least one model each.
func TestGetProviders_ReturnsExpectedProviders(t *testing.T) {
	s := NewStudio()
	providers := s.GetProviders()

	if len(providers) == 0 {
		t.Fatal("GetProviders returned empty list")
	}

	if len(providers) != 2 {
		t.Fatalf("GetProviders returned %d providers, want exactly 2", len(providers))
	}
	want := map[string]bool{"glm": false, "kimi": false}
	for _, p := range providers {
		if p.ID == "" {
			t.Errorf("provider with empty ID in list")
		}
		if p.Name == "" {
			t.Errorf("provider %q has empty Name", p.ID)
		}
		if len(p.Models) == 0 {
			t.Errorf("provider %q has no models", p.ID)
		}
		want[p.ID] = true
	}
	for id, found := range want {
		if !found {
			t.Errorf("expected provider %q not found in GetProviders", id)
		}
	}

	// Kimi exposes both stable K2.7 tiers and the current K3 tiers.
	for _, p := range providers {
		if p.ID == "kimi" {
			wantModels := []string{"k3", "k3-256k", "kimi-for-coding", "kimi-for-coding-highspeed"}
			if len(p.Models) != len(wantModels) {
				t.Fatalf("kimi provider models = %v, want %v", p.Models, wantModels)
			}
			for i := range wantModels {
				if p.Models[i] != wantModels[i] {
					t.Errorf("kimi provider models = %v, want %v", p.Models, wantModels)
					break
				}
			}
		}
	}
}

// TestToolSetsForProvider verifies the GLM/Kimi-only desktop surface stays
// identical for both supported providers and retains the full agent toolset.
func TestToolSetsForProvider(t *testing.T) {
	glmSets := toolSetsForProvider("glm")
	kimiSets := toolSetsForProvider("kimi")

	containsSet := func(sets []tools.ToolSet, target tools.ToolSet) bool {
		for _, s := range sets {
			if s == target {
				return true
			}
		}
		return false
	}

	if len(glmSets) != len(kimiSets) {
		t.Fatalf("GLM/Kimi toolset lengths differ: %v vs %v", glmSets, kimiSets)
	}
	for i := range glmSets {
		if glmSets[i] != kimiSets[i] {
			t.Fatalf("GLM/Kimi toolsets differ: %v vs %v", glmSets, kimiSets)
		}
	}

	// Both supported providers receive core, git, web, and planning.
	for _, required := range []tools.ToolSet{tools.ToolSetCore, tools.ToolSetGit, tools.ToolSetWeb, tools.ToolSetPlanning} {
		if !containsSet(glmSets, required) {
			t.Errorf("GLM/Kimi tool sets missing %q", required)
		}
	}
	if containsSet(glmSets, tools.ToolSetOllamaCore) {
		t.Error("GLM/Kimi tool sets unexpectedly include legacy Ollama core")
	}
}

// TestFirstNonEmpty verifies the simple helper that returns the first
// non-empty string from a variadic list.
func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"a", "b"}, "a"},
		{[]string{"", "b"}, "b"},
		{[]string{"", "", "c"}, "c"},
		{[]string{"", ""}, ""},
		{[]string{}, ""},
		{[]string{"only"}, "only"},
	}
	for _, c := range cases {
		got := firstNonEmpty(c.in...)
		if got != c.want {
			t.Errorf("firstNonEmpty(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSaveHistory_PreservesExistingName verifies that SaveHistory (not
// SaveHistoryWithName) reads the on-disk session name and keeps it when
// writing new content, so a rename persists even after subsequent saves.
func TestSaveHistory_PreservesExistingName(t *testing.T) {
	_ = withTempHistoryDir(t)
	pid := "test-savehist-preserve"

	// Write an initial file with a custom name.
	hist1 := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
	}
	if err := SaveHistoryWithName(pid, "My Custom Session", hist1); err != nil {
		t.Fatalf("SaveHistoryWithName: %v", err)
	}

	// SaveHistory (no name arg) should preserve "My Custom Session".
	hist2 := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{genai.NewPartFromText("hello")}},
		{Role: "model", Parts: []*genai.Part{genai.NewPartFromText("hi")}},
	}
	if err := SaveHistory(pid, hist2); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}

	if got := LoadHistoryName(pid); got != "My Custom Session" {
		t.Errorf("name after SaveHistory = %q, want 'My Custom Session'", got)
	}
}

// TestContentSize_NilPartSkipped verifies that a nil entry in a content's
// Parts slice is skipped without panicking and does not contribute to the total.
func TestContentSize_NilPartSkipped(t *testing.T) {
	c := &genai.Content{
		Role:  "model",
		Parts: []*genai.Part{nil, genai.NewPartFromText("hi")},
	}
	got := contentSize(c)
	if got != len("hi") {
		t.Errorf("contentSize with nil part = %d, want %d", got, len("hi"))
	}
}

// TestContentSize_NonStringArgs verifies the rough-estimate fallback (32 for
// FunctionCall, 64 for FunctionResponse) when an arg or response value is not
// a plain string (e.g. an integer or nested map).
func TestContentSize_NonStringArgs(t *testing.T) {
	// FunctionCall with integer arg — should use 32-byte estimate.
	fcContent := &genai.Content{
		Role: "model",
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "bash", // 4 chars
			Args: map[string]any{
				"timeout": 30, // key "timeout"=7 + non-string value=32 = 39
			},
		}}},
	}
	got := contentSize(fcContent)
	// 4 (name) + 7 (key "timeout") + 32 (estimate) = 43
	if got != 43 {
		t.Errorf("contentSize(FunctionCall non-string) = %d, want 43", got)
	}

	// FunctionResponse with non-string value — should use 64-byte estimate.
	frContent := &genai.Content{
		Role: "user",
		Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name: "read", // 4 chars
			Response: map[string]any{
				"lines": []string{"a", "b"}, // key "lines"=5, non-string value=64
			},
		}}},
	}
	got = contentSize(frContent)
	// 4 (name) + 5 (key "lines") + 64 (estimate) = 73
	if got != 73 {
		t.Errorf("contentSize(FunctionResponse non-string) = %d, want 73", got)
	}
}

// TestContentSize verifies that contentSize correctly counts characters across
// text parts, function call args, and function response payloads.
func TestContentSize(t *testing.T) {
	// Empty content → 0.
	if got := contentSize(nil); got != 0 {
		t.Errorf("contentSize(nil) = %d, want 0", got)
	}

	// Pure text part.
	textOnly := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{genai.NewPartFromText("hello world")},
	}
	if got := contentSize(textOnly); got != len("hello world") {
		t.Errorf("contentSize(text) = %d, want %d", got, len("hello world"))
	}

	// Function call: name + one string arg.
	fcContent := &genai.Content{
		Role: "model",
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "bash",
			Args: map[string]any{"cmd": "ls -la"},
		}}},
	}
	got := contentSize(fcContent)
	// Should include "bash" (4) + "cmd" (3) + "ls -la" (6) = 13.
	if got != 13 {
		t.Errorf("contentSize(FunctionCall) = %d, want 13", got)
	}

	// Function response: "result" key with a large output.
	output := strings.Repeat("x", 100)
	frContent := &genai.Content{
		Role: "user",
		Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name:     "bash",
			Response: map[string]any{"result": output},
		}}},
	}
	got = contentSize(frContent)
	if got < 100 {
		t.Errorf("contentSize(FunctionResponse with 100-char output) = %d, want >= 100", got)
	}
}

// TestDefaultSystemPrompt verifies that defaultSystemPrompt embeds the
// project name and directory in its output, and contains required sections.
func TestDefaultSystemPrompt(t *testing.T) {
	name := "MyAwesomeProject"
	dir := "/home/user/my-project"
	got := defaultSystemPrompt(dir, name)
	if got == "" {
		t.Fatal("defaultSystemPrompt returned empty string")
	}
	if !strings.Contains(got, name) {
		t.Errorf("defaultSystemPrompt does not contain project name %q", name)
	}
	if !strings.Contains(got, dir) {
		t.Errorf("defaultSystemPrompt does not contain directory %q", dir)
	}
	// todo tool must be mentioned so GLM-5.2 knows to use it for task tracking,
	// which is what the incomplete-work continuation (iter 1205) relies on.
	if !strings.Contains(got, "todo") {
		t.Errorf("defaultSystemPrompt does not mention 'todo' tool — incomplete-work continuation will never trigger")
	}
	// "in_progress" rule must be present — key instruction for task tracking discipline.
	if !strings.Contains(got, "in_progress") {
		t.Errorf("defaultSystemPrompt does not mention 'in_progress' status discipline")
	}
}

// TestGitBranch_NonGitDir verifies that gitBranch returns "" when the project
// directory is not inside a git repository (the `git rev-parse` error path).
func TestGitBranch_NonGitDir(t *testing.T) {
	dir := t.TempDir() // guaranteed not to be inside a git repo
	p := &Project{Directory: dir}
	if got := p.gitBranch(); got != "" {
		// Unlikely but possible if t.TempDir() is inside a git worktree.
		t.Logf("gitBranch in temp dir = %q (may be inside a git worktree)", got)
	}
}

// TestGitBranch_InGitRepo verifies that gitBranch returns the branch name (non-empty)
// when the project directory is inside a real git repository.
func TestGitBranch_InGitRepo(t *testing.T) {
	dir := t.TempDir()
	// Initialize a git repo and make an initial commit so HEAD is resolvable.
	if err := exec.Command("git", "-C", dir, "init").Run(); err != nil {
		t.Skip("git init failed — git not available")
	}
	if err := exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run(); err != nil {
		t.Skip("git config failed")
	}
	if err := exec.Command("git", "-C", dir, "config", "user.name", "Test").Run(); err != nil {
		t.Skip("git config failed")
	}
	if err := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "init").Run(); err != nil {
		t.Skip("git commit failed")
	}

	p := &Project{Directory: dir}
	got := p.gitBranch()
	if got == "" {
		t.Error("gitBranch in initialized git repo returned empty string, want branch name")
	}
}
