package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOptimizersRejectSymlinkedStorageAndNilRecords(t *testing.T) {
	root := t.TempDir()
	memoryDir := filepath.Join(root, "memory")
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, []byte(`{"danger":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prompt_variants.json", "strategy_metrics.json", "delegation_metrics.json"} {
		link := filepath.Join(memoryDir, name)
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}

	if stats := NewPromptOptimizer(root).GetStats(); stats.TotalVariants != 0 {
		t.Fatalf("prompt optimizer loaded symlinked state: %+v", stats)
	}
	if metrics := NewStrategyOptimizer(root).GetAllMetrics(); len(metrics) != 0 {
		t.Fatalf("strategy optimizer loaded symlinked state: %+v", metrics)
	}
	if stats := NewDelegationMetrics(root).GetStats(); stats["total_paths"] != 0 {
		t.Fatalf("delegation metrics loaded symlinked state: %+v", stats)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != `{"danger":null}` {
		t.Fatalf("optimizer changed symlink target: %q err=%v", data, err)
	}
}

func TestOptimizersDiscardNilPersistedRecords(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"prompt_variants.json":    `{"nil":null}`,
		"strategy_metrics.json":   `{"nil":null}`,
		"delegation_metrics.json": `{"path_metrics":{"nil":null},"rule_weights":null}`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := NewPromptOptimizer(root).GetStats().TotalVariants; got != 0 {
		t.Fatalf("prompt optimizer retained %d nil records", got)
	}
	if got := len(NewStrategyOptimizer(root).GetAllMetrics()); got != 0 {
		t.Fatalf("strategy optimizer retained %d nil records", got)
	}
	if got := NewDelegationMetrics(root).GetStats()["total_paths"]; got != 0 {
		t.Fatalf("delegation metrics retained nil record: %v", got)
	}
}

func TestOptimizersRecoverFromNullDocuments(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prompt_variants.json", "strategy_metrics.json", "delegation_metrics.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("null"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prompts := NewPromptOptimizer(root)
	prompts.RecordExecution("base", "variation", true, 1, 0)
	strategies := NewStrategyOptimizer(root)
	strategies.RecordExecution("strategy", "task", true, 0)
	delegations := NewDelegationMetrics(root)
	delegations.RecordExecution("from", "to", "task", true, 0, "")
	if prompts.GetStats().TotalVariants != 1 || len(strategies.GetAllMetrics()) != 1 || delegations.GetStats()["total_paths"] != 1 {
		t.Fatal("optimizer did not recover from a null persisted document")
	}
	if err := prompts.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := strategies.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := delegations.Clear(); err != nil {
		t.Fatal(err)
	}
}
