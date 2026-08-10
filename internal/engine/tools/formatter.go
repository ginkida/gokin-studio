package tools

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
	"github.com/ginkida/gokin-studio/internal/engine/security"
)

// Formatter runs language-specific formatters on files after write/edit operations.
// The zero value is usable but does nothing; configure it with a command map.
//
// Ported from gokin internal/tools/formatter.go.
type Formatter struct {
	commands map[string]string // ext → command template, e.g. ".go" → "gofmt -w"
	timeout  time.Duration
}

// DefaultFormatters returns built-in formatter commands by file extension.
func DefaultFormatters() map[string]string {
	return map[string]string{
		".go": "gofmt -w",
	}
}

// NewFormatter creates a Formatter with the given command map and timeout.
// If timeout is ≤ 0, a 5-second default is used.
func NewFormatter(commands map[string]string, timeout time.Duration) *Formatter {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Formatter{
		commands: commands,
		timeout:  timeout,
	}
}

// Format runs the appropriate formatter for the given file path.
// Returns nil if no formatter is configured for this extension or the
// formatter binary is not found. Formatter errors are logged but not
// returned so they don't break the main tool execution flow.
func (f *Formatter) Format(ctx context.Context, filePath string) error {
	if f == nil || len(f.commands) == 0 {
		return nil
	}

	ext := filepath.Ext(filePath)
	cmdTemplate, ok := f.commands[ext]
	if !ok || cmdTemplate == "" {
		return nil
	}

	parts := strings.Fields(cmdTemplate)
	if len(parts) == 0 {
		return nil
	}
	if _, err := exec.LookPath(parts[0]); err != nil {
		return nil // Formatter not installed — silently skip
	}

	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	absoluteFile, err := filepath.Abs(filePath)
	if err != nil {
		logging.Warn("auto-format skipped: could not resolve file", "file", filePath, "error", err)
		return nil
	}
	isolation := security.DetectWorkspaceIsolation()
	if !isolation.Available {
		logging.Warn("auto-format skipped: workspace isolation unavailable",
			"file", filePath,
			"formatter", cmdTemplate,
			"detail", isolation.Detail)
		return nil
	}
	isolated, err := security.NewSandboxedCommandArgs(
		ctx,
		filepath.Dir(absoluteFile),
		parts[0],
		append(parts[1:], absoluteFile),
		security.DefaultSandboxConfig(),
	)
	if err != nil {
		logging.Warn("auto-format skipped: could not create workspace sandbox",
			"file", filePath,
			"formatter", cmdTemplate,
			"error", err)
		return nil
	}
	cmd := isolated.Command()
	if output, err := cmd.CombinedOutput(); err != nil {
		logging.Warn("auto-format failed",
			"file", filePath,
			"formatter", cmdTemplate,
			"error", err,
			"output", string(output))
	}

	return nil
}
