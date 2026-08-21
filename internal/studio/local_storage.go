package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// safeStorageKey turns an externally persisted ID into one filename component.
// Generated UUID-style IDs remain unchanged. Unsafe or very long IDs receive a
// readable prefix plus a digest, avoiding both traversal and sanitizer clashes.
func safeStorageKey(value string) string {
	if value != "" && len(value) <= 128 {
		safe := true
		for _, r := range value {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_') {
				safe = false
				break
			}
		}
		if safe {
			return value
		}
	}

	prefix := sanitiseDraftKey(value)
	prefix = strings.Trim(prefix, "_")
	if prefix == "" {
		prefix = "id"
	}
	prefixRunes := []rune(prefix)
	if len(prefixRunes) > 40 {
		prefix = string(prefixRunes[:40])
	}
	digest := sha256.Sum256([]byte(value))
	return prefix + "-" + hex.EncodeToString(digest[:8])
}

// projectSessionStorageKey preserves the discoverable "project_session"
// layout while independently containing either externally persisted ID.
func projectSessionStorageKey(projectID, sessionID string) string {
	return safeStorageKey(projectID) + "_" + safeStorageKey(sessionID)
}

// readRegularFileLimited reads only trusted, regular storage files. It rejects
// symlinks (which could otherwise disclose another local file) and returns an
// error before allocating beyond maxBytes.
func readRegularFileLimited(path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("invalid file size limit")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("storage path is not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("storage file is too large (%d bytes, maximum %d)", info.Size(), maxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !sameOpenedFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("storage file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("storage file is too large (%d bytes, maximum %d)", len(data), maxBytes)
	}
	return data, nil
}

// openRegularFileAppend opens an existing regular file without following a
// symlink, or creates a brand-new file with O_EXCL. It never recreates a file
// removed between operations and verifies the inode after opening.
func openRegularFileAppend(path string, perm os.FileMode) (*os.File, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, perm)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("storage path is not a regular file")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, perm)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !sameOpenedFile(info, opened) || !opened.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("storage file changed while opening")
	}
	return f, nil
}
