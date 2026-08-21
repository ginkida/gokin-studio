package context

import (
	stdcontext "context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/chat"
)

func TestContextAgentCheckpointIsAtomicPrivateAndUnique(t *testing.T) {
	root := t.TempDir()
	session := chat.NewSession()
	session.AddUserMessage("checkpoint me")
	manager := &ContextManager{}
	agent := NewContextAgent(manager, session, root)
	agent.Checkpoint(stdcontext.Background())
	agent.Checkpoint(stdcontext.Background())

	dir := filepath.Join(root, "checkpoints")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("checkpoint count=%d, want two unique files", len(entries))
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("checkpoint %s mode=%v, want private regular file", entry.Name(), info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			History int
		}
		if err := json.Unmarshal(data, &record); err != nil || record.History != 1 {
			t.Fatalf("checkpoint record=%+v err=%v", record, err)
		}
	}
	if temps, err := filepath.Glob(filepath.Join(dir, ".gokin-*.tmp")); err != nil || len(temps) != 0 {
		t.Fatalf("checkpoint leaked temps=%v err=%v", temps, err)
	}
}

func TestContextAgentRotationIgnoresSymlinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "checkpoints")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "cp_link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	agent := &ContextAgent{}
	agent.rotateCheckpoints(dir, 0)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("rotation removed symlink target: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("rotation removed ignored symlink: %v", err)
	}
}

func TestContextAgentStopIsIdempotent(t *testing.T) {
	agent := &ContextAgent{stopChan: make(chan struct{})}
	agent.Stop()
	agent.Stop()
}
