package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// session_order.go (iter 540+) -- per-project user-defined ordering of
// session tabs. Independent of session pinning (iter 480+): pinning still
// takes precedence (pinned-first), but within each pin group, the order
// array overrides the lastUsedAt-default sort. Frontend drives this via
// HTML5 drag-and-drop on the tab list.
//
// Storage: per-project JSON file at `<configDir>/session-order/<projectID>.json`,
// containing an array of session IDs. Order = position in array.
// Sessions not in the array fall back to lastUsedAt-default within their
// pin group (newly created sessions appear at top of unordered until the
// user explicitly drags them).

func sessionOrderPath(projectID string) string {
	return filepath.Join(configDir(), "session-order", projectID+".json")
}

// loadSessionOrder reads the on-disk session order for a project. Returns
// an empty slice for missing files, malformed JSON, or empty input. Only
// surfaces real read errors (e.g. permission denied) — corruption is
// silently treated as "no order set" because ordering is a non-critical
// UX feature.
func loadSessionOrder(projectID string) ([]string, error) {
	if projectID == "" {
		return []string{}, nil
	}
	data, err := os.ReadFile(sessionOrderPath(projectID))
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return []string{}, nil
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return []string{}, nil // treat corrupt as empty
	}
	// Drop empty strings defensively.
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// saveSessionOrder writes the order array to disk. Empty input removes
// the file rather than persisting `[]`.
func saveSessionOrder(projectID string, order []string) error {
	if projectID == "" {
		return fmt.Errorf("projectID cannot be empty")
	}
	path := sessionOrderPath(projectID)
	if len(order) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(order, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o600)
}

// removeProjectSessionOrder clears the session-order file for a project.
// Called from RemoveProject so deleted projects don't leak orphan files.
func removeProjectSessionOrder(projectID string) {
	if projectID == "" {
		return
	}
	_ = os.Remove(sessionOrderPath(projectID))
}

// ReorderChatSessions persists a new tab ordering for the project.
// Frontend passes the full visible-tab order on every drop; we filter
// against the live session map (drop unknown IDs, keep only existing
// sessions) so a stale browser state doesn't corrupt the file.
func (s *Studio) ReorderChatSessions(projectID string, orderedIDs []string) error {
	if projectID == "" {
		return fmt.Errorf("projectID cannot be empty")
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	// Build the validated order: keep only IDs that match a live session.
	p.mu.RLock()
	live := make(map[string]bool, len(p.sessions))
	for sid := range p.sessions {
		live[sid] = true
	}
	p.mu.RUnlock()
	cleaned := make([]string, 0, len(orderedIDs))
	seen := make(map[string]bool, len(orderedIDs))
	for _, id := range orderedIDs {
		if id == "" || !live[id] || seen[id] {
			continue
		}
		seen[id] = true
		cleaned = append(cleaned, id)
	}
	return saveSessionOrder(projectID, cleaned)
}
