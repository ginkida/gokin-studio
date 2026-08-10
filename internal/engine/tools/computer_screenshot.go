package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"google.golang.org/genai"
)

const computerScreenshotMaxBytes = 12 << 20

var ErrDesktopCaptureCancelled = errors.New("desktop capture cancelled")

// ComputerScreenshotTool captures the current desktop. It is registered only
// for projects where the user explicitly enabled computer use.
type ComputerScreenshotTool struct {
	workDir      string
	includeImage bool
	capture      func(context.Context) ([]byte, error)
}

func NewComputerScreenshotTool(workDir string, includeImage bool) *ComputerScreenshotTool {
	return &ComputerScreenshotTool{workDir: workDir, includeImage: includeImage, capture: CaptureDesktopPNG}
}

// CaptureDesktopPNG exposes the same bounded OS capture primitive to explicit
// user-initiated composer actions. Agent-driven captures still go through the
// computer_screenshot tool and its permission gate.
func CaptureDesktopPNG(ctx context.Context) ([]byte, error) {
	return captureDesktopPNG(ctx)
}

// CaptureInteractiveDesktopPNG opens the operating system's native region /
// window picker. It is reserved for explicit composer actions: agent-driven
// computer use continues to capture the full desktop behind its approval gate.
func CaptureInteractiveDesktopPNG(ctx context.Context) ([]byte, error) {
	return captureInteractiveDesktopPNG(ctx)
}

// SaveDesktopCapture stores a validated capture in the project-private
// computer-use directory and returns its absolute path.
func SaveDesktopCapture(workDir string, data []byte) (string, error) {
	dir, err := ensureComputerCaptureDir(workDir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "latest-screen.png")
	if err := writePrivateRegularFile(path, data); err != nil {
		return "", err
	}
	return path, nil
}

func (*ComputerScreenshotTool) Name() string { return "computer_screenshot" }
func (*ComputerScreenshotTool) Description() string {
	return "Capture the current desktop screen. On Kimi K3 the screenshot is returned as vision input. On GLM the image is saved locally for inspection through a configured Vision MCP connector."
}
func (*ComputerScreenshotTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "computer_screenshot",
		Description: "Capture the current desktop before deciding where to click or type. Screen contents may be sensitive.",
		Parameters: &genai.Schema{
			Type:       genai.TypeObject,
			Properties: map[string]*genai.Schema{},
		},
	}
}
func (*ComputerScreenshotTool) Validate(args map[string]any) error {
	if len(args) != 0 {
		return NewValidationError("arguments", "computer_screenshot accepts no arguments")
	}
	return nil
}

func (t *ComputerScreenshotTool) Execute(ctx context.Context, _ map[string]any) (ToolResult, error) {
	data, err := t.capture(ctx)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("desktop capture failed: %v", err)), nil
	}
	if len(data) == 0 || len(data) > computerScreenshotMaxBytes {
		return NewErrorResult(fmt.Sprintf("desktop capture has invalid size %d bytes", len(data))), nil
	}
	if detected := http.DetectContentType(data); detected != "image/png" {
		return NewErrorResult(fmt.Sprintf("desktop capture returned %s instead of image/png", detected)), nil
	}

	path, err := SaveDesktopCapture(t.workDir, data)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("save desktop capture: %v", err)), nil
	}

	result := NewSuccessResult(fmt.Sprintf("Desktop captured to %s (%d bytes).", path, len(data)))
	if t.includeImage {
		result.Content += " The screenshot is attached to this tool result for visual inspection."
		result.MultimodalParts = []*MultimodalPart{{MimeType: "image/png", Data: data}}
	} else {
		result.Content += " GLM is text-only; use an enabled Z.AI Vision MCP image-analysis tool with this path before taking any UI action."
	}
	return result, nil
}

func ensureComputerCaptureDir(workDir string) (string, error) {
	current := workDir
	for _, component := range []string{".gokin", "computer-use"} {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("%s must be a real directory, not a symlink", current)
		}
	}
	return current, nil
}

func writePrivateRegularFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".screen-*.png")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
