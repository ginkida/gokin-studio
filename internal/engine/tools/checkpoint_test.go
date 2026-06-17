package tools

import (
	"fmt"
	"sync"
	"testing"

	"google.golang.org/genai"
)

func makeFC(id, name string, args map[string]any) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: name, Args: args}
}

func makeResult(content string) ToolResult {
	return ToolResult{Content: content, Success: true}
}

func TestCheckpointJournal_RoundTripByCallID(t *testing.T) {
	j := NewCheckpointJournal()
	call := makeFC("id-1", "edit", map[string]any{"file_path": "foo.go"})
	res := makeResult("ok")
	j.Record(call, res)

	got, matchType, found := j.Lookup(call)
	if !found {
		t.Fatal("expected hit, got miss")
	}
	if matchType != "checkpoint_call_id" {
		t.Errorf("matchType = %q, want checkpoint_call_id", matchType)
	}
	if got.Content != res.Content {
		t.Errorf("content = %q, want %q", got.Content, res.Content)
	}
}

func TestCheckpointJournal_LookupBySignature(t *testing.T) {
	j := NewCheckpointJournal()
	// Record with one call ID
	call1 := makeFC("id-1", "edit", map[string]any{"file_path": "foo.go"})
	j.Record(call1, makeResult("written"))

	// Lookup with different call ID but same tool+args → signature hit
	call2 := makeFC("id-999", "edit", map[string]any{"file_path": "foo.go"})
	got, matchType, found := j.Lookup(call2)
	if !found {
		t.Fatal("expected signature hit, got miss")
	}
	if matchType != "checkpoint_signature" {
		t.Errorf("matchType = %q, want checkpoint_signature", matchType)
	}
	if got.Content != "written" {
		t.Errorf("content = %q, want written", got.Content)
	}
}

func TestCheckpointJournal_LookupMiss(t *testing.T) {
	j := NewCheckpointJournal()
	call := makeFC("id-x", "read", map[string]any{"file_path": "bar.go"})
	_, matchType, found := j.Lookup(call)
	if found {
		t.Fatalf("expected miss, got hit with matchType=%q", matchType)
	}
	if matchType != "" {
		t.Errorf("matchType = %q, want empty string", matchType)
	}
}

func TestCheckpointJournal_RecordEmptyCallID(t *testing.T) {
	j := NewCheckpointJournal()
	// Empty call ID — only signature lookup should work
	call := makeFC("", "bash", map[string]any{"command": "ls"})
	j.Record(call, makeResult("file.go"))

	if j.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", j.Len())
	}

	got, matchType, found := j.Lookup(call)
	if !found {
		t.Fatal("expected signature hit for empty-ID record, got miss")
	}
	if matchType != "checkpoint_signature" {
		t.Errorf("matchType = %q, want checkpoint_signature", matchType)
	}
	if got.Content != "file.go" {
		t.Errorf("content = %q, want file.go", got.Content)
	}
}

func TestCheckpointJournal_Clear(t *testing.T) {
	j := NewCheckpointJournal()
	call := makeFC("id-2", "write", map[string]any{"path": "x.go"})
	j.Record(call, makeResult("done"))

	j.Clear()

	if j.Len() != 0 {
		t.Fatalf("Len() after Clear = %d, want 0", j.Len())
	}
	_, _, found := j.Lookup(call)
	if found {
		t.Fatal("expected miss after Clear, got hit")
	}
}

func TestCheckpointJournal_EntriesIsCopy(t *testing.T) {
	j := NewCheckpointJournal()
	call := makeFC("id-3", "read", map[string]any{"file_path": "z.go"})
	j.Record(call, makeResult("content"))

	entries := j.Entries()
	if len(entries) != 1 {
		t.Fatalf("len(Entries()) = %d, want 1", len(entries))
	}
	// Mutate the returned copy
	entries[0].ToolName = "MUTATED"

	// Internal state must be unchanged
	internal := j.Entries()
	if internal[0].ToolName == "MUTATED" {
		t.Fatal("Entries() returned a reference, not a copy — mutation leaked")
	}
}

func TestCheckpointJournal_ConcurrentSafe(t *testing.T) {
	j := NewCheckpointJournal()
	var wg sync.WaitGroup
	for g := range 10 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 100 {
				call := makeFC(fmt.Sprintf("id-%d-%d", g, i), "edit",
					map[string]any{"file_path": fmt.Sprintf("file%d.go", i)})
				j.Record(call, makeResult("ok"))
				j.Lookup(call)
			}
		}(g)
	}
	wg.Wait()
	if j.Len() == 0 {
		t.Fatal("no entries recorded")
	}
}
