package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/genai"
)

// ReviewChangesTool shows a consolidated diff of all uncommitted changes,
// optimized for agent self-verification after a batch of edits.
//
// Ported from gokin internal/tools/review_changes.go.
type ReviewChangesTool struct {
	workDir string
}

// NewReviewChangesTool creates a new ReviewChangesTool.
func NewReviewChangesTool(workDir string) *ReviewChangesTool {
	return &ReviewChangesTool{workDir: workDir}
}

func (t *ReviewChangesTool) Name() string { return "review_changes" }

func (t *ReviewChangesTool) Description() string {
	return "Shows a consolidated view of all uncommitted working-tree changes, including newly-created (untracked) files. " +
		"Use this after making edits to verify what changed before running tests or committing. " +
		"Returns a compact summary: changed files list + diff per file (truncated to first 60 lines each)."
}

func (t *ReviewChangesTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"staged": {
					Type:        genai.TypeBoolean,
					Description: "If true, show staged (cached) changes instead of working-tree changes.",
				},
				"name_only": {
					Type:        genai.TypeBoolean,
					Description: "If true, return only the list of changed file names without diffs.",
				},
				"file": {
					Type:        genai.TypeString,
					Description: "Focus on a single file path (absolute or relative to project root).",
				},
			},
		},
	}
}

func (t *ReviewChangesTool) Validate(args map[string]any) error { return nil }

const (
	reviewMaxLinesPerFile = 60
	reviewMaxTotalLines   = 500
	reviewMaxOutputRunes  = 30000
	// reviewMaxNewFileBytes caps how much of an untracked file is read for the
	// 60-line preview — a huge generated artifact must not be slurped whole.
	reviewMaxNewFileBytes = 256 << 10
)

// gitReviewCmd builds a git invocation rooted at workDir.
// -c core.quotepath=off: prevents non-ASCII filenames from being C-quoted with
// octal escapes (which would then not match any real path on disk).
// --relative (appended by callers): emits cwd-relative paths so the names can
// be resolved back to files when workDir is a subdirectory of the repo root.
func (t *ReviewChangesTool) gitReviewCmd(ctx context.Context, args ...string) *exec.Cmd {
	full := append([]string{"-c", "core.quotepath=off"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = t.workDir
	return cmd
}

func (t *ReviewChangesTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	staged := GetBoolDefault(args, "staged", false)
	nameOnly := GetBoolDefault(args, "name_only", false)
	file := GetStringDefault(args, "file", "")

	var relFile string
	if file != "" {
		relFile = t.relReviewPath(file)
	}

	// --stat header (best-effort; its failure only degrades the header line).
	statArgs := []string{"diff", "--relative", "--stat", "--stat-width=120"}
	if staged {
		statArgs = append(statArgs, "--cached")
	}
	if relFile != "" {
		statArgs = append(statArgs, "--", relFile)
	}
	statOut, statErr := t.gitReviewCmd(ctx, statArgs...).Output()

	// Tracked changed files.
	nameArgs := []string{"diff", "--relative", "--name-only"}
	if staged {
		nameArgs = append(nameArgs, "--cached")
	}
	if relFile != "" {
		nameArgs = append(nameArgs, "--", relFile)
	}
	nameOut, nameErr := t.gitReviewCmd(ctx, nameArgs...).Output()
	trackedRaw := strings.TrimSpace(string(nameOut))
	if nameErr != nil && trackedRaw == "" {
		return NewErrorResult(fmt.Sprintf("review_changes failed: %s", reviewGitErrText(nameErr))), nil
	}

	var tracked []string
	if trackedRaw != "" {
		tracked = strings.Split(trackedRaw, "\n")
	}

	// Untracked (newly-created) files. git diff never shows these, but they
	// are exactly the files an agent just wrote and wants to verify. Only in
	// the working-tree (non-staged) view.
	var untracked []string
	untrackedSet := map[string]bool{}
	if !staged {
		othersArgs := []string{"ls-files", "--others", "--exclude-standard"}
		if relFile != "" {
			othersArgs = append(othersArgs, "--", relFile)
		}
		if othersOut, err := t.gitReviewCmd(ctx, othersArgs...).Output(); err == nil {
			if raw := strings.TrimSpace(string(othersOut)); raw != "" {
				untracked = strings.Split(raw, "\n")
				for _, f := range untracked {
					untrackedSet[strings.TrimSpace(f)] = true
				}
			}
		}
	}

	if len(tracked) == 0 && len(untracked) == 0 {
		return NewSuccessResult("No uncommitted changes found. Working tree is clean."), nil
	}

	allFiles := make([]string, 0, len(tracked)+len(untracked))
	allFiles = append(allFiles, tracked...)
	allFiles = append(allFiles, untracked...)
	fileCount := len(allFiles)

	var result strings.Builder

	// Header.
	if statErr == nil && strings.TrimSpace(string(statOut)) != "" {
		result.WriteString(strings.TrimSpace(string(statOut)))
		result.WriteString("\n")
	} else {
		fmt.Fprintf(&result, "%d file(s) changed\n", fileCount)
	}
	if len(untracked) > 0 {
		fmt.Fprintf(&result, "(%d new/untracked file(s))\n", len(untracked))
	}

	if nameOnly {
		result.WriteString("\nChanged files:\n")
		for _, f := range allFiles {
			f = strings.TrimSpace(f)
			if untrackedSet[f] {
				fmt.Fprintf(&result, "  %s (new)\n", f)
			} else {
				fmt.Fprintf(&result, "  %s\n", f)
			}
		}
		return NewSuccessResult(result.String()), nil
	}

	// Per-file diff (truncated to reviewMaxLinesPerFile per file).
	totalLines := 0
	filesShown := 0
	truncated := false

	for _, f := range allFiles {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if totalLines >= reviewMaxTotalLines {
			truncated = true
			break
		}

		if untrackedSet[f] {
			block, n, err := renderReviewUntrackedFile(filepath.Join(t.workDir, f))
			result.WriteString("\n")
			result.WriteString(strings.Repeat("─", 60))
			if err != nil {
				fmt.Fprintf(&result, "\n📄 %s (new file — unreadable: %v)\n", f, err)
				totalLines += 2
				filesShown++
				continue
			}
			fmt.Fprintf(&result, "\n📄 %s (new file)\n", f)
			result.WriteString(block)
			totalLines += n
			filesShown++
			continue
		}

		diffArgs := []string{"diff", "--relative"}
		if staged {
			diffArgs = append(diffArgs, "--cached")
		}
		diffArgs = append(diffArgs, "--", f)

		diffOut, err := t.gitReviewCmd(ctx, diffArgs...).Output()
		if err != nil {
			result.WriteString("\n")
			result.WriteString(strings.Repeat("─", 60))
			fmt.Fprintf(&result, "\n📄 %s (diff unavailable: %s)\n", f, reviewGitErrText(err))
			totalLines += 2
			filesShown++
			continue
		}

		diffStr := string(diffOut)
		diffLines := strings.Split(diffStr, "\n")

		result.WriteString("\n")
		result.WriteString(strings.Repeat("─", 60))
		fmt.Fprintf(&result, "\n📄 %s", f)

		if len(diffLines) > reviewMaxLinesPerFile+4 {
			show := diffLines[:reviewMaxLinesPerFile+4]
			fmt.Fprintf(&result, "  (%d lines changed, showing first %d)\n", len(diffLines)-4, reviewMaxLinesPerFile)
			result.WriteString(strings.Join(show, "\n"))
			fmt.Fprintf(&result, "\n  ... (%d more lines)", len(diffLines)-reviewMaxLinesPerFile-4)
			totalLines += reviewMaxLinesPerFile + 5
		} else {
			result.WriteString("\n")
			result.WriteString(diffStr)
			totalLines += len(diffLines)
		}
		filesShown++
	}

	if truncated {
		fmt.Fprintf(&result, "\n\n⚠️  Showing diffs for first %d of %d files. Use 'file' parameter to focus.", filesShown, fileCount)
	}

	// Overall output cap (rune-safe).
	if runes := []rune(result.String()); len(runes) > reviewMaxOutputRunes {
		return NewSuccessResult(string(runes[:reviewMaxOutputRunes]) + "\n\n... (output truncated at 30,000 chars)"), nil
	}

	return NewSuccessResult(result.String()), nil
}

// renderReviewUntrackedFile reads at most reviewMaxNewFileBytes of an untracked
// file and renders it as an all-added block for review display.
func renderReviewUntrackedFile(path string) (string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	content, err := io.ReadAll(io.LimitReader(f, reviewMaxNewFileBytes))
	if err != nil {
		return "", 0, err
	}
	block, n := renderReviewNewFileBlock(string(content), reviewMaxLinesPerFile)
	if info.Size() > reviewMaxNewFileBytes {
		block += fmt.Sprintf("  ... (file is %d bytes; preview capped)\n", info.Size())
		n++
	}
	return block, n, nil
}

// renderReviewNewFileBlock renders an untracked file's content as an all-added
// block capped at maxLines lines.
func renderReviewNewFileBlock(content string, maxLines int) (string, int) {
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total := len(lines)
	shown := lines
	if total > maxLines {
		shown = lines[:maxLines]
	}
	var b strings.Builder
	for _, l := range shown {
		b.WriteString("+")
		b.WriteString(l)
		b.WriteString("\n")
	}
	if total > maxLines {
		fmt.Fprintf(&b, "  ... (%d more lines)\n", total-maxLines)
	}
	return b.String(), len(shown) + 1
}

// reviewGitErrText extracts the most useful message from a failed git call.
func reviewGitErrText(err error) string {
	if exitErr, ok := err.(*exec.ExitError); ok {
		if s := strings.TrimSpace(string(exitErr.Stderr)); s != "" {
			return s
		}
	}
	return err.Error()
}

func (t *ReviewChangesTool) relReviewPath(absPath string) string {
	if filepath.IsAbs(absPath) {
		if rel, err := filepath.Rel(t.workDir, absPath); err == nil {
			return rel
		}
	}
	return absPath
}
