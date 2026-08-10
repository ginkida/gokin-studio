package studio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sessionTerminalDirectory resolves a real directory beneath the exact chat
// checkout. Symlink components are rejected even when they currently point
// inward because a shell keeps its cwd after launch; this avoids surprising
// aliases into service metadata and keeps the displayed cwd unambiguous.
func sessionTerminalDirectory(s *Studio, projectID, sessionID, subPath string) (string, string, error) {
	project, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return "", "", err
	}
	workDir, err := sessionWorkingDirectory(project, session)
	if err != nil {
		return "", "", err
	}
	rel, err := normalizeProjectSubPath(subPath)
	if err != nil {
		return "", "", err
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.EqualFold(part, ".git") || strings.EqualFold(part, ".gokin") {
			return "", "", fmt.Errorf("opening a terminal in service metadata is not allowed")
		}
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return "", "", fmt.Errorf("open session workspace: %w", err)
	}
	defer root.Close()
	if rel != "." {
		if err := rejectSessionEditorSymlinkComponents(root, rel); err != nil {
			return "", "", err
		}
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return "", "", fmt.Errorf("stat session terminal directory: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("session terminal path is not a directory")
	}
	return filepath.ToSlash(rel), filepath.Join(workDir, rel), nil
}

// OpenSessionTerminalAt opens an independent PTY in a validated directory of
// the selected chat worktree. An empty path means the worktree root.
func (s *Studio) OpenSessionTerminalAt(projectID, sessionID, subPath string) (string, error) {
	_, absolute, err := sessionTerminalDirectory(s, projectID, sessionID, subPath)
	if err != nil {
		return "", err
	}
	return s.openTerminalAt(projectID, absolute)
}
