package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// session_pins.go (iter 480+) -- per-project storage for which session tabs
// are pinned to the top of the tab list. Independent of message pinning
// (which is per-(project,session) in pins.go) and project pinning (which is
// in the main config). Stored as a flat JSON array of session IDs because
// the structure is small, stable, and frequently read at startup.

// sessionPinsPath returns ~/.config/gokin-studio/session-pins/<projectID>.json.
// One file per project; mirrors the per-project drafts/pins layout.
func sessionPinsPath(projectID string) string {
	return filepath.Join(configDir(), "session-pins", safeStorageKey(projectID)+".json")
}

// loadPinnedSessions reads the on-disk pinned-session set for a project.
// Returns an empty map for missing files, malformed JSON, or empty arrays.
// Errors only when the file exists but ReadFile fails for a reason other
// than IsNotExist (e.g. permission denied) — callers may surface those.
func loadPinnedSessions(projectID string) (map[string]bool, error) {
	if projectID == "" {
		return map[string]bool{}, nil
	}
	data, err := readRegularFileLimited(sessionPinsPath(projectID), 256<<10)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]bool{}, nil
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		// Treat corrupt files as empty rather than failing — pinning is a
		// non-critical UX feature; we don't want a malformed file to break
		// project loading.
		return map[string]bool{}, nil
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			out[id] = true
		}
	}
	return out, nil
}

// savePinnedSessions writes the pinned-session set to disk. Empty input
// removes the file rather than persisting "[]" — keeps the directory clean
// when the user unpins everything.
func savePinnedSessions(projectID string, pins map[string]bool) error {
	if projectID == "" {
		return fmt.Errorf("projectID cannot be empty")
	}
	path := sessionPinsPath(projectID)
	if len(pins) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	ids := make([]string, 0, len(pins))
	for id, pinned := range pins {
		if pinned {
			ids = append(ids, id)
		}
	}
	// Sort for deterministic on-disk output (helps diff reviews + tests).
	sort.Strings(ids)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o600)
}

// removeProjectSessionPins clears the session-pin file for a project. Called
// from RemoveProject so deleted projects don't leak orphan pin files.
func removeProjectSessionPins(projectID string) {
	if projectID == "" {
		return
	}
	_ = os.Remove(sessionPinsPath(projectID))
}
