package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/memory"
	"github.com/ginkida/gokin-studio/internal/engine/plan"
	"google.golang.org/genai"
)

// ──────────────────────────────────────────────────────────────────────────────
// pin_context
// ──────────────────────────────────────────────────────────────────────────────

func TestPinContext_SetAndClear(t *testing.T) {
	dir := t.TempDir()
	var pinned string
	tool := NewPinContextTool(func(c string) { pinned = c })
	tool.SetWorkDir(dir)

	// Pin some content.
	result, err := tool.Execute(context.Background(), map[string]any{"content": "keep this"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
	if pinned != "keep this" {
		t.Errorf("pinned = %q, want %q", pinned, "keep this")
	}
	// Verify file was written.
	pinPath := filepath.Join(dir, ".gokin", "pinned_context.md")
	if _, err := os.Stat(pinPath); err != nil {
		t.Errorf("pin file should exist after pin: %v", err)
	}

	// Clear via empty string (the bug we fixed — must treat "" as clear).
	result, err = tool.Execute(context.Background(), map[string]any{"content": ""})
	if err != nil {
		t.Fatalf("unexpected error on clear: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success on clear, got: %s", result.Error)
	}
	if pinned != "" {
		t.Errorf("pinned should be empty after clear, got %q", pinned)
	}
	if _, err := os.Stat(pinPath); !os.IsNotExist(err) {
		t.Error("pin file should be removed after clear")
	}
}

func TestPinContext_ClearFlag(t *testing.T) {
	var pinned string
	tool := NewPinContextTool(func(c string) { pinned = c })
	tool.SetWorkDir(t.TempDir())

	// Pin then clear with explicit clear=true.
	tool.Execute(context.Background(), map[string]any{"content": "note"})
	result, err := tool.Execute(context.Background(), map[string]any{"content": "ignored", "clear": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success: %s", result.Error)
	}
	if pinned != "" {
		t.Errorf("pinned should be empty after clear=true, got %q", pinned)
	}
}

func TestPinContext_ClearFlagIgnoresStaleOversizedContent(t *testing.T) {
	dir := t.TempDir()
	pinned := "existing"
	tool := NewPinContextTool(func(c string) { pinned = c })
	tool.SetWorkDir(dir)
	if err := tool.Validate(map[string]any{
		"content": strings.Repeat("x", MaxPinnedContextBytes+1),
		"clear":   true,
	}); err != nil {
		t.Fatalf("clear request rejected by stale content: %v", err)
	}
	result, err := tool.Execute(context.Background(), map[string]any{
		"content": strings.Repeat("x", MaxPinnedContextBytes+1),
		"clear":   true,
	})
	if err != nil || !result.Success || pinned != "" {
		t.Fatalf("clear result=%#v err=%v pinned=%q", result, err, pinned)
	}
}

func TestPinContext_PersistFailureDoesNotChangeMemory(t *testing.T) {
	dir := t.TempDir()
	blockedWorkDir := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockedWorkDir, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	pinned := "previous value"
	tool := NewPinContextTool(func(c string) { pinned = c })
	tool.SetWorkDir(blockedWorkDir)
	result, err := tool.Execute(context.Background(), map[string]any{"content": "new value"})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "failed to persist") {
		t.Fatalf("expected persistence failure, got %#v", result)
	}
	if pinned != "previous value" {
		t.Fatalf("memory changed despite persistence failure: %q", pinned)
	}
}

func TestPinContext_ClearFailureDoesNotChangeMemory(t *testing.T) {
	dir := t.TempDir()
	pinPath := filepath.Join(dir, ".gokin", "pinned_context.md")
	if err := os.MkdirAll(filepath.Join(pinPath, "child"), 0750); err != nil {
		t.Fatal(err)
	}

	pinned := "keep me"
	tool := NewPinContextTool(func(c string) { pinned = c })
	tool.SetWorkDir(dir)
	result, err := tool.Execute(context.Background(), map[string]any{"content": "", "clear": true})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "failed to clear") {
		t.Fatalf("expected clear failure, got %#v", result)
	}
	if pinned != "keep me" {
		t.Fatalf("memory changed despite clear failure: %q", pinned)
	}
}

func TestPinContext_HonorsCancellationBeforeMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	tool := NewPinContextTool(func(string) { called = true })
	tool.SetWorkDir(t.TempDir())

	result, err := tool.Execute(ctx, map[string]any{"content": "must not persist"})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "cancelled") {
		t.Fatalf("expected cancellation result, got %#v", result)
	}
	if called {
		t.Fatal("updater called for a cancelled operation")
	}
}

func TestPinContext_PersistsPrivatelyAndReturnsValidUTF8(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("🙂", 121)
	tool := NewPinContextTool(func(string) {})
	tool.SetWorkDir(dir)

	result, err := tool.Execute(context.Background(), map[string]any{"content": content})
	if err != nil || !result.Success {
		t.Fatalf("Execute() = %#v, %v", result, err)
	}
	if !utf8.ValidString(result.Content) {
		t.Fatalf("success preview is invalid UTF-8: %q", result.Content)
	}
	info, err := os.Stat(filepath.Join(dir, ".gokin", "pinned_context.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("pin permissions = %#o, want 0600", got)
	}
}

func TestPinContext_NilUpdater(t *testing.T) {
	tool := NewPinContextTool(nil)
	result, err := tool.Execute(context.Background(), map[string]any{"content": "note"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when updater is nil")
	}
}

func TestPinContext_LoadPersistedPin(t *testing.T) {
	dir := t.TempDir()
	// Pre-write a pin file.
	pinPath := filepath.Join(dir, ".gokin", "pinned_context.md")
	os.MkdirAll(filepath.Dir(pinPath), 0750)
	os.WriteFile(pinPath, []byte("restored note"), 0644)

	var pinned string
	tool := NewPinContextTool(func(c string) { pinned = c })
	tool.SetWorkDir(dir)
	tool.LoadPersistedPin()

	if pinned != "restored note" {
		t.Errorf("LoadPersistedPin: pinned = %q, want %q", pinned, "restored note")
	}
}

func TestPinContext_LoadPersistedPin_NoFile(t *testing.T) {
	var called bool
	tool := NewPinContextTool(func(c string) { called = true })
	tool.SetWorkDir(t.TempDir())
	tool.LoadPersistedPin() // no file exists — should be a no-op
	if called {
		t.Error("updater should not be called when no pin file exists")
	}
}

func TestPinContext_RejectsOversizedContentWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	called := false
	tool := NewPinContextTool(func(string) { called = true })
	tool.SetWorkDir(dir)
	content := strings.Repeat("x", MaxPinnedContextBytes+1)

	if err := tool.Validate(map[string]any{"content": content}); err == nil {
		t.Fatal("Validate accepted oversized pinned context")
	}
	result, err := tool.Execute(context.Background(), map[string]any{"content": content})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "too large") {
		t.Fatalf("expected size error, got %#v", result)
	}
	if called {
		t.Fatal("updater called for oversized pinned context")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gokin", "pinned_context.md")); !os.IsNotExist(err) {
		t.Fatalf("oversized pin was persisted: %v", err)
	}
}

func TestPinContext_DoesNotRestoreOversizedOrSymlinkedFile(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		dir := t.TempDir()
		pinPath := filepath.Join(dir, ".gokin", "pinned_context.md")
		if err := os.MkdirAll(filepath.Dir(pinPath), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pinPath, []byte(strings.Repeat("x", MaxPinnedContextBytes+1)), 0600); err != nil {
			t.Fatal(err)
		}
		called := false
		tool := NewPinContextTool(func(string) { called = true })
		tool.SetWorkDir(dir)
		tool.LoadPersistedPin()
		if called {
			t.Fatal("oversized persisted pin was restored")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(outside, []byte("outside secret"), 0600); err != nil {
			t.Fatal(err)
		}
		pinPath := filepath.Join(dir, ".gokin", "pinned_context.md")
		if err := os.MkdirAll(filepath.Dir(pinPath), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, pinPath); err != nil {
			t.Fatal(err)
		}
		called := false
		tool := NewPinContextTool(func(string) { called = true })
		tool.SetWorkDir(dir)
		tool.LoadPersistedPin()
		if called {
			t.Fatal("symlinked persisted pin was restored")
		}
	})
}

func TestPinContext_SuccessMessageEchoesContent(t *testing.T) {
	var pinned string
	tool := NewPinContextTool(func(c string) { pinned = c })
	result, _ := tool.Execute(context.Background(), map[string]any{"content": "my note"})
	if !strings.Contains(result.Content, "my note") {
		t.Errorf("success message %q should contain the pinned content", result.Content)
	}
	_ = pinned
}

// ──────────────────────────────────────────────────────────────────────────────
// history_search
// ──────────────────────────────────────────────────────────────────────────────

func TestHistorySearch_ContextGetter(t *testing.T) {
	tool := NewHistorySearchTool(nil) // no stored getter

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "how do I use goroutines?"}}},
		{Role: "model", Parts: []*genai.Part{{Text: "use the go keyword before a function call"}}},
	}

	ctx := context.WithValue(context.Background(), HistoryGetterCtxKey{}, func() []*genai.Content {
		return history
	})

	result, err := tool.Execute(ctx, map[string]any{"pattern": "goroutine"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
	if !strings.Contains(result.Content, "goroutine") {
		t.Errorf("result %q should contain match", result.Content)
	}
}

func TestHistorySearch_NoGetter(t *testing.T) {
	tool := NewHistorySearchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "anything"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when no getter is wired")
	}
}

func TestHistorySearch_NoMatches(t *testing.T) {
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "hello world"}}},
	}
	ctx := context.WithValue(context.Background(), HistoryGetterCtxKey{}, func() []*genai.Content {
		return history
	})
	tool := NewHistorySearchTool(nil)
	result, err := tool.Execute(ctx, map[string]any{"pattern": "golang"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success with no matches, got: %s", result.Error)
	}
	if !strings.Contains(result.Content, "No matches") {
		t.Errorf("expected 'No matches' message, got: %q", result.Content)
	}
}

func TestHistorySearch_ValidateMissingPattern(t *testing.T) {
	tool := NewHistorySearchTool(nil)
	if err := tool.Validate(map[string]any{}); err == nil {
		t.Error("expected validation error for missing pattern")
	}
}

func TestHistorySearch_ValidateInvalidRegex(t *testing.T) {
	tool := NewHistorySearchTool(nil)
	if err := tool.Validate(map[string]any{"pattern": "[invalid"}); err == nil {
		t.Error("expected validation error for invalid regex")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// coordinate (executeSimple path)
// ──────────────────────────────────────────────────────────────────────────────

func TestCoordinate_ExecuteSimpleFallback(t *testing.T) {
	tool := NewCoordinateTool() // no factory wired → triggers executeSimple

	args := map[string]any{
		"tasks": []any{
			map[string]any{
				"id":         "t1",
				"prompt":     "Read the README",
				"agent_type": "explore",
			},
			map[string]any{
				"id":         "t2",
				"prompt":     "Run tests",
				"agent_type": "bash",
				"depends_on": []any{"t1"},
			},
		},
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
	if !strings.Contains(result.Content, "Task Plan") {
		t.Errorf("expected 'Task Plan' in output, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "Read the README") {
		t.Errorf("expected task prompt in output, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "t1") {
		t.Errorf("expected task ID in output, got: %q", result.Content)
	}
}

func TestCoordinate_ValidateDuplicateID(t *testing.T) {
	tool := NewCoordinateTool()
	args := map[string]any{
		"tasks": []any{
			map[string]any{"id": "dup", "prompt": "one", "agent_type": "bash"},
			map[string]any{"id": "dup", "prompt": "two", "agent_type": "bash"},
		},
	}
	if err := tool.Validate(args); err == nil {
		t.Error("expected validation error for duplicate task ID")
	}
}

func TestCoordinate_ValidateUnknownDependency(t *testing.T) {
	tool := NewCoordinateTool()
	args := map[string]any{
		"tasks": []any{
			map[string]any{
				"id": "t1", "prompt": "run", "agent_type": "bash",
				"depends_on": []any{"nonexistent"},
			},
		},
	}
	if err := tool.Validate(args); err == nil {
		t.Error("expected validation error for unknown dependency")
	}
}

func TestCoordinate_ValidateSelfDependency(t *testing.T) {
	tool := NewCoordinateTool()
	args := map[string]any{
		"tasks": []any{
			map[string]any{
				"id": "t1", "prompt": "run", "agent_type": "bash",
				"depends_on": []any{"t1"},
			},
		},
	}
	if err := tool.Validate(args); err == nil {
		t.Error("expected validation error for self-dependency")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// update_scratchpad
// ──────────────────────────────────────────────────────────────────────────────

func TestUpdateScratchpad_NilUpdater(t *testing.T) {
	tool := NewUpdateScratchpadTool(nil)
	result, err := tool.Execute(context.Background(), map[string]any{"content": "note"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when updater is nil")
	}
	if !strings.Contains(result.Error, "pin_context") {
		t.Errorf("error %q should mention pin_context", result.Error)
	}
}

func TestUpdateScratchpad_WithUpdater(t *testing.T) {
	var stored string
	tool := NewUpdateScratchpadTool(func(c string) { stored = c })
	result, err := tool.Execute(context.Background(), map[string]any{"content": "my note"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
	if stored != "my note" {
		t.Errorf("stored = %q, want %q", stored, "my note")
	}
}

func TestUpdateScratchpad_ValidateMissingContent(t *testing.T) {
	tool := NewUpdateScratchpadTool(nil)
	if err := tool.Validate(map[string]any{}); err == nil {
		t.Error("expected validation error for missing content")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// tools_list
// ──────────────────────────────────────────────────────────────────────────────

// minimalTool is a lightweight Tool used only in tests.
type minimalTool struct {
	name string
	desc string
}

func (m *minimalTool) Name() string        { return m.name }
func (m *minimalTool) Description() string { return m.desc }
func (m *minimalTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        m.name,
		Description: m.desc,
		Parameters:  &genai.Schema{Type: genai.TypeObject},
	}
}
func (m *minimalTool) Validate(_ map[string]any) error { return nil }
func (m *minimalTool) Execute(_ context.Context, _ map[string]any) (ToolResult, error) {
	return NewSuccessResult("ok"), nil
}

func TestToolsList_NoRegistry(t *testing.T) {
	tool := &ToolsListTool{} // neither registry nor lister
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when no registry or lister configured")
	}
}

func TestToolsList_WithRegistry(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&minimalTool{name: "alpha", desc: "does alpha"})
	reg.MustRegister(&minimalTool{name: "beta", desc: "does beta"})

	tool := NewToolsListTool(reg)
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Error)
	}
	if !strings.Contains(result.Content, "alpha") {
		t.Errorf("output should contain tool name 'alpha', got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "beta") {
		t.Errorf("output should contain tool name 'beta', got: %q", result.Content)
	}
}

func TestToolsList_DescriptionTruncation(t *testing.T) {
	// The eager (baseRegistry) path does NOT truncate — descriptions are
	// passed verbatim so MCP tool names with long descriptions stay readable.
	// Truncation only applies to the lazy-lister path. Verify that separately.
	longDesc := strings.Repeat("x", 150)
	reg := NewRegistry()
	reg.MustRegister(&minimalTool{name: "verbose", desc: longDesc})

	tool := NewToolsListTool(reg)
	result, _ := tool.Execute(context.Background(), nil)
	// Eager path: full description passes through.
	if !strings.Contains(result.Content, "verbose") {
		t.Error("output should contain tool name 'verbose'")
	}
}

func TestToolsList_Name(t *testing.T) {
	tool := NewToolsListTool(nil)
	if tool.Name() != "tools_list" {
		t.Errorf("Name() = %q, want tools_list", tool.Name())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// thoroughness
// ──────────────────────────────────────────────────────────────────────────────

func TestParseThoroughness(t *testing.T) {
	cases := []struct {
		input string
		want  Thoroughness
	}{
		{"quick", ThoroughnessQuick},
		{"QUICK", ThoroughnessQuick},
		{"  quick  ", ThoroughnessQuick},
		{"thorough", ThoroughnessThorough},
		{"very thorough", ThoroughnessThorough},
		{"VERY THOROUGH", ThoroughnessThorough},
		{"normal", ThoroughnessNormal},
		{"", ThoroughnessNormal},
		{"unknown", ThoroughnessNormal},
	}
	for _, c := range cases {
		if got := ParseThoroughness(c.input); got != c.want {
			t.Errorf("ParseThoroughness(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestThoroughness_ContextRoundTrip(t *testing.T) {
	ctx := WithThoroughness(context.Background(), ThoroughnessThorough)
	if got := ThoroughnessFromContext(ctx); got != ThoroughnessThorough {
		t.Errorf("ThoroughnessFromContext = %q, want thorough", got)
	}
}

func TestThoroughness_ContextDefault(t *testing.T) {
	if got := ThoroughnessFromContext(context.Background()); got != ThoroughnessNormal {
		t.Errorf("default thoroughness = %q, want normal", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ToolEntry (factory.go)
// ──────────────────────────────────────────────────────────────────────────────

func TestToolEntry_LazyInstantiation(t *testing.T) {
	called := 0
	entry := NewToolEntry(func() Tool {
		called++
		return &minimalTool{name: "lazy", desc: "lazy tool"}
	})

	if entry.IsInstantiated() {
		t.Error("should not be instantiated before Get()")
	}
	// First call instantiates.
	got := entry.Get()
	if got == nil {
		t.Fatal("Get() returned nil")
	}
	if called != 1 {
		t.Errorf("factory called %d times, want 1", called)
	}
	// Second call reuses instance.
	_ = entry.Get()
	if called != 1 {
		t.Errorf("factory called %d times after second Get(), want 1", called)
	}
	if !entry.IsInstantiated() {
		t.Error("should be instantiated after Get()")
	}
}

func TestToolEntry_ConfigureBeforeGet(t *testing.T) {
	var configured bool
	entry := NewToolEntry(func() Tool {
		return &minimalTool{name: "cfg", desc: "cfg tool"}
	})
	entry.Configure(func(t Tool) { configured = true })

	if configured {
		t.Error("configure callback should not have fired before Get()")
	}
	entry.Get()
	if !configured {
		t.Error("configure callback should have fired after Get()")
	}
}

func TestToolEntry_ConfigureAfterGet(t *testing.T) {
	entry := NewToolEntry(func() Tool {
		return &minimalTool{name: "cfg2", desc: "cfg tool"}
	})
	entry.Get() // instantiate first
	var configured bool
	entry.Configure(func(t Tool) { configured = true })
	if !configured {
		t.Error("configure callback should fire immediately when instance already exists")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ask_user
// ──────────────────────────────────────────────────────────────────────────────

func TestAskUser_ValidateMissingQuestion(t *testing.T) {
	tool := NewAskUserTool()
	if err := tool.Validate(map[string]any{}); err == nil {
		t.Error("expected validation error for missing question")
	}
	if err := tool.Validate(map[string]any{"question": ""}); err == nil {
		t.Error("expected validation error for empty question")
	}
}

func TestAskUser_NilHandler(t *testing.T) {
	tool := NewAskUserTool()
	result, err := tool.Execute(context.Background(), map[string]any{"question": "what?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when handler is nil")
	}
}

func TestAskUser_WithOptions(t *testing.T) {
	tool := NewAskUserTool()
	tool.SetHandler(func(_ context.Context, q string, opts []string, def string) (string, error) {
		return "yes", nil
	})
	result, err := tool.Execute(context.Background(), map[string]any{
		"question": "Do you agree?",
		"options":  []any{"yes", "no"},
		"default":  "yes",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
	if !strings.Contains(result.Content, "User selected: yes") {
		t.Errorf("expected 'User selected: yes' in result, got: %q", result.Content)
	}
}

func TestAskUser_WithoutOptions(t *testing.T) {
	tool := NewAskUserTool()
	tool.SetHandler(func(_ context.Context, q string, opts []string, def string) (string, error) {
		return "my answer", nil
	})
	result, err := tool.Execute(context.Background(), map[string]any{"question": "what now?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "User answered: my answer") {
		t.Errorf("expected 'User answered:' prefix, got: %q", result.Content)
	}
}

func TestFormatQuestion_NoOptions(t *testing.T) {
	got := FormatQuestion("what?", nil, "")
	if got != "what?" {
		t.Errorf("FormatQuestion with no options: %q", got)
	}
}

func TestFormatQuestion_WithOptionsAndDefault(t *testing.T) {
	got := FormatQuestion("Pick one", []string{"a", "b"}, "a")
	if !strings.Contains(got, "a (default)") {
		t.Errorf("expected default marker in output: %q", got)
	}
	if !strings.Contains(got, "2. b") {
		t.Errorf("expected '2. b' in output: %q", got)
	}
}

func TestGetStringSlice_MixedTypes(t *testing.T) {
	args := map[string]any{
		"items": []any{"one", 2, "three"},
	}
	got, ok := GetStringSlice(args, "items")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "three" {
		t.Errorf("GetStringSlice: %v", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// plan_mode (Validate paths only — Execute requires a wired plan.Manager)
// ──────────────────────────────────────────────────────────────────────────────

func TestEnterPlanMode_ValidateMissingTitle(t *testing.T) {
	tool := NewEnterPlanModeTool()
	if err := tool.Validate(map[string]any{
		"steps": []any{map[string]any{"title": "s1"}},
	}); err == nil {
		t.Error("expected validation error for missing title")
	}
}

func TestEnterPlanMode_ValidateMissingSteps(t *testing.T) {
	tool := NewEnterPlanModeTool()
	if err := tool.Validate(map[string]any{"title": "My Plan"}); err == nil {
		t.Error("expected validation error for missing steps")
	}
}

func TestEnterPlanMode_ValidateEmptySteps(t *testing.T) {
	tool := NewEnterPlanModeTool()
	if err := tool.Validate(map[string]any{
		"title": "My Plan",
		"steps": []any{},
	}); err == nil {
		t.Error("expected validation error for empty steps")
	}
}

func TestEnterPlanMode_ValidateStepMissingTitle(t *testing.T) {
	tool := NewEnterPlanModeTool()
	if err := tool.Validate(map[string]any{
		"title": "My Plan",
		"steps": []any{map[string]any{"description": "no title here"}},
	}); err == nil {
		t.Error("expected validation error for step missing title")
	}
}

func TestEnterPlanMode_ValidateValid(t *testing.T) {
	tool := NewEnterPlanModeTool()
	if err := tool.Validate(map[string]any{
		"title": "My Plan",
		"steps": []any{
			map[string]any{"title": "step 1"},
			map[string]any{"title": "step 2", "description": "optional"},
		},
	}); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestUpdatePlanProgress_ValidateMissingStepID(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	if err := tool.Validate(map[string]any{"action": "start"}); err == nil {
		t.Error("expected validation error for missing step_id")
	}
}

func TestUpdatePlanProgress_ValidateInvalidAction(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	if err := tool.Validate(map[string]any{"step_id": 0, "action": "jump"}); err == nil {
		t.Error("expected validation error for invalid action")
	}
}

func TestUpdatePlanProgress_ValidateValid(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	for _, action := range []string{"start", "complete", "fail", "skip"} {
		if err := tool.Validate(map[string]any{"step_id": 0, "action": action}); err != nil {
			t.Errorf("unexpected validation error for action %q: %v", action, err)
		}
	}
}

func TestEnterPlanMode_NilManager(t *testing.T) {
	tool := NewEnterPlanModeTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "My Plan",
		"steps": []any{map[string]any{"title": "step 1"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when manager is nil")
	}
}

func TestUpdatePlanProgress_NilManager(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	result, err := tool.Execute(context.Background(), map[string]any{"step_id": 0, "action": "start"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when manager is nil")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory tool
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_ValidateMissingAction(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{}); err == nil {
		t.Error("expected validation error for missing action")
	}
}

func TestMemoryTool_ValidateInvalidAction(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{"action": "jump"}); err == nil {
		t.Error("expected validation error for unknown action")
	}
}

func TestMemoryTool_ValidateRememberMissingContent(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{"action": "remember"}); err == nil {
		t.Error("expected validation error for remember without content")
	}
}

func TestMemoryTool_ValidateRememberValid(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{"action": "remember", "content": "some note"}); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestMemoryTool_ValidateForgetMissingIdAndKey(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{"action": "forget"}); err == nil {
		t.Error("expected validation error for forget without id or key")
	}
}

func TestMemoryTool_ValidateRecallValid(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{"action": "recall"}); err != nil {
		t.Errorf("unexpected validation error for recall: %v", err)
	}
}

func TestMemoryTool_NilStore(t *testing.T) {
	tool := NewMemoryTool()
	result, err := tool.Execute(context.Background(), map[string]any{"action": "recall"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when store is nil")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memorize tool
// ──────────────────────────────────────────────────────────────────────────────

func TestMemorizeTool_ValidateMissingType(t *testing.T) {
	tool := NewMemorizeTool(nil)
	if err := tool.Validate(map[string]any{"key": "k", "content": "c"}); err == nil {
		t.Error("expected validation error for missing type")
	}
}

func TestMemorizeTool_ValidateMissingKey(t *testing.T) {
	tool := NewMemorizeTool(nil)
	if err := tool.Validate(map[string]any{"type": "preference", "content": "c"}); err == nil {
		t.Error("expected validation error for missing key")
	}
}

func TestMemorizeTool_ValidateMissingContent(t *testing.T) {
	tool := NewMemorizeTool(nil)
	if err := tool.Validate(map[string]any{"type": "preference", "key": "k"}); err == nil {
		t.Error("expected validation error for missing content")
	}
}

func TestMemorizeTool_ValidateValid(t *testing.T) {
	tool := NewMemorizeTool(nil)
	if err := tool.Validate(map[string]any{
		"type": "preference", "key": "verbosity", "content": "brief",
	}); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestMemorizeTool_NilLearning(t *testing.T) {
	tool := NewMemorizeTool(nil)
	result, err := tool.Execute(context.Background(), map[string]any{
		"type": "preference", "key": "verbosity", "content": "brief",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when learning store is nil")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// output_style
// ──────────────────────────────────────────────────────────────────────────────

func TestParseOutputStyle(t *testing.T) {
	cases := []struct {
		input string
		want  OutputStyle
	}{
		{"concise", OutputStyleConcise},
		{"compact", OutputStyleConcise},
		{"brief", OutputStyleConcise},
		{"CONCISE", OutputStyleConcise},
		{"detailed", OutputStyleDetailed},
		{"verbose", OutputStyleDetailed},
		{"normal", OutputStyleNormal},
		{"", OutputStyleNormal},
		{"unknown", OutputStyleNormal},
		{"  concise  ", OutputStyleConcise},
	}
	for _, c := range cases {
		if got := ParseOutputStyle(c.input); got != c.want {
			t.Errorf("ParseOutputStyle(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestOutputStyle_ContextRoundTrip(t *testing.T) {
	ctx := WithOutputStyle(context.Background(), OutputStyleDetailed)
	if got := OutputStyleFromContext(ctx); got != OutputStyleDetailed {
		t.Errorf("OutputStyleFromContext = %q, want detailed", got)
	}
}

func TestOutputStyle_ContextDefault(t *testing.T) {
	if got := OutputStyleFromContext(context.Background()); got != OutputStyleNormal {
		t.Errorf("default output style = %q, want normal", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// streaming context utilities
// ──────────────────────────────────────────────────────────────────────────────

func TestStreamingCallback_ContextRoundTrip(t *testing.T) {
	var received string
	cb := StreamingCallback(func(text string) { received = text })
	ctx := ContextWithStreamingCallback(context.Background(), cb)
	got := GetStreamingCallback(ctx)
	if got == nil {
		t.Fatal("GetStreamingCallback returned nil")
	}
	got("hello")
	if received != "hello" {
		t.Errorf("streaming callback received %q, want %q", received, "hello")
	}
}

func TestStreamingCallback_MissingFromContext(t *testing.T) {
	if got := GetStreamingCallback(context.Background()); got != nil {
		t.Error("expected nil from plain context")
	}
}

func TestToolsTracker_AddAndList(t *testing.T) {
	tracker := &ToolsUsedTracker{}
	tracker.Add("read")
	tracker.Add("bash")
	got := tracker.List()
	if len(got) != 2 || got[0] != "read" || got[1] != "bash" {
		t.Errorf("ToolsUsedTracker.List() = %v", got)
	}
}

func TestToolsTracker_ContextRoundTrip(t *testing.T) {
	tracker := &ToolsUsedTracker{}
	ctx := ContextWithToolsTracker(context.Background(), tracker)
	got := GetToolsTracker(ctx)
	if got != tracker {
		t.Error("GetToolsTracker returned different tracker")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// output_filter (FilterBashOutput)
// ──────────────────────────────────────────────────────────────────────────────

func TestFilterBashOutput_ShortOutputPassThrough(t *testing.T) {
	input := "ok"
	if got := FilterBashOutput(input); got != input {
		t.Errorf("short output should pass through unchanged, got %q", got)
	}
}

func TestFilterBashOutput_DeduplicatesLines(t *testing.T) {
	// Repeat the same line 15 times with a total length > 100 bytes — should be collapsed.
	line := "compiling package foobar_very_long_name_here..."
	lines := make([]string, 15)
	for i := range lines {
		lines[i] = line
	}
	input := strings.Join(lines, "\n")
	got := FilterBashOutput(input)
	// Collapsed output should be shorter than the input.
	if len(got) >= len(input) {
		t.Errorf("FilterBashOutput did not deduplicate; output length %d >= input length %d\noutput: %q", len(got), len(input), got)
	}
}

func TestFilterBashOutput_CollapsesBlankLines(t *testing.T) {
	// Build an input >100 chars with many blank lines between real lines.
	realLine := "this is a real output line with enough content to push total over 100 bytes"
	input := realLine + "\n" + strings.Repeat("\n", 10) + realLine
	got := FilterBashOutput(input)
	// Should not have runs of more than 2 consecutive blank lines.
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("FilterBashOutput left triple blank lines in output: %q", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// tool.go helpers (GetString, GetInt, GetBool, ValidationError, isValidGitRef)
// ──────────────────────────────────────────────────────────────────────────────

func TestGetString_Present(t *testing.T) {
	got, ok := GetString(map[string]any{"k": "v"}, "k")
	if !ok || got != "v" {
		t.Errorf("GetString: got (%q, %v)", got, ok)
	}
}

func TestGetString_Missing(t *testing.T) {
	_, ok := GetString(map[string]any{}, "missing")
	if ok {
		t.Error("expected ok=false for missing key")
	}
}

func TestGetString_WrongType(t *testing.T) {
	_, ok := GetString(map[string]any{"k": 42}, "k")
	if ok {
		t.Error("expected ok=false for non-string value")
	}
}

func TestGetStringDefault(t *testing.T) {
	if got := GetStringDefault(map[string]any{}, "k", "def"); got != "def" {
		t.Errorf("GetStringDefault returned %q, want 'def'", got)
	}
	if got := GetStringDefault(map[string]any{"k": "v"}, "k", "def"); got != "v" {
		t.Errorf("GetStringDefault returned %q, want 'v'", got)
	}
}

func TestGetInt_Int(t *testing.T) {
	got, ok := GetInt(map[string]any{"n": 5}, "n")
	if !ok || got != 5 {
		t.Errorf("GetInt(int): got (%d, %v)", got, ok)
	}
}

func TestGetInt_Float64(t *testing.T) {
	// Gemini sends numbers as float64.
	got, ok := GetInt(map[string]any{"n": float64(7)}, "n")
	if !ok || got != 7 {
		t.Errorf("GetInt(float64): got (%d, %v)", got, ok)
	}
}

func TestGetInt_Missing(t *testing.T) {
	_, ok := GetInt(map[string]any{}, "n")
	if ok {
		t.Error("expected ok=false for missing key")
	}
}

func TestGetBool_Present(t *testing.T) {
	got, ok := GetBool(map[string]any{"b": true}, "b")
	if !ok || !got {
		t.Errorf("GetBool: got (%v, %v)", got, ok)
	}
}

func TestGetBoolDefault(t *testing.T) {
	if got := GetBoolDefault(map[string]any{}, "b", true); !got {
		t.Error("GetBoolDefault should return true when missing")
	}
	if got := GetBoolDefault(map[string]any{"b": false}, "b", true); got {
		t.Error("GetBoolDefault should return false from map")
	}
}

func TestValidationError_Error(t *testing.T) {
	err := NewValidationError("field", "is required")
	if err.Error() != "field: is required" {
		t.Errorf("ValidationError.Error() = %q", err.Error())
	}
}

func TestNewSuccessResult(t *testing.T) {
	r := NewSuccessResult("done")
	if !r.Success || r.Content != "done" || r.Error != "" {
		t.Errorf("NewSuccessResult: %+v", r)
	}
}

func TestNewErrorResult(t *testing.T) {
	r := NewErrorResult("oops")
	if r.Success || r.Error != "oops" {
		t.Errorf("NewErrorResult: %+v", r)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// write tool (Validate paths only)
// ──────────────────────────────────────────────────────────────────────────────

func TestWriteTool_ValidateMissingFilePath(t *testing.T) {
	tool := NewWriteTool(t.TempDir())
	if err := tool.Validate(map[string]any{"content": "hello"}); err == nil {
		t.Error("expected validation error for missing file_path")
	}
}

func TestWriteTool_ValidateMissingContent(t *testing.T) {
	tool := NewWriteTool(t.TempDir())
	if err := tool.Validate(map[string]any{"file_path": "/tmp/x.txt"}); err == nil {
		t.Error("expected validation error for missing content")
	}
}

func TestWriteTool_ValidateValid(t *testing.T) {
	tool := NewWriteTool(t.TempDir())
	if err := tool.Validate(map[string]any{"file_path": "/tmp/x.txt", "content": "hi"}); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// web_search tool (Validate paths only)
// ──────────────────────────────────────────────────────────────────────────────

func TestWebSearch_ValidateMissingQuery(t *testing.T) {
	tool := NewWebSearchTool()
	if err := tool.Validate(map[string]any{}); err == nil {
		t.Error("expected validation error for missing query")
	}
}

func TestWebSearch_ValidateGoogleRequiresKey(t *testing.T) {
	tool := NewWebSearchTool()
	tool.SetProvider(SearchProviderGoogle)
	// No API key set — should fail validation.
	if err := tool.Validate(map[string]any{"query": "test"}); err == nil {
		t.Error("expected validation error for Google without API key")
	}
}

func TestWebSearch_ValidateDuckDuckGoNoKey(t *testing.T) {
	tool := NewWebSearchTool()
	tool.SetProvider(SearchProviderDuckDuckGo)
	// DuckDuckGo doesn't require an API key.
	if err := tool.Validate(map[string]any{"query": "test"}); err != nil {
		t.Errorf("unexpected validation error for DuckDuckGo: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// bash tool (Validate only — Execute requires a PTY/shell)
// ──────────────────────────────────────────────────────────────────────────────

func TestBashTool_ValidateMissingCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	if err := tool.Validate(map[string]any{}); err == nil {
		t.Error("expected validation error for missing command")
	}
}

func TestBashTool_ValidateEmptyCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	if err := tool.Validate(map[string]any{"command": ""}); err == nil {
		t.Error("expected validation error for empty command")
	}
}

func TestBashTool_ValidateBlockedCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	// Fork bomb is always blocked.
	if err := tool.Validate(map[string]any{"command": ":(){ :|:& };:"}); err == nil {
		t.Error("expected validation error for fork bomb")
	}
}

func TestBashTool_ValidateSimpleCommand(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	if err := tool.Validate(map[string]any{"command": "echo hello"}); err != nil {
		t.Errorf("unexpected validation error for simple command: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// git_commit tool (Validate only)
// ──────────────────────────────────────────────────────────────────────────────

func TestGitCommit_ValidateMissingMessage(t *testing.T) {
	tool := NewGitCommitTool(t.TempDir())
	if err := tool.Validate(map[string]any{}); err == nil {
		t.Error("expected validation error for missing message")
	}
}

func TestGitCommit_ValidateEmptyMessage(t *testing.T) {
	tool := NewGitCommitTool(t.TempDir())
	if err := tool.Validate(map[string]any{"message": ""}); err == nil {
		t.Error("expected validation error for empty message")
	}
}

func TestGitCommit_ValidateAutoMessage(t *testing.T) {
	tool := NewGitCommitTool(t.TempDir())
	// auto_message=true bypasses the message requirement.
	if err := tool.Validate(map[string]any{"auto_message": true}); err != nil {
		t.Errorf("unexpected validation error with auto_message=true: %v", err)
	}
}

func TestGitCommit_ValidateWithMessage(t *testing.T) {
	tool := NewGitCommitTool(t.TempDir())
	if err := tool.Validate(map[string]any{"message": "add feature X"}); err != nil {
		t.Errorf("unexpected validation error for valid message: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// grep tool (Validate only)
// ──────────────────────────────────────────────────────────────────────────────

func TestGrepTool_ValidateMissingPattern(t *testing.T) {
	tool := NewGrepTool(t.TempDir())
	if err := tool.Validate(map[string]any{}); err == nil {
		t.Error("expected validation error for missing pattern")
	}
}

func TestGrepTool_ValidateInvalidRegex(t *testing.T) {
	tool := NewGrepTool(t.TempDir())
	if err := tool.Validate(map[string]any{"pattern": "[invalid"}); err == nil {
		t.Error("expected validation error for invalid regex")
	}
}

func TestGrepTool_ValidateValid(t *testing.T) {
	tool := NewGrepTool(t.TempDir())
	if err := tool.Validate(map[string]any{"pattern": "func.*Error"}); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GetStringSlice — missing branches
// ──────────────────────────────────────────────────────────────────────────────

func TestGetStringSlice_MissingKey(t *testing.T) {
	_, ok := GetStringSlice(map[string]any{}, "missing")
	if ok {
		t.Error("expected ok=false for missing key")
	}
}

func TestGetStringSlice_TypedStringSlice(t *testing.T) {
	// []string direct type assertion — the branch missing from coverage.
	got, ok := GetStringSlice(map[string]any{"items": []string{"a", "b", "c"}}, "items")
	if !ok {
		t.Fatal("expected ok=true for []string value")
	}
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("GetStringSlice([]string): %v", got)
	}
}

func TestGetStringSlice_UnknownType(t *testing.T) {
	// Neither []string nor []any — fallthrough nil return.
	_, ok := GetStringSlice(map[string]any{"items": 42}, "items")
	if ok {
		t.Error("expected ok=false for non-slice value")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GetInt — missing int64 branch
// ──────────────────────────────────────────────────────────────────────────────

func TestGetInt_Int64(t *testing.T) {
	got, ok := GetInt(map[string]any{"n": int64(42)}, "n")
	if !ok || got != 42 {
		t.Errorf("GetInt(int64): got (%d, %v)", got, ok)
	}
}

func TestGetInt_UnknownType(t *testing.T) {
	_, ok := GetInt(map[string]any{"n": "not-a-number"}, "n")
	if ok {
		t.Error("expected ok=false for string value")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.Validate — feedback action branches
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_ValidateFeedbackMissingIdAndKey(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{"action": "feedback", "success": true}); err == nil {
		t.Error("expected validation error for feedback without id or key")
	}
}

func TestMemoryTool_ValidateFeedbackMissingSuccess(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{"action": "feedback", "id": "mem-1"}); err == nil {
		t.Error("expected validation error for feedback without success field")
	}
}

func TestMemoryTool_ValidateFeedbackValid(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{"action": "feedback", "id": "mem-1", "success": true}); err != nil {
		t.Errorf("unexpected validation error for valid feedback: %v", err)
	}
}

func TestMemoryTool_ValidateListValid(t *testing.T) {
	tool := NewMemoryTool()
	if err := tool.Validate(map[string]any{"action": "list"}); err != nil {
		t.Errorf("unexpected validation error for list: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// coordinate.Validate — missing branches
// ──────────────────────────────────────────────────────────────────────────────

func TestCoordinate_ValidateNonArrayTasks(t *testing.T) {
	tool := NewCoordinateTool()
	if err := tool.Validate(map[string]any{"tasks": "not-an-array"}); err == nil {
		t.Error("expected validation error for non-array tasks")
	}
}

func TestCoordinate_ValidateNonObjectTask(t *testing.T) {
	tool := NewCoordinateTool()
	if err := tool.Validate(map[string]any{
		"tasks": []any{"not-an-object"},
	}); err == nil {
		t.Error("expected validation error for non-object task element")
	}
}

func TestCoordinate_ValidateMissingTaskID(t *testing.T) {
	tool := NewCoordinateTool()
	if err := tool.Validate(map[string]any{
		"tasks": []any{map[string]any{"prompt": "do something", "agent_type": "general"}},
	}); err == nil {
		t.Error("expected validation error for task with missing id")
	}
}

func TestCoordinate_ValidateMissingPrompt(t *testing.T) {
	tool := NewCoordinateTool()
	if err := tool.Validate(map[string]any{
		"tasks": []any{map[string]any{"id": "t1", "agent_type": "general"}},
	}); err == nil {
		t.Error("expected validation error for task with missing prompt")
	}
}

func TestCoordinate_ValidateMissingAgentType(t *testing.T) {
	tool := NewCoordinateTool()
	if err := tool.Validate(map[string]any{
		"tasks": []any{map[string]any{"id": "t1", "prompt": "do something"}},
	}); err == nil {
		t.Error("expected validation error for task with missing agent_type")
	}
}

func TestCoordinate_ValidateValid(t *testing.T) {
	tool := NewCoordinateTool()
	if err := tool.Validate(map[string]any{
		"tasks": []any{map[string]any{"id": "t1", "prompt": "do something", "agent_type": "general"}},
	}); err != nil {
		t.Errorf("unexpected validation error for valid tasks: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// coordinate.Execute — timeout_minutes branch
// ──────────────────────────────────────────────────────────────────────────────

func TestCoordinate_ExecuteWithTimeout(t *testing.T) {
	tool := NewCoordinateTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{map[string]any{
			"id":         "t1",
			"prompt":     "write tests",
			"agent_type": "general",
		}},
		"timeout_minutes": float64(30),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// FilterBashOutput — noise lines and short-with-few-lines branches
// ──────────────────────────────────────────────────────────────────────────────

func TestFilterBashOutput_NoiseLineFiltered(t *testing.T) {
	// "go: downloading" is a noise pattern; should be stripped.
	lines := []string{
		"Starting build...",
		"go: downloading github.com/foo/bar v1.2.3",
		"go: downloading github.com/baz/qux v0.1.0",
		"go: downloading github.com/a/b v3.0.0",
		"Build complete.",
	}
	// Pad to ensure >100 chars total.
	output := strings.Join(lines, "\n") + "\n" + strings.Repeat("a", 60)
	out := FilterBashOutput(output)
	if strings.Contains(out, "go: downloading") {
		t.Error("expected noise lines to be filtered out")
	}
	if !strings.Contains(out, "noise lines filtered") {
		t.Errorf("expected '[N noise lines filtered]' annotation, got: %q", out)
	}
}

func TestFilterBashOutput_NpmNoiseFiltered(t *testing.T) {
	// npm WARN line is a noise pattern.
	// "npm WARN" (uppercase) matches the noise pattern.
	lines2 := make([]string, 10)
	for i := range lines2 {
		lines2[i] = "regular output line with some content here"
	}
	lines2[3] = "npm WARN deprecated package@1.0.0"
	output2 := strings.Join(lines2, "\n")
	filtered2 := FilterBashOutput(output2)
	if strings.Contains(filtered2, "npm WARN") {
		t.Error("expected npm WARN noise lines to be filtered")
	}
}

func TestFilterBashOutput_FewLinesPassThrough(t *testing.T) {
	// >100 chars but <3 lines: should pass through unchanged.
	output := strings.Repeat("x", 150) // single line, >100 chars
	got := FilterBashOutput(output)
	if got != output {
		t.Errorf("expected pass-through for <3 lines, got different output")
	}
}

func TestFilterBashOutput_ProgressArtifacts(t *testing.T) {
	// ANSI escape codes should be stripped. Include a noise line so
	// filtered > 0 and the function returns the cleaned version rather
	// than the original.
	lines := []string{
		"Starting deployment process output here",
		"\x1b[32mBuilding...\x1b[0m",
		"go: downloading github.com/foo/bar v1.2.3",
		"Build step completed successfully now",
		"Deployment finished successfully here",
	}
	output := strings.Join(lines, "\n")
	out := FilterBashOutput(output)
	// ANSI codes should be stripped.
	if strings.Contains(out, "\x1b[") {
		t.Errorf("expected ANSI escape codes to be stripped, got: %q", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// scratchpad.Validate — valid case (return nil path)
// ──────────────────────────────────────────────────────────────────────────────

func TestUpdateScratchpad_ValidateValid(t *testing.T) {
	tool := NewUpdateScratchpadTool(nil)
	if err := tool.Validate(map[string]any{"content": "my scratchpad note"}); err != nil {
		t.Errorf("unexpected validation error for valid content: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// tools_list with ToolLister (lazy registry path)
// ──────────────────────────────────────────────────────────────────────────────

// mockToolLister implements ToolLister for testing.
type mockToolLister struct {
	decls []*genai.FunctionDeclaration
}

func (m *mockToolLister) Names() []string {
	names := make([]string, len(m.decls))
	for i, d := range m.decls {
		names[i] = d.Name
	}
	return names
}

func (m *mockToolLister) Declarations() []*genai.FunctionDeclaration {
	return m.decls
}

func TestToolsList_WithLister(t *testing.T) {
	lister := &mockToolLister{
		decls: []*genai.FunctionDeclaration{
			{Name: "alpha", Description: "does alpha things"},
			{Name: "beta", Description: "does beta things"},
		},
	}
	tool := NewToolsListToolLazy(lister)
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "alpha") || !strings.Contains(result.Content, "beta") {
		t.Errorf("expected tool names in output: %q", result.Content)
	}
}

func TestToolsList_ListerDescriptionTruncation(t *testing.T) {
	longDesc := strings.Repeat("x", 110) // > 100 chars
	lister := &mockToolLister{
		decls: []*genai.FunctionDeclaration{
			{Name: "longtool", Description: longDesc},
		},
	}
	tool := NewToolsListToolLazy(lister)
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if strings.Contains(result.Content, longDesc) {
		t.Error("expected long description to be truncated")
	}
	if !strings.Contains(result.Content, "...") {
		t.Error("expected truncation indicator '...' in output")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GetToolsTracker — nil return when not in context
// ──────────────────────────────────────────────────────────────────────────────

func TestGetToolsTracker_NotInContext(t *testing.T) {
	tracker := GetToolsTracker(context.Background())
	if tracker != nil {
		t.Error("expected nil tracker when not in context")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// BashTool.Validate — unrestricted mode and managed workspace blocked paths
// ──────────────────────────────────────────────────────────────────────────────

func TestBashTool_ValidateUnrestrictedMode(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.SetUnrestrictedMode(true)
	// In unrestricted mode, any non-empty, non-dangerous command should pass.
	if err := tool.Validate(map[string]any{"command": "echo hello"}); err != nil {
		t.Errorf("unexpected validation error in unrestricted mode: %v", err)
	}
}

func TestBashTool_ValidateManagedWorkspaceBlocked(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.EnableManagedWorkspaceApplyBackMode(t.TempDir())
	// "git commit" is blocked in managed workspace mode.
	if err := tool.Validate(map[string]any{"command": "git commit -m 'test'"}); err == nil {
		t.Error("expected validation error for git commit in managed workspace mode")
	}
}

func TestBashTool_ValidateManagedWorkspaceAllowed(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.EnableManagedWorkspaceApplyBackMode(t.TempDir())
	// "echo" is allowed in managed workspace mode.
	if err := tool.Validate(map[string]any{"command": "echo hello world"}); err != nil {
		t.Errorf("unexpected validation error for allowed command: %v", err)
	}
}

// validateManagedWorkspaceCommand — all blocked fragment kinds
func TestValidateManagedWorkspaceCommand_EachBlockedFragment(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.EnableManagedWorkspaceApplyBackMode(t.TempDir())

	blockedCmds := []string{
		"git push origin main",
		"git fetch --all",
		"git stash pop",
		"git reset --hard HEAD",
		"git clean -fd",
		"git tag v1.0",
		"git merge main",
		"git rebase main",
		"git cherry-pick abc123",
		"git branch -d feature",
	}
	for _, cmd := range blockedCmds {
		if err := tool.Validate(map[string]any{"command": cmd}); err == nil {
			t.Errorf("expected validation error for %q in managed workspace mode", cmd)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// history_search.Execute — FunctionCall/FunctionResponse path + >20 results
// ──────────────────────────────────────────────────────────────────────────────

func TestHistorySearch_FunctionCallPath(t *testing.T) {
	history := []*genai.Content{
		{
			Role: "model",
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": "ls"}}},
			},
		},
		{
			Role: "user",
			Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{Name: "bash"}},
			},
		},
	}
	tool := NewHistorySearchTool(func() []*genai.Content { return history })
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "bash"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Error)
	}
	// Should find matches in both FunctionCall and FunctionResponse.
	if !strings.Contains(result.Content, "bash") {
		t.Errorf("expected 'bash' in results: %q", result.Content)
	}
}

func TestHistorySearch_TruncatesOver20Results(t *testing.T) {
	// Create history with >20 matching messages.
	parts := make([]*genai.Content, 25)
	for i := range parts {
		parts[i] = &genai.Content{
			Role:  "user",
			Parts: []*genai.Part{{Text: "match pattern here in message content"}},
		}
	}
	tool := NewHistorySearchTool(func() []*genai.Content { return parts })
	result, err := tool.Execute(context.Background(), map[string]any{"pattern": "match pattern"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "truncated") {
		t.Errorf("expected truncation notice for >20 results: %q", result.Content)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// plan_mode.Validate — non-object step path
// ──────────────────────────────────────────────────────────────────────────────

func TestEnterPlanMode_ValidateNonObjectStep(t *testing.T) {
	tool := NewEnterPlanModeTool()
	if err := tool.Validate(map[string]any{
		"title": "My Plan",
		"steps": []any{"not-an-object"},
	}); err == nil {
		t.Error("expected validation error for non-object step")
	}
}

func TestUpdatePlanProgress_ValidateMissingAction(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	if err := tool.Validate(map[string]any{"step_id": 1}); err == nil {
		t.Error("expected validation error for missing action")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// BashTool.isWithinWorkspace — rel=="." path (workdir equals workspace root)
// ──────────────────────────────────────────────────────────────────────────────

func TestBashTool_IsWithinWorkspace_ExactRoot(t *testing.T) {
	dir := t.TempDir()
	// Session workDir = dir; workspace root also = dir → rel = "." → true.
	tool := NewBashTool(dir)
	tool.SetWorkspaceBoundary(dir) // calls isWithinWorkspace(dir) → rel="." path
	// No error expected; workspace boundary enabled.
	if err := tool.Validate(map[string]any{"command": "echo hello"}); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// plan_mode.Execute — paths reachable with a wired plan.Manager
// ──────────────────────────────────────────────────────────────────────────────

func TestEnterPlanMode_ExecuteDisabled(t *testing.T) {
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(false, false) // enabled=false
	tool.SetManager(mgr)
	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "My Plan",
		"steps": []any{map[string]any{"title": "Step 1"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when plan mode is disabled")
	}
}

func TestGetPlanStatus_ExecuteNilManager(t *testing.T) {
	tool := NewGetPlanStatusTool()
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when manager is nil")
	}
}

func TestGetPlanStatus_ExecuteNoActivePlan(t *testing.T) {
	tool := NewGetPlanStatusTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success for no-active-plan status: %s", result.Error)
	}
	if !strings.Contains(result.Content, "No active plan") {
		t.Errorf("expected 'No active plan' in result: %q", result.Content)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Valid-return-nil paths missing from Validate functions
// ──────────────────────────────────────────────────────────────────────────────

func TestAskUserTool_ValidateValid(t *testing.T) {
	tool := NewAskUserTool()
	if err := tool.Validate(map[string]any{"question": "What would you like?"}); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestHistorySearchTool_ValidateValid(t *testing.T) {
	tool := NewHistorySearchTool(nil)
	if err := tool.Validate(map[string]any{"pattern": "func.*Error"}); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Registry.Register — duplicate tool error path
// ──────────────────────────────────────────────────────────────────────────────

func TestRegistry_RegisterDuplicate(t *testing.T) {
	reg := NewRegistry()
	tool := &minimalTool{name: "alpha", desc: "first"}
	if err := reg.Register(tool); err != nil {
		t.Fatalf("first register unexpected error: %v", err)
	}
	dup := &minimalTool{name: "alpha", desc: "duplicate"}
	if err := reg.Register(dup); err == nil {
		t.Error("expected error for duplicate tool registration")
	}
}

func TestRegistry_UnregisterUpdatesRegistryBackedViews(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&minimalTool{name: "alpha", desc: "first"})
	lister := NewToolsListTool(reg)

	if !reg.Unregister("alpha") {
		t.Fatal("Unregister reported an existing tool as absent")
	}
	if reg.Unregister("alpha") {
		t.Fatal("second Unregister reported success")
	}
	if _, ok := reg.Get("alpha"); ok {
		t.Fatal("unregistered tool is still addressable")
	}
	result, err := lister.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "**alpha**") {
		t.Fatalf("tools_list retained unregistered tool: %q", result.Content)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// UpdateScratchpadTool.Execute — wrong content type error path
// ──────────────────────────────────────────────────────────────────────────────

func TestUpdateScratchpad_ExecuteWrongType(t *testing.T) {
	tool := NewUpdateScratchpadTool(func(_ string) {})
	_, err := tool.Execute(context.Background(), map[string]any{"content": 42})
	if err == nil {
		t.Error("expected error when content is not a string")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ask_user.Execute — handler error path
// ──────────────────────────────────────────────────────────────────────────────

func TestAskUserTool_ExecuteHandlerError(t *testing.T) {
	tool := NewAskUserTool()
	tool.SetHandler(func(_ context.Context, _ string, _ []string, _ string) (string, error) {
		return "", fmt.Errorf("user dismissed the question")
	})
	result, err := tool.Execute(context.Background(), map[string]any{"question": "Ready?"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when handler returns error")
	}
	if !strings.Contains(result.Error, "failed to get user answer") {
		t.Errorf("unexpected error message: %q", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// pin_context.Execute — preview truncation (content > 120 chars)
// ──────────────────────────────────────────────────────────────────────────────

func TestPinContextTool_ExecuteLongContent(t *testing.T) {
	var pinned string
	tool := NewPinContextTool(func(c string) { pinned = c })

	longContent := strings.Repeat("a", 150)
	result, err := tool.Execute(context.Background(), map[string]any{"content": longContent})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if pinned != longContent {
		t.Errorf("updater should receive full content, got %d chars", len(pinned))
	}
	// Response should show truncated preview.
	if !strings.Contains(result.Content, "...") {
		t.Errorf("expected truncation in preview, got: %q", result.Content)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ToolEntry.Get — panic recovery and nil factory paths
// ──────────────────────────────────────────────────────────────────────────────

func TestToolEntry_PanicRecovery(t *testing.T) {
	entry := NewToolEntry(func() Tool {
		panic("factory exploded")
	})
	tool := entry.Get()
	if tool != nil {
		t.Error("expected nil tool after factory panic")
	}
}

func TestToolEntry_NilFactory(t *testing.T) {
	entry := NewToolEntry(func() Tool {
		return nil
	})
	tool := entry.Get()
	if tool != nil {
		t.Error("expected nil tool when factory returns nil")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// FilterBashOutput — remaining uncovered branches
// ──────────────────────────────────────────────────────────────────────────────

func TestFilterBashOutput_AnsiOnlyLineBecomesEmpty(t *testing.T) {
	// A line that's purely ANSI escape codes becomes empty after cleaning;
	// the resulting empty line should be counted as filtered (not added to result).
	lines := []string{
		"Starting build here for testing purposes",
		"\x1b[0m", // pure ANSI → empty after clean
		"Build step one completed successfully now",
		"Build step two completed successfully now",
		"go: downloading github.com/foo/bar v1.2.3",
	}
	output := strings.Join(lines, "\n")
	out := FilterBashOutput(output)
	// The pure-ANSI line should not appear in output.
	if strings.Contains(out, "\x1b") {
		t.Errorf("expected no ANSI escape codes in output: %q", out)
	}
}

func TestFilterBashOutput_MidStreamRepeatFlush(t *testing.T) {
	// Repeated lines followed by a DIFFERENT line should flush the repeat count.
	lines := []string{
		"Build step A starts here with enough content to fill",
		"same line repeated here and some more content for length",
		"same line repeated here and some more content for length",
		"same line repeated here and some more content for length",
		"Build step B starts here with enough content to fill",
		"go: downloading github.com/foo/bar v1.2.3",
	}
	output := strings.Join(lines, "\n")
	out := FilterBashOutput(output)
	if !strings.Contains(out, "repeated") {
		t.Errorf("expected repeat annotation in output: %q", out)
	}
}

func TestFilterBashOutput_NoSavingsReturnOriginal(t *testing.T) {
	// No filtering and dedup < 2: should return the original unchanged.
	lines := []string{
		"line one with plenty of content here",
		"line two with plenty of content here",
		"line three with plenty of content",
		"line four with plenty of content",
	}
	output := strings.Join(lines, "\n")
	got := FilterBashOutput(output)
	if got != output {
		t.Errorf("expected original output when no savings; got %q", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.Execute — live memory.Store tests
// ──────────────────────────────────────────────────────────────────────────────

func newTestMemoryStore(t *testing.T) *memory.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := memory.NewStore(dir, filepath.Join(dir, "project"), 100)
	if err != nil {
		t.Fatalf("failed to create test memory store: %v", err)
	}
	return store
}

func TestMemoryTool_ExecuteRemember(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "The build uses Makefile targets",
		"key":     "build-info",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "build-info") {
		t.Errorf("expected key in result: %q", result.Content)
	}
}

func TestMemoryTool_ExecuteRememberWithTags(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Use Go modules for dependency management",
		"tags":    []any{"go", "deps"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_ExecuteRememberSessionScope(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Temporary note for this session",
		"scope":   "session",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_ExecuteRememberGlobalScope(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	// global scope is redirected to project scope
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Global note redirected to project",
		"scope":   "global",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_ExecuteRecall(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	// First remember something.
	_, _ = tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "The API uses REST endpoints",
		"key":     "api-style",
	})

	// Now recall by key.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"key":    "api-style",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "api-style") {
		t.Errorf("expected key in result: %q", result.Content)
	}
}

func TestMemoryTool_ExecuteRecallByQuery(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	_, _ = tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Database uses PostgreSQL 14",
	})

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"query":  "database",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_ExecuteList(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	_, _ = tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Note one",
	})

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "list",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_ExecuteForgetByID(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	// Remember something and get the ID.
	remResult, _ := tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "To be forgotten",
	})
	if !remResult.Success {
		t.Skip("remember failed, skipping forget test")
	}

	// Extract ID from data.
	dataMap, _ := remResult.Data.(map[string]any)
	entryID, _ := dataMap["id"].(string)
	if entryID == "" {
		t.Skip("no entry ID in remember result")
	}

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "forget",
		"id":     entryID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_ExecuteForgetByKey(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	_, _ = tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "To be forgotten by key",
		"key":     "forget-me",
	})

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "forget",
		"key":    "forget-me",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_ExecuteFeedback(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	remResult, _ := tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Feedback target",
		"key":     "fb-key",
	})
	if !remResult.Success {
		t.Skip("remember failed")
	}

	fbDataMap, _ := remResult.Data.(map[string]any)
	entryID, _ := fbDataMap["id"].(string)
	if entryID == "" {
		t.Skip("no entry ID")
	}

	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "feedback",
		"id":      entryID,
		"success": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_ExecuteInvalidAction(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "explode",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for invalid action")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memorize.Execute — live memory.ProjectLearning tests
// ──────────────────────────────────────────────────────────────────────────────

func newTestProjectLearning(t *testing.T) *memory.ProjectLearning {
	t.Helper()
	dir := t.TempDir()
	pl, err := memory.NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("failed to create test project learning: %v", err)
	}
	return pl
}

func TestMemorizeTool_ExecutePreference(t *testing.T) {
	tool := NewMemorizeTool(newTestProjectLearning(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"type":    "preference",
		"key":     "indent-style",
		"content": "tabs",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "preference") {
		t.Errorf("expected 'preference' in result: %q", result.Content)
	}
}

func TestMemorizeTool_ExecuteFact(t *testing.T) {
	tool := NewMemorizeTool(newTestProjectLearning(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"type":    "fact",
		"key":     "go-version",
		"content": "1.25",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemorizeTool_ExecuteConvention(t *testing.T) {
	tool := NewMemorizeTool(newTestProjectLearning(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"type":    "convention",
		"key":     "error-handling",
		"content": "always wrap errors with fmt.Errorf",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemorizeTool_ExecutePattern(t *testing.T) {
	tool := NewMemorizeTool(newTestProjectLearning(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"type":    "pattern",
		"key":     "struct-init",
		"content": "always use named fields in struct literals",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemorizeTool_ExecuteUnknownType(t *testing.T) {
	tool := NewMemorizeTool(newTestProjectLearning(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"type":    "mystery",
		"key":     "key",
		"content": "value",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for unknown memorize type")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// EnterPlanModeTool.Execute — enabled manager, no approval required, plan creation
// ──────────────────────────────────────────────────────────────────────────────

func TestEnterPlanMode_ExecuteCreatesAndApprovesPlan(t *testing.T) {
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, false) // enabled, no approval required
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"title":       "Implement feature X",
		"description": "Add feature X to the project",
		"steps": []any{
			map[string]any{
				"title":       "Write tests",
				"description": "Add unit tests for feature X",
			},
			map[string]any{
				"title":       "Implement code",
				"description": "Write the implementation",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Error)
	}
	if !strings.Contains(result.Content, "Implement feature X") {
		t.Errorf("expected plan title in result: %q", result.Content)
	}
}

func TestGetPlanStatus_ExecuteWithActivePlan(t *testing.T) {
	mgr := plan.NewManager(true, false)

	// Create a plan via EnterPlanModeTool first.
	enterTool := NewEnterPlanModeTool()
	enterTool.SetManager(mgr)
	_, _ = enterTool.Execute(context.Background(), map[string]any{
		"title": "Status Test Plan",
		"steps": []any{
			map[string]any{"title": "Step one"},
		},
	})

	// Now check status.
	statusTool := NewGetPlanStatusTool()
	statusTool.SetManager(mgr)
	result, err := statusTool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestEnterPlanMode_ExecuteWhileAlreadyExecuting(t *testing.T) {
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)

	// Create + approve a plan — transitions manager to executing state.
	_, _ = tool.Execute(context.Background(), map[string]any{
		"title": "First Plan",
		"steps": []any{map[string]any{"title": "Step 1"}},
	})

	// Now try to create another plan while first is executing.
	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Nested Plan",
		"steps": []any{map[string]any{"title": "Step A"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when creating plan while another is executing")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory helpers — truncate, GetIntDefault, getStringSliceField
// ──────────────────────────────────────────────────────────────────────────────

func TestTruncate_LongString(t *testing.T) {
	// Use the tool to trigger truncation of a long content string.
	tool := NewMemoryTool()
	store := newTestMemoryStore(t)
	tool.SetStore(store)
	// Content longer than the 100-char limit used in remember.
	longContent := strings.Repeat("x", 110)
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": longContent,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	// The result message truncates the content to 100 chars with "...".
	if !strings.Contains(result.Content, "...") {
		t.Errorf("expected truncation indicator in result: %q", result.Content)
	}
}

func TestGetIntDefault_KeyPresent(t *testing.T) {
	got := GetIntDefault(map[string]any{"ttl": 5}, "ttl", 0)
	if got != 5 {
		t.Errorf("GetIntDefault with key present: got %d, want 5", got)
	}
}

func TestGetIntDefault_KeyAbsent(t *testing.T) {
	got := GetIntDefault(map[string]any{}, "ttl", 99)
	if got != 99 {
		t.Errorf("GetIntDefault with key absent: got %d, want 99", got)
	}
}

func TestEnterPlanMode_WithStringSliceFields(t *testing.T) {
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Plan with rich steps",
		"steps": []any{
			map[string]any{
				"title":            "Step 1",
				"description":      "Do the first thing",
				"inputs":           []any{"file1.go", "file2.go"},
				"success_criteria": []any{"tests pass", "no lint errors"},
				"verify_commands":  []any{"go test ./...", "golint ./..."},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.recall — tags filter, empty result, >3 results
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_RecallByTags(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	// Add a memory with tags.
	_, _ = tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Use Docker for containers",
		"tags":    []any{"docker", "containers"},
	})

	// Recall with tags filter.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"tags":   []any{"docker"},
		"query":  "containers",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_RecallNoResults(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	// No entries stored; query should return "No memories found".
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"query":  "completely unrelated topic xyz",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "No memories found") {
		t.Errorf("expected 'No memories found', got: %q", result.Content)
	}
}

func TestMemoryTool_RecallMoreThan3Results(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	// Add 4+ semantically distinct entries so the store keeps them all.
	// Entries must have low Jaccard similarity (<0.90) to avoid dedup.
	contents := []string{
		"always use goroutines for concurrent work",
		"validate every input parameter before processing",
		"prefer context cancellation over global flags",
		"write unit tests before implementing features",
	}
	for _, c := range contents {
		_, _ = tool.Execute(context.Background(), map[string]any{
			"action":  "remember",
			"content": c,
		})
	}

	// Recall with no query so all stored entries are returned (empty query adds
	// a base +1.0 score to each entry rather than using content matching).
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.list — empty store and include_archived
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_ListEmptyStore(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "list",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "No memories") {
		t.Errorf("expected 'No memories' in result: %q", result.Content)
	}
}

func TestMemoryTool_ListWithArchived(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	_, _ = tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Archived entry",
	})

	result, err := tool.Execute(context.Background(), map[string]any{
		"action":           "list",
		"include_archived": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestMemoryTool_ListWithKey(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	_, _ = tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Entry with a key",
		"key":     "list-key",
	})

	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "list",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "list-key") {
		t.Errorf("expected key in list result: %q", result.Content)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.feedback — by key path
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_FeedbackByKey(t *testing.T) {
	store := newTestMemoryStore(t)
	tool := NewMemoryTool()
	tool.SetStore(store)

	_, _ = tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "Content with feedback key",
		"key":     "fb-by-key",
	})

	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "feedback",
		"key":     "fb-by-key",
		"success": false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "failure") {
		t.Errorf("expected 'failure' in result: %q", result.Content)
	}
}

func TestMemoryTool_FeedbackMissingID(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	// Neither id nor key provided: should return error.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "feedback",
		"success": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when neither id nor key provided for feedback")
	}
}

func TestMemoryTool_FeedbackKeyNotFound(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	// Feedback by non-existent key.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "feedback",
		"key":     "nonexistent-key",
		"success": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for feedback on nonexistent key")
	}
}

func TestMemoryTool_FeedbackIDNotFound(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	// Feedback by non-existent ID.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":  "feedback",
		"id":      "nonexistent-id-xyz",
		"success": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for feedback on nonexistent ID")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.remember — TTL path
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_RememberWithTTL(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"action":      "remember",
		"content":     "Temporary deployment note",
		"ttl_minutes": 60, // 1 hour TTL
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.forget — non-existent entry
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_ForgetNotFound(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "forget",
		"id":     "nonexistent-id-xyz",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for forget on nonexistent ID")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// getStringSliceField — non-[]any value (returns nil)
// ──────────────────────────────────────────────────────────────────────────────

func TestEnterPlanMode_StepWithStringInputs_NonSlice(t *testing.T) {
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)

	// Pass inputs as a string (not []any) — getStringSliceField returns nil gracefully.
	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Plan non-slice inputs",
		"steps": []any{
			map[string]any{
				"title":  "Step 1",
				"inputs": "not-a-slice", // should be gracefully ignored
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// EnterPlanMode.Execute — IsExecuting and IsActive paths
// ──────────────────────────────────────────────────────────────────────────────

func TestEnterPlanMode_ExecuteIsExecuting(t *testing.T) {
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)
	// Put manager into execution mode with an active plan.
	p := mgr.CreatePlan("running", "desc", "req")
	p.AddStep("step1", "")
	mgr.SetExecutionMode(true)
	mgr.SetCurrentStepID(1)

	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "New Plan",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure: nested plan should be blocked")
	}
	if !strings.Contains(result.Error, "cannot create a new plan") {
		t.Errorf("unexpected error message: %s", result.Error)
	}
}

func TestEnterPlanMode_ExecuteIsActive(t *testing.T) {
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)
	// Pre-seed an active plan (draft state, not executing).
	mgr.CreatePlan("Existing Plan", "desc", "req")

	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Another Plan",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure: should reject duplicate active plan")
	}
	if !strings.Contains(result.Error, "there is already an active plan") {
		t.Errorf("unexpected error message: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// UpdatePlanProgress.Execute — all branches with active plan
// ──────────────────────────────────────────────────────────────────────────────

func newPlanManagerWithSteps(t *testing.T, nSteps int) (*plan.Manager, *plan.Plan) {
	t.Helper()
	mgr := plan.NewManager(true, false)
	p := mgr.CreatePlan("Test Plan", "desc", "req")
	for i := range nSteps {
		p.AddStep(fmt.Sprintf("Step %d", i+1), "")
	}
	return mgr, p
}

func TestUpdatePlanProgress_ExecuteIsExecuting(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	mgr, _ := newPlanManagerWithSteps(t, 1)
	mgr.SetExecutionMode(true)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"step_id": 1,
		"action":  "start",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success (execution mode path): %s", result.Error)
	}
	if !strings.Contains(result.Content, "managed automatically") {
		t.Errorf("unexpected content: %s", result.Content)
	}
}

func TestUpdatePlanProgress_ExecuteNoActivePlan(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)
	// No plan created — IsActive() = false.

	result, err := tool.Execute(context.Background(), map[string]any{
		"step_id": 1,
		"action":  "start",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure: no active plan")
	}
	if !strings.Contains(result.Error, "no active plan") {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestUpdatePlanProgress_ExecuteStepNotFound(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	mgr, _ := newPlanManagerWithSteps(t, 1)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"step_id": 99,
		"action":  "start",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure: step not found")
	}
	if !strings.Contains(result.Error, "not found") {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestUpdatePlanProgress_ExecuteStart(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	mgr, _ := newPlanManagerWithSteps(t, 2)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"step_id": 1,
		"action":  "start",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "Started step 1") {
		t.Errorf("unexpected content: %s", result.Content)
	}
}

func TestUpdatePlanProgress_ExecuteComplete(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	mgr, _ := newPlanManagerWithSteps(t, 1)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"step_id": 1,
		"action":  "complete",
		"output":  "done",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "Completed step 1") {
		t.Errorf("unexpected content: %s", result.Content)
	}
}

func TestUpdatePlanProgress_ExecuteFail(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	mgr, _ := newPlanManagerWithSteps(t, 1)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"step_id": 1,
		"action":  "fail",
		"output":  "something went wrong",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success (fail action): %s", result.Error)
	}
	if !strings.Contains(result.Content, "failed") {
		t.Errorf("unexpected content: %s", result.Content)
	}
}

func TestUpdatePlanProgress_ExecuteSkip(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	mgr, _ := newPlanManagerWithSteps(t, 1)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"step_id": 1,
		"action":  "skip",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "Skipped step 1") {
		t.Errorf("unexpected content: %s", result.Content)
	}
}

func TestUpdatePlanProgress_ExecuteInvalidAction(t *testing.T) {
	tool := NewUpdatePlanProgressTool()
	mgr, _ := newPlanManagerWithSteps(t, 1)
	tool.SetManager(mgr)

	// Bypass Validate by calling Execute directly with an invalid action.
	result, err := tool.Execute(context.Background(), map[string]any{
		"step_id": 1,
		"action":  "bogus",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for invalid action")
	}
	if !strings.Contains(result.Error, "invalid action") {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ExitPlanMode.Execute
// ──────────────────────────────────────────────────────────────────────────────

func TestExitPlanMode_ExecuteNilManager(t *testing.T) {
	tool := NewExitPlanModeTool()
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure: nil manager")
	}
}

func TestExitPlanMode_ExecuteIsExecuting(t *testing.T) {
	tool := NewExitPlanModeTool()
	mgr := plan.NewManager(true, false)
	mgr.CreatePlan("p", "", "")
	mgr.SetExecutionMode(true)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure during orchestrated execution")
	}
}

func TestExitPlanMode_ExecuteNoPlan(t *testing.T) {
	tool := NewExitPlanModeTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"reason": "abandoned",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success with no plan: %s", result.Error)
	}
}

func TestExitPlanMode_ExecuteAbandon(t *testing.T) {
	tool := NewExitPlanModeTool()
	mgr, _ := newPlanManagerWithSteps(t, 1)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"reason":  "abandoned",
		"summary": "gave up",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "abandoned") {
		t.Errorf("unexpected content: %s", result.Content)
	}
}

func TestExitPlanMode_ExecuteCompleted_TransitionError(t *testing.T) {
	tool := NewExitPlanModeTool()
	mgr, _ := newPlanManagerWithSteps(t, 1)
	tool.SetManager(mgr)
	// Plan is in Draft state — transitioning directly to Completed is invalid.

	result, err := tool.Execute(context.Background(), map[string]any{
		"reason": "completed",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure: invalid lifecycle transition Draft→Completed")
	}
	if !strings.Contains(result.Error, "cannot exit plan mode") {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// getStringField — string and non-string value branches
// ──────────────────────────────────────────────────────────────────────────────

func TestGetStringField_NonStringValue(t *testing.T) {
	// getStringField is package-private; exercise it via EnterPlanMode step fields.
	// Pass expected_artifact as a non-string (int) — should be silently ignored.
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Plan",
		"steps": []any{
			map[string]any{
				"title":             "Step 1",
				"expected_artifact": 42, // non-string: getStringField returns ""
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

func TestGetStringField_StringValue(t *testing.T) {
	// Pass expected_artifact and rollback as proper strings → exercises return s branch.
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, false)
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Plan",
		"steps": []any{
			map[string]any{
				"title":             "Step 1",
				"expected_artifact": "output.txt",
				"rollback":          "git checkout .",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.recall — tags with non-string element (skipped gracefully)
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_RecallTagsWithNonStringElement(t *testing.T) {
	tool := NewMemoryTool()
	store := newTestMemoryStore(t)
	tool.SetStore(store)

	// Tags array contains a non-string element (int 42) that should be skipped.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"query":  "anything",
		"tags":   []any{42, "validtag"}, // 42 is skipped, "validtag" added
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.recall — key exact miss (falls through to search)
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_RecallByKeyMiss(t *testing.T) {
	tool := NewMemoryTool()
	tool.SetStore(newTestMemoryStore(t))
	// Recall with a key that was never stored → store.Get returns !ok → falls through to search.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"key":    "nonexistent-key-xyz",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "No memories found") {
		t.Errorf("expected 'No memories found', got: %s", result.Content)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// memory.recall — Search path with keyed entry (byType loop, entry.Key != "")
// ──────────────────────────────────────────────────────────────────────────────

func TestMemoryTool_RecallSearchWithKeyedEntry(t *testing.T) {
	tool := NewMemoryTool()
	store := newTestMemoryStore(t)
	tool.SetStore(store)

	// Remember an entry with a key — it will appear in Search results.
	_, err := tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"content": "unique-keyed-value",
		"key":     "mykey",
	})
	if err != nil {
		t.Fatalf("remember error: %v", err)
	}

	// Recall by query (not by key) so it goes through Search, not store.Get.
	result, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"query":  "unique-keyed",
	})
	if err != nil {
		t.Fatalf("recall error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	// The keyed entry should appear in the byType loop with "- [mykey] ..." format.
	if !strings.Contains(result.Content, "mykey") {
		t.Errorf("expected key in recall result, got: %s", result.Content)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// streaming.go — context utilities
// ──────────────────────────────────────────────────────────────────────────────

func TestContextWithProgressCallback_RoundTrip(t *testing.T) {
	var called float64
	ctx := ContextWithProgressCallback(context.Background(), func(p float64, step string) {
		called = p
	})
	cb := GetProgressCallback(ctx)
	if cb == nil {
		t.Fatal("expected non-nil progress callback")
	}
	cb(0.5, "step1")
	if called != 0.5 {
		t.Errorf("expected 0.5, got %v", called)
	}
}

func TestGetProgressCallback_Missing(t *testing.T) {
	cb := GetProgressCallback(context.Background())
	if cb != nil {
		t.Error("expected nil progress callback for bare context")
	}
}

func TestContextWithFilePeekCallback_RoundTrip(t *testing.T) {
	var gotPath string
	ctx := ContextWithFilePeekCallback(context.Background(), func(path, title, content, action string) {
		gotPath = path
	})
	cb := GetFilePeekCallback(ctx)
	if cb == nil {
		t.Fatal("expected non-nil file peek callback")
	}
	cb("/tmp/foo.go", "foo", "content", "read")
	if gotPath != "/tmp/foo.go" {
		t.Errorf("expected /tmp/foo.go, got %s", gotPath)
	}
}

func TestGetFilePeekCallback_Missing(t *testing.T) {
	cb := GetFilePeekCallback(context.Background())
	if cb != nil {
		t.Error("expected nil file peek callback for bare context")
	}
}

func TestEmitFilePeek_WithCallback(t *testing.T) {
	var actions []string
	ctx := ContextWithFilePeekCallback(context.Background(), func(_, _, _, action string) {
		actions = append(actions, action)
	})
	EmitFilePeek(ctx, "f.go", "title", "body", "write")
	if len(actions) != 1 || actions[0] != "write" {
		t.Errorf("expected write action, got %v", actions)
	}
}

func TestEmitFilePeek_NoCallback(t *testing.T) {
	// No panic, no-op when callback absent.
	EmitFilePeek(context.Background(), "f.go", "title", "body", "read")
}

func TestContextWithMemoryNotify_RoundTrip(t *testing.T) {
	var gotAction string
	ctx := ContextWithMemoryNotify(context.Background(), func(action, summary string) {
		gotAction = action
	})
	EmitMemoryNotify(ctx, "remember", "stored a fact")
	if gotAction != "remember" {
		t.Errorf("expected remember, got %s", gotAction)
	}
}

func TestEmitMemoryNotify_NoCallback(t *testing.T) {
	// No panic when no callback attached.
	EmitMemoryNotify(context.Background(), "remember", "fact")
}

// ──────────────────────────────────────────────────────────────────────────────
// tool.go — NewErrorResultWithContext, ToMap, isValidGitRef, SkipDiff
// ──────────────────────────────────────────────────────────────────────────────

func TestNewErrorResultWithContext(t *testing.T) {
	r := NewErrorResultWithContext("bad thing", "file context here")
	if r.Success {
		t.Error("expected failure result")
	}
	if r.Error != "bad thing" {
		t.Errorf("unexpected error: %s", r.Error)
	}
	if r.Content != "file context here" {
		t.Errorf("unexpected content: %s", r.Content)
	}
}

func TestToolResultToMap_Success(t *testing.T) {
	r := NewSuccessResultWithData("hello", map[string]any{"key": "val"})
	m := r.ToMap()
	if m["success"] != true {
		t.Error("expected success=true")
	}
	if m["content"] != "hello" {
		t.Errorf("unexpected content: %v", m["content"])
	}
	if m["data"] == nil {
		t.Error("expected data field")
	}
}

func TestToolResultToMap_Error(t *testing.T) {
	r := NewErrorResultWithContext("oops", "context info")
	m := r.ToMap()
	if m["success"] != false {
		t.Error("expected success=false")
	}
	if m["error"] != "oops" {
		t.Errorf("unexpected error: %v", m["error"])
	}
	if m["content"] != "context info" {
		t.Errorf("unexpected content: %v", m["content"])
	}
}

func TestIsValidGitRef(t *testing.T) {
	// isValidGitRef is package-private; test via behaviour: non-empty ref not starting with '-'.
	// We test by directly using the function (same package).
	cases := []struct {
		ref   string
		valid bool
	}{
		{"main", true},
		{"HEAD~1", true},
		{"", false},
		{"-b", false},
	}
	for _, tc := range cases {
		got := isValidGitRef(tc.ref)
		if got != tc.valid {
			t.Errorf("isValidGitRef(%q) = %v, want %v", tc.ref, got, tc.valid)
		}
	}
}

func TestContextWithSkipDiff(t *testing.T) {
	ctx := context.Background()
	if ShouldSkipDiff(ctx) {
		t.Error("bare context should not skip diff")
	}
	ctx2 := ContextWithSkipDiff(ctx)
	if !ShouldSkipDiff(ctx2) {
		t.Error("skip-diff context should return true")
	}
}

func TestToolResultToMap_SuccessNoData(t *testing.T) {
	r := NewSuccessResult("just text, no data")
	m := r.ToMap()
	if m["success"] != true {
		t.Error("expected success=true")
	}
	if m["content"] != "just text, no data" {
		t.Errorf("unexpected content: %v", m["content"])
	}
	if _, hasData := m["data"]; hasData {
		t.Error("expected no data field when Data is nil")
	}
}

func TestToolResultToMap_ErrorNoContent(t *testing.T) {
	r := NewErrorResult("plain error, no context")
	m := r.ToMap()
	if m["success"] != false {
		t.Error("expected success=false")
	}
	if m["error"] != "plain error, no context" {
		t.Errorf("unexpected error: %v", m["error"])
	}
	if _, hasContent := m["content"]; hasContent {
		t.Error("expected no content field when Content is empty")
	}
}

func TestToolResultToMap_SuccessContentTruncated(t *testing.T) {
	// Generate content longer than DefaultToolResultMaxChars (30000).
	longContent := strings.Repeat("x", 31000)
	r := NewSuccessResult(longContent)
	m := r.ToMap()
	if m["success"] != true {
		t.Error("expected success=true")
	}
	content, _ := m["content"].(string)
	if len(content) <= 30000 {
		t.Errorf("expected truncated content >30000 chars in message, got %d", len(content))
	}
	if !strings.Contains(content, "OUTPUT TRUNCATED") {
		t.Errorf("expected truncation notice, got: %s", content[:100])
	}
}

func TestToolResultToMap_ErrorContentTruncated(t *testing.T) {
	// Error result with long context content that needs truncation.
	longContext := strings.Repeat("y", 31000)
	r := NewErrorResultWithContext("something failed", longContext)
	m := r.ToMap()
	if m["success"] != false {
		t.Error("expected success=false")
	}
	content, _ := m["content"].(string)
	if !strings.Contains(content, "OUTPUT TRUNCATED") {
		t.Errorf("expected truncation notice in error content")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// EnterPlanMode.Execute — approval rejection, modification, and lint error
// ──────────────────────────────────────────────────────────────────────────────

func newManagedPlanTool(decision plan.ApprovalDecision, handlerErr error) *EnterPlanModeTool {
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, true) // requireApproval=true so handler is called
	mgr.SetApprovalHandler(func(_ context.Context, _ *plan.Plan) (plan.ApprovalDecision, error) {
		return decision, handlerErr
	})
	tool.SetManager(mgr)
	return tool
}

func TestEnterPlanMode_ApprovalRejected(t *testing.T) {
	tool := newManagedPlanTool(plan.ApprovalRejected, nil)
	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Plan to reject",
		"steps": []any{map[string]any{"title": "step1"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success result (rejected plans return success with message): %s", result.Error)
	}
	if !strings.Contains(result.Content, "rejected") {
		t.Errorf("expected 'rejected' in result: %s", result.Content)
	}
}

func TestEnterPlanMode_ApprovalModified(t *testing.T) {
	tool := newManagedPlanTool(plan.ApprovalModified, nil)
	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Plan to modify",
		"steps": []any{map[string]any{"title": "step1"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success result (modification requested): %s", result.Error)
	}
	if !strings.Contains(result.Content, "modifications") {
		t.Errorf("expected 'modifications' in result: %s", result.Content)
	}
}

func TestEnterPlanMode_ApprovalUnknownDecision(t *testing.T) {
	tool := newManagedPlanTool(plan.ApprovalDecision(99), nil)
	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Plan unknown",
		"steps": []any{map[string]any{"title": "step1"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for unknown approval decision")
	}
	if !strings.Contains(result.Error, "unknown approval decision") {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestEnterPlanMode_LintError(t *testing.T) {
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, true)
	mgr.SetLintHandler(func(_ context.Context, _ *plan.Plan) error {
		return fmt.Errorf("steps must have descriptions")
	})
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Plan lint failure",
		"steps": []any{map[string]any{"title": "step1"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure due to lint error")
	}
	if !strings.Contains(result.Error, "plan validation failed") {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestEnterPlanMode_ApprovalHandlerReturnsError(t *testing.T) {
	// Handler returns a non-lint error → "approval request failed:" path.
	tool := NewEnterPlanModeTool()
	mgr := plan.NewManager(true, true)
	mgr.SetApprovalHandler(func(_ context.Context, _ *plan.Plan) (plan.ApprovalDecision, error) {
		return plan.ApprovalRejected, fmt.Errorf("network error from approval service")
	})
	tool.SetManager(mgr)

	result, err := tool.Execute(context.Background(), map[string]any{
		"title": "Plan with handler error",
		"steps": []any{map[string]any{"title": "step1"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure")
	}
	if !strings.Contains(result.Error, "approval request failed") {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// coordinate.Execute — factory paths and executeSimple with depends_on
// ──────────────────────────────────────────────────────────────────────────────

func TestCoordinate_ExecuteFactoryReturnsNil(t *testing.T) {
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return nil })
	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{map[string]any{
			"id":         "t1",
			"prompt":     "do something",
			"agent_type": "general",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure: factory returned nil")
	}
	if !strings.Contains(result.Error, "failed to create coordinator") {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestCoordinate_ExecuteFactoryNonCoordinator(t *testing.T) {
	// Factory returns something that doesn't implement coordinatorInterface.
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return "not a coordinator" })
	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{map[string]any{
			"id":         "t1",
			"prompt":     "do something",
			"agent_type": "general",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success (falls back to executeSimple): %s", result.Error)
	}
	if !strings.Contains(result.Content, "Task Plan") {
		t.Errorf("expected fallback task plan, got: %s", result.Content)
	}
}

func TestCoordinate_ExecuteSimpleWithDependsOn(t *testing.T) {
	tool := NewCoordinateTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id":         "t1",
				"prompt":     "first task",
				"agent_type": "general",
			},
			map[string]any{
				"id":         "t2",
				"prompt":     "second task",
				"agent_type": "bash",
				"depends_on": []any{"t1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}
	if !strings.Contains(result.Content, "Depends on") {
		t.Errorf("expected 'Depends on' in result, got: %s", result.Content)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Reliability fixes — regression guards (iter 1070+)
// ──────────────────────────────────────────────────────────────────────────────

// cappedBuffer ─ bounds output, reports dropped bytes, no marker when clean

func TestCappedBuffer_NoDrop(t *testing.T) {
	cb := newCappedBuffer(100)
	n, err := cb.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected Write to report n=5, got %d", n)
	}
	s := cb.String()
	if s != "hello" {
		t.Errorf("expected 'hello', got %q", s)
	}
	if strings.Contains(s, "truncated") {
		t.Error("unexpected truncation marker when nothing was dropped")
	}
}

func TestCappedBuffer_DropsOverLimit(t *testing.T) {
	cb := newCappedBuffer(10)
	n, err := cb.Write([]byte("0123456789ABCDE")) // 15 bytes, 5 over cap
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 15 {
		t.Errorf("Write must report full len even when truncated; got %d", n)
	}
	s := cb.String()
	if !strings.HasPrefix(s, "0123456789") {
		t.Errorf("expected stored prefix '0123456789', got %q", s)
	}
	if !strings.Contains(s, "truncated") {
		t.Errorf("expected truncation marker in %q", s)
	}
	if !strings.Contains(s, "5") {
		t.Errorf("expected dropped byte count '5' in %q", s)
	}
}

func TestCappedBuffer_WriteAfterFull(t *testing.T) {
	cb := newCappedBuffer(5)
	cb.Write([]byte("12345"))        // fills the buffer
	n, err := cb.Write([]byte("XY")) // entirely dropped
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("Write must report full len; got %d", n)
	}
	s := cb.String()
	if strings.Contains(s, "X") || strings.Contains(s, "Y") {
		t.Errorf("dropped bytes must not appear in output: %q", s)
	}
	if !strings.Contains(s, "truncated") {
		t.Errorf("expected truncation marker: %q", s)
	}
}

// BashTool.Execute ─ security validation runs inside Execute, not only Validate

func TestBashTool_ExecuteBlocksDangerousCommand(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewBashTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"command": "rm -rf /",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for dangerous command rm -rf /")
	}
	if !strings.Contains(result.Error, "blocked") {
		t.Errorf("expected 'blocked' in error, got: %s", result.Error)
	}
}

// HistorySearchTool ─ invalid regex returns clean error instead of panic

func TestHistorySearch_InvalidRegexReturnsError(t *testing.T) {
	tool := NewHistorySearchTool(nil)
	result, err := tool.Execute(context.Background(), map[string]any{
		"pattern": "[",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for invalid regex pattern '['")
	}
	if !strings.Contains(result.Error, "invalid regex") {
		t.Errorf("expected 'invalid regex' in error, got: %s", result.Error)
	}
}

// WebFetchTool ─ SSRF protection fires in Execute, not only in Validate

func TestWebFetchTool_BlocksLocalhostSSRF(t *testing.T) {
	tool := NewWebFetchTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"url": "http://localhost:8080/secret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for localhost URL")
	}
	if !strings.Contains(result.Error, "SSRF") && !strings.Contains(result.Error, "blocked") {
		t.Errorf("expected SSRF protection message, got: %s", result.Error)
	}
}

func TestWebFetchTool_BlocksAWSMetadata(t *testing.T) {
	tool := NewWebFetchTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"url": "http://169.254.169.254/latest/meta-data/",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for AWS IMDSv1 metadata endpoint")
	}
	if !strings.Contains(result.Error, "SSRF") && !strings.Contains(result.Error, "blocked") {
		t.Errorf("expected SSRF protection message, got: %s", result.Error)
	}
}

// BatchTool ─ path traversal blocked for all targets

func TestBatchTool_BlocksPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewBatchTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"operation":  "replace",
		"files":      []any{"../../../etc/passwd"},
		"old_string": "root",
		"new_string": "pwned",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for path traversal attempt")
	}
	if !strings.Contains(result.Error, "path validation") && !strings.Contains(result.Error, "outside") {
		t.Errorf("expected path validation error, got: %s", result.Error)
	}
}

// EditTool ─ multi-edit rejects non-unique old_string (was silently applied to first match)

func TestEditTool_MultiEdit_DuplicateOldStringRejected(t *testing.T) {
	tmpDir := t.TempDir()
	// Resolve symlinks so the path validator (which rejects symlink paths on
	// macOS where /var → /private/var) accepts the working directory.
	realTmpDir, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("failed to resolve tmpDir symlink: %v", err)
	}
	tmpDir = realTmpDir
	filePath := filepath.Join(tmpDir, "dup.txt")
	if err := os.WriteFile(filePath, []byte("foo\nbar\nfoo\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	tool := NewEditTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": filePath,
		"edits": []any{
			map[string]any{
				"old_string": "foo",
				"new_string": "qux",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when old_string is not unique in multi-edit")
	}
	// Error must mention the occurrence count (2) and context guidance
	if !strings.Contains(result.Error, "2") {
		t.Errorf("expected occurrence count '2' in error, got: %s", result.Error)
	}
}

// memory.Store ─ Get/GetByID return deep copies; mutating them must not affect the store

func TestMemoryStore_GetReturnsCopy(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir, tmpDir, 100)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	e := memory.NewEntry("test content", memory.MemoryProject)
	e.Tags = []string{"original"}
	e.Key = "mykey"
	if addErr := store.Add(e); addErr != nil {
		t.Fatalf("failed to add entry: %v", addErr)
	}

	got, ok := store.Get("mykey")
	if !ok {
		t.Fatal("expected entry to be found")
	}
	// Mutate the returned copy — must NOT affect the live store entry
	got.Tags = append(got.Tags, "mutated")
	got.Content = "mutated content"

	got2, ok := store.Get("mykey")
	if !ok {
		t.Fatal("expected entry to still be found after mutation of returned copy")
	}
	if got2.Content != "test content" {
		t.Errorf("store Content was mutated by caller; got %q", got2.Content)
	}
	if len(got2.Tags) != 1 || got2.Tags[0] != "original" {
		t.Errorf("store Tags were mutated by caller; got %v", got2.Tags)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// benign exit code handling — grep no-match should be success, not error
// ──────────────────────────────────────────────────────────────────────────────

func TestBenignNonZeroExit_GrepNoMatch(t *testing.T) {
	// grep exits 1 with no stderr when no lines match — should be benign
	if !benignNonZeroExit("grep foo file.txt", 1, "") {
		t.Error("expected grep exit 1 (no match) to be benign")
	}
}

func TestBenignNonZeroExit_GrepRealError(t *testing.T) {
	// grep exits 1 WITH stderr = real error
	if benignNonZeroExit("grep foo file.txt", 1, "grep: file.txt: No such file or directory") {
		t.Error("expected grep exit 1 with stderr to be a real error")
	}
}

func TestBenignNonZeroExit_DiffFindsChanges(t *testing.T) {
	// diff exits 1 with no stderr when files differ — benign
	if !benignNonZeroExit("diff a.txt b.txt", 1, "") {
		t.Error("expected diff exit 1 (files differ) to be benign")
	}
}

func TestBenignNonZeroExit_ExitCode2(t *testing.T) {
	// exit 2+ is always a real error even for grep/diff
	if benignNonZeroExit("grep foo file.txt", 2, "") {
		t.Error("expected exit code 2 to be a real error for grep")
	}
}

func TestBenignNonZeroExit_PipelineGrep(t *testing.T) {
	// "ps aux | grep foo" — last command determines exit
	if !benignNonZeroExit("ps aux | grep foo", 1, "") {
		t.Error("expected piped grep exit 1 to be benign")
	}
}

func TestBenignNonZeroExit_RipgrepNoMatch(t *testing.T) {
	if !benignNonZeroExit("rg 'pattern' .", 1, "") {
		t.Error("expected rg exit 1 (no match) to be benign")
	}
}

func TestExitDeterminingProgram_SimpleCommand(t *testing.T) {
	prog, _ := exitDeterminingProgram("grep foo bar.txt")
	if prog != "grep" {
		t.Errorf("expected prog='grep', got %q", prog)
	}
}

func TestExitDeterminingProgram_Pipeline(t *testing.T) {
	prog, _ := exitDeterminingProgram("cat file | grep pattern")
	if prog != "grep" {
		t.Errorf("expected prog='grep' (last in pipe), got %q", prog)
	}
}

func TestExitDeterminingProgram_CdPrefix(t *testing.T) {
	prog, _ := exitDeterminingProgram("cd /tmp && grep pattern file")
	if prog != "grep" {
		t.Errorf("expected prog='grep' after cd prefix, got %q", prog)
	}
}

func TestBenignEmptyLabel_Search(t *testing.T) {
	label := benignEmptyLabel("grep foo bar.txt")
	if label != "(no matches)" {
		t.Errorf("expected '(no matches)', got %q", label)
	}
}

func TestBenignEmptyLabel_Diff(t *testing.T) {
	label := benignEmptyLabel("diff a.txt b.txt")
	if label != "(differs)" {
		t.Errorf("expected '(differs)', got %q", label)
	}
}

func TestCommandLooksLikeValidation(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"go test ./...", true},
		{"npm test", true},
		{"cargo test", true},
		{"eslint src/", true},
		{"tsc --noEmit", true},
		{"ls -la", false},
		{"git status", false},
	}
	for _, c := range cases {
		if got := commandLooksLikeValidation(c.cmd); got != c.want {
			t.Errorf("commandLooksLikeValidation(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestBashFailureSummary_ContainsActionableHint(t *testing.T) {
	summary := bashFailureSummary("go test ./...", 1, "", "FAIL: TestFoo")
	if !strings.Contains(summary, "Actionable summary:") {
		t.Error("expected 'Actionable summary:' in failure summary")
	}
	if !strings.Contains(summary, "STDERR") {
		t.Error("expected 'STDERR' reference in failure summary")
	}
	// go test is a validation command — should give rerun hint
	if !strings.Contains(summary, "rerun") {
		t.Error("expected 'rerun' hint in validation failure summary")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// run_tests tool — framework detection and result parsing
// ──────────────────────────────────────────────────────────────────────────────

func TestRunTests_DetectGoFramework(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/test\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fw := detectTestFramework(tmpDir)
	if fw != "go" {
		t.Errorf("expected 'go', got %q", fw)
	}
}

func TestRunTests_DetectNoFramework(t *testing.T) {
	tmpDir := t.TempDir()
	fw := detectTestFramework(tmpDir)
	if fw != "" {
		t.Errorf("expected empty string for unknown dir, got %q", fw)
	}
}

func TestRunTests_ParseGoTestResultsPass(t *testing.T) {
	// Simulate a passing Go test JSON stream
	jsonOutput := `{"Action":"pass","Package":"example.com/foo","Test":"TestFoo","Elapsed":0.001}
{"Action":"pass","Package":"example.com/foo","Elapsed":0.001}`
	result := parseGoTestResults(jsonOutput, nil, 500*time.Millisecond)
	if !strings.Contains(result, "PASS") {
		t.Errorf("expected 'PASS' in result, got: %s", result)
	}
	if strings.Contains(result, "FAIL") {
		t.Errorf("unexpected 'FAIL' in passing result: %s", result)
	}
}

func TestRunTests_ParseGoTestResultsFail(t *testing.T) {
	jsonOutput := `{"Action":"output","Package":"example.com/foo","Test":"TestBar","Output":"    foo_test.go:42: got 1, want 2\n"}
{"Action":"fail","Package":"example.com/foo","Test":"TestBar","Elapsed":0.002}
{"Action":"fail","Package":"example.com/foo","Elapsed":0.002}`
	result := parseGoTestResults(jsonOutput, fmt.Errorf("exit 1"), 200*time.Millisecond)
	if !strings.Contains(result, "FAIL") {
		t.Errorf("expected 'FAIL' in result, got: %s", result)
	}
	if !strings.Contains(result, "TestBar") {
		t.Errorf("expected 'TestBar' in result, got: %s", result)
	}
	// failure location extracted from "foo_test.go:42:"
	if !strings.Contains(result, "foo_test.go:42") {
		t.Errorf("expected file:line 'foo_test.go:42' in failure locations, got: %s", result)
	}
}

func TestRunTests_FallbackGenericOnNonJSON(t *testing.T) {
	// Non-JSON output should fall through to generic parser
	result := parseGoTestResults("some plain text output\nnot json at all", nil, 100*time.Millisecond)
	if !strings.Contains(result, "PASS") && !strings.Contains(result, "some plain text") {
		t.Errorf("expected fallback to generic output, got: %s", result)
	}
}

func TestRunTests_BuildGoCommand(t *testing.T) {
	cmd, args := buildTestCommand("go", "/tmp", "TestFoo", false, false)
	if cmd != "go" {
		t.Errorf("expected 'go', got %q", cmd)
	}
	if !strings.Contains(strings.Join(args, " "), "-json") {
		t.Errorf("expected -json flag, got: %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), "TestFoo") {
		t.Errorf("expected filter 'TestFoo', got: %v", args)
	}
}

func TestRunTests_BuildGoCommandWithCoverage(t *testing.T) {
	_, args := buildTestCommand("go", "/tmp", "", false, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-coverprofile") {
		t.Errorf("expected -coverprofile with coverage=true, got: %v", args)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// read tool — path suggestion helpers
// ──────────────────────────────────────────────────────────────────────────────

func TestSuggestFilesInWorkDir_Finds(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "other.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	suggestions := suggestFilesInWorkDir(tmpDir, "main.go")
	if len(suggestions) == 0 {
		t.Fatal("expected at least one suggestion for 'main.go'")
	}
	if !strings.Contains(suggestions[0], "main.go") {
		t.Errorf("expected suggestion to contain 'main.go', got %v", suggestions)
	}
}

func TestSuggestFilesInWorkDir_EmptyOnMiss(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "app.ts"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	// Search for a completely unrelated name
	suggestions := suggestFilesInWorkDir(tmpDir, "completelydifferentname.rb")
	if len(suggestions) != 0 {
		t.Errorf("expected no suggestions, got %v", suggestions)
	}
}

func TestSuggestFilesInWorkDir_TooShortNameSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "x"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	// Single-char basename should return nothing (noise guard)
	suggestions := suggestFilesInWorkDir(tmpDir, "x")
	if len(suggestions) != 0 {
		t.Errorf("expected no suggestions for 1-char name, got %v", suggestions)
	}
}

func TestReadTool_PathValidationSuggestsWorkDirFiles(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "server.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path": "/nonexistent/outside/server.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("expected failure for outside-workdir path")
	}
	// Error message should suggest the matching file in the workdir
	combined := result.Error + result.Content
	if !strings.Contains(combined, "server.go") {
		t.Errorf("expected workdir suggestion for 'server.go', got:\nError: %s\nContent: %s", result.Error, result.Content)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// FormatUnknownToolError / suggestToolNames
// ──────────────────────────────────────────────────────────────────────────────

func TestFormatUnknownToolError_Suggestion(t *testing.T) {
	available := []string{"edit", "read", "bash", "write", "git_status"}
	msg := FormatUnknownToolError("editt", available)
	if !strings.Contains(msg, "edit") {
		t.Errorf("expected suggestion 'edit' in message, got: %s", msg)
	}
}

func TestFormatUnknownToolError_NoSuggestion(t *testing.T) {
	available := []string{"edit", "read", "bash"}
	msg := FormatUnknownToolError("xyz123completelydifferent", available)
	if strings.Contains(msg, "Did you mean") {
		t.Errorf("expected no suggestion, got: %s", msg)
	}
	if !strings.Contains(msg, "unknown tool") {
		t.Errorf("expected 'unknown tool' in message, got: %s", msg)
	}
}

func TestSuggestToolNames_PrefixMatch(t *testing.T) {
	available := []string{"git_status", "git_diff", "git_commit", "bash"}
	suggestions := suggestToolNames("git_sta", available, 3)
	if len(suggestions) == 0 {
		t.Fatal("expected at least one suggestion with 'git_sta' prefix")
	}
	if suggestions[0] != "git_status" {
		t.Errorf("expected 'git_status' as top suggestion, got %s", suggestions[0])
	}
}

func TestLevenshtein_Basic(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "a", 1},
		{"edit", "edit", 0},
		{"edit", "editt", 1}, // one insertion
		{"read", "reed", 1},  // one substitution
		{"bash", "batch", 2}, // two operations
	}
	for _, tc := range tests {
		got := levenshtein(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d; want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// VerifyCodeTool — project-type detection (no external tools required)
// ──────────────────────────────────────────────────────────────────────────────

func TestVerifyCodeTool_DetectsGo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewVerifyCodeTool(dir)
	pType, tDir := tool.detectProjectTarget(dir)
	if pType != "go" {
		t.Errorf("expected 'go', got %q", pType)
	}
	if tDir != dir {
		t.Errorf("expected target dir %q, got %q", dir, tDir)
	}
}

func TestVerifyCodeTool_DetectsRust(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewVerifyCodeTool(dir)
	pType, _ := tool.detectProjectTarget(dir)
	if pType != "rust" {
		t.Errorf("expected 'rust', got %q", pType)
	}
}

func TestVerifyCodeTool_DetectsNode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewVerifyCodeTool(dir)
	pType, _ := tool.detectProjectTarget(dir)
	if pType != "node" {
		t.Errorf("expected 'node', got %q", pType)
	}
}

func TestVerifyCodeTool_DetectsPython(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("requests\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewVerifyCodeTool(dir)
	pType, _ := tool.detectProjectTarget(dir)
	if pType != "python" {
		t.Errorf("expected 'python', got %q", pType)
	}
}

func TestVerifyCodeTool_UnknownProjectType(t *testing.T) {
	dir := t.TempDir()
	tool := NewVerifyCodeTool(dir)
	pType, _ := tool.detectProjectTarget(dir)
	if pType != "" {
		t.Errorf("expected empty type for unknown project, got %q", pType)
	}
}

func TestTrimDeltaOutput(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := trimDeltaOutput(long, 100)
	if len([]rune(got)) > 120 { // 100 + some trailing notice
		t.Errorf("trimDeltaOutput didn't trim: len=%d", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("trimDeltaOutput missing truncation notice")
	}
}

func TestTrimOutputKeepEnds(t *testing.T) {
	// 600 runes -> trim to 200; check head AND tail present
	head := strings.Repeat("H", 300)
	tail := strings.Repeat("T", 300)
	input := head + tail
	got := trimOutputKeepEnds(input, 200)
	if !strings.Contains(got, "HHH") {
		t.Error("trimOutputKeepEnds missing head")
	}
	if !strings.Contains(got, "TTT") {
		t.Error("trimOutputKeepEnds missing tail")
	}
	if !strings.Contains(got, "elided") {
		t.Error("trimOutputKeepEnds missing elided notice")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// FileReadTracker — DupCount / dedupReadStub / RecentlyReadFiles / HasBeenRead
// ──────────────────────────────────────────────────────────────────────────────

func TestFileReadTracker_DupCountAndStub(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.go")
	if err := os.WriteFile(f, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	tr := NewFileReadTracker()

	// First read — not a dup
	isDup, _, _, _ := tr.CheckAndRecord(f, 1, 100, 5)
	if isDup {
		t.Fatal("first read should not be dup")
	}

	// Second read (dupCount=1) — stub returned
	isDup, origRec, _, dupCount := tr.CheckAndRecord(f, 1, 100, 5)
	if !isDup {
		t.Fatal("second read should be dup")
	}
	if dupCount != 1 {
		t.Errorf("expected dupCount=1, got %d", dupCount)
	}
	stub, ok := dedupReadStub(origRec, f, dupCount)
	if !ok {
		t.Fatal("dedupReadStub(dupCount=1) should return ok=true")
	}
	if stub == "" {
		t.Fatal("stub should be non-empty")
	}

	// Third read (dupCount=2) — self-heal: full content should flow through
	isDup, origRec2, _, dupCount2 := tr.CheckAndRecord(f, 1, 100, 5)
	if !isDup {
		t.Fatal("third read should still be dup")
	}
	if dupCount2 != 2 {
		t.Errorf("expected dupCount=2, got %d", dupCount2)
	}
	_, ok2 := dedupReadStub(origRec2, f, dupCount2)
	if ok2 {
		t.Fatal("dedupReadStub(dupCount=2) should return ok=false (self-healing: send full content)")
	}

	// Fourth read (dupCount=3) — back to stub
	isDup, origRec3, _, dupCount3 := tr.CheckAndRecord(f, 1, 100, 5)
	if !isDup {
		t.Fatal("fourth read should be dup")
	}
	_, ok3 := dedupReadStub(origRec3, f, dupCount3)
	if !ok3 {
		t.Fatal("dedupReadStub(dupCount=3) should return ok=true again")
	}
}

func TestFileReadTracker_HasBeenRead(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "b.go")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	tr := NewFileReadTracker()
	if tr.HasBeenRead(f) {
		t.Fatal("should not have been read yet")
	}
	tr.CheckAndRecord(f, 1, 100, 1)
	if !tr.HasBeenRead(f) {
		t.Fatal("should have been read after CheckAndRecord")
	}
	tr.InvalidateFile(f)
	if tr.HasBeenRead(f) {
		t.Fatal("should not have been read after invalidation")
	}
}

func TestFileReadTracker_RecentlyReadFiles(t *testing.T) {
	dir := t.TempDir()
	files := []string{"c.go", "d.go", "e.go"}
	for _, name := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tr := NewFileReadTracker()
	for _, name := range files {
		tr.CheckAndRecord(filepath.Join(dir, name), 1, 100, len(name))
	}
	recent := tr.RecentlyReadFiles(2)
	if len(recent) != 2 {
		t.Fatalf("expected 2, got %d", len(recent))
	}
	// Most recently recorded should be first
	if !strings.HasSuffix(recent[0], "e.go") {
		t.Errorf("expected e.go first, got %s", recent[0])
	}
}

// TestEditTool_ReadBeforeEdit_Blocked verifies that editing an existing file
// that hasn't been read in the session is blocked when a ReadTracker is
// injected via ReadTrackerCtxKey.
func TestEditTool_ReadBeforeEdit_Blocked(t *testing.T) {
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(tmpDir, "target.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool(tmpDir)
	rt := NewFileReadTracker()
	ctx := context.WithValue(context.Background(), ReadTrackerCtxKey{}, rt)

	// No read recorded → should be blocked.
	result, err := tool.Execute(ctx, map[string]any{
		"file_path":  filePath,
		"old_string": "package main",
		"new_string": "package main // edited",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected edit to be blocked (read-before-edit), but it succeeded")
	}
	if !strings.Contains(result.Error+result.Content, "read-before-edit") {
		t.Errorf("expected read-before-edit message, got: %q", result.Error+result.Content)
	}
}

// TestEditTool_ReadBeforeEdit_AllowedAfterRead verifies that an edit succeeds
// once the file appears in the ReadTracker (i.e., the agent has read it).
func TestEditTool_ReadBeforeEdit_AllowedAfterRead(t *testing.T) {
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(tmpDir, "target.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool(tmpDir)
	rt := NewFileReadTracker()
	rt.CheckAndRecord(filePath, 0, 0, 13) // simulate having read the file
	ctx := context.WithValue(context.Background(), ReadTrackerCtxKey{}, rt)

	result, err := tool.Execute(ctx, map[string]any{
		"file_path":  filePath,
		"old_string": "package main",
		"new_string": "package main // edited",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected edit to succeed after read, got error: %q", result.Error)
	}
}

// TestEditTool_ReadBeforeEdit_NoTrackerNoBlock verifies that without a
// ReadTracker in context, the safety check is a no-op (backward compat).
func TestEditTool_ReadBeforeEdit_NoTrackerNoBlock(t *testing.T) {
	tmpDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(tmpDir, "target.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool(tmpDir)
	// No ReadTrackerCtxKey in context — check should be skipped.
	result, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  filePath,
		"old_string": "package main",
		"new_string": "package main // edited",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected edit to succeed without tracker, got: %q", result.Error)
	}
}

// ----- check_impact.go tests (moved here; file_relevance + review_changes moved to standalone *_test.go) -----

func TestFileRelevanceScore_SourceHigherThanVendor(t *testing.T) {
	src := fileRelevanceScore("internal/pkg/core.go", 3)
	vnd := fileRelevanceScore("vendor/pkg/lib.go", 100)
	if src <= vnd {
		t.Errorf("source (%v) should outrank vendor (%v) even with lower match count", src, vnd)
	}
}

func TestFileRelevanceScore_GeneratedLower(t *testing.T) {
	src := fileRelevanceScore("internal/service.go", 5)
	gen := fileRelevanceScore("internal/service_gen.go", 5)
	if src <= gen {
		t.Errorf("source (%v) should outrank generated (%v)", src, gen)
	}
}

func TestIsTestPath_Detects(t *testing.T) {
	cases := []struct {
		path   string
		expect bool
	}{
		{"internal/foo_test.go", true},
		{"src/foo.test.ts", true},
		{"src/foo.spec.js", true},
		{"tests/helper.go", true},
		{"testdata/fixture.json", true},
		{"internal/foo.go", false},
		{"internal/testing_utils.go", false},
	}
	for _, c := range cases {
		if got := isTestPath(c.path); got != c.expect {
			t.Errorf("isTestPath(%q) = %v, want %v", c.path, got, c.expect)
		}
	}
}

func TestIsVendorPath_Detects(t *testing.T) {
	cases := []struct {
		path   string
		expect bool
	}{
		{"vendor/pkg/lib.go", true},
		{"node_modules/react/index.js", true},
		{"/repo/build/output.js", true},
		{"internal/pkg/lib.go", false},
	}
	for _, c := range cases {
		if got := isVendorPath(c.path); got != c.expect {
			t.Errorf("isVendorPath(%q) = %v, want %v", c.path, got, c.expect)
		}
	}
}

// ----- check_impact.go tests -----

func TestCheckImpactTool_SymbolRequired(t *testing.T) {
	tool := NewCheckImpactTool(t.TempDir())
	err := tool.Validate(map[string]any{})
	if err == nil {
		t.Fatal("expected validation error for missing symbol")
	}
}

func TestCheckImpactTool_FindsSymbol(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a file that uses a known symbol.
	src := `package main

func MySymbol() {}
func caller() { MySymbol() }
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// Initialize a git repo so gitignore logic works.
	cmd := exec.Command("git", "init", tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	tool := NewCheckImpactTool(tmpDir)
	result, err := tool.Execute(context.Background(), map[string]any{"symbol": "MySymbol"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %q", result.Error)
	}
	if !strings.Contains(result.Content, "MySymbol") {
		t.Errorf("expected symbol in report, got: %q", result.Content)
	}
}
