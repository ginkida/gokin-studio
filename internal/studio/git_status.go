package studio

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GitFileChange is one entry from `git status --porcelain`.
//
//	Status is the two-letter porcelain code ("M ", " M", "??", "A ", etc.)
//	normalised to a human label: "modified", "added", "deleted", "untracked".
//	The frontend uses Status to colour the chip and Path as the click action.
type GitFileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// GitCommit is one entry from `git log --pretty=...`.
type GitCommit struct {
	Hash    string `json:"hash"`    // 7-char short hash
	Subject string `json:"subject"` // first line of the commit message
	Age     string `json:"age"`     // human-readable "3 hours ago", computed locally
	AgeMs   int64  `json:"ageMs"`   // raw age in millis for sort/format choices
}

// ProjectGitContext is what the frontend's smart welcome screen renders.
// All fields are best-effort: an empty Branch / nil ChangedFiles / nil
// RecentCommits all mean "git not available or this isn't a repo" — the
// frontend shrugs and shows the language-detection welcome instead.
type ProjectGitContext struct {
	Branch         string          `json:"branch,omitempty"`
	IsRepo         bool            `json:"isRepo"`
	ChangedFiles   []GitFileChange `json:"changedFiles,omitempty"`
	UntrackedFiles []GitFileChange `json:"untrackedFiles,omitempty"`
	RecentCommits  []GitCommit     `json:"recentCommits,omitempty"`
	// AheadBehind: "+3 -1" if the branch is 3 ahead and 1 behind upstream;
	// "" if there's no upstream or it can't be determined.
	AheadBehind string `json:"aheadBehind,omitempty"`
	// Insertions / Deletions: summed line counts of uncommitted tracked
	// changes vs HEAD (`git diff HEAD --numstat`). Untracked files are not
	// counted (they're not in the diff). Both 0 = clean tracked tree.
	Insertions int `json:"insertions,omitempty"`
	Deletions  int `json:"deletions,omitempty"`
}

// GetProjectGitContext gathers a snapshot of the project's git state for
// the welcome screen. Caps lists at 20 changed / 20 untracked / 5 commits
// so a noisy repo doesn't bloat the response. Each git invocation has a
// 3-second timeout so a hung git operation can't block the UI.
//
// All errors are intentionally swallowed — this is a "show what you can"
// API. The frontend reads IsRepo to decide whether to render the git
// section at all.
func (s *Studio) GetProjectGitContext(projectID string) (*ProjectGitContext, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	dir := p.Directory
	out := &ProjectGitContext{}

	// rev-parse --is-inside-work-tree confirms we're actually in a git
	// repo (covers both ".git/" and submodules / worktrees).
	if !runGitBool(dir, "rev-parse", "--is-inside-work-tree") {
		return out, nil // not a repo; IsRepo stays false
	}
	out.IsRepo = true

	if branch := runGit(dir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "" {
		out.Branch = branch
	}

	// `git status --porcelain=v1` is stable + script-friendly.
	if status := runGit(dir, "status", "--porcelain=v1"); status != "" {
		const maxChanged = 20
		const maxUntracked = 20
		for _, line := range strings.Split(status, "\n") {
			if len(line) < 4 {
				continue
			}
			code := line[:2]
			path := strings.TrimSpace(line[3:])
			if path == "" {
				continue
			}
			switch {
			case code == "??":
				if len(out.UntrackedFiles) < maxUntracked {
					out.UntrackedFiles = append(out.UntrackedFiles, GitFileChange{Path: path, Status: "untracked"})
				}
			case strings.Contains(code, "M"):
				if len(out.ChangedFiles) < maxChanged {
					out.ChangedFiles = append(out.ChangedFiles, GitFileChange{Path: path, Status: "modified"})
				}
			case strings.Contains(code, "A"):
				if len(out.ChangedFiles) < maxChanged {
					out.ChangedFiles = append(out.ChangedFiles, GitFileChange{Path: path, Status: "added"})
				}
			case strings.Contains(code, "D"):
				if len(out.ChangedFiles) < maxChanged {
					out.ChangedFiles = append(out.ChangedFiles, GitFileChange{Path: path, Status: "deleted"})
				}
			case strings.Contains(code, "R"):
				if len(out.ChangedFiles) < maxChanged {
					out.ChangedFiles = append(out.ChangedFiles, GitFileChange{Path: path, Status: "renamed"})
				}
			default:
				if len(out.ChangedFiles) < maxChanged {
					out.ChangedFiles = append(out.ChangedFiles, GitFileChange{Path: path, Status: "changed"})
				}
			}
		}
	}

	// Recent commits — short hash, subject, and age in seconds since epoch.
	// Use a printable delimiter unlikely to appear in commit messages but
	// that survives the exec pipe (NUL bytes get mangled). `|||~~~|||` is
	// the chosen marker; if a future commit message contains exactly that
	// substring the parse will under-count fields and the line will be
	// dropped — acceptable because the alternative is rendering nothing.
	const sep = "|||~~~|||"
	logFmt := "%h" + sep + "%ct" + sep + "%s"
	if logOut := runGit(dir, "log", "--pretty=format:"+logFmt, "-5"); logOut != "" {
		now := time.Now()
		for _, line := range strings.Split(logOut, "\n") {
			parts := strings.SplitN(line, sep, 3)
			if len(parts) != 3 {
				continue
			}
			hash := parts[0]
			// `%ct` is unix seconds.
			var unix int64
			fmt.Sscanf(parts[1], "%d", &unix)
			ageMs := int64(0)
			ageStr := ""
			if unix > 0 {
				commitTime := time.Unix(unix, 0)
				ageMs = now.Sub(commitTime).Milliseconds()
				ageStr = humanAge(now.Sub(commitTime))
			}
			out.RecentCommits = append(out.RecentCommits, GitCommit{
				Hash:    hash,
				Subject: strings.TrimSpace(parts[2]),
				Age:     ageStr,
				AgeMs:   ageMs,
			})
		}
	}

	// Ahead/behind upstream — "@{u}" syntax fails silently when there's
	// no upstream tracking branch; we just leave AheadBehind empty.
	if ab := runGit(dir, "rev-list", "--left-right", "--count", "HEAD...@{u}"); ab != "" {
		// Output is "<ahead>\t<behind>".
		fields := strings.Fields(ab)
		if len(fields) == 2 {
			ahead, behind := fields[0], fields[1]
			if ahead != "0" || behind != "0" {
				out.AheadBehind = "+" + ahead + " -" + behind
			}
		}
	}

	// Line-count diff stat for uncommitted tracked changes vs HEAD. Each
	// numstat line is "<added>\t<deleted>\t<path>"; binary files show "-".
	// On a repo with no commits yet, `git diff HEAD` fails → empty → skipped.
	if ns := runGit(dir, "diff", "HEAD", "--numstat"); ns != "" {
		for _, line := range strings.Split(ns, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if a, err := strconv.Atoi(fields[0]); err == nil {
				out.Insertions += a
			}
			if d, err := strconv.Atoi(fields[1]); err == nil {
				out.Deletions += d
			}
		}
	}
	return out, nil
}

// runGit executes `git <args>` in dir with a 3-second timeout, returning
// stdout trimmed. Any error or non-zero exit yields "".
//
// iter 970+: hardened against two crash modes seen on Linux:
//  1. nil-deref on cmd.Process.Kill() when timeout fires before git's PATH
//     lookup completes — fork/exec hasn't populated Process yet, so the
//     pointer is nil. Guarded by an explicit nil check now.
//  2. goroutine panics (e.g. cmd.Output's internal buffer pool race) used
//     to take down the whole app since the goroutine wasn't recovered.
//     defer recoverPanic now catches it; runGit then returns "" as if the
//     command failed cleanly.
func runGit(dir string, args ...string) string {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	// Use Output() — captures stdout, drops stderr. Errors → "".
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		defer recoverPanic("git-exec", nil)
		defer close(done)
		out, err = cmd.Output()
	}()
	select {
	case <-done:
		if err != nil {
			return ""
		}
		return strings.TrimRight(string(out), "\n")
	case <-time.After(3 * time.Second):
		// Process may be nil if exec.LookPath failed (git not installed),
		// or if the goroutine panicked before fork/exec completed. Either
		// way Kill on nil would panic the caller — guard explicitly.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return ""
	}
}

// runGitBool returns true iff `git <args>` exits 0 and prints "true".
// Used for boolean queries like rev-parse --is-inside-work-tree.
func runGitBool(dir string, args ...string) bool {
	return runGit(dir, args...) == "true"
}

// humanAge converts a duration into a short relative string like
// "3 minutes ago", "2 days ago", "5 weeks ago". Used for the recent-
// commits list in the welcome screen.
func humanAge(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		return plural(mins, "minute") + " ago"
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		return plural(hours, "hour") + " ago"
	}
	days := int(d.Hours() / 24)
	if days < 7 {
		return plural(days, "day") + " ago"
	}
	if days < 30 {
		weeks := days / 7
		return plural(weeks, "week") + " ago"
	}
	if days < 365 {
		months := days / 30
		return plural(months, "month") + " ago"
	}
	years := days / 365
	return plural(years, "year") + " ago"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
