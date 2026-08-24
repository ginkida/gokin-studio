package studio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Draft persistence: when the user types a message into the chat input but
// hasn't sent it yet, we persist it to disk so a crash, reboot, or app
// restart doesn't lose it. Each draft lives in its own file under
// `<configDir>/drafts/<projectID>_<sessionID>.txt`. Plain UTF-8, no encoding.
//
// Why a flat file (not embedded in the session JSON):
// - Saves are very frequent (every keystroke after debounce). Rewriting the
//   full session history on every save would thrash a much larger file.
// - A corrupt/truncated draft file is recoverable: drop it and the user
//   loses one in-progress draft, not a whole session's history.
// - The draft is per-(project,session) which exactly mirrors the chatStore
//   key, so loading on session switch is one stat + one read.

// DraftMaxBytes is a defensive cap: an accidental paste of a huge file
// shouldn't fill the disk. The frontend already caps the textarea at
// 100k chars; this is a backstop in case the bound is bypassed.
const DraftMaxBytes = 200 * 1024 // 200 KB

func draftsDir() string {
	return filepath.Join(configDir(), "drafts")
}

// truncateUTF8 cuts s to at most maxBytes WITHOUT splitting a multibyte
// UTF-8 rune at the boundary. A naive s[:maxBytes] can land mid-rune for
// Cyrillic/CJK/emoji content, persisting an invalid-UTF-8 tail that the
// frontend then renders as a corrupted trailing character. Backs off to the
// previous rune boundary (at most 3 bytes, since UTF-8 runes are ≤4 bytes).
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// DisplayNameMaxRunes bounds user-visible names — projects and chat sessions.
// It is counted in CHARACTERS because that is the unit the composer inputs
// enforce (maxLength={60}, which the browser applies to UTF-16 code units, not
// bytes). Bounding the same field in bytes on this side let the UI accept a
// name the backend then halved: Cyrillic is 2 bytes per character and CJK 3, so
// a 60-character Russian project name came back cut to 30 characters, mid-word,
// with nothing to indicate it had happened.
const DisplayNameMaxRunes = 60

// truncateRunes cuts s to at most maxRunes characters. Like truncateUTF8 it
// never splits a multibyte rune; unlike it, the limit is expressed in the same
// unit the user and the UI see. Use it for anything with a visible character
// budget, and keep truncateUTF8 for genuinely storage-bounded payloads (system
// prompts, log lines, tool answers) where the byte ceiling is the real
// constraint.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

// draftPath returns the on-disk path for a given (project, session) pair.
// Sanitises the IDs against directory traversal: AddProject + CreateChatSession
// only generate alphanumeric IDs, but we sanitise anyway so a future change
// (or a hand-edited config) can't escape draftsDir().
func draftPath(projectID, sessionID string) string {
	return filepath.Join(draftsDir(), safeStorageKey(projectID)+"_"+safeStorageKey(sessionID)+".txt")
}

// sanitiseDraftKey strips path separators, control characters, and dot
// prefixes from an ID so it can be used as a filename component without
// allowing escapes via "../" or hidden-file naming.
func sanitiseDraftKey(s string) string {
	if s == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == '.' || r == ':':
			b.WriteByte('_')
		case r < 0x20:
			// Control chars: replace with underscore to keep the filename printable.
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" || out == "_" {
		return "_"
	}
	return out
}

// SaveDraft persists the user's in-progress message to disk so it survives
// app restarts. Empty / whitespace-only text removes the file rather than
// writing it, keeping the drafts directory clean.
func (s *Studio) SaveDraft(projectID, sessionID, text string) error {
	if projectID == "" {
		return nil // nothing to do; frontend rendered with no active project
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	if strings.TrimSpace(text) == "" {
		return s.ClearDraft(projectID, sid)
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("draft must be valid UTF-8")
	}
	text = truncateUTF8(text, DraftMaxBytes)
	if err := os.MkdirAll(draftsDir(), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(draftPath(projectID, sid), []byte(text), 0o600)
}

// GetDraft loads the persisted draft for a session, returning the empty
// string when no draft exists. A read error is propagated so the frontend
// can decide whether to retry or fall back to the in-memory copy.
func (s *Studio) GetDraft(projectID, sessionID string) (string, error) {
	if projectID == "" {
		return "", nil
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	data, err := readRegularFileLimited(draftPath(projectID, sid), DraftMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("draft file contains invalid UTF-8")
	}
	return string(data), nil
}

// ClearDraft removes the on-disk draft for a session. Returns nil when the
// file doesn't exist — clearing an already-clear draft is not an error.
// Called on send, /clear, session deletion, and project removal.
func (s *Studio) ClearDraft(projectID, sessionID string) error {
	if projectID == "" {
		return nil
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	err := os.Remove(draftPath(projectID, sid))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// removeProjectDrafts deletes every draft file for a project. Called from
// RemoveProject so a deleted project doesn't leave orphan draft files
// behind. Best-effort: errors are swallowed since this runs as cleanup.
func removeProjectDrafts(projectID string) {
	if projectID == "" {
		return
	}
	dir := draftsDir()
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
