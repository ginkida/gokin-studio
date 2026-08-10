package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"
)

const (
	MaxHistoryFileBytes  = 32 << 20
	MaxHistoryEntries    = 50_000
	MaxHistoryMediaBytes = 256 << 20
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
// Lock order is session.mu -> history-file lock. History helpers never acquire
// session/project/studio locks themselves, so snapshot+disk commits may safely
// remain linearizable with session rename/edit while holding session.mu. Code
// must never acquire session.mu after taking a history-file lock.
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
	return filepath.Join(historyDir(), safeStorageKey(projectID)+".json")
}

// quarantineCorruptHistory renames an unreadable history file aside (to a
// ".corrupt-<stamp>" suffix that ListHistoryFilesForProject won't match, since
// it only accepts an exact ".json" suffix). This stops a corrupt file from
// shadowing its session slot on every restart while preserving the bytes for
// manual recovery — far better than silently dropping the session. Serialized
// under the per-file history lock so it can't race a concurrent write/rename.
// Returns the new base name, or "" if the rename failed (best-effort).
func quarantineCorruptHistory(projectID string) string {
	src := historyPath(projectID)
	unlock := lockHistoryFile(src) // same key the Save* paths use
	defer unlock()
	dst := src + ".corrupt-" + time.Now().Format("20060102-150405.000")
	if err := os.Rename(src, dst); err != nil {
		return ""
	}
	return filepath.Base(dst)
}

// HistoryEntry is a JSON-serializable representation of a single chat turn.
type HistoryEntry struct {
	Role        string                       `json:"role"`
	Text        string                       `json:"text,omitempty"`
	Attachments []PersistedHistoryAttachment `json:"attachments,omitempty"`
}

// PersistedHistoryAttachment references a content-addressed binary stored
// beside the session JSON. This keeps media out of the JSON document while
// preserving it across restarts and session forks.
type PersistedHistoryAttachment struct {
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mimeType"`
	Blob     string `json:"blob,omitempty"`
	Size     int    `json:"size"`
	// Data is used only by portable session/project exports. On-disk history
	// always stores Blob references so JSON remains small.
	Data string `json:"data,omitempty"`
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
//
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
	if data, err := readRegularFileLimited(historyPath(projectID), MaxHistoryFileBytes); err == nil {
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

// SaveNewHistoryWithMetadata is the create-only variant used before a new
// session is published. Refusing to replace an existing file prevents a rare
// generated-ID collision from overwriting another conversation.
func SaveNewHistoryWithMetadata(projectID, name, parentSessionID string, history []*genai.Content) error {
	path := historyPath(projectID)
	unlock := lockHistoryFile(path)
	defer unlock()
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("history already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return saveHistoryFull(projectID, name, parentSessionID, nil, history)
}

// SaveHistoryWithUsage is the explicit-everything variant. Used by the
// agent loop after each chat:complete to bump the running usage totals.
// Caller provides parent (preserves lineage) AND usage (the new totals).
func SaveHistoryWithUsage(projectID, name, parentSessionID string, usage *SessionUsage, history []*genai.Content) error {
	unlock := lockHistoryFile(historyPath(projectID))
	defer unlock()
	return saveHistoryFull(projectID, name, parentSessionID, usage, history)
}

// RenameHistory changes only the display-name metadata of an existing history
// file. Unlike SaveHistoryWithName it never reconstructs entries from a caller
// snapshot, so a rename racing a completed agent save cannot truncate newer
// turns or discard usage/lineage metadata. fallback is used only for a legacy
// in-memory session whose file does not exist yet.
func RenameHistory(projectID, name, parentSessionID string, usage *SessionUsage, fallback []*genai.Content) error {
	path := historyPath(projectID)
	unlock := lockHistoryFile(path)
	defer unlock()
	data, err := readRegularFileLimited(path, MaxHistoryFileBytes)
	if os.IsNotExist(err) {
		return saveHistoryFull(projectID, name, parentSessionID, usage, fallback)
	}
	if err != nil {
		return err
	}
	var hf historyFile
	if err := json.Unmarshal(data, &hf); err != nil {
		// Pre-v2 histories were a bare entry array. Preserve every entry while
		// upgrading the envelope and adding the requested name.
		var legacy []HistoryEntry
		if legacyErr := json.Unmarshal(data, &legacy); legacyErr != nil {
			return fmt.Errorf("decode history for rename: %w", err)
		}
		hf = historyFile{Version: 2, Entries: legacy}
	}
	if len(hf.Entries) > 0 {
		hf.Version = 3
	} else {
		hf.Version = 2
	}
	hf.Name = name
	return writeHistoryFile(projectID, hf)
}

// loadPrevMetadata reads the previously-saved parent + usage so the
// public Save* helpers can preserve them across writes.
func loadPrevMetadata(projectID string) (string, *SessionUsage) {
	data, err := readRegularFileLimited(historyPath(projectID), MaxHistoryFileBytes)
	if err != nil {
		return "", nil
	}
	var hf historyFile
	if json.Unmarshal(data, &hf) != nil {
		return "", nil
	}
	return hf.ParentSessionID, hf.Usage
}

// appendUnpersistableAttachmentNote records, inside the message text, that one
// inline blob could not be stored. Keeping a visible marker is better than
// silently dropping media the user can see in the live transcript but would
// not find after a restart.
func appendUnpersistableAttachmentNote(text, mimeType string) string {
	kind := strings.TrimSpace(mimeType)
	if kind == "" {
		kind = "unknown type"
	}
	note := "[attachment omitted from saved history: " + kind + "]"
	if strings.TrimSpace(text) == "" {
		return note
	}
	return text + "\n\n" + note
}

// saveHistoryFull is the canonical writer that all public Save* helpers
// delegate to. Keeps the entry-extraction + atomic-write logic in one place.
func saveHistoryFull(projectID, name, parentSessionID string, usage *SessionUsage, history []*genai.Content) error {
	if len(history) == 0 && name == "" && parentSessionID == "" && usage == nil {
		return nil
	}
	if len(history) > MaxHistoryEntries {
		return fmt.Errorf("history has too many entries (%d, maximum %d)", len(history), MaxHistoryEntries)
	}

	if err := os.MkdirAll(historyDir(), 0o700); err != nil {
		return err
	}

	entries := make([]HistoryEntry, 0, len(history))
	referencedBlobs := make(map[string]struct{})
	mediaBytes := 0
	for _, c := range history {
		text := ""
		var attachments []PersistedHistoryAttachment
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
			if p.InlineData != nil && len(p.InlineData.Data) > 0 {
				mediaBytes += len(p.InlineData.Data)
				if mediaBytes > MaxHistoryMediaBytes {
					return fmt.Errorf("history media exceeds the %d MiB limit", MaxHistoryMediaBytes>>20)
				}
				ref, err := persistHistoryAttachment(projectID, p.InlineData)
				if err != nil {
					// Never fail the whole transcript over one blob. Tool
					// output reaches history too — `read` on an .svg/.bmp/.ico
					// /.tiff yields a MIME the composer allowlist rejects — and
					// aborting here froze the session's on-disk history
					// permanently: the offending part stays in memory, so every
					// later save failed too and the conversation was lost on
					// restart. Drop the blob, keep the conversation.
					text = appendUnpersistableAttachmentNote(text, p.InlineData.MIMEType)
					continue
				}
				attachments = append(attachments, ref)
				referencedBlobs[ref.Blob] = struct{}{}
			}
		}
		// Skip entries with function calls/responses (internal to agent loop).
		if text == "" && len(attachments) == 0 {
			continue
		}
		entries = append(entries, HistoryEntry{
			Role:        c.Role,
			Text:        text,
			Attachments: attachments,
		})
		if len(entries) > MaxHistoryEntries {
			return fmt.Errorf("history has too many entries (%d, maximum %d)", len(entries), MaxHistoryEntries)
		}
	}

	hf := historyFile{
		Version:         3,
		Name:            name,
		ParentSessionID: parentSessionID,
		Usage:           usage,
		Entries:         entries,
	}
	if err := writeHistoryFile(projectID, hf); err != nil {
		return err
	}
	cleanupUnreferencedHistoryAttachments(projectID, referencedBlobs)
	return nil
}

func writeHistoryFile(projectID string, hf historyFile) error {
	if len(hf.Entries) > MaxHistoryEntries {
		return fmt.Errorf("history has too many entries (%d, maximum %d)", len(hf.Entries), MaxHistoryEntries)
	}
	if err := os.MkdirAll(historyDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(hf, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > MaxHistoryFileBytes {
		return fmt.Errorf("history is too large (%d bytes, maximum %d)", len(data), MaxHistoryFileBytes)
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
		parts := make([]*genai.Part, 0, 1+len(e.Attachments))
		if e.Text != "" {
			parts = append(parts, genai.NewPartFromText(e.Text))
		}
		for _, attachment := range e.Attachments {
			blob, err := loadHistoryAttachment(projectID, attachment)
			if err != nil {
				// A missing/corrupt media blob must not make the textual
				// conversation unusable. Skip only that attachment.
				continue
			}
			parts = append(parts, &genai.Part{InlineData: blob})
		}
		if len(parts) > 0 {
			history = append(history, &genai.Content{Role: e.Role, Parts: parts})
		}
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
	data, err := readRegularFileLimited(historyPath(projectID), MaxHistoryFileBytes)
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
	if !utf8.Valid(trimmed) {
		return nil, "", "", nil, fmt.Errorf("corrupt history file: invalid UTF-8")
	}
	// Legacy format — bare array.
	if trimmed[0] == '[' {
		var entries []HistoryEntry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, "", "", nil, fmt.Errorf("corrupt history file: %w", err)
		}
		if err := validateHistoryEntries(entries); err != nil {
			return nil, "", "", nil, err
		}
		return entries, "", "", nil, nil
	}
	// New format — versioned object.
	var hf historyFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return nil, "", "", nil, fmt.Errorf("corrupt history file: %w", err)
	}
	if err := validateHistoryEntries(hf.Entries); err != nil {
		return nil, "", "", nil, err
	}
	if hf.Usage != nil && (hf.Usage.TotalCostUSD < 0 || hf.Usage.TotalInputTokens < 0 ||
		hf.Usage.TotalOutputTokens < 0 || hf.Usage.TotalCacheTokens < 0 ||
		hf.Usage.TurnCount < 0 || hf.Usage.LastTurnAt < 0) {
		return nil, "", "", nil, fmt.Errorf("corrupt history file: invalid negative usage metadata")
	}
	return hf.Entries, hf.Name, hf.ParentSessionID, hf.Usage, nil
}

func validateHistoryEntries(entries []HistoryEntry) error {
	if len(entries) > MaxHistoryEntries {
		return fmt.Errorf("history has too many entries (%d, maximum %d)", len(entries), MaxHistoryEntries)
	}
	for i := range entries {
		switch entries[i].Role {
		case "user", "model":
		case "assistant":
			// Accept common external/legacy spelling but normalize to the role
			// expected by provider APIs before the history reaches a model.
			entries[i].Role = "model"
		default:
			return fmt.Errorf("corrupt history file: invalid role %q at entry %d", entries[i].Role, i)
		}
		if entries[i].Text == "" && len(entries[i].Attachments) == 0 {
			return fmt.Errorf("corrupt history file: empty entry at index %d", i)
		}
		for j, attachment := range entries[i].Attachments {
			if attachment.Data != "" {
				return fmt.Errorf("corrupt history file: inline attachment data at entry %d attachment %d", i, j)
			}
			mimeType := normalizeAttachmentMIME(attachment.MIMEType)
			imageExt, isImage := supportedImageMIMEs[mimeType]
			documentExt, isDocument := supportedDocumentMIMEs[mimeType]
			if !isImage && !isDocument {
				return fmt.Errorf("corrupt history file: unsupported attachment type at entry %d attachment %d", i, j)
			}
			if attachment.Size <= 0 || attachment.Size > MessageAttachmentMaxBytes {
				return fmt.Errorf("corrupt history file: invalid attachment size at entry %d attachment %d", i, j)
			}
			if isImage && attachment.Size > MessageImageAttachmentMaxBytes {
				return fmt.Errorf("corrupt history file: invalid image attachment size at entry %d attachment %d", i, j)
			}
			if attachment.Name != "" {
				if _, err := validateAttachmentName(attachment.Name, j, imageExt, documentExt); err != nil {
					return fmt.Errorf("corrupt history file: invalid attachment name at entry %d attachment %d", i, j)
				}
			}
			if len(attachment.Blob) != sha256.Size*2 {
				return fmt.Errorf("corrupt history file: invalid attachment reference at entry %d attachment %d", i, j)
			}
			if _, err := hex.DecodeString(attachment.Blob); err != nil {
				return fmt.Errorf("corrupt history file: invalid attachment reference at entry %d attachment %d", i, j)
			}
		}
	}
	return nil
}

// DeleteHistory removes a project's history file. Takes the per-file lock so a
// delete can't interleave with an in-flight save of the same file (which would
// otherwise leave the just-deleted file recreated).
func DeleteHistory(projectID string) {
	_ = deleteHistoryChecked(projectID)
}

// deleteHistoryChecked is the durable variant used by user-facing destructive
// operations. Cleanup/migration callers may keep using the best-effort wrapper.
func deleteHistoryChecked(projectID string) error {
	unlock := lockHistoryFile(historyPath(projectID))
	defer unlock()
	if err := os.Remove(historyPath(projectID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(historyMediaDir(projectID)); err != nil {
		return err
	}
	return nil
}

func historyMediaDir(projectID string) string {
	return historyPath(projectID) + ".media"
}

func persistHistoryAttachment(projectID string, blob *genai.Blob) (PersistedHistoryAttachment, error) {
	if blob == nil || len(blob.Data) == 0 || len(blob.Data) > MessageAttachmentMaxBytes {
		return PersistedHistoryAttachment{}, fmt.Errorf("invalid attachment size")
	}
	mimeType := normalizeAttachmentMIME(blob.MIMEType)
	imageExt, isImage := supportedImageMIMEs[mimeType]
	documentExt, isDocument := supportedDocumentMIMEs[mimeType]
	if !isImage && !isDocument {
		return PersistedHistoryAttachment{}, fmt.Errorf("unsupported attachment type %q", blob.MIMEType)
	}
	if isImage && len(blob.Data) > MessageImageAttachmentMaxBytes {
		return PersistedHistoryAttachment{}, fmt.Errorf("invalid image attachment size")
	}
	name, err := validateAttachmentName(blob.DisplayName, 0, imageExt, documentExt)
	if err != nil {
		return PersistedHistoryAttachment{}, err
	}
	sum := sha256.Sum256(blob.Data)
	id := hex.EncodeToString(sum[:])
	dir := historyMediaDir(projectID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return PersistedHistoryAttachment{}, err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return PersistedHistoryAttachment{}, fmt.Errorf("history media path is not a regular directory")
	}
	path := filepath.Join(dir, id)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() != int64(len(blob.Data)) {
			return PersistedHistoryAttachment{}, fmt.Errorf("attachment blob path is not a matching regular file")
		}
	} else if os.IsNotExist(err) {
		if err := atomicWriteFile(path, blob.Data, 0o600); err != nil {
			return PersistedHistoryAttachment{}, err
		}
	} else {
		return PersistedHistoryAttachment{}, err
	}
	return PersistedHistoryAttachment{Name: name, MIMEType: mimeType, Blob: id, Size: len(blob.Data)}, nil
}

func loadHistoryAttachment(projectID string, ref PersistedHistoryAttachment) (*genai.Blob, error) {
	if ref.Size <= 0 || ref.Size > MessageAttachmentMaxBytes || len(ref.Blob) != sha256.Size*2 {
		return nil, fmt.Errorf("invalid attachment reference")
	}
	mimeType := normalizeAttachmentMIME(ref.MIMEType)
	imageExt, isImage := supportedImageMIMEs[mimeType]
	documentExt, isDocument := supportedDocumentMIMEs[mimeType]
	if !isImage && !isDocument {
		return nil, fmt.Errorf("invalid attachment type")
	}
	if isImage && ref.Size > MessageImageAttachmentMaxBytes {
		return nil, fmt.Errorf("invalid image attachment size")
	}
	name := ""
	if ref.Name != "" {
		var err error
		name, err = validateAttachmentName(ref.Name, 0, imageExt, documentExt)
		if err != nil {
			return nil, err
		}
	}
	if _, err := hex.DecodeString(ref.Blob); err != nil {
		return nil, fmt.Errorf("invalid attachment reference")
	}
	path := filepath.Join(historyMediaDir(projectID), ref.Blob)
	data, err := readRegularFileLimited(path, MessageAttachmentMaxBytes)
	if err != nil {
		return nil, err
	}
	if len(data) != ref.Size {
		return nil, fmt.Errorf("attachment size mismatch")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != ref.Blob {
		return nil, fmt.Errorf("attachment digest mismatch")
	}
	return &genai.Blob{MIMEType: mimeType, DisplayName: name, Data: data}, nil
}

func cleanupUnreferencedHistoryAttachments(projectID string, referenced map[string]struct{}) {
	dir := historyMediaDir(projectID)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if _, ok := referenced[entry.Name()]; ok {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// ListHistoryFilesForProject returns session IDs that have persisted history for a project.
// Looks for files matching "{projectID}_{sessionID}.json" in the history directory.
func ListHistoryFilesForProject(projectID string) []string {
	entries, err := os.ReadDir(historyDir())
	if err != nil {
		return nil
	}

	prefix := safeStorageKey(projectID) + "_"
	suffix := ".json"
	var sessionIDs []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
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
