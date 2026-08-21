package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ginkida/gokin-studio/internal/engine/security"
	"github.com/ginkida/gokin-studio/internal/engine/wsl"
	"google.golang.org/genai"
)

// GitPRTool creates and manages pull requests via gh CLI.
type GitPRTool struct {
	workDir string
}

// NewGitPRTool creates a new GitPRTool instance.
func NewGitPRTool(workDir string) *GitPRTool {
	return &GitPRTool{workDir: workDir}
}

// runGH runs a gh invocation in the tool's work directory.
//
// For a WSL project the checkout lives inside the distro, so host gh would read
// the distro's git config over the 9P share while authenticating as the Windows
// user — a different `gh auth` identity than the one the user set up next to the
// repo, and a different git. Routing puts gh where the repo is.
//
// wsl.DetectFor returns a host target for every non-WSL directory and ApplyExec
// then leaves the command untouched, so this is byte-identical off Windows.
func (t *GitPRTool) runGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	// Dir must be final before ApplyExec, which deliberately CLEARS it: a UNC
	// path is not a legal working directory for the wsl.exe process itself, and
	// the distro-side directory travels in the plan instead. Restoring cmd.Dir
	// afterwards would hand CreateProcess the illegal value again.
	cmd.Dir = t.workDir
	wsl.ApplyExec(cmd, wsl.DetectFor(t.workDir), append([]string{"gh"}, args...),
		security.WorkspaceEnvironmentSnapshot())
	return cmd.CombinedOutput()
}

func (t *GitPRTool) Name() string { return "git_pr" }

func (t *GitPRTool) Description() string {
	return "Creates and manages GitHub pull requests using gh CLI. Supports auto-generating PR descriptions from commit history."
}

func (t *GitPRTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type:        genai.TypeString,
					Description: "PR action: 'create', 'list', 'view', 'checks', 'merge', 'close'",
					Enum:        []string{"create", "list", "view", "checks", "merge", "close"},
				},
				"title": {
					Type:        genai.TypeString,
					Description: "PR title (for create). If empty with auto_description=true, will be auto-generated.",
				},
				"body": {
					Type:        genai.TypeString,
					Description: "PR body/description (for create)",
				},
				"base": {
					Type:        genai.TypeString,
					Description: "Base branch for PR (default: main/master)",
				},
				"draft": {
					Type:        genai.TypeBoolean,
					Description: "Create as draft PR",
				},
				"pr_number": {
					Type:        genai.TypeString,
					Description: "PR number (for view, checks, merge, close)",
				},
				"auto_description": {
					Type:        genai.TypeBoolean,
					Description: "Auto-generate PR title and description from commits (default: false)",
				},
			},
			Required: []string{"action"},
		},
	}
}

func (t *GitPRTool) Validate(args map[string]any) error {
	action, ok := GetString(args, "action")
	if !ok || action == "" {
		return NewValidationError("action", "is required")
	}

	switch action {
	case "create":
		// Title can be auto-generated
	case "view", "checks", "merge", "close":
		pr, _ := GetString(args, "pr_number")
		if pr == "" {
			return NewValidationError("pr_number", "is required for "+action)
		}
		for _, c := range pr {
			if c < '0' || c > '9' {
				return NewValidationError("pr_number", "must be a numeric PR number")
			}
		}
	case "list":
		// no extra params
	}

	return nil
}

func (t *GitPRTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	// Ask where the command will actually run. A host exec.LookPath cannot see a
	// gh installed inside the distro — WSL interop pushes the Windows PATH in,
	// never the distro's PATH out — so a host-only check would reject exactly
	// the projects whose gh sits next to the repo, and would do it before any
	// routing could happen.
	target := wsl.DetectFor(t.workDir)
	if err := wsl.LookPathFor(ctx, target, "gh"); err != nil {
		return NewErrorResult(wsl.MissingCommandHint(target, "gh",
			"gh CLI is not installed. Install it from https://cli.github.com/")), nil
	}

	action, _ := GetString(args, "action")

	switch action {
	case "create":
		return t.createPR(ctx, args)
	case "list":
		return t.listPRs(ctx, args)
	case "view":
		return t.viewPR(ctx, args)
	case "checks":
		return t.checksPR(ctx, args)
	case "merge":
		return t.mergePR(ctx, args)
	case "close":
		return t.closePR(ctx, args)
	default:
		return NewErrorResult(fmt.Sprintf("unknown action: %s", action)), nil
	}
}

func (t *GitPRTool) createPR(ctx context.Context, args map[string]any) (ToolResult, error) {
	title, _ := GetString(args, "title")
	body, _ := GetString(args, "body")
	base := GetStringDefault(args, "base", "")
	draft := GetBoolDefault(args, "draft", false)
	autoDesc := GetBoolDefault(args, "auto_description", false)

	// Auto-generate title and body from commits
	if autoDesc || (title == "" && body == "") {
		generatedTitle, generatedBody := t.generatePRDescription(ctx, base)
		if title == "" {
			title = generatedTitle
		}
		if body == "" {
			body = generatedBody
		}
	}

	if title == "" {
		return NewErrorResult("title is required (or use auto_description=true)"), nil
	}

	// Ensure branch is pushed
	pushCmd := newGitCommand(ctx, t.workDir, "push", "-u", "origin", "HEAD")
	pushOutput, err := pushCmd.CombinedOutput()
	if err != nil {
		outStr := string(pushOutput)
		if !strings.Contains(outStr, "Everything up-to-date") {
			return NewErrorResult(fmt.Sprintf("failed to push branch: %s\n%s", err, outStr)), nil
		}
	}

	// Build gh pr create command
	cmdArgs := []string{"pr", "create", "--title", title}
	if body != "" {
		cmdArgs = append(cmdArgs, "--body", body)
	}
	if base != "" {
		cmdArgs = append(cmdArgs, "--base", base)
	}
	if draft {
		cmdArgs = append(cmdArgs, "--draft")
	}

	output, err := t.runGH(ctx, cmdArgs...)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("failed to create PR: %s\n%s", err, string(output))), nil
	}

	return NewSuccessResult(fmt.Sprintf("Pull request created:\n%s", strings.TrimSpace(string(output)))), nil
}

func (t *GitPRTool) listPRs(ctx context.Context, _ map[string]any) (ToolResult, error) {
	output, err := t.runGH(ctx, "pr", "list", "--limit", "20")
	if err != nil {
		return NewErrorResult(fmt.Sprintf("failed to list PRs: %s\n%s", err, string(output))), nil
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return NewSuccessResult("No open pull requests."), nil
	}
	return NewSuccessResult(result), nil
}

func (t *GitPRTool) viewPR(ctx context.Context, args map[string]any) (ToolResult, error) {
	prNum, _ := GetString(args, "pr_number")
	output, err := t.runGH(ctx, "pr", "view", prNum)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("failed to view PR: %s\n%s", err, string(output))), nil
	}
	return NewSuccessResult(strings.TrimSpace(string(output))), nil
}

func (t *GitPRTool) checksPR(ctx context.Context, args map[string]any) (ToolResult, error) {
	prNum, _ := GetString(args, "pr_number")
	output, err := t.runGH(ctx, "pr", "checks", prNum)
	if err != nil {
		// Checks command may exit non-zero if checks are failing
		return NewSuccessResult(fmt.Sprintf("PR #%s checks:\n%s", prNum, strings.TrimSpace(string(output)))), nil
	}
	return NewSuccessResult(fmt.Sprintf("PR #%s checks:\n%s", prNum, strings.TrimSpace(string(output)))), nil
}

func (t *GitPRTool) mergePR(ctx context.Context, args map[string]any) (ToolResult, error) {
	prNum, _ := GetString(args, "pr_number")
	output, err := t.runGH(ctx, "pr", "merge", prNum, "--merge")
	if err != nil {
		return NewErrorResult(fmt.Sprintf("failed to merge PR: %s\n%s", err, string(output))), nil
	}
	return NewSuccessResult(fmt.Sprintf("PR #%s merged.\n%s", prNum, strings.TrimSpace(string(output)))), nil
}

func (t *GitPRTool) closePR(ctx context.Context, args map[string]any) (ToolResult, error) {
	prNum, _ := GetString(args, "pr_number")
	output, err := t.runGH(ctx, "pr", "close", prNum)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("failed to close PR: %s\n%s", err, string(output))), nil
	}
	return NewSuccessResult(fmt.Sprintf("PR #%s closed.\n%s", prNum, strings.TrimSpace(string(output)))), nil
}

// generatePRDescription creates a PR title and body from commit history.
func (t *GitPRTool) generatePRDescription(ctx context.Context, base string) (string, string) {
	if base == "" {
		base = t.detectDefaultBranch(ctx)
	}

	if !isValidGitRef(base) {
		return "Update", ""
	}

	// Get commits since base
	logCmd := newGitCommand(ctx, t.workDir, "log", base+"..HEAD", "--oneline", "--no-decorate")
	logOutput, err := logCmd.Output()
	if err != nil {
		return "Update", ""
	}

	commits := strings.Split(strings.TrimSpace(string(logOutput)), "\n")
	if len(commits) == 0 || (len(commits) == 1 && commits[0] == "") {
		return "Update", ""
	}

	// Generate title from first commit or summary
	var title string
	if len(commits) == 1 {
		parts := strings.SplitN(commits[0], " ", 2)
		if len(parts) >= 2 {
			title = parts[1]
		} else {
			title = commits[0]
		}
	} else {
		// Multiple commits - summarize
		title = fmt.Sprintf("Update: %d changes", len(commits))
		// Try to find a common theme
		if firstParts := strings.SplitN(commits[0], " ", 2); len(firstParts) >= 2 {
			first := firstParts[1]
			if len(first) <= 70 {
				title = first
			}
		}
	}

	// Truncate title (rune-safe for non-ASCII branch/PR titles)
	if runes := []rune(title); len(runes) > 70 {
		title = string(runes[:67]) + "..."
	}

	// Generate body
	var body strings.Builder
	body.WriteString("## Summary\n\n")

	// Get diff stats
	statCmd := newGitCommand(ctx, t.workDir, "diff", "--stat", base+"..HEAD")
	statOutput, _ := statCmd.Output()

	// List commits as bullet points
	for _, commit := range commits {
		parts := strings.SplitN(commit, " ", 2)
		if len(parts) >= 2 {
			fmt.Fprintf(&body, "- %s\n", parts[1])
		}
	}

	if len(statOutput) > 0 {
		body.WriteString("\n## Changed files\n\n```\n")
		body.WriteString(strings.TrimSpace(string(statOutput)))
		body.WriteString("\n```\n")
	}

	body.WriteString("\n## Test plan\n\n- [ ] Tests pass\n- [ ] Manual verification\n")

	return title, body.String()
}

// detectDefaultBranch finds the default branch name (main or master).
func (t *GitPRTool) detectDefaultBranch(ctx context.Context) string {
	// Try to detect from remote
	cmd := newGitCommand(ctx, t.workDir, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	output, err := cmd.Output()
	if err == nil {
		branch := strings.TrimSpace(string(output))
		// Strip "origin/" prefix
		if _, after, ok := strings.Cut(branch, "/"); ok {
			return after
		}
		return branch
	}

	// Fallback: check if main or master exists
	for _, name := range []string{"main", "master"} {
		checkCmd := newGitCommand(ctx, t.workDir, "rev-parse", "--verify", name)
		if err := checkCmd.Run(); err == nil {
			return name
		}
	}

	return "main"
}
