package studio

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const sessionFileEditorMaxBytes = 2 << 20

// SessionFileSnapshot is the exact editable state of one regular UTF-8 file in
// a chat's checkout. Revision is content-addressed so Save can detect changes
// made by an agent, terminal, formatter, or another editor after this snapshot.
type SessionFileSnapshot struct {
	Path         string `json:"path"`
	AbsolutePath string `json:"absolutePath"`
	Content      string `json:"content"`
	Revision     string `json:"revision"`
	Size         int64  `json:"size"`
	ModifiedAt   int64  `json:"modifiedAt"`
	ReadOnly     bool   `json:"readOnly"`
}

// SessionFileSaveResult reports an optimistic-concurrency conflict without
// losing either side. Current is always the latest on-disk snapshot.
type SessionFileSaveResult struct {
	Saved    bool                 `json:"saved"`
	Conflict bool                 `json:"conflict"`
	Current  *SessionFileSnapshot `json:"current"`
}

func editableSessionPath(subPath string) (string, error) {
	rel, err := normalizeProjectSubPath(subPath)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", fmt.Errorf("session file path is required")
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.EqualFold(part, ".git") || strings.EqualFold(part, ".gokin") {
			return "", fmt.Errorf("editing service metadata is not allowed")
		}
	}
	return rel, nil
}

func sessionFileRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func rejectSessionEditorSymlinkComponents(root *os.Root, rel string) error {
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	prefix := ""
	for _, part := range parts {
		prefix = filepath.Join(prefix, part)
		info, err := root.Lstat(prefix)
		if err != nil {
			return fmt.Errorf("stat session file path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("session file editor does not follow symlinks")
		}
	}
	return nil
}

func readEditableSessionFile(root *os.Root, workDir, rel string) (*SessionFileSnapshot, error) {
	// Every component must be real, not even a safe in-worktree symlink.
	// Otherwise an alias could hide .git metadata or replacing the final link
	// could have surprising semantics for a linked source.
	if err := rejectSessionEditorSymlinkComponents(root, rel); err != nil {
		return nil, err
	}
	linkInfo, err := root.Lstat(rel)
	if err != nil {
		return nil, fmt.Errorf("stat session file: %w", err)
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("session path is not an editable regular file")
	}
	if linkInfo.Size() > sessionFileEditorMaxBytes {
		return nil, fmt.Errorf("session file exceeds the %d byte editor limit", sessionFileEditorMaxBytes)
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open session file: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, sessionFileEditorMaxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read session file: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close session file: %w", closeErr)
	}
	if len(data) > sessionFileEditorMaxBytes {
		return nil, fmt.Errorf("session file exceeds the %d byte editor limit", sessionFileEditorMaxBytes)
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return nil, fmt.Errorf("session file is not valid UTF-8 text")
	}
	return &SessionFileSnapshot{
		Path:         filepath.ToSlash(rel),
		AbsolutePath: filepath.Join(workDir, rel),
		Content:      string(data),
		Revision:     sessionFileRevision(data),
		Size:         int64(len(data)),
		ModifiedAt:   linkInfo.ModTime().UnixMilli(),
		ReadOnly:     linkInfo.Mode().Perm()&0o222 == 0,
	}, nil
}

// GetSessionFileSnapshot opens a bounded text file from the exact checkout used
// by the selected chat. It is intentionally separate from @mention expansion,
// whose 100 KiB truncated representation is unsuitable for editing.
func (s *Studio) GetSessionFileSnapshot(projectID, sessionID, subPath string) (*SessionFileSnapshot, error) {
	project, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	workDir, err := sessionWorkingDirectory(project, session)
	if err != nil {
		return nil, err
	}
	rel, err := editableSessionPath(subPath)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, fmt.Errorf("open session workspace: %w", err)
	}
	defer root.Close()
	return readEditableSessionFile(root, workDir, rel)
}

func newSessionEditorTempName(rel string) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".gokin-edit-"+hex.EncodeToString(random)), nil
}

// SaveSessionFileContent atomically replaces an editable file only if its
// current content still matches expectedRevision. A conflict is data, not an
// error: the caller receives the current snapshot and can offer Discard or an
// explicit Override against that exact newer revision.
func (s *Studio) SaveSessionFileContent(projectID, sessionID, subPath, content, expectedRevision string) (*SessionFileSaveResult, error) {
	if len(content) > sessionFileEditorMaxBytes {
		return nil, fmt.Errorf("edited content exceeds the %d byte editor limit", sessionFileEditorMaxBytes)
	}
	if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return nil, fmt.Errorf("edited content must be valid UTF-8 text")
	}
	if len(expectedRevision) != sha256.Size*2 {
		return nil, fmt.Errorf("expected file revision is invalid")
	}
	if _, err := hex.DecodeString(expectedRevision); err != nil {
		return nil, fmt.Errorf("expected file revision is invalid")
	}

	project, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	workDir, err := sessionWorkingDirectory(project, session)
	if err != nil {
		return nil, err
	}
	rel, err := editableSessionPath(subPath)
	if err != nil {
		return nil, err
	}
	// Without this critical section, two UI save requests carrying the same
	// revision could both pass their final compare and then race their renames.
	// Agent/terminal writers remain covered by the second on-disk read below.
	s.sessionFileSaveMu.Lock()
	defer s.sessionFileSaveMu.Unlock()
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, fmt.Errorf("open session workspace: %w", err)
	}
	defer root.Close()

	current, err := readEditableSessionFile(root, workDir, rel)
	if err != nil {
		return nil, err
	}
	if current.ReadOnly {
		return nil, fmt.Errorf("session file is read-only")
	}
	if current.Revision != strings.ToLower(expectedRevision) {
		return &SessionFileSaveResult{Conflict: true, Current: current}, nil
	}

	tempRel, err := newSessionEditorTempName(rel)
	if err != nil {
		return nil, fmt.Errorf("prepare atomic session file save: %w", err)
	}
	mode := currentFileMode(root, rel)
	temp, err := root.OpenFile(tempRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return nil, fmt.Errorf("create atomic session file save: %w", err)
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = root.Remove(tempRel)
		}
	}()
	writeFailed := func(saveErr error) (*SessionFileSaveResult, error) {
		_ = temp.Close()
		return nil, saveErr
	}
	if _, err := io.WriteString(temp, content); err != nil {
		return writeFailed(fmt.Errorf("write session file: %w", err))
	}
	// Write may clear set-id bits on Unix, so restore the complete supported mode
	// only after the candidate contains its final bytes.
	if err := temp.Chmod(mode); err != nil {
		return writeFailed(fmt.Errorf("preserve session file mode: %w", err))
	}
	if err := temp.Sync(); err != nil {
		return writeFailed(fmt.Errorf("flush session file: %w", err))
	}
	if err := temp.Close(); err != nil {
		return nil, fmt.Errorf("close session file: %w", err)
	}

	// Re-read immediately before promotion. This catches edits that landed while
	// the complete candidate was being written and flushed.
	latest, err := readEditableSessionFile(root, workDir, rel)
	if err != nil {
		return nil, err
	}
	if latest.Revision != strings.ToLower(expectedRevision) {
		return &SessionFileSaveResult{Conflict: true, Current: latest}, nil
	}
	if err := root.Rename(tempRel, rel); err != nil {
		return nil, fmt.Errorf("replace session file: %w", err)
	}
	cleanupTemp = false
	if dir, err := root.Open(filepath.Dir(rel)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	saved, err := readEditableSessionFile(root, workDir, rel)
	if err != nil {
		return nil, err
	}
	return &SessionFileSaveResult{Saved: true, Current: saved}, nil
}

func currentFileMode(root *os.Root, rel string) os.FileMode {
	info, err := root.Lstat(rel)
	if err != nil {
		return 0o600
	}
	return info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}
