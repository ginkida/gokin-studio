package studio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"google.golang.org/genai"
)

// historyDir returns the directory for storing chat history files.
func historyDir() string {
	return filepath.Join(configDir(), "history")
}

// historyFileLocks serializes the read-modify-write / write / delete of a
// given session's history file, keyed by file path. Without it, a rename or
// edit landing concurrently with an agent turn's save can interleave their
// load-modify-write cycles: the slower writer reads stale metadata (losing the
// turn's usage stats) or overwrites the full history with a shorter snapshot
// (losing the last turn until the next save — permanently if the app restarts
// first). The atomic write (atomicWriteFile) keeps any single file valid, but
// only this lock makes the load+write a single critical section.
//
// It is a LEAF lock: held only across the load + marshal + atomic write of one
// file, never while holding session.mu / p.mu / s.mu, so it cannot take part
// in a lock-order cycle even when a caller (e.g. RemoveProject) holds s.mu
// across DeleteHistory.
var (
	historyFileLocksMu sync.Mutex
	historyFileLocks   = map[string]*sync.Mutex{}
)

// lockHistoryFile acquires the per-file lock for path and returns its unlock.
func lockHistoryFile(path string) func() {
	historyFileLocksMu.Lock()
	mu, ok := historyFileLocks[path]
	if !ok {
		mu = &sync.Mutex{}
		historyFileLocks[path] = mu
	}
	historyFileLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// atomicWriteFile writes data to path atomically by writing to a UNIQUE
// temp file in the same directory, then renaming. Prevents a partial write
// from corrupting the file if the process crashes mid-write or the disk
// fills up. Used by config and history saves where a corrupt file means
// lost user state.
//
// The temp name is unique per call (os.CreateTemp) rather than a fixed
// path+".tmp": two goroutines writing the SAME target concurrently (e.g.
// two projects each firing saveConfigAsync on the same agent turn, both
// targeting config.yaml) would otherwise share one temp inode, truncate
// each other mid-write, and promote a torn/garbage file on rename — which
// for config.yaml silently wipes every project + API key on next startup
// (LoadConfig falls back to defaults on a parse error). A unique temp per
// writer makes the rename last-writer-wins with each candidate being a
// complete, valid snapshot. The leading dot keeps the temp hidden and out
// of any "{prefix}*.json" history-file discovery scan.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Best-effort cleanup if we return before a successful rename. After a
	// successful rename tmp no longer exists, so the Remove is a harmless no-op.
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	// Flush to disk before the rename so a crash can't leave a renamed-but-
	// empty file (the rename is only atomic w.r.t. the file's existence, not
	// its buffered contents).
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// historyPath returns the file path for a project's chat history.
func historyPath(projectID string) string {
	return filepath.Join(historyDir(), projectID+".json")
}

// HistoryEntry is a JSON-serializable representation of a single chat turn.
type HistoryEntry struct {
	Role string `json:"role"`
	Text string `json:"text,omitempty"`
}

// SessionUsage is per-session aggregated billing data: total cost +
// token counts + turn count. Persisted in the session's history file
// (iter 360+) so per-project usage statistics survive a restart.
//
// Why store on the session not the project: the project file is the
// config (which is often hand-edited); session history is append-only
// and rebuilt per turn. Aggregating across sessions at read time is
// cheap (typically <50 sessions per project).
type SessionUsage struct {
	TotalCostUSD      float64 `json:"totalCostUSD,omitempty"`
	TotalInputTokens  int     `json:"totalInputTokens,omitempty"`
	TotalOutputTokens int     `json:"totalOutputTokens,omitempty"`
	TotalCacheTokens  int     `json:"totalCacheTokens,omitempty"`
	TurnCount         int     `json:"turnCount,omitempty"`
	LastTurnAt        int64   `json:"lastTurnAt,omitempty"` // unix millis
}

// historyFile is the versioned on-disk format. Older files are a bare
// []HistoryEntry; new files wrap entries with metadata so we can preserve
// session name + lineage + usage across restarts.
//
// Fields added over time without a schema bump (Go's JSON is forgiving):
//   - ParentSessionID (iter 310+) for fork lineage
//   - Usage (iter 360+) for per-session usage stats
// Old files without these fields unmarshal with the zero value, which
// matches the "absent" semantic for both.
type historyFile struct {
	Version         int            `json:"v"`
	Name            string         `json:"name,omitempty"`
	ParentSessionID string         `json:"parentSessionID,omitempty"`
	Usage           *SessionUsage  `json:"usage,omitempty"`
	Entries         []HistoryEntry `json:"entries"`
}

// SaveHistory writes a project's conversation history to disk (legacy API
// without session name — preserves any previously saved name).
func SaveHistory(projectID string, history []*genai.Content) error {
	prevName := ""
	if data, err := os.ReadFile(historyPath(projectID)); err == nil {
		var hf historyFile
		if json.Unmarshal(data, &hf) == nil {
			prevName = hf.Name
		}
	}
	return SaveHistoryWithName(projectID, prevName, history)
}

// SaveHistoryWithName writes history plus a display name. Preserves any
// previously-saved parent session ID AND usage stats by reading the
// existing file first — callers without explicit parent/usage context
// (the normal turn-finished save path) shouldn't need to thread either
// through every layer.
//
// If both history and name are empty, the save is skipped (nothing to
// preserve). When history is empty but name is set (e.g. a freshly-created
// session with a custom label), we still write a file so the session tab
// survives a restart.
func SaveHistoryWithName(projectID, name string, history []*genai.Content) error {
	unlock := lockHistoryFile(historyPath(projectID))
	defer unlock()
	prevParent, prevUsage := loadPrevMetadata(projectID)
	return saveHistoryFull(projectID, name, prevParent, prevUsage, history)
}

// SaveHistoryWithMetadata is the explicit-parent variant. Preserves usage
// from disk. Used by ForkChatSession to stamp lineage onto a new session's
// first save.
func SaveHistoryWithMetadata(projectID, name, parentSessionID string, history []*genai.Content) error {
	unlock := lockHistoryFile(historyPath(projectID))
	defer unlock()
	_, prevUsage := loadPrevMetadata(projectID)
	return saveHistoryFull(projectID, name, parentSessionID, prevUsage, history)
}

// SaveHistoryWithUsage is the explicit-everything variant. Used by the
// agent loop after each chat:complete to bump the running usage totals.
// Caller provides parent (preserves lineage) AND usage (the new totals).
func SaveHistoryWithUsage(projectID, name, parentSessionID string, usage *SessionUsage, history []*genai.Content) error {
	unlock := lockHistoryFile(historyPath(projectID))
	defer unlock()
	return saveHistoryFull(projectID, name, parentSessionID, usage, history)
}

// loadPrevMetadata reads the previously-saved parent + usage so the
// public Save* helpers can preserve them across writes.
func loadPrevMetadata(projectID string) (string, *SessionUsage) {
	data, err := os.ReadFile(historyPath(projectID))
	if err != nil {
		return "", nil
	}
	var hf historyFile
	if json.Unmarshal(data, &hf) != nil {
		return "", nil
	}
	return hf.ParentSessionID, hf.Usage
}

// saveHistoryFull is the canonical writer that all public Save* helpers
// delegate to. Keeps the entry-extraction + atomic-write logic in one place.
func saveHistoryFull(projectID, name, parentSessionID string, usage *SessionUsage, history []*genai.Content) error {
	if len(history) == 0 && name == "" && parentSessionID == "" && usage == nil {
		return nil
	}

	if err := os.MkdirAll(historyDir(), 0o700); err != nil {
		return err
	}

	entries := make([]HistoryEntry, 0, len(history))
	for _, c := range history {
		text := ""
		for _, p := range c.Parts {
			// Thinking (reasoning) parts carry Text but should NOT bleed into
			// the persisted assistant message — they're reconstructed at
			// runtime and the signatures don't survive a save/load anyway.
			if p.Thought {
				continue
			}
			if p.Text != "" {
				text += p.Text
			}
		}
		// Skip entries with function calls/responses (internal to agent loop).
		if text == "" {
			continue
		}
		entries = append(entries, HistoryEntry{
			Role: c.Role,
			Text: text,
		})
	}

	hf := historyFile{
		Version:         2,
		Name:            name,
		ParentSessionID: parentSessionID,
		Usage:           usage,
		Entries:         entries,
	}
	data, err := json.MarshalIndent(hf, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(historyPath(projectID), data, 0o600)
}

// LoadHistory reads a project's conversation history from disk.
func LoadHistory(projectID string) ([]*genai.Content, error) {
	entries, _, _, _, err := loadHistoryRaw(projectID)
	if err != nil {
		return nil, err
	}
	history := make([]*genai.Content, 0, len(entries))
	for _, e := range entries {
		history = append(history, &genai.Content{
			Role:  e.Role,
			Parts: []*genai.Part{genai.NewPartFromText(e.Text)},
		})
	}
	return history, nil
}

// LoadHistoryName returns just the stored session name, or "" if none.
func LoadHistoryName(projectID string) string {
	_, name, _, _, _ := loadHistoryRaw(projectID)
	return name
}

// LoadHistoryParent returns the persisted parent session ID for a forked
// session, or "" if the session has no parent (top-level / pre-iter-310).
func LoadHistoryParent(projectID string) string {
	_, _, parent, _, _ := loadHistoryRaw(projectID)
	return parent
}

// LoadHistoryUsage returns the persisted per-session usage stats, or nil
// if none (legacy file / never-run session). The pointer is meant for
// in-place mutation — agent loop bumps the totals after each turn.
func LoadHistoryUsage(projectID string) *SessionUsage {
	_, _, _, usage, _ := loadHistoryRaw(projectID)
	return usage
}

// loadHistoryRaw returns entries, name, parent session ID, and usage
// stats (any of which may be empty/nil). Supports both legacy (bare
// array) and versioned (object) file formats.
func loadHistoryRaw(projectID string) ([]HistoryEntry, string, string, *SessionUsage, error) {
	data, err := os.ReadFile(historyPath(projectID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", "", nil, nil
		}
		return nil, "", "", nil, err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, "", "", nil, nil
	}
	// Legacy format — bare array.
	if trimmed[0] == '[' {
		var entries []HistoryEntry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, "", "", nil, fmt.Errorf("corrupt history file: %w", err)
		}
		return entries, "", "", nil, nil
	}
	// New format — versioned object.
	var hf historyFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return nil, "", "", nil, fmt.Errorf("corrupt history file: %w", err)
	}
	return hf.Entries, hf.Name, hf.ParentSessionID, hf.Usage, nil
}

// DeleteHistory removes a project's history file. Takes the per-file lock so a
// delete can't interleave with an in-flight save of the same file (which would
// otherwise leave the just-deleted file recreated).
func DeleteHistory(projectID string) {
	unlock := lockHistoryFile(historyPath(projectID))
	defer unlock()
	_ = os.Remove(historyPath(projectID))
}

// ListHistoryFilesForProject returns session IDs that have persisted history for a project.
// Looks for files matching "{projectID}_{sessionID}.json" in the history directory.
func ListHistoryFilesForProject(projectID string) []string {
	entries, err := os.ReadDir(historyDir())
	if err != nil {
		return nil
	}

	prefix := projectID + "_"
	suffix := ".json"
	var sessionIDs []string
	for _, e := range entries {
		name := e.Name()
		if len(name) < len(prefix)+len(suffix) {
			continue
		}
		if name[:len(prefix)] != prefix || name[len(name)-len(suffix):] != suffix {
			continue
		}
		sid := name[len(prefix) : len(name)-len(suffix)]
		if sid != "" {
			sessionIDs = append(sessionIDs, sid)
		}
	}
	return sessionIDs
}
