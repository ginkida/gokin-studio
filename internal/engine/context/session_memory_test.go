package context

import (
	stdcontext "context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

type blockingSessionSummarizer struct {
	started chan struct{}
	release chan struct{}
	result  string
}

func (s *blockingSessionSummarizer) Summarize(stdcontext.Context, []*genai.Content, string) (string, error) {
	close(s.started)
	<-s.release
	return s.result, nil
}

func sessionMemoryHistory(task string, count int) []*genai.Content {
	history := make([]*genai.Content, 0, count)
	for i := 0; i < count-1; i++ {
		var role genai.Role = genai.RoleModel
		if i%2 == 0 {
			role = genai.RoleUser
		}
		history = append(history, genai.NewContentFromText("context message with enough useful detail", role))
	}
	return append(history, genai.NewContentFromText(task, genai.RoleUser))
}

func TestSessionMemoryWritesAtomicPrivateSnapshot(t *testing.T) {
	root := t.TempDir()
	manager := NewSessionMemoryManager(root, DefaultSessionMemoryConfig())
	manager.Extract(sessionMemoryHistory("Newest concrete task that must be persisted", 4), 100)
	path := filepath.Join(root, ".gokin", ".session-memory.md")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "Newest concrete task") {
		t.Fatalf("session memory=%q err=%v", data, err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("session memory mode=%#o, want 0640", got)
	}
	if temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".gokin-*.tmp")); err != nil || len(temps) != 0 {
		t.Fatalf("session memory leaked temps=%v err=%v", temps, err)
	}
}

func TestSessionMemoryRejectsSymlinkOnLoad(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".gokin")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside")
	if err := os.WriteFile(target, []byte("secret outside content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".session-memory.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	manager := NewSessionMemoryManager(root, DefaultSessionMemoryConfig())
	manager.content = "existing"
	manager.LoadFromDisk()
	if got := manager.GetContent(); got != "existing" {
		t.Fatalf("symlink content loaded: %q", got)
	}
}

func TestOlderLLMSessionMemoryCannotOverwriteNewerExtraction(t *testing.T) {
	root := t.TempDir()
	summarizer := &blockingSessionSummarizer{
		started: make(chan struct{}),
		release: make(chan struct{}),
		result:  "STALE LLM RESULT",
	}
	manager := NewSessionMemoryManager(root, DefaultSessionMemoryConfig())
	manager.revision = 1
	done := make(chan struct{})
	go func() {
		manager.extractWithLLM(sessionMemoryHistory("Older task that starts delayed summary", 10), 1, summarizer)
		close(done)
	}()
	select {
	case <-summarizer.started:
	case <-time.After(time.Second):
		t.Fatal("LLM summary did not start")
	}
	manager.Extract(sessionMemoryHistory("Fresh task that must remain authoritative", 4), 200)
	close(summarizer.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stale LLM summary did not finish")
	}
	if got := manager.GetContent(); !strings.Contains(got, "Fresh task") || strings.Contains(got, "STALE LLM RESULT") {
		t.Fatalf("latest session memory was overwritten: %q", got)
	}
}

func TestCloneSessionMemoryHistoryOwnsPartsAndSignatures(t *testing.T) {
	original := genai.NewContentFromText("original", genai.RoleUser)
	original.Parts[0].ThoughtSignature = []byte("signature")
	original.Parts[0].FunctionCall = &genai.FunctionCall{Name: "tool", Args: map[string]any{"path": "original"}}
	clone := cloneSessionMemoryHistory([]*genai.Content{original})
	original.Parts[0].Text = "mutated"
	original.Parts[0].ThoughtSignature[0] = 'X'
	original.Parts[0].FunctionCall.Args["path"] = "mutated"
	if clone[0] == original || clone[0].Parts[0] == original.Parts[0] {
		t.Fatal("history clone retained mutable content or part pointers")
	}
	if clone[0].Parts[0].Text != "original" || string(clone[0].Parts[0].ThoughtSignature) != "signature" ||
		clone[0].Parts[0].FunctionCall.Args["path"] != "original" {
		t.Fatalf("clone changed with source: %+v", clone[0].Parts[0])
	}
}
