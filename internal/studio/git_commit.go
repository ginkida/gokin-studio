package studio

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CommitResult is returned by CommitChanges after a successful commit.
type CommitResult struct {
	Hash    string `json:"hash"`    // short hash of the new commit
	Subject string `json:"subject"` // first line of the commit message
	Branch  string `json:"branch"`
}

// CommitChanges stages ALL changes (`git add -A`, including untracked files) in
// the project's directory and commits them with the given message. It backs the
// context panel's one-click commit composer.
//
// Errors when: the message is empty, the project/dir isn't a git repo, there's
// nothing to commit, or git fails (e.g. user.name/user.email not configured, a
// pre-commit hook rejects). git's combined output is surfaced in the error so
// the user can see exactly why it failed.
func (s *Studio) CommitChanges(projectID, message string) (*CommitResult, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, fmt.Errorf("commit message is required")
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	dir := p.Directory
	pName := p.Name
	p.mu.RUnlock()

	if runGit(dir, "rev-parse", "--is-inside-work-tree") != "true" {
		return nil, fmt.Errorf("not a git repository")
	}
	if runGit(dir, "status", "--porcelain") == "" {
		return nil, fmt.Errorf("nothing to commit — working tree clean")
	}

	if out, err := runGitErr(dir, 30*time.Second, "add", "-A"); err != nil {
		return nil, fmt.Errorf("git add failed: %s", gitErrText(out, err))
	}
	if out, err := runGitErr(dir, 30*time.Second, "commit", "-m", message); err != nil {
		return nil, fmt.Errorf("git commit failed: %s", gitErrText(out, err))
	}

	res := &CommitResult{
		Hash:    runGit(dir, "rev-parse", "--short", "HEAD"),
		Subject: firstLine(message),
		Branch:  runGit(dir, "rev-parse", "--abbrev-ref", "HEAD"),
	}
	s.logf("info", "git", "committed in %q: %s (%s)", pName, res.Subject, res.Hash)
	return res, nil
}

// runGitErr runs `git -C dir <args>` with a timeout and returns the combined
// stdout+stderr (trimmed) plus the error. Unlike runGit it surfaces failures —
// commits need to report why they failed.
func runGitErr(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func gitErrText(out string, err error) string {
	if strings.TrimSpace(out) != "" {
		return out
	}
	return err.Error()
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(first)
}
