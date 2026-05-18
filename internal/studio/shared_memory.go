package studio

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

// SharedMemory is a process-wide scratchpad that agents across different
// projects can read and write. It implements tools.SharedMemoryInterface so
// the shared_memory tool in the registry can be wired into it directly.
//
// This is a lightweight in-memory implementation — entries do not survive
// app restarts. For durable cross-session memory the agent should use the
// per-project `memory` tool instead.
type SharedMemory struct {
	entries map[string]*sharedEntry
	mu      sync.RWMutex
}

type sharedEntry struct {
	key       string
	value     any
	entryType string
	source    string
	timestamp time.Time
	ttl       time.Duration
	version   int
}

func (e *sharedEntry) expired(now time.Time) bool {
	if e.ttl == 0 {
		return false
	}
	return now.Sub(e.timestamp) > e.ttl
}

// NewSharedMemory creates an empty shared-memory instance.
func NewSharedMemory() *SharedMemory {
	return &SharedMemory{entries: make(map[string]*sharedEntry)}
}

// Write stores a value with no TTL. If the key exists, version is bumped.
func (s *SharedMemory) Write(key string, value any, entryType string, sourceAgent string) {
	s.WriteWithTTL(key, value, entryType, sourceAgent, 0)
}

// WriteWithTTL stores a value with an expiration window. ttl=0 means forever.
func (s *SharedMemory) WriteWithTTL(key string, value any, entryType string, sourceAgent string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.entries[key]
	version := 1
	if ok {
		version = prev.version + 1
	}
	s.entries[key] = &sharedEntry{
		key:       key,
		value:     value,
		entryType: entryType,
		source:    sourceAgent,
		timestamp: time.Now(),
		ttl:       ttl,
		version:   version,
	}
}

// Read returns the value stored under key, or ok=false if missing/expired.
func (s *SharedMemory) Read(key string) (tools.SharedMemoryEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[key]
	if !ok || e.expired(time.Now()) {
		return tools.SharedMemoryEntry{}, false
	}
	return toToolsEntry(e), true
}

// ReadByType returns all non-expired entries whose type matches.
func (s *SharedMemory) ReadByType(entryType string) []tools.SharedMemoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	var out []tools.SharedMemoryEntry
	for _, e := range s.entries {
		if e.entryType == entryType && !e.expired(now) {
			out = append(out, toToolsEntry(e))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out
}

// ReadAll returns every live entry, newest first.
func (s *SharedMemory) ReadAll() []tools.SharedMemoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	out := make([]tools.SharedMemoryEntry, 0, len(s.entries))
	for _, e := range s.entries {
		if !e.expired(now) {
			out = append(out, toToolsEntry(e))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out
}

// Delete removes a key; returns true if it existed.
func (s *SharedMemory) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[key]; !ok {
		return false
	}
	delete(s.entries, key)
	return true
}

// GetForContext produces a compact text digest suitable for injecting into a
// system prompt or tool result. Caps at maxEntries, newest first; filters
// out entries authored by the calling agent so they don't see their own echo.
func (s *SharedMemory) GetForContext(agentID string, maxEntries int) string {
	if maxEntries <= 0 {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	picked := make([]*sharedEntry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.expired(now) || e.source == agentID {
			continue
		}
		picked = append(picked, e)
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].timestamp.After(picked[j].timestamp) })
	if len(picked) > maxEntries {
		picked = picked[:maxEntries]
	}
	if len(picked) == 0 {
		return ""
	}

	var out string
	for _, e := range picked {
		out += fmt.Sprintf("[%s/%s by %s] %s = %v\n", e.entryType, e.key, e.source, e.key, e.value)
	}
	return out
}

// toToolsEntry converts the internal representation to the public tools type.
func toToolsEntry(e *sharedEntry) tools.SharedMemoryEntry {
	return tools.SharedMemoryEntry{
		Key:       e.key,
		Value:     e.value,
		Type:      e.entryType,
		Source:    e.source,
		Timestamp: e.timestamp,
		Version:   e.version,
	}
}
