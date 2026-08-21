package agent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSharedMemoryOwnsEntrySnapshots(t *testing.T) {
	memory := NewSharedMemory()
	input := map[string]any{
		"nested": map[string]any{"value": "original"},
		"items":  []any{map[string]any{"value": "original"}},
	}
	memory.Write("entry", input, SharedEntryTypeFact, "writer")
	input["nested"].(map[string]any)["value"] = "input-mutated"
	input["items"].([]any)[0].(map[string]any)["value"] = "input-mutated"

	first, ok := memory.Read("entry")
	if !ok {
		t.Fatal("stored entry is missing")
	}
	firstValue := first.Value.(map[string]any)
	if firstValue["nested"].(map[string]any)["value"] != "original" ||
		firstValue["items"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatalf("write retained caller-owned data: %#v", firstValue)
	}
	first.Key = "reader-mutated"
	firstValue["nested"].(map[string]any)["value"] = "reader-mutated"

	second, ok := memory.Read("entry")
	if !ok || second.Key != "entry" || second.Value.(map[string]any)["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("read exposed internal entry: %+v, ok=%v", second, ok)
	}
	all := memory.ReadAll()
	all[0].Value.(map[string]any)["nested"].(map[string]any)["value"] = "read-all-mutated"
	third, _ := memory.Read("entry")
	if third.Value.(map[string]any)["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("ReadAll exposed internal value: %+v", third.Value)
	}
}

func TestSharedMemorySubscribersReceiveStableIndependentEntries(t *testing.T) {
	memory := NewSharedMemory()
	firstSubscriber := memory.Subscribe("first")
	secondSubscriber := memory.Subscribe("second")
	t.Cleanup(func() {
		memory.Unsubscribe("first")
		memory.Unsubscribe("second")
	})

	memory.Write("entry", map[string]any{"value": "version-one"}, SharedEntryTypeFact, "writer")
	firstEvent := <-firstSubscriber
	secondEvent := <-secondSubscriber
	firstEvent.Key = "mutated"
	firstEvent.Value.(map[string]any)["value"] = "subscriber-mutated"
	if secondEvent.Key != "entry" || secondEvent.Value.(map[string]any)["value"] != "version-one" {
		t.Fatalf("subscriber events were aliased: first=%+v second=%+v", firstEvent, secondEvent)
	}

	memory.Write("entry", map[string]any{"value": "version-two"}, SharedEntryTypeFact, "writer")
	if secondEvent.Version != 1 || secondEvent.Value.(map[string]any)["value"] != "version-one" {
		t.Fatalf("previous event changed after update: %+v", secondEvent)
	}
	updated := <-secondSubscriber
	if updated.Version != 2 || updated.Value.(map[string]any)["value"] != "version-two" {
		t.Fatalf("updated event = %+v", updated)
	}
}

func TestSharedMemorySubscriptionReplacementClosesPreviousChannel(t *testing.T) {
	memory := NewSharedMemory()
	previous := memory.Subscribe("agent")
	replacement := memory.Subscribe("agent")
	if stats := memory.Stats(); stats.Subscribers != 1 {
		t.Fatalf("subscriber count after replacement = %d", stats.Subscribers)
	}
	select {
	case _, open := <-previous:
		if open {
			t.Fatal("previous subscription remained open")
		}
	case <-time.After(time.Second):
		t.Fatal("previous subscription was not closed")
	}

	memory.Write("entry", "value", SharedEntryTypeFact, "writer")
	select {
	case event := <-replacement:
		if event == nil || event.Key != "entry" {
			t.Fatalf("replacement event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement subscription did not receive updates")
	}
	memory.Unsubscribe("agent")
	if _, open := <-replacement; open {
		t.Fatal("replacement subscription remained open after unsubscribe")
	}
}

func TestSharedMemoryRestorePreservesMetadataAndDoesNotNotify(t *testing.T) {
	memory := NewSharedMemory()
	memory.Write("entry", "stale", SharedEntryTypeInsight, "runtime")
	subscriber := memory.Subscribe("observer")
	t.Cleanup(func() { memory.Unsubscribe("observer") })

	timestamp := time.Now().Add(-30 * time.Second)
	restored := memory.restoreEntries(map[string]*SharedEntry{
		"entry": {
			Key: "entry", Value: map[string]any{"value": "restored"}, Type: SharedEntryTypeFact,
			Source: "checkpoint", Timestamp: timestamp, TTL: time.Minute, Version: 7,
		},
		"expired": {
			Key: "expired", Value: "old", Type: SharedEntryTypeFact,
			Timestamp: time.Now().Add(-2 * time.Hour), TTL: time.Hour, Version: 3,
		},
		"map-key": {Key: "different-key", Value: "corrupt", Type: SharedEntryTypeFact},
	})
	if restored != 1 {
		t.Fatalf("restored entry count = %d", restored)
	}
	select {
	case event := <-subscriber:
		t.Fatalf("restore emitted a synthetic update: %+v", event)
	default:
	}

	entry, ok := memory.Read("entry")
	if !ok || entry.Version != 7 || !entry.Timestamp.Equal(timestamp) || entry.TTL != time.Minute ||
		entry.Source != "checkpoint" || entry.Value.(map[string]any)["value"] != "restored" {
		t.Fatalf("restored entry = %+v, ok=%v", entry, ok)
	}
	if _, ok := memory.Read("expired"); ok {
		t.Fatal("expired checkpoint entry was restored")
	}
	stats := memory.Stats()
	if stats.TotalEntries != 1 || stats.ByType[SharedEntryTypeFact] != 1 || stats.ByType[SharedEntryTypeInsight] != 0 {
		t.Fatalf("restored indexes = %+v", stats)
	}

	memory.WriteWithTTL("entry", "updated", SharedEntryTypeFact, "runtime", time.Minute)
	updated, _ := memory.Read("entry")
	if updated.Version != 8 {
		t.Fatalf("version after restored update = %d", updated.Version)
	}
}

func TestContextSnapshotIsOwnedBySharedMemory(t *testing.T) {
	memory := NewSharedMemory()
	source := &ContextSnapshot{
		KeyFiles:      map[string]string{"main.go": "important"},
		Discoveries:   []string{"original"},
		ErrorPatterns: map[string]string{"panic": "recover"},
		Requirements:  []string{"safe"},
	}
	memory.SaveContextSnapshot(source, "planner")
	if !source.CreatedAt.IsZero() || source.Source != "" {
		t.Fatalf("SaveContextSnapshot mutated caller state: %+v", source)
	}
	source.KeyFiles["main.go"] = "source-mutated"
	source.Discoveries[0] = "source-mutated"

	first := memory.GetContextSnapshot()
	if first == nil || first.KeyFiles["main.go"] != "important" || first.Discoveries[0] != "original" ||
		first.Source != "planner" || time.Since(first.CreatedAt) > time.Second {
		t.Fatalf("stored context snapshot = %+v", first)
	}
	first.KeyFiles["main.go"] = "reader-mutated"
	second := memory.GetContextSnapshot()
	if second.KeyFiles["main.go"] != "important" {
		t.Fatalf("context snapshot read was aliased: %+v", second)
	}

	checkpoint := &AgentCheckpoint{
		AgentState: &AgentState{ID: "snapshot-agent"},
		SharedMemorySnapshot: map[string]*SharedEntry{
			"context_snapshot": func() *SharedEntry {
				entry, _ := memory.Read("context_snapshot")
				return entry
			}(),
		},
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AgentCheckpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	restoredMemory := NewSharedMemory()
	restoredAgent := &Agent{sharedMemory: restoredMemory}
	if err := restoredAgent.RestoreFromCheckpoint(&decoded); err != nil {
		t.Fatal(err)
	}
	if restored := restoredMemory.GetContextSnapshot(); restored == nil || restored.KeyFiles["main.go"] != "important" {
		t.Fatalf("context snapshot type was lost through checkpoint JSON: %+v", restored)
	}
}
