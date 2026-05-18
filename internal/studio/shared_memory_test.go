package studio

import (
	"testing"
	"time"
)

func TestSharedMemoryWriteReadDelete(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("k1", "value-1", "fact", "agent-A")
	got, ok := sm.Read("k1")
	if !ok {
		t.Fatal("Read missed an entry that was just written")
	}
	if got.Value != "value-1" || got.Type != "fact" || got.Source != "agent-A" || got.Version != 1 {
		t.Errorf("unexpected entry: %+v", got)
	}

	// Write again — version must bump.
	sm.Write("k1", "value-2", "fact", "agent-B")
	got, _ = sm.Read("k1")
	if got.Value != "value-2" || got.Version != 2 || got.Source != "agent-B" {
		t.Errorf("expected version 2 with new value/source, got %+v", got)
	}

	// Delete.
	if !sm.Delete("k1") {
		t.Error("Delete on existing key should return true")
	}
	if _, ok := sm.Read("k1"); ok {
		t.Error("Read after Delete should miss")
	}
	if sm.Delete("k1") {
		t.Error("second Delete on missing key should return false")
	}
}

func TestSharedMemoryTTLExpires(t *testing.T) {
	sm := NewSharedMemory()
	sm.WriteWithTTL("expiring", "hi", "fact", "agent-A", 10*time.Millisecond)
	if _, ok := sm.Read("expiring"); !ok {
		t.Fatal("entry should be live immediately after write")
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := sm.Read("expiring"); ok {
		t.Error("entry should have expired after TTL elapsed")
	}
}

func TestSharedMemoryReadByType(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("a", 1, "fact", "x")
	sm.Write("b", 2, "insight", "x")
	sm.Write("c", 3, "fact", "y")

	facts := sm.ReadByType("fact")
	if len(facts) != 2 {
		t.Errorf("expected 2 facts, got %d: %+v", len(facts), facts)
	}
	insights := sm.ReadByType("insight")
	if len(insights) != 1 {
		t.Errorf("expected 1 insight, got %d: %+v", len(insights), insights)
	}
}

func TestSharedMemoryGetForContextFiltersOwnAgent(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("mine", "secret", "fact", "agent-A")
	sm.Write("theirs", "hi", "fact", "agent-B")

	ctx := sm.GetForContext("agent-A", 10)
	if ctx == "" {
		t.Fatal("context should include agent-B's entry")
	}
	if contains(ctx, "mine") {
		t.Errorf("agent-A should NOT see its own entry, got: %q", ctx)
	}
	if !contains(ctx, "theirs") {
		t.Errorf("agent-A should see agent-B's entry, got: %q", ctx)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestSharedMemory_ReadAll verifies that ReadAll returns live entries (sorted
// newest-first) and skips expired ones.
func TestSharedMemory_ReadAll(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("a", "val-a", "fact", "agent-A")
	sm.Write("b", "val-b", "fact", "agent-B")
	// Write an entry that expires immediately.
	sm.WriteWithTTL("exp", "gone", "fact", "agent-C", time.Millisecond)
	time.Sleep(5 * time.Millisecond) // let it expire

	all := sm.ReadAll()
	if len(all) != 2 {
		t.Fatalf("ReadAll: expected 2 live entries, got %d: %+v", len(all), all)
	}
	// Both live entries must be present (order may vary).
	keys := map[string]bool{}
	for _, e := range all {
		keys[e.Key] = true
	}
	if !keys["a"] || !keys["b"] {
		t.Errorf("ReadAll missing expected keys; got keys: %v", keys)
	}
}

// TestGetForContext_ZeroMaxEntries verifies the maxEntries <= 0 early-return.
func TestGetForContext_ZeroMaxEntries(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("k", "v", "fact", "agent-B")
	if got := sm.GetForContext("agent-A", 0); got != "" {
		t.Errorf("GetForContext with maxEntries=0: got %q, want empty string", got)
	}
	if got := sm.GetForContext("agent-A", -1); got != "" {
		t.Errorf("GetForContext with maxEntries=-1: got %q, want empty string", got)
	}
}

// TestGetForContext_AllOwnAgent verifies that when every entry belongs to the
// calling agent, picked is empty and GetForContext returns "".
func TestGetForContext_AllOwnAgent(t *testing.T) {
	sm := NewSharedMemory()
	sm.Write("mine1", "val1", "fact", "agent-A")
	sm.Write("mine2", "val2", "fact", "agent-A")
	if got := sm.GetForContext("agent-A", 10); got != "" {
		t.Errorf("GetForContext should return empty when all entries are own-agent; got %q", got)
	}
}

// TestGetForContext_Truncation verifies that when there are more live entries
// than maxEntries, only the maxEntries newest entries are returned.
func TestGetForContext_Truncation(t *testing.T) {
	sm := NewSharedMemory()
	for i := 0; i < 5; i++ {
		sm.Write(string(rune('a'+i)), i, "fact", "agent-B")
	}
	ctx := sm.GetForContext("agent-A", 2)
	if ctx == "" {
		t.Fatal("GetForContext returned empty when entries exist")
	}
	// Count newlines — one per entry.
	count := 0
	for _, c := range ctx {
		if c == '\n' {
			count++
		}
	}
	if count > 2 {
		t.Errorf("expected at most 2 entries (lines), got %d lines in context: %q", count, ctx)
	}
}
