package tools

import (
	"fmt"
	"sync"
	"testing"
	"time"

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

func TestCheckpointJournalMultipleEntries(t *testing.T) {
	j := NewCheckpointJournal()
	for i := 0; i < 50; i++ {
		call := makeFC("call-"+string(rune('A'+i%26)), "write",
			map[string]any{"file_path": "/tmp/test" + string(rune('A'+i%26)) + ".go"})
		j.Record(call, makeResult("ok"))
	}
	if j.Len() != 50 {
		t.Errorf("Len() = %d, want 50", j.Len())
	}
}

func TestCheckpointJournalRecordSerialized(t *testing.T) {
	j := NewCheckpointJournal()
	sig := checkpointSignature("write", map[string]any{"file_path": "/tmp/restored.go"})
	j.RecordSerialized("call-restored", "write", map[string]any{"file_path": "/tmp/restored.go"}, "File written", sig, time.Now())

	if j.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", j.Len())
	}
	// Find by call ID.
	call := makeFC("call-restored", "write", map[string]any{"file_path": "/tmp/restored.go"})
	result, reason, ok := j.Lookup(call)
	if !ok {
		t.Fatal("should find restored checkpoint by call ID")
	}
	if reason != "checkpoint_call_id" {
		t.Errorf("reason = %q, want checkpoint_call_id", reason)
	}
	if result.Content != "File written" {
		t.Errorf("restored result = %q, want File written", result.Content)
	}
	// Find by signature (different call ID).
	call2 := makeFC("", "write", map[string]any{"file_path": "/tmp/restored.go"})
	_, reason2, ok2 := j.Lookup(call2)
	if !ok2 {
		t.Fatal("should find restored checkpoint by signature")
	}
	if reason2 != "checkpoint_signature" {
		t.Errorf("reason = %q, want checkpoint_signature", reason2)
	}

	// Empty resultContent falls back to "[restored from session]".
	j2 := NewCheckpointJournal()
	j2.RecordSerialized("c2", "read", map[string]any{"file_path": "/tmp/x.go"}, "", "", time.Now())
	r2, _, _ := j2.Lookup(makeFC("c2", "read", nil))
	if r2.Content != "[restored from session]" {
		t.Errorf("empty content fallback = %q", r2.Content)
	}
}

func TestCheckpointSignature(t *testing.T) {
	sig1 := checkpointSignature("write", map[string]any{"path": "/tmp/a.go"})
	sig2 := checkpointSignature("write", map[string]any{"path": "/tmp/a.go"})
	sig3 := checkpointSignature("write", map[string]any{"path": "/tmp/b.go"})
	sig4 := checkpointSignature("edit", map[string]any{"path": "/tmp/a.go"})
	if sig1 != sig2 {
		t.Error("same tool+args should have same signature")
	}
	if sig1 == sig3 {
		t.Error("different args should have different signature")
	}
	if sig1 == sig4 {
		t.Error("different tool should have different signature")
	}
	if empty := checkpointSignature("", nil); empty != "" {
		t.Error("empty tool name should return empty signature")
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
