package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloneRegistryForWorkDirRetargetsAndSeparatesBuiltins(t *testing.T) {
	baseDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	isolatedDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := DefaultRegistry(baseDir)

	originalPin, _ := base.Get("pin_context")
	originalPin.(*PinContextTool).SetWorkDir(baseDir)
	originalShared, _ := base.Get("shared_memory")
	originalShared.(*SharedMemoryTool).SetAgentID("parent-agent")

	clonedRegistry, ok := CloneRegistryForWorkDir(base, isolatedDir).(*Registry)
	if !ok {
		t.Fatalf("clone type = %T, want *Registry", CloneRegistryForWorkDir(base, isolatedDir))
	}
	if len(clonedRegistry.Names()) != len(base.Names()) {
		t.Fatalf("clone has %d tools, base has %d", len(clonedRegistry.Names()), len(base.Names()))
	}
	for _, sourceTool := range base.List() {
		clonedTool, exists := clonedRegistry.Get(sourceTool.Name())
		if !exists {
			t.Errorf("clone lost built-in tool %q", sourceTool.Name())
			continue
		}
		// Zero-sized stateless tools may legally share the runtime's zerobase
		// address even when constructed separately.
		_, statelessDiff := sourceTool.(*DiffTool)
		_, statelessEnv := sourceTool.(*EnvTool)
		if sourceTool == clonedTool && !statelessDiff && !statelessEnv {
			t.Errorf("built-in tool %q still shares its source instance", sourceTool.Name())
		}
	}

	originalRead, _ := base.Get("read")
	clonedRead, _ := clonedRegistry.Get("read")
	if originalRead == clonedRead {
		t.Fatal("read tool instance was shared")
	}
	if got := clonedRead.(*ReadTool).workDir; got != isolatedDir {
		t.Fatalf("cloned read root = %q, want %q", got, isolatedDir)
	}
	if got := originalRead.(*ReadTool).workDir; got != baseDir {
		t.Fatalf("source read root changed to %q", got)
	}

	sameRootRead := CloneToolForWorkDir(originalRead, "").(*ReadTool)
	if sameRootRead == originalRead || sameRootRead.workDir != baseDir {
		t.Fatalf("ordinary agent clone = %#v, source = %#v", sameRootRead, originalRead)
	}

	clonedPin, _ := clonedRegistry.Get("pin_context")
	if clonedPin == originalPin || clonedPin.(*PinContextTool).workDir != isolatedDir {
		t.Fatalf("pin_context was not independently retargeted: %#v", clonedPin)
	}
	clonedShared, _ := clonedRegistry.Get("shared_memory")
	if clonedShared == originalShared || clonedShared.(*SharedMemoryTool).agentID != "" {
		t.Fatalf("shared_memory retained per-agent identity: %#v", clonedShared)
	}

	writeTool, _ := clonedRegistry.Get("write")
	blocked, err := writeTool.Execute(context.Background(), map[string]any{
		"file_path": filepath.Join(baseDir, "must-not-write.txt"),
		"content":   "wrong workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Success {
		t.Fatal("retargeted write tool accepted the source workspace")
	}
	writtenPath := filepath.Join(isolatedDir, "isolated.txt")
	written, err := writeTool.Execute(context.Background(), map[string]any{
		"file_path": writtenPath,
		"content":   "isolated",
	})
	if err != nil || !written.Success {
		t.Fatalf("write in isolated workspace = %+v, %v", written, err)
	}
	if data, readErr := os.ReadFile(writtenPath); readErr != nil || string(data) != "isolated" {
		t.Fatalf("isolated output = %q, %v", data, readErr)
	}

	originalTodo, _ := base.Get("todo")
	clonedTodo, _ := clonedRegistry.Get("todo")
	if originalTodo == clonedTodo {
		t.Fatal("todo state was shared")
	}
	_, err = clonedTodo.Execute(context.Background(), map[string]any{"todos": []any{
		map[string]any{"content": "isolated task", "status": "in_progress", "active_form": "Testing isolation"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(originalTodo.(*TodoTool).GetItems()) != 0 {
		t.Fatal("updating cloned todo mutated source todo state")
	}

	listedTool, _ := clonedRegistry.Get("tools_list")
	listed := listedTool.(*ToolsListTool)
	if listed.baseRegistry != clonedRegistry {
		t.Fatal("tools_list still points at the source registry")
	}
	result, err := listed.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(result.Content, "- **read**:"); count != 1 {
		t.Fatalf("cloned tools_list contains read %d times", count)
	}
}
