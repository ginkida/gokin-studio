package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/fileutil"
)

const maxChatHistoryFileBytes int64 = 128 << 20

// HistoryEntry represents a saved history entry.
type HistoryEntry struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// HistoryFile represents a saved session history.
type HistoryFile struct {
	SessionID string         `json:"session_id"`
	StartTime time.Time      `json:"start_time"`
	EndTime   time.Time      `json:"end_time"`
	Entries   []HistoryEntry `json:"entries"`
}

// HistoryManager manages session history persistence.
type HistoryManager struct {
	dataDir            string
	sessionsDir        string
	mu                 sync.RWMutex
	beforeFullSaveLock func() // deterministic publication-order test seam
}

// NewHistoryManager creates a new history manager.
func NewHistoryManager() (*HistoryManager, error) {
	// Get data directory
	dataDir, err := getDataDir()
	if err != nil {
		return nil, err
	}

	// Create directory if it doesn't exist (0700: only owner can access session data)
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	sessionsDir, err := getSessionsDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		return nil, err
	}

	return &HistoryManager{
		dataDir:     dataDir,
		sessionsDir: sessionsDir,
	}, nil
}

// Save saves a session history to disk.
func (m *HistoryManager) Save(session *Session) error {
	if session == nil {
		return fmt.Errorf("cannot save nil session")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := session.GetState()
	if err := fileutil.ValidateStoreID("session", state.ID); err != nil {
		return err
	}

	file := HistoryFile{
		SessionID: state.ID,
		StartTime: state.StartTime,
		EndTime:   time.Now(),
		Entries:   make([]HistoryEntry, 0),
	}

	for _, content := range state.History {
		var text string
		for _, part := range content.Parts {
			if part.Text != "" {
				text += part.Text
			}
		}

		file.Entries = append(file.Entries, HistoryEntry{
			Role:      string(content.Role),
			Content:   text,
			Timestamp: time.Now(),
		})
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}

	// Write file (0600: only owner can read/write session data)
	filename := filepath.Join(m.dataDir, state.ID+".json")
	return fileutil.AtomicWrite(filename, data, 0o600)
}

// Load loads a session history from disk.
func (m *HistoryManager) Load(sessionID string) (*HistoryFile, error) {
	if err := fileutil.ValidateStoreID("session", sessionID); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	filename := filepath.Join(m.dataDir, sessionID+".json")

	data, err := fileutil.ReadRegularFileLimited(filename, maxChatHistoryFileBytes)
	if err != nil {
		return nil, err
	}

	var file HistoryFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.SessionID != sessionID {
		return nil, fmt.Errorf("session ID mismatch: requested %s, file contains %s", sessionID, file.SessionID)
	}

	return &file, nil
}

// List lists all saved sessions.
func (m *HistoryManager) List() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entries, err := os.ReadDir(m.dataDir)
	if err != nil {
		return nil, err
	}

	var sessions []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			sessionID := entry.Name()[:len(entry.Name())-5]
			if fileutil.SafeFilenameComponent(sessionID) && fileutil.RegularFileExists(filepath.Join(m.dataDir, entry.Name())) {
				sessions = append(sessions, sessionID)
			}
		}
	}

	return sessions, nil
}

// Delete deletes a session history.
func (m *HistoryManager) Delete(sessionID string) error {
	if err := fileutil.ValidateStoreID("session", sessionID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	filename := filepath.Join(m.dataDir, sessionID+".json")
	return os.Remove(filename)
}

// getDataDir returns the data directory for history storage.
func getDataDir() (string, error) {
	// Check XDG_DATA_HOME first
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "gokin", "history"), nil
	}

	// Fall back to ~/.local/share
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(homeDir, ".local", "share", "gokin", "history"), nil
}

// getSessionsDir returns the data directory for full session storage.
func getSessionsDir() (string, error) {
	// Check XDG_DATA_HOME first
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "gokin", "sessions"), nil
	}

	// Fall back to ~/.local/share
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(homeDir, ".local", "share", "gokin", "sessions"), nil
}

// SaveFull saves a complete session state including all content.
// Uses atomic write (tmp + rename) to prevent corruption on crash.
func (m *HistoryManager) SaveFull(session *Session) error {
	if session == nil {
		return fmt.Errorf("cannot save nil session")
	}
	if m.beforeFullSaveLock != nil {
		m.beforeFullSaveLock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := session.GetState()
	if err := fileutil.ValidateStoreID("session", state.ID); err != nil {
		return err
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	filename := filepath.Join(m.sessionsDir, state.ID+".json")
	return fileutil.AtomicWrite(filename, data, 0o600)
}

// LoadFull loads a complete session state.
func (m *HistoryManager) LoadFull(sessionID string) (*SessionState, error) {
	if err := fileutil.ValidateStoreID("session", sessionID); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loadFullLocked(sessionID)
}

func (m *HistoryManager) loadFullLocked(sessionID string) (*SessionState, error) {
	filename := filepath.Join(m.sessionsDir, sessionID+".json")

	data, err := fileutil.ReadRegularFileLimited(filename, maxChatHistoryFileBytes)
	if err != nil {
		return nil, err
	}

	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.ID != sessionID {
		return nil, fmt.Errorf("session ID mismatch: requested %s, file contains %s", sessionID, state.ID)
	}

	return &state, nil
}

// ListSessions returns information about all saved sessions.
func (m *HistoryManager) ListSessions() ([]SessionInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entries, err := os.ReadDir(m.sessionsDir)
	if err != nil {
		return nil, err
	}

	var sessions []SessionInfo
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".json")
		if !fileutil.SafeFilenameComponent(sessionID) || !fileutil.RegularFileExists(filepath.Join(m.sessionsDir, entry.Name())) {
			continue
		}
		state, err := m.loadFullLocked(sessionID)
		if err != nil {
			continue // Skip invalid files
		}

		sessions = append(sessions, SessionInfo{
			ID:           state.ID,
			StartTime:    state.StartTime,
			LastActive:   state.LastActive,
			Summary:      state.Summary,
			MessageCount: len(state.History),
			WorkDir:      state.WorkDir,
		})
	}

	return sessions, nil
}

// DeleteSession deletes a saved session.
func (m *HistoryManager) DeleteSession(sessionID string) error {
	if err := fileutil.ValidateStoreID("session", sessionID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	filename := filepath.Join(m.sessionsDir, sessionID+".json")
	return os.Remove(filename)
}
