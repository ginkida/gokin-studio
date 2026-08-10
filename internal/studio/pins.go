package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Message pinning: a session-scoped bookmark. The user marks a particular
// message ("here's where the agent finally figured out the bug" / "this is
// the requirements I gave it") so they can jump back to it from the pin
// list later. Pins store a SNAPSHOT of the content so they remain useful
// even after edits/trims that would shift the original message's position.
//
// Why a snapshot instead of an ID-pointer:
//   - Frontend message IDs are regenerated on every GetHistory call (the
//     in-memory chatStore IDs are not persistent). An ID-based pin would
//     break on session reload.
//   - History indices shift with EditUserMessage/trim. An index-based pin
//     would silently point to the wrong message after an edit.
//   - A content snapshot is durable: even if the original is deleted, the
//     pin still holds the bookmark text. Jump-to-message uses content
//     match; if no match, the snapshot is shown in the modal as-is.
//
// Storage: one JSON file per (project, session) at
// `<configDir>/pins/<projectID>_<sessionID>.json`. Same flat-file pattern
// as drafts.go so we don't have to rev the session history JSON schema.

// PinnedMessage is the JSON-serializable shape persisted per pin and
// returned to the frontend. Mirrors ChatMessage's text-only fields plus
// a stable Pin ID and timestamp.
type PinnedMessage struct {
	ID        string `json:"id"`                  // stable pin ID, generated on Pin()
	Role      string `json:"role"`                // "user" | "assistant"
	Content   string `json:"content"`             // full message text snapshot
	PinnedAt  int64  `json:"pinnedAt"`            // unix millis
	MessageID string `json:"messageID,omitempty"` // frontend message ID at pin time (best-effort jump target)
}

// PinContentMaxBytes caps the snapshot size so a pinned 10 MB pasted log
// doesn't bloat the pins file. Frontend already caps message text at 100k
// chars; we go a bit higher (200k) to keep wraparound from truncating a
// long-but-real assistant response.
const PinContentMaxBytes = 200 * 1024

const (
	pinsFileMaxBytes     = 4 << 20
	maxPinnedMessages    = 100
	maxPinMessageIDBytes = 256
)

// A pin operation is a read-modify-write transaction. Serialize these small,
// infrequent files so concurrent UI calls cannot overwrite one another.
var pinsFileMu sync.Mutex

func pinsDir() string {
	return filepath.Join(configDir(), "pins")
}

// pinsPath returns the on-disk path for a session's pins file. Reuses the
// drafts.go sanitiser to defend against path traversal in malformed IDs.
func pinsPath(projectID, sessionID string) string {
	return filepath.Join(pinsDir(), safeStorageKey(projectID)+"_"+safeStorageKey(sessionID)+".json")
}

func loadPinsFile(projectID, sessionID string) ([]PinnedMessage, error) {
	data, err := readRegularFileLimited(pinsPath(projectID, sessionID), pinsFileMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var pins []PinnedMessage
	if err := json.Unmarshal(data, &pins); err != nil {
		return nil, fmt.Errorf("corrupt pins file: %w", err)
	}
	if len(pins) > maxPinnedMessages {
		return nil, fmt.Errorf("pins file has too many entries (%d, maximum %d)", len(pins), maxPinnedMessages)
	}
	for i, pin := range pins {
		if pin.ID == "" || (pin.Role != "user" && pin.Role != "assistant") ||
			len(pin.Content) > PinContentMaxBytes || !utf8.ValidString(pin.Content) {
			return nil, fmt.Errorf("corrupt pins file: invalid pin at index %d", i)
		}
	}
	return pins, nil
}

func savePinsFile(projectID, sessionID string, pins []PinnedMessage) error {
	if err := os.MkdirAll(pinsDir(), 0o700); err != nil {
		return err
	}
	if len(pins) == 0 {
		// Empty list → remove the file rather than persist `[]`.
		if err := os.Remove(pinsPath(projectID, sessionID)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > pinsFileMaxBytes {
		return fmt.Errorf("pins file would be too large (%d bytes, maximum %d)", len(data), pinsFileMaxBytes)
	}
	return atomicWriteFile(pinsPath(projectID, sessionID), data, 0o600)
}

// PinMessage adds a snapshot of (role, content) to the session's pin list
// and returns the new pin's ID. The content is trimmed (whitespace-only
// rejected) and truncated at PinContentMaxBytes. Duplicate pinning of the
// SAME (role, content) pair is suppressed — the existing pin is returned
// instead of creating a second copy.
func (s *Studio) PinMessage(projectID, sessionID, role, content, messageID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("projectID required")
	}
	if role != "user" && role != "assistant" {
		return "", fmt.Errorf("role must be 'user' or 'assistant'")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("content cannot be empty")
	}
	if !utf8.ValidString(content) || !utf8.ValidString(messageID) {
		return "", fmt.Errorf("pin content and message ID must be valid UTF-8")
	}
	content = truncateUTF8(content, PinContentMaxBytes)
	sid := sessionID
	if sid == "" {
		sid = "default"
	}

	// Verify project exists so we don't write pins files for ghost projects.
	s.mu.RLock()
	_, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}

	pinsFileMu.Lock()
	defer pinsFileMu.Unlock()
	pins, err := loadPinsFile(projectID, sid)
	if err != nil {
		return "", err
	}
	// Dedup: if there's an existing pin with the same (role, content),
	// return its ID instead of creating a second one. Two identical pins
	// would just clutter the modal.
	for _, p := range pins {
		if p.Role == role && p.Content == content {
			return p.ID, nil
		}
	}
	if len(pins) >= maxPinnedMessages {
		return "", fmt.Errorf("pin limit reached (%d per session)", maxPinnedMessages)
	}
	newPin := PinnedMessage{
		ID:        uuid.New().String()[:12],
		Role:      role,
		Content:   content,
		PinnedAt:  time.Now().UnixMilli(),
		MessageID: truncateUTF8(messageID, maxPinMessageIDBytes),
	}
	pins = append(pins, newPin)
	if err := savePinsFile(projectID, sid, pins); err != nil {
		return "", err
	}
	return newPin.ID, nil
}

// UnpinMessage removes a pin by its ID. Returns nil if the pin doesn't
// exist (idempotent — better UX than failing on double-clicks).
func (s *Studio) UnpinMessage(projectID, sessionID, pinID string) error {
	if projectID == "" {
		return fmt.Errorf("projectID required")
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	pinsFileMu.Lock()
	defer pinsFileMu.Unlock()
	pins, err := loadPinsFile(projectID, sid)
	if err != nil {
		return err
	}
	out := pins[:0]
	for _, p := range pins {
		if p.ID == pinID {
			continue
		}
		out = append(out, p)
	}
	return savePinsFile(projectID, sid, out)
}

// ListPinnedMessages returns the session's pins sorted most-recent-first
// (matching the chat order — newest pins at the top of the modal). Returns
// an empty slice rather than nil for easy frontend handling.
func (s *Studio) ListPinnedMessages(projectID, sessionID string) ([]PinnedMessage, error) {
	if projectID == "" {
		return []PinnedMessage{}, nil
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	pinsFileMu.Lock()
	defer pinsFileMu.Unlock()
	pins, err := loadPinsFile(projectID, sid)
	if err != nil {
		return nil, err
	}
	if pins == nil {
		return []PinnedMessage{}, nil
	}
	sort.SliceStable(pins, func(i, j int) bool {
		return pins[i].PinnedAt > pins[j].PinnedAt
	})
	return pins, nil
}

// removeProjectPins drops every pins file for the given project. Called
// from RemoveProject so a deleted project's pins don't leak forever.
// Best-effort: errors are swallowed since this is cleanup.
func removeProjectPins(projectID string) {
	if projectID == "" {
		return
	}
	pinsFileMu.Lock()
	defer pinsFileMu.Unlock()
	dir := pinsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := safeStorageKey(projectID) + "_"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// removeSessionPins drops just the pins file for one session. Called from
// DeleteChatSession.
func removeSessionPins(projectID, sessionID string) {
	if projectID == "" {
		return
	}
	pinsFileMu.Lock()
	defer pinsFileMu.Unlock()
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	_ = os.Remove(pinsPath(projectID, sid))
}
