package studio

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// EventLogDiskMaxBytes caps the on-disk persistence file. When the file
// grows past this, rotateIfNeeded keeps only the most recent half of lines.
// 1 MB is enough to hold ~5000 typical events (events are <200 bytes each)
// without bloating user config dirs.
const EventLogDiskMaxBytes = 1024 * 1024

// diskState holds the persistent-storage configuration for an EventLog.
// Separated from the EventLog struct so the core in-memory ring buffer
// stays self-contained and disk concerns can be added/removed cleanly.
type eventLogDiskState struct {
	mu   sync.Mutex
	path string // empty = persistence disabled
}

// Global registry: which EventLog instance has which disk path. We use a
// map keyed by the EventLog pointer rather than embedding the disk state
// in the struct so existing tests that construct bare `EventLog{}` still
// compile and behave as before (no disk activity).
var (
	eventLogDiskRegistry   = map[*EventLog]*eventLogDiskState{}
	eventLogDiskRegistryMu sync.Mutex
)

// SetPersistPath enables disk persistence at the given path. Subsequent
// Log() calls will append a JSON line per event to this file. Idempotent;
// calling with the same path is a no-op. Empty path disables persistence.
//
// Does NOT load existing content from disk — call LoadFromDisk for that.
// Splits load and persist so callers can choose: hydrate the ring from
// disk on startup, then enable ongoing persistence; or just enable
// persistence without replaying.
func (l *EventLog) SetPersistPath(path string) {
	if l == nil {
		return
	}
	eventLogDiskRegistryMu.Lock()
	defer eventLogDiskRegistryMu.Unlock()
	state, exists := eventLogDiskRegistry[l]
	if !exists {
		state = &eventLogDiskState{}
		eventLogDiskRegistry[l] = state
	}
	state.mu.Lock()
	state.path = path
	state.mu.Unlock()
}

// PersistPath returns the currently-set persistence path, or "" if disabled.
func (l *EventLog) PersistPath() string {
	if l == nil {
		return ""
	}
	eventLogDiskRegistryMu.Lock()
	state, ok := eventLogDiskRegistry[l]
	eventLogDiskRegistryMu.Unlock()
	if !ok {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.path
}

// LoadFromDisk reads the persistence file (if it exists) and replays its
// entries into the ring buffer in chronological order. The most recent
// EventLogCapacity entries survive (older ones drop off the back of the
// ring naturally). Skips malformed lines silently — a corrupt file shouldn't
// prevent the app from starting.
//
// Should be called ONCE on startup, before SetPersistPath, so the disk
// content seeds the in-memory ring without re-persisting it back to disk.
func (l *EventLog) LoadFromDisk(path string) error {
	if l == nil {
		return errors.New("nil EventLog")
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("empty path")
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // not having a log file yet is fine
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Allow lines up to 64 KB — events are normally <2 KB but be tolerant.
	sc.Buffer(make([]byte, 4096), 65536)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e EventLogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // skip corrupt line
		}
		// Append without dedup, without re-persistence — pure replay.
		l.appendDirect(e)
	}
	return sc.Err()
}

// appendDirect inserts a raw entry into the ring buffer without going
// through dedup logic or disk persistence. Used by LoadFromDisk for replay
// so we don't infinite-loop write the same entries back to disk.
//
// iter 900+: re-applies sanitizeLogMessage. Iter 870+'s redaction only
// fired in `Log()` for new entries; legacy events.log files written
// before iter 870+ deployed could still contain unredacted secrets on
// disk. Without this re-sanitization, a fresh app launch would
// LoadFromDisk → put the secret in the ring → it'd surface in Snapshot,
// CSV export, and new auto-backups (iter 840+). Idempotent — already-
// redacted text passes through unchanged.
func (l *EventLog) appendDirect(e EventLogEntry) {
	if l == nil {
		return
	}
	if e.Count == 0 {
		e.Count = 1
	}
	e.Message = sanitizeLogMessage(e.Message)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries[l.next] = e
	l.next = (l.next + 1) % EventLogCapacity
	if l.next == 0 {
		l.full = true
	}
}

// persistEntry writes one entry as a JSON line to the configured disk path.
// Silent on failure — logging is a side-effect that must never break callers.
// Triggers rotation when the file grows past EventLogDiskMaxBytes.
func (l *EventLog) persistEntry(e EventLogEntry) {
	if l == nil {
		return
	}
	eventLogDiskRegistryMu.Lock()
	state, ok := eventLogDiskRegistry[l]
	eventLogDiskRegistryMu.Unlock()
	if !ok {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	path := state.path
	if path == "" {
		return
	}

	// Ensure parent dir exists. Cheap and lets the very first event create
	// the directory tree without needing a separate "initialize" step.
	_ = os.MkdirAll(filepath.Dir(path), 0o755)

	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(data)
	_, _ = f.Write([]byte{'\n'})
	_ = f.Close()

	// Cheap stat to check rotation. We could throttle this but stat is fast.
	if info, err := os.Stat(path); err == nil && info.Size() > EventLogDiskMaxBytes {
		_ = l.rotateLocked(path)
	}
}

// rotateLocked rewrites the disk file with only the most recent half of its
// lines. MUST be called with state.mu held. The "keep second half" strategy
// is the simplest correct way to bound the file: read everything, split,
// rewrite. For 1 MB files this is fast enough to run inline.
func (l *EventLog) rotateLocked(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	// Drop trailing empty line from final \n.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= 1 {
		return nil // nothing to rotate
	}
	keepFrom := len(lines) / 2
	kept := lines[keepFrom:]
	// Atomic-ish rewrite via tmp + rename.
	tmp := path + ".rot"
	if err := os.WriteFile(tmp, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClearDisk removes the persistence file. Called by EventLog.Clear so the
// UI's "Clear logs" button wipes both memory AND disk — users expect a
// clean slate, not a phantom file that re-hydrates on next startup.
func (l *EventLog) clearDisk() {
	if l == nil {
		return
	}
	eventLogDiskRegistryMu.Lock()
	state, ok := eventLogDiskRegistry[l]
	eventLogDiskRegistryMu.Unlock()
	if !ok {
		return
	}
	state.mu.Lock()
	path := state.path
	state.mu.Unlock()
	if path == "" {
		return
	}
	_ = os.Remove(path)
}
