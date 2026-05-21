package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// project_order.go (iter 1060+) -- user-defined ordering of the sidebar
// project list. Independent of project pinning (iter 430+): pinning still
// takes precedence (pinned projects float to the top), but within each pin
// group, the order array overrides the lastUsedAt-default sort.
//
// Storage: single JSON file at `<configDir>/project-order.json`, containing
// an array of project IDs. Order = position in array. Projects not in the
// array fall back to lastUsedAt-default within their pin group (newly added
// projects appear at top of the unordered set until the user drags them).
//
// Why a separate file (not config.yaml Settings): the order is updated on
// every drop, sometimes rapidly. Round-tripping the entire config YAML for
// each drag was disproportionately expensive. A small standalone JSON file
// keeps the hot path cheap.

func projectOrderPath() string {
	return filepath.Join(configDir(), "project-order.json")
}

// loadProjectOrder reads the on-disk project order. Returns an empty slice
// for missing files, malformed JSON, or empty input. Only surfaces real
// read errors (e.g. permission denied) — corruption is silently treated as
// "no order set" because ordering is a non-critical UX feature; dropping
// the user into the lastUsedAt-default order is preferable to refusing to
// list projects at all.
func loadProjectOrder() ([]string, error) {
	data, err := os.ReadFile(projectOrderPath())
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
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// saveProjectOrder writes the order array to disk. Empty input removes the
// file rather than persisting `[]` — keeps the config dir clean for users
// who never reorder.
func saveProjectOrder(order []string) error {
	path := projectOrderPath()
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

// GetProjectOrder returns the persisted ordering as a list of project IDs.
// Used by the frontend's sidebar to build a sort key alongside pinned-first
// + lastUsedAt-default. IDs not in the live projects map are filtered out
// so a stale file from a deleted project doesn't break sort logic.
func (s *Studio) GetProjectOrder() []string {
	order, err := loadProjectOrder()
	if err != nil || len(order) == 0 {
		return []string{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(order))
	for _, id := range order {
		if _, ok := s.projects[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// ReorderProjects persists a new sidebar ordering. Frontend passes the
// full visible-list order on every drop; we filter against the live
// projects map (drop unknown IDs, keep only existing projects) so a
// stale frontend snapshot can't corrupt the file. Duplicates are also
// dropped silently.
//
// Passing an empty slice clears the custom order — projects revert to the
// lastUsedAt-default sort within their pin groups.
func (s *Studio) ReorderProjects(orderedIDs []string) error {
	s.mu.RLock()
	live := make(map[string]bool, len(s.projects))
	for id := range s.projects {
		live[id] = true
	}
	s.mu.RUnlock()
	cleaned := make([]string, 0, len(orderedIDs))
	seen := make(map[string]bool, len(orderedIDs))
	for _, id := range orderedIDs {
		if id == "" || !live[id] || seen[id] {
			continue
		}
		seen[id] = true
		cleaned = append(cleaned, id)
	}
	if err := saveProjectOrder(cleaned); err != nil {
		return fmt.Errorf("save project order: %w", err)
	}
	return nil
}
