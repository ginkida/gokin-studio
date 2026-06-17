package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"google.golang.org/genai"

	"github.com/ginkida/gokin-studio/internal/engine/security"
)

// GitStatusTool shows git repository status.
type GitStatusTool struct {
	workDir       string
	pathValidator *security.PathValidator
}

// NewGitStatusTool creates a new GitStatusTool instance.
func NewGitStatusTool(workDir string) *GitStatusTool {
	return &GitStatusTool{
		workDir:       workDir,
		pathValidator: security.NewPathValidator([]string{workDir}, false),
	}
}

func (t *GitStatusTool) Name() string {
	return "git_status"
}

func (t *GitStatusTool) Description() string {
	return "Shows the working tree status of a git repository."
}

func (t *GitStatusTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {
					Type:        genai.TypeString,
					Description: "Path to the repository (default: working directory)",
				},
				"short": {
					Type:        genai.TypeBoolean,
					Description: "If true, use short format output",
				},
			},
		},
	}
}

func (t *GitStatusTool) Validate(args map[string]any) error {
	// All parameters are optional
	return nil
}

func (t *GitStatusTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	path := GetStringDefault(args, "path", t.workDir)
	short := GetBoolDefault(args, "short", false)

	// Confine the caller-supplied path to the project sandbox so the agent
	// can't probe git repositories outside the project (info disclosure).
	if t.pathValidator != nil {
		validPath, err := t.pathValidator.Validate(path)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("path validation failed: %s", err)), nil
		}
		path = validPath
	}

	// Build git status command
	cmdArgs := []string{"status"}
	if short {
		cmdArgs = append(cmdArgs, "--short")
	}

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Dir = path

	output, err := cmd.CombinedOutput()
	if err != nil {
		return NewErrorResult(fmt.Sprintf("git status failed: %s\n%s", err, string(output))), nil
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return NewSuccessResult("Nothing to report."), nil
	}

	return NewSuccessResult(result), nil
}
