package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	sessionSuggestionMaxRecords  = 1000
	sessionSuggestionMaxBytes    = 128 << 10
	sessionSuggestionTitleBytes  = 240
	sessionSuggestionPromptBytes = 32 << 10
)

var sessionSuggestionsMu sync.Mutex

func sessionSuggestionsPath(projectID, sessionID string) string {
	return filepath.Join(configDir(), "session-suggestions", safeStorageKey(projectID)+"_"+safeStorageKey(sessionID)+".json")
}

func sessionSuggestionKey(title, prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(title) + "\x00" + strings.TrimSpace(prompt)))
	return hex.EncodeToString(sum[:16])
}

func loadConsumedSessionSuggestions(projectID, sessionID string) (map[string]bool, error) {
	data, err := readRegularFileLimited(sessionSuggestionsPath(projectID, sessionID), sessionSuggestionMaxBytes)
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("parse consumed session suggestions: %w", err)
	}
	if len(keys) > sessionSuggestionMaxRecords {
		return nil, fmt.Errorf("consumed session suggestions exceed the %d-record limit", sessionSuggestionMaxRecords)
	}
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		if len(key) == 32 {
			out[key] = true
		}
	}
	return out, nil
}

func saveConsumedSessionSuggestions(projectID, sessionID string, records map[string]bool) error {
	path := sessionSuggestionsPath(projectID, sessionID)
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if len(records) > sessionSuggestionMaxRecords {
		return fmt.Errorf("at most %d session suggestions can be retained", sessionSuggestionMaxRecords)
	}
	keys := make([]string, 0, len(records))
	for key := range records {
		if len(key) == 32 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	data, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(path, append(data, '\n'), 0o600)
}

func setSessionSuggestionConsumed(projectID, sessionID, title, prompt string, consumed bool) error {
	sessionSuggestionsMu.Lock()
	defer sessionSuggestionsMu.Unlock()
	records, err := loadConsumedSessionSuggestions(projectID, sessionID)
	if err != nil {
		return err
	}
	key := sessionSuggestionKey(title, prompt)
	if consumed {
		records[key] = true
	} else {
		delete(records, key)
	}
	return saveConsumedSessionSuggestions(projectID, sessionID, records)
}

func claimSessionSuggestion(projectID, sessionID, title, prompt string) error {
	sessionSuggestionsMu.Lock()
	defer sessionSuggestionsMu.Unlock()
	records, err := loadConsumedSessionSuggestions(projectID, sessionID)
	if err != nil {
		return err
	}
	key := sessionSuggestionKey(title, prompt)
	if records[key] {
		return fmt.Errorf("session suggestion was already handled")
	}
	records[key] = true
	return saveConsumedSessionSuggestions(projectID, sessionID, records)
}

func sessionContainsSuggestion(session *ChatSession, title, prompt string) bool {
	title, prompt = strings.TrimSpace(title), strings.TrimSpace(prompt)
	session.mu.RLock()
	defer session.mu.RUnlock()
	for _, content := range session.history {
		for _, part := range content.Parts {
			if part == nil || part.FunctionCall == nil || part.FunctionCall.Name != "session_agent" {
				continue
			}
			args := part.FunctionCall.Args
			if strings.EqualFold(strings.TrimSpace(stringArg(args, "action")), "suggest") &&
				strings.TrimSpace(stringArg(args, "name")) == title &&
				strings.TrimSpace(stringArg(args, "message")) == prompt {
				return true
			}
		}
	}
	return false
}

func (s *Studio) DismissSessionSuggestion(projectID, sessionID, title, prompt string) error {
	if err := validateSessionSuggestionText(title, prompt); err != nil {
		return err
	}
	_, session, err := s.exactStudioSession(projectID, sessionID)
	if err != nil {
		return err
	}
	if !sessionContainsSuggestion(session, title, prompt) {
		return fmt.Errorf("session suggestion was not found in the source transcript")
	}
	return setSessionSuggestionConsumed(projectID, sessionID, title, prompt, true)
}

// StartSessionSuggestion is the user-click path for a model-proposed task.
// The prompt must already exist in the exact source transcript, so this RPC
// cannot be abused to start arbitrary hidden work. Its new chat gets the same
// normal worktree provisioning and message pipeline as a manually created tab.
func (s *Studio) StartSessionSuggestion(projectID, sessionID, title, prompt string) (*ChatSessionInfo, error) {
	if err := validateSessionSuggestionText(title, prompt); err != nil {
		return nil, err
	}
	_, source, err := s.exactStudioSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	title, prompt = strings.TrimSpace(title), strings.TrimSpace(prompt)
	source.mu.RLock()
	archived := source.ArchivedAt > 0
	source.mu.RUnlock()
	if archived {
		return nil, fmt.Errorf("source session is archived")
	}
	if !sessionContainsSuggestion(source, title, prompt) {
		return nil, fmt.Errorf("session suggestion was not found in the source transcript")
	}
	if err := claimSessionSuggestion(projectID, sessionID, title, prompt); err != nil {
		return nil, err
	}
	created, err := s.CreateChatSession(projectID)
	if err != nil {
		_ = setSessionSuggestionConsumed(projectID, sessionID, title, prompt, false)
		return nil, err
	}
	if err := s.RenameChatSession(projectID, created.ID, title); err != nil {
		_ = s.DeleteChatSession(projectID, created.ID)
		_ = setSessionSuggestionConsumed(projectID, sessionID, title, prompt, false)
		return nil, err
	}
	if _, target, exactErr := s.exactStudioSession(projectID, created.ID); exactErr == nil {
		created = target.Info()
	}
	if err := s.SendMessage(projectID, prompt, created.ID); err != nil {
		// Keep the already-created chat and preserve the proposed task as an
		// editable draft when the provider cannot start immediately.
		_ = s.SaveDraft(projectID, created.ID, prompt)
	}
	return created, nil
}

func validateSessionSuggestionText(title, prompt string) error {
	if err := validateRPCText("suggestion title", title, sessionSuggestionTitleBytes, true); err != nil {
		return err
	}
	return validateRPCText("suggestion prompt", prompt, sessionSuggestionPromptBytes, true)
}

func removeSessionSuggestions(projectID, sessionID string) {
	_ = os.Remove(sessionSuggestionsPath(projectID, sessionID))
}

func removeProjectSessionSuggestions(projectID string) {
	dir := filepath.Join(configDir(), "session-suggestions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := safeStorageKey(projectID) + "_"
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
