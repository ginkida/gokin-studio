package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var tinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

func TestComputerScreenshotReturnsKimiImageAndPrivateFile(t *testing.T) {
	tool := NewComputerScreenshotTool(t.TempDir(), true)
	tool.capture = func(context.Context) ([]byte, error) { return append([]byte(nil), tinyPNG...), nil }

	result, err := tool.Execute(context.Background(), nil)
	if err != nil || !result.Success {
		t.Fatalf("Execute = %#v, %v", result, err)
	}
	if len(result.MultimodalParts) != 1 || result.MultimodalParts[0].MimeType != "image/png" {
		t.Fatalf("multimodal result = %#v", result.MultimodalParts)
	}
	path := filepath.Join(tool.workDir, ".gokin", "computer-use", "latest-screen.png")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("screenshot mode = %o, want 600", info.Mode().Perm())
	}
}

func TestComputerScreenshotGLMReturnsVisionMCPPathWithoutInlineImage(t *testing.T) {
	tool := NewComputerScreenshotTool(t.TempDir(), false)
	tool.capture = func(context.Context) ([]byte, error) { return append([]byte(nil), tinyPNG...), nil }
	result, _ := tool.Execute(context.Background(), map[string]any{})
	if !result.Success || len(result.MultimodalParts) != 0 {
		t.Fatalf("GLM result = %#v", result)
	}
	if !strings.Contains(result.Content, "Vision MCP") || !strings.Contains(result.Content, "latest-screen.png") {
		t.Fatalf("GLM fallback content = %q", result.Content)
	}
}

func TestComputerScreenshotRejectsArgumentsAndSpoofedCapture(t *testing.T) {
	tool := NewComputerScreenshotTool(t.TempDir(), true)
	if err := tool.Validate(map[string]any{"display": 1}); err == nil {
		t.Fatal("accepted unsupported screenshot argument")
	}
	tool.capture = func(context.Context) ([]byte, error) { return []byte("not an image"), nil }
	result, _ := tool.Execute(context.Background(), nil)
	if result.Success || !strings.Contains(result.Error, "instead of image/png") {
		t.Fatalf("spoofed capture result = %#v", result)
	}
}

func TestComputerScreenshotRejectsSymlinkedMetadataDirectory(t *testing.T) {
	workDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workDir, ".gokin")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := NewComputerScreenshotTool(workDir, true)
	tool.capture = func(context.Context) ([]byte, error) { return append([]byte(nil), tinyPNG...), nil }
	result, _ := tool.Execute(context.Background(), nil)
	if result.Success || !strings.Contains(result.Error, "not a symlink") {
		t.Fatalf("symlinked metadata result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(outside, "computer-use", "latest-screen.png")); !os.IsNotExist(err) {
		t.Fatalf("capture escaped through .gokin symlink: %v", err)
	}
}
