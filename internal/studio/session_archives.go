package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	sessionArchivesMaxBytes   = 1 << 20
	sessionArchivesMaxRecords = 10_000
	sessionArchiveIDMaxBytes  = 128
)

type archivedSessionRecord struct {
	ID         string `json:"id"`
	ArchivedAt int64  `json:"archivedAt"`
}

func sessionArchivesPath(projectID string) string {
	return filepath.Join(configDir(), "session-archives", safeStorageKey(projectID)+".json")
}

func loadArchivedSessions(projectID string) (map[string]int64, error) {
	if strings.TrimSpace(projectID) == "" {
		return map[string]int64{}, nil
	}
	data, err := readRegularFileLimited(sessionArchivesPath(projectID), sessionArchivesMaxBytes)
	if os.IsNotExist(err) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []archivedSessionRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse archived sessions: %w", err)
	}
	if len(records) > sessionArchivesMaxRecords {
		return nil, fmt.Errorf("archived-session file exceeds the %d-record limit", sessionArchivesMaxRecords)
	}
	out := make(map[string]int64, len(records))
	for _, record := range records {
		id := strings.TrimSpace(record.ID)
		if id == "" || len(id) > sessionArchiveIDMaxBytes || record.ArchivedAt <= 0 {
			continue
		}
		out[id] = record.ArchivedAt
	}
	return out, nil
}

func saveArchivedSessions(projectID string, records map[string]int64) error {
	path := sessionArchivesPath(projectID)
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if len(records) > sessionArchivesMaxRecords {
		return fmt.Errorf("at most %d sessions can be archived", sessionArchivesMaxRecords)
	}
	list := make([]archivedSessionRecord, 0, len(records))
	for id, archivedAt := range records {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > sessionArchiveIDMaxBytes || archivedAt <= 0 {
			continue
		}
		list = append(list, archivedSessionRecord{ID: id, ArchivedAt: archivedAt})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].ArchivedAt != list[j].ArchivedAt {
			return list[i].ArchivedAt > list[j].ArchivedAt
		}
		return list[i].ID < list[j].ID
	})
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > sessionArchivesMaxBytes {
		return fmt.Errorf("archived sessions exceed the 1 MiB storage limit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(path, append(data, '\n'), 0o600)
}

func cloneArchivedSessions(records map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(records))
	for id, archivedAt := range records {
		out[id] = archivedAt
	}
	return out
}

// applySessionArchives restores durable archive flags and repairs stale or
// impossible metadata. At least one session is always kept active so a corrupt
// or externally-edited archive file cannot leave a project with no usable chat.
func applySessionArchives(projectID string, sessions map[string]*ChatSession) {
	records, err := loadArchivedSessions(projectID)
	if err != nil {
		return // fail open: history remains visible if optional metadata is corrupt
	}
	changed := false
	for id := range records {
		if _, ok := sessions[id]; !ok {
			delete(records, id)
			changed = true
		}
	}
	active := 0
	for id, session := range sessions {
		if archivedAt := records[id]; archivedAt > 0 {
			session.ArchivedAt = archivedAt
		} else {
			active++
		}
	}
	if active == 0 && len(sessions) > 0 {
		keepID := "default"
		if _, ok := sessions[keepID]; !ok {
			keepID = ""
			var keepCreated time.Time
			for id, session := range sessions {
				if keepID == "" || session.CreatedAt.Before(keepCreated) {
					keepID, keepCreated = id, session.CreatedAt
				}
			}
		}
		sessions[keepID].ArchivedAt = 0
		delete(records, keepID)
		changed = true
	}
	if changed {
		_ = saveArchivedSessions(projectID, records)
	}
}

func removeSessionArchiveRecord(projectID, sessionID string) error {
	records, err := loadArchivedSessions(projectID)
	if err != nil {
		return err
	}
	if _, ok := records[sessionID]; !ok {
		return nil
	}
	delete(records, sessionID)
	return saveArchivedSessions(projectID, records)
}

func removeAllSessionArchiveRecords(projectID string) {
	_ = os.Remove(sessionArchivesPath(projectID))
}

// ArchiveChatSession hides one idle chat while preserving every session-owned
// artifact. At least one active chat remains available in each project.
func (s *Studio) ArchiveChatSession(projectID, sessionID string) error {
	return s.archiveChatSession(projectID, sessionID, nil)
}

// archiveChatSession performs the durable archive transition and attaches
// optional, non-authoritative UI context to the resulting event. Callers must
// never rely on eventFields for archive safety; all invariants live below.
func (s *Studio) archiveChatSession(projectID, sessionID string, eventFields map[string]any) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("projectID and sessionID are required")
	}
	s.mu.RLock()
	project, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	project.metadataMu.Lock()
	defer project.metadataMu.Unlock()
	err := func() error {
		project.mu.RLock()
		defer project.mu.RUnlock()
		session, exists := project.sessions[sessionID]
		if !exists || session == nil {
			return fmt.Errorf("session not found: %s", sessionID)
		}
		activeCount := 0
		for _, candidate := range project.sessions {
			candidate.mu.RLock()
			if candidate.ArchivedAt == 0 {
				activeCount++
			}
			candidate.mu.RUnlock()
		}
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.ArchivedAt > 0 {
			return fmt.Errorf("session is already archived")
		}
		if session.active || session.queueWorker {
			return fmt.Errorf("stop the running session before archiving it")
		}
		if activeCount <= 1 {
			return fmt.Errorf("cannot archive the last active session")
		}
		records, loadErr := loadArchivedSessions(projectID)
		if loadErr != nil {
			return fmt.Errorf("load session archive: %w", loadErr)
		}
		archivedAt := time.Now().UnixMilli()
		next := cloneArchivedSessions(records)
		next[sessionID] = archivedAt
		if saveErr := saveArchivedSessions(projectID, next); saveErr != nil {
			return fmt.Errorf("persist session archive: %w", saveErr)
		}
		session.ArchivedAt = archivedAt
		return nil
	}()
	if err != nil {
		return err
	}
	// The archived chat no longer has a visible pane that can own local
	// listeners. Tear them down only after the durable archive commit so a
	// failed archive leaves the still-active chat completely unchanged.
	s.cancelSideQuestions(projectID, sessionID)
	s.stopPreviewServers(projectID, sessionID, true)
	s.stopExternalBrowserTabs(projectID, sessionID)
	event := map[string]any{
		"projectID": projectID, "sessionID": sessionID, "action": "archived",
	}
	for key, value := range eventFields {
		if key == "projectID" || key == "sessionID" || key == "action" {
			continue
		}
		event[key] = value
	}
	project.emitEvent(s.ctx, EventSessionsChanged, event)
	return nil
}

func (s *Studio) RestoreChatSession(projectID, sessionID string) (*ChatSessionInfo, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("projectID and sessionID are required")
	}
	s.mu.RLock()
	project, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	project.metadataMu.Lock()
	defer project.metadataMu.Unlock()
	var session *ChatSession
	err := func() error {
		project.mu.RLock()
		defer project.mu.RUnlock()
		var exists bool
		session, exists = project.sessions[sessionID]
		if !exists || session == nil {
			return fmt.Errorf("session not found: %s", sessionID)
		}
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.ArchivedAt == 0 {
			return fmt.Errorf("session is not archived")
		}
		records, loadErr := loadArchivedSessions(projectID)
		if loadErr != nil {
			return fmt.Errorf("load session archive: %w", loadErr)
		}
		next := cloneArchivedSessions(records)
		delete(next, sessionID)
		if saveErr := saveArchivedSessions(projectID, next); saveErr != nil {
			return fmt.Errorf("persist session restore: %w", saveErr)
		}
		session.ArchivedAt = 0
		return nil
	}()
	if err != nil {
		return nil, err
	}
	info := session.Info()
	project.emitEvent(s.ctx, EventSessionsChanged, map[string]any{
		"projectID": projectID, "sessionID": sessionID, "action": "restored",
	})
	return info, nil
}
