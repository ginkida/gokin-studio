package studio

import (
	"os"
	"path/filepath"
	"strings"
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

// draftPath returns the on-disk path for a given (project, session) pair.
// Sanitises the IDs against directory traversal: AddProject + CreateChatSession
// only generate alphanumeric IDs, but we sanitise anyway so a future change
// (or a hand-edited config) can't escape draftsDir().
func draftPath(projectID, sessionID string) string {
	return filepath.Join(draftsDir(), sanitiseDraftKey(projectID)+"_"+sanitiseDraftKey(sessionID)+".txt")
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
	if len(text) > DraftMaxBytes {
		text = text[:DraftMaxBytes]
	}
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
	data, err := os.ReadFile(draftPath(projectID, sid))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
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
	prefix := sanitiseDraftKey(projectID) + "_"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
