package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/logging"

	"google.golang.org/genai"
)

// MaxPinnedContextBytes caps text injected into every system prompt. A pin is
// intentionally concise hot memory, not a document store; bounding it prevents
// a corrupt or locally replaced persistence file from exhausting model context.
const MaxPinnedContextBytes = 64 << 10

// PinContextTool allows the agent to pin information to the system prompt.
// Pinned context is persisted to .gokin/pinned_context.md and restored on restart.
type PinContextTool struct {
	updater func(content string)
	workDir string
}

// NewPinContextTool creates a new PinContextTool.
func NewPinContextTool(updater func(content string)) *PinContextTool {
	return &PinContextTool{
		updater: updater,
	}
}

// SetWorkDir sets the working directory for pin persistence.
func (t *PinContextTool) SetWorkDir(dir string) {
	t.workDir = dir
}

// LoadPersistedPin reads pinned context from disk and applies it via updater.
// Called at app startup to restore the pin from a previous session.
func (t *PinContextTool) LoadPersistedPin() {
	if t.workDir == "" || t.updater == nil {
		return
	}
	content, err := ReadPersistedPin(t.workDir)
	if err != nil {
		logging.Debug("pinned context was not restored", "error", err)
		return
	}
	if content != "" {
		t.updater(content)
		logging.Debug("restored pinned context from disk", "size", len(content))
	}
}

// SetUpdater sets the function to update pinned context.
func (t *PinContextTool) SetUpdater(fn func(string)) {
	t.updater = fn
}

func (t *PinContextTool) Name() string {
	return "pin_context"
}

func (t *PinContextTool) Description() string {
	return `Pins a snippet of information to your system prompt for the rest of the session.
Use this for "hot memory" — to keep track of your current high-level goal, important file paths, or complex constraints that you don't want to lose focus on.

PARAMETERS:
- content (required): The information to pin. Providing an empty string or 'clear' will unpin all context.
- clear (optional): If true, clears the pinned context rather than setting it.`
}

func (t *PinContextTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"content": {
					Type:        genai.TypeString,
					Description: "Text to pin to system prompt",
				},
				"clear": {
					Type:        genai.TypeBoolean,
					Description: "If true, clear existing pinned context",
				},
			},
			Required: []string{"content"},
		},
	}
}

func (t *PinContextTool) Validate(args map[string]any) error {
	content, ok := GetString(args, "content")
	if !ok {
		return NewValidationError("content", "is required")
	}
	if clear, _ := args["clear"].(bool); clear {
		return nil
	}
	return ValidatePinnedContext(content)
}

func (t *PinContextTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return NewErrorResult(fmt.Sprintf("pin_context cancelled: %s", err)), nil
	}

	content, _ := GetString(args, "content")
	clear, _ := args["clear"].(bool)

	if t.updater == nil {
		return NewErrorResult("pinned context not supported by this agent"), nil
	}
	if clear || content == "clear" || content == "" {
		if err := t.persistPin(""); err != nil {
			return NewErrorResult(fmt.Sprintf("failed to clear pinned context: %s", err)), nil
		}
		t.updater("")
		EmitMemoryNotify(ctx, "unpinned", "")
		return NewSuccessResult("Pinned context cleared."), nil
	}

	if err := ValidatePinnedContext(content); err != nil {
		return NewErrorResult(fmt.Sprintf("invalid pinned context: %s", err)), nil
	}
	if err := t.persistPin(content); err != nil {
		return NewErrorResult(fmt.Sprintf("failed to persist pinned context: %s", err)), nil
	}
	t.updater(content)
	EmitMemoryNotify(ctx, "pinned", content)
	previewRunes := []rune(content)
	if len(previewRunes) > 120 {
		previewRunes = append(previewRunes[:120], []rune("...")...)
	}
	return NewSuccessResult("Pinned to system prompt: " + string(previewRunes)), nil
}

// ValidatePinnedContext applies the same trust boundary to tool calls and
// persisted pins loaded at startup.
func ValidatePinnedContext(content string) error {
	if len(content) > MaxPinnedContextBytes {
		return fmt.Errorf("content is too large (%d bytes, maximum %d)", len(content), MaxPinnedContextBytes)
	}
	if !utf8.ValidString(content) {
		return fmt.Errorf("content must be valid UTF-8")
	}
	return nil
}

// ReadPersistedPin safely reads a project's pin without following a symlink or
// allocating an unbounded file. Missing pins are returned as os.ErrNotExist.
func ReadPersistedPin(workDir string) (string, error) {
	path := filepath.Join(workDir, ".gokin", "pinned_context.md")
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing symlinked pinned context")
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("pinned context is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return "", fmt.Errorf("pinned context changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxPinnedContextBytes+1))
	if err != nil {
		return "", err
	}
	content := string(data)
	if err := ValidatePinnedContext(content); err != nil {
		return "", err
	}
	return content, nil
}

// persistPin saves or removes the pin file on disk.
func (t *PinContextTool) persistPin(content string) error {
	if t.workDir == "" {
		return nil
	}
	path := filepath.Join(t.workDir, ".gokin", "pinned_context.md")
	if content == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	// Pins can contain project details or credentials copied from a prompt.
	// Keep them private and replace the file atomically so a crash cannot leave
	// a partially written system prompt behind.
	return AtomicWriteString(path, content, 0600)
}
