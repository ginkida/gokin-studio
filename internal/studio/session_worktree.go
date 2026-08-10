package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	sessionWorktreeRecordVersion = 1
	sessionWorktreeRecordMax     = 16 << 10
	sessionWorktreeGitTimeout    = 60 * time.Second
	sessionWorktreeIncludeMax    = 64 << 10
	sessionWorktreeCopyFileMax   = 64 << 20
	sessionWorktreeCopyTotalMax  = 256 << 20
	sessionWorktreeCopyCountMax  = 4096
)

type sessionWorktreeRecord struct {
	Version    int    `json:"version"`
	ProjectID  string `json:"projectID"`
	SessionID  string `json:"sessionID"`
	Branch     string `json:"branch"`
	BaseHead   string `json:"baseHead"`
	WorkDirRel string `json:"workDirRel,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
}

type SessionWorktreeStatus struct {
	Isolated     bool   `json:"isolated"`
	Path         string `json:"path,omitempty"`
	Branch       string `json:"branch,omitempty"`
	BaseHead     string `json:"baseHead,omitempty"`
	Dirty        bool   `json:"dirty,omitempty"`
	ChangedFiles int    `json:"changedFiles,omitempty"`
	CommitsAhead int    `json:"commitsAhead,omitempty"`
	Error        string `json:"error,omitempty"`
}

func sessionWorktreeRecordPath(projectID, sessionID string) string {
	return filepath.Join(configDir(), "session-worktrees", projectSessionStorageKey(projectID, sessionID)+".json")
}

// sessionWorktreeDirName is the configDir-relative root that holds every
// session checkout. data_archive.go excludes it from exports and backups.
const sessionWorktreeDirName = "worktrees"

func sessionWorktreeCheckoutPath(projectID, sessionID string) string {
	base := configDir()
	if canonical, err := filepath.EvalSymlinks(base); err == nil {
		base = canonical
	}
	return filepath.Join(base, sessionWorktreeDirName, safeStorageKey(projectID), safeStorageKey(sessionID))
}

func sessionWorktreeBranch(projectID, sessionID string) string {
	return "gokin/" + safeStorageKey(projectID) + "/" + safeStorageKey(sessionID)
}

func saveSessionWorktreeRecord(record sessionWorktreeRecord) error {
	path := sessionWorktreeRecordPath(record.ProjectID, record.SessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o600)
}

func loadSessionWorktreeRecord(projectID, sessionID string) (*sessionWorktreeRecord, error) {
	data, err := readRegularFileLimited(sessionWorktreeRecordPath(projectID, sessionID), sessionWorktreeRecordMax)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var record sessionWorktreeRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode worktree metadata: %w", err)
	}
	if record.Version != sessionWorktreeRecordVersion || record.ProjectID != projectID || record.SessionID != sessionID {
		return nil, fmt.Errorf("worktree metadata identity mismatch")
	}
	if strings.TrimSpace(record.Branch) == "" || strings.TrimSpace(record.BaseHead) == "" {
		return nil, fmt.Errorf("worktree metadata is incomplete")
	}
	if record.WorkDirRel != "" && !safeRelativeWorktreePath(record.WorkDirRel) {
		return nil, fmt.Errorf("worktree metadata contains an unsafe relative path")
	}
	return &record, nil
}

func safeRelativeWorktreePath(rel string) bool {
	if rel == "" || rel == "." {
		return true
	}
	if filepath.IsAbs(rel) {
		return false
	}
	clean := filepath.Clean(rel)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func loadSessionWorktree(projectID string, session *ChatSession) {
	record, err := loadSessionWorktreeRecord(projectID, session.ID)
	if err != nil {
		session.WorktreeError = err.Error()
		return
	}
	if record == nil {
		return
	}
	checkout := sessionWorktreeCheckoutPath(projectID, session.ID)
	workDir := checkout
	if record.WorkDirRel != "" && record.WorkDirRel != "." {
		workDir = filepath.Join(checkout, record.WorkDirRel)
	}
	session.WorktreePath = checkout
	session.WorktreeWorkDir = workDir
	session.WorktreeBranch = record.Branch
	session.WorktreeBaseHead = record.BaseHead
	if err := validateLoadedSessionWorktree(checkout, workDir, record.Branch); err != nil {
		session.WorktreeError = err.Error()
	}
}

func validateLoadedSessionWorktree(checkout, workDir, branch string) error {
	checkoutInfo, err := os.Lstat(checkout)
	if err != nil {
		return fmt.Errorf("isolated worktree is unavailable: %w", err)
	}
	if checkoutInfo.Mode()&os.ModeSymlink != 0 || !checkoutInfo.IsDir() {
		return fmt.Errorf("isolated worktree path is not a real directory")
	}
	workInfo, err := os.Stat(workDir)
	if err != nil || !workInfo.IsDir() {
		return fmt.Errorf("isolated session working directory is unavailable")
	}
	top, err := runGitWorktreeCommand(workDir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return fmt.Errorf("isolated worktree is no longer registered with Git")
	}
	canonicalTop, topErr := filepath.EvalSymlinks(top)
	canonicalCheckout, checkoutErr := filepath.EvalSymlinks(checkout)
	canonicalWorkDir, workDirErr := filepath.EvalSymlinks(workDir)
	if topErr != nil || checkoutErr != nil || workDirErr != nil || canonicalTop != canonicalCheckout {
		return fmt.Errorf("isolated worktree is no longer registered with Git")
	}
	rel, relErr := filepath.Rel(canonicalCheckout, canonicalWorkDir)
	if relErr != nil || !safeRelativeWorktreePath(rel) {
		return fmt.Errorf("isolated session working directory escapes its checkout")
	}
	currentBranch, err := runGitWorktreeCommand(workDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("cannot inspect isolated worktree branch")
	}
	if currentBranch != branch {
		return fmt.Errorf("isolated worktree branch changed from %q to %q", branch, currentBranch)
	}
	return nil
}

// sessionWorkingDirectory resolves the directory that UI-adjacent features
// (terminal, side chat, previews) should associate with a conversation. A
// recorded worktree always fails closed: silently falling back to the project
// root would let one session modify another session's files.
func sessionWorkingDirectory(project *Project, session *ChatSession) (string, error) {
	project.mu.RLock()
	projectDir := project.Directory
	project.mu.RUnlock()

	session.mu.RLock()
	checkout := session.WorktreePath
	workDir := session.WorktreeWorkDir
	branch := session.WorktreeBranch
	worktreeErr := session.WorktreeError
	session.mu.RUnlock()
	if worktreeErr != "" {
		return "", fmt.Errorf("isolated session worktree is unavailable: %s", worktreeErr)
	}
	if checkout == "" {
		return projectDir, nil
	}
	if err := validateLoadedSessionWorktree(checkout, workDir, branch); err != nil {
		session.mu.Lock()
		if session.WorktreePath == checkout && session.WorktreeWorkDir == workDir {
			session.WorktreeError = err.Error()
		}
		session.mu.Unlock()
		return "", fmt.Errorf("isolated session worktree is unavailable: %w", err)
	}
	return workDir, nil
}

func worktreeStartDirForParent(project *Project, parentSessionID string) (string, error) {
	project.mu.RLock()
	projectDir := project.Directory
	parent := project.sessions[parentSessionID]
	project.mu.RUnlock()
	if parent == nil {
		return projectDir, nil
	}
	return sessionWorkingDirectory(project, parent)
}

// provisionSessionWorktree gives a newly-created session an independent Git
// checkout. Non-Git projects intentionally remain shared-directory sessions.
func provisionSessionWorktree(project *Project, session *ChatSession, startDir string) error {
	project.mu.RLock()
	projectDir := project.Directory
	project.mu.RUnlock()
	repoRoot := runGit(projectDir, "rev-parse", "--show-toplevel")
	if repoRoot == "" {
		return nil
	}
	canonicalProject, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project directory: %w", err)
	}
	canonicalRepo, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return fmt.Errorf("resolve Git root: %w", err)
	}
	rel, err := filepath.Rel(canonicalRepo, canonicalProject)
	if err != nil || !safeRelativeWorktreePath(rel) {
		return fmt.Errorf("project directory is outside its Git root")
	}
	if startDir == "" {
		startDir = projectDir
	}
	baseHead := runGit(startDir, "rev-parse", "HEAD")
	if baseHead == "" {
		return fmt.Errorf("cannot resolve HEAD for isolated session")
	}

	checkout := sessionWorktreeCheckoutPath(project.ID, session.ID)
	branch := sessionWorktreeBranch(project.ID, session.ID)
	if _, err := os.Lstat(checkout); err == nil {
		return fmt.Errorf("managed worktree path already exists: %s", checkout)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect managed worktree path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(checkout), 0o700); err != nil {
		return fmt.Errorf("create managed worktree directory: %w", err)
	}
	if output, err := runGitWorktreeCommand(canonicalRepo, "worktree", "add", "-b", branch, checkout, baseHead); err != nil {
		return fmt.Errorf("create isolated Git worktree: %s", gitErrText(output, err))
	}
	sourceRoot := runGit(startDir, "rev-parse", "--show-toplevel")
	if sourceRoot == "" {
		sourceRoot = canonicalRepo
	}
	if err := copyWorktreeIncludes(sourceRoot, checkout); err != nil {
		_, _ = runGitWorktreeCommand(canonicalRepo, "worktree", "remove", "--force", checkout)
		_, _ = runGitWorktreeCommand(canonicalRepo, "branch", "-D", branch)
		return fmt.Errorf("copy .worktreeinclude files: %w", err)
	}

	record := sessionWorktreeRecord{
		Version: sessionWorktreeRecordVersion, ProjectID: project.ID, SessionID: session.ID,
		Branch: branch, BaseHead: baseHead, WorkDirRel: rel, CreatedAt: time.Now().UnixMilli(),
	}
	if err := saveSessionWorktreeRecord(record); err != nil {
		_, _ = runGitWorktreeCommand(canonicalRepo, "worktree", "remove", "--force", checkout)
		_, _ = runGitWorktreeCommand(canonicalRepo, "branch", "-D", branch)
		return fmt.Errorf("persist isolated worktree metadata: %w", err)
	}
	loadSessionWorktree(project.ID, session)
	if session.WorktreeError != "" {
		_ = os.Remove(sessionWorktreeRecordPath(project.ID, session.ID))
		_, _ = runGitWorktreeCommand(canonicalRepo, "worktree", "remove", "--force", checkout)
		_, _ = runGitWorktreeCommand(canonicalRepo, "branch", "-D", branch)
		return fmt.Errorf("validate isolated worktree: %s", session.WorktreeError)
	}
	return nil
}

func runGitWorktreeCommand(dir string, args ...string) (string, error) {
	output, err := runGitWorktreeCommandRaw(dir, args...)
	return strings.TrimSpace(output), err
}

func runGitWorktreeCommandRaw(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sessionWorktreeGitTimeout)
	defer cancel()
	full := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", dir}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = gitWaitDelay
	output := &cappedCommandOutput{limit: maxGitOutputBytes}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	return output.String(), err
}

// copyWorktreeIncludes implements Claude-compatible .worktreeinclude support.
// The file uses gitignore syntax, but a match is copied only when Git also
// considers it ignored. Tracked files are already checked out and are never
// duplicated; symlinks are skipped to keep the managed checkout boundary
// intact.
func copyWorktreeIncludes(sourceRoot, checkout string) error {
	includePath := filepath.Join(sourceRoot, ".worktreeinclude")
	if _, err := readRegularFileLimited(includePath, sessionWorktreeIncludeMax); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	out, err := runGitWorktreeCommandRaw(sourceRoot,
		"ls-files", "--others", "--ignored", "--exclude-from=.worktreeinclude", "-z")
	if err != nil {
		return fmt.Errorf("list included ignored files: %s", gitErrText(out, err))
	}
	parts := strings.Split(out, "\x00")
	if len(parts) > sessionWorktreeCopyCountMax+1 {
		return fmt.Errorf(".worktreeinclude matched more than %d files", sessionWorktreeCopyCountMax)
	}
	var copiedBytes int64
	for _, rel := range parts {
		if rel == "" {
			continue
		}
		if !safeRelativeWorktreePath(rel) || rel == "." {
			return fmt.Errorf("unsafe included path %q", rel)
		}
		// Intersection, not union: a .worktreeinclude match alone is not
		// enough. The source must also be ignored by the repository rules.
		if checkOut, checkErr := runGitWorktreeCommand(sourceRoot, "check-ignore", "-q", "--", rel); checkErr != nil {
			var exitErr *exec.ExitError
			if errors.As(checkErr, &exitErr) && exitErr.ExitCode() == 1 {
				continue
			}
			return fmt.Errorf("verify ignored include %q: %s", rel, gitErrText(checkOut, checkErr))
		}
		source := filepath.Join(sourceRoot, rel)
		destination := filepath.Join(checkout, rel)
		info, statErr := os.Lstat(source)
		if statErr != nil {
			return fmt.Errorf("inspect included file %q: %w", rel, statErr)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > sessionWorktreeCopyFileMax {
			return fmt.Errorf("included file %q exceeds %d bytes", rel, sessionWorktreeCopyFileMax)
		}
		copiedBytes += info.Size()
		if copiedBytes > sessionWorktreeCopyTotalMax {
			return fmt.Errorf("included files exceed %d bytes in total", sessionWorktreeCopyTotalMax)
		}
		if err := copyWorktreeRegularFile(source, destination, info); err != nil {
			return fmt.Errorf("copy included file %q: %w", rel, err)
		}
	}
	return nil
}

func copyWorktreeRegularFile(source, destination string, expected os.FileInfo) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return fmt.Errorf("source changed while opening")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(in, sessionWorktreeCopyFileMax+1))
	if err != nil {
		return err
	}
	if len(data) > sessionWorktreeCopyFileMax {
		return fmt.Errorf("source exceeds copy limit")
	}
	perm := opened.Mode().Perm()
	if perm == 0 {
		perm = 0o600
	}
	return atomicWriteFile(destination, data, perm)
}

func sessionWorktreeStatus(session *ChatSession) SessionWorktreeStatus {
	session.mu.RLock()
	status := SessionWorktreeStatus{
		Isolated: session.WorktreePath != "", Path: session.WorktreeWorkDir,
		Branch: session.WorktreeBranch, BaseHead: session.WorktreeBaseHead, Error: session.WorktreeError,
	}
	checkout := session.WorktreePath
	workDir := session.WorktreeWorkDir
	branch := session.WorktreeBranch
	session.mu.RUnlock()
	if !status.Isolated || status.Error != "" {
		return status
	}
	if err := validateLoadedSessionWorktree(checkout, workDir, branch); err != nil {
		status.Error = err.Error()
		session.mu.Lock()
		if session.WorktreePath == checkout && session.WorktreeWorkDir == workDir {
			session.WorktreeError = status.Error
		}
		session.mu.Unlock()
		return status
	}
	out, err := runGitWorktreeCommand(workDir, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		status.Error = fmt.Sprintf("inspect isolated worktree status: %s", gitErrText(out, err))
		return status
	}
	if out != "" {
		status.Dirty = true
		status.ChangedFiles = len(strings.Split(out, "\n"))
	}
	if status.BaseHead != "" {
		countOut, countErr := runGitWorktreeCommand(workDir, "rev-list", "--count", status.BaseHead+"..HEAD")
		if countErr != nil {
			status.Error = fmt.Sprintf("inspect isolated worktree commits: %s", gitErrText(countOut, countErr))
			return status
		}
		status.CommitsAhead, _ = strconv.Atoi(strings.TrimSpace(countOut))
	}
	return status
}

func removeSessionWorktree(project *Project, session *ChatSession) error {
	project.mu.RLock()
	projectDir := project.Directory
	project.mu.RUnlock()
	return removeSessionWorktreeAt(project, session, projectDir)
}

// removeSessionWorktreeAt is used by metadata transactions that already hold
// project.mu and therefore cannot call the locking wrapper above.
func removeSessionWorktreeAt(project *Project, session *ChatSession, projectDir string) error {
	status := sessionWorktreeStatus(session)
	if status.Error != "" {
		return fmt.Errorf("cannot remove unavailable worktree safely: %s", status.Error)
	}
	if !status.Isolated {
		return nil
	}
	if status.Dirty {
		return fmt.Errorf("session worktree has %d uncommitted file change(s); commit or discard them before deleting this chat", status.ChangedFiles)
	}
	session.mu.RLock()
	checkout := session.WorktreePath
	branch := session.WorktreeBranch
	session.mu.RUnlock()
	if output, err := runGitWorktreeCommand(projectDir, "worktree", "remove", checkout); err != nil {
		return fmt.Errorf("remove session worktree: %s", gitErrText(output, err))
	}
	if status.CommitsAhead == 0 && branch != "" {
		if output, err := runGitWorktreeCommand(projectDir, "branch", "-D", branch); err != nil && project.studio != nil {
			project.studio.logf("warn", "worktree", "remove unused branch %q: %s", branch, gitErrText(output, err))
		}
	}
	if err := os.Remove(sessionWorktreeRecordPath(project.ID, session.ID)); err != nil && !os.IsNotExist(err) {
		if project.studio != nil {
			project.studio.logf("warn", "worktree", "remove stale metadata for project %q session %q: %v", project.ID, session.ID, err)
		}
	}
	session.mu.Lock()
	session.WorktreePath = ""
	session.WorktreeWorkDir = ""
	session.WorktreeBranch = ""
	session.WorktreeBaseHead = ""
	session.WorktreeError = ""
	session.registry = nil
	session.taskManager = nil
	session.planManager = nil
	session.mu.Unlock()
	return nil
}

func (s *Studio) GetSessionWorktreeStatus(projectID, sessionID string) (*SessionWorktreeStatus, error) {
	_, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	status := sessionWorktreeStatus(session)
	return &status, nil
}

func (s *Studio) projectSession(projectID, sessionID string) (*Project, *ChatSession, error) {
	s.mu.RLock()
	project, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("project not found: %s", projectID)
	}
	if sessionID == "" {
		sessionID = "default"
	}
	project.mu.RLock()
	session, ok := project.sessions[sessionID]
	project.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return project, session, nil
}
