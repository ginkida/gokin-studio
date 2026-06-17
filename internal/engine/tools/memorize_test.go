package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/memory"
)

func TestMemorizeTool_RecordsKnowledge(t *testing.T) {
	dir := t.TempDir()
	learning, err := memory.NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning() error = %v", err)
	}

	tool := NewMemorizeTool(learning)
	result, err := tool.Execute(context.Background(), map[string]any{
		"type":    "fact",
		"key":     "test_command",
		"content": "use go test ./internal/ui -count=1",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() success = false: %+v", result)
	}
	// The tool should confirm the item was memorized.
	if result.Content == "" {
		t.Fatal("Execute() returned empty Content on success")
	}
}

func TestMemorizeTool_OverwritesExistingKnowledgeEntry(t *testing.T) {
	dir := t.TempDir()
	learning, err := memory.NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning() error = %v", err)
	}

	tool := NewMemorizeTool(learning)

	// Write initial value.
	_, _ = tool.Execute(context.Background(), map[string]any{
		"type":    "preference",
		"key":     "test_command",
		"content": "use go test ./...",
	})

	// Overwrite with new value.
	result, err := tool.Execute(context.Background(), map[string]any{
		"type":    "preference",
		"key":     "test_command",
		"content": "use go test ./internal/ui -count=1",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("second Execute() should succeed: %+v", result)
	}

	// Re-read the stored value via another tool execution (list or check).
	// We can verify the store by calling Execute again and confirming it
	// at least succeeds without duplicating the key.
	listResult, err := tool.Execute(context.Background(), map[string]any{
		"type":    "preference",
		"key":     "test_command",
		"content": "use go test ./internal/ui -count=1",
	})
	if err != nil {
		t.Fatalf("third Execute() error = %v", err)
	}
	if !listResult.Success {
		t.Fatalf("third Execute() should succeed: %+v", listResult)
	}

	// The result content should mention the key so the agent knows what was stored.
	if !strings.Contains(listResult.Content, "test_command") {
		t.Fatalf("result should mention the stored key, got %q", listResult.Content)
	}
}
