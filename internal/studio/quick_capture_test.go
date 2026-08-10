package studio

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func TestCaptureComposerScreenForKimiReturnsAttachment(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Kimi capture")
	if err := s.SetProjectProvider(info.ID, "kimi", "k3"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectComputerUse(info.ID, true); err != nil {
		t.Fatal(err)
	}
	png := testPNGBytes(t)
	s.testDesktopCapture = func(context.Context) ([]byte, error) {
		return append([]byte(nil), png...), nil
	}
	result, err := s.CaptureComposerScreen(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "kimi" || result.Attachment == nil ||
		result.CaptureMode != "desktop" || result.Attachment.MIMEType != "image/png" ||
		!strings.HasPrefix(result.Attachment.Name, "screen-") {
		t.Fatalf("Kimi capture = %#v", result)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Attachment.Data)
	if err != nil || !bytes.Equal(decoded, png) {
		t.Fatalf("Kimi capture bytes changed: %v", err)
	}
}

func TestCaptureComposerSelectionForKimiUsesInteractivePicker(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Kimi selection")
	if err := s.SetProjectProvider(info.ID, "kimi", "k3"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectComputerUse(info.ID, true); err != nil {
		t.Fatal(err)
	}
	png := testPNGBytes(t)
	fullCalled := false
	s.testDesktopCapture = func(context.Context) ([]byte, error) {
		fullCalled = true
		return nil, errors.New("full capture must not run")
	}
	s.testInteractiveCapture = func(context.Context) ([]byte, error) {
		return append([]byte(nil), png...), nil
	}

	result, err := s.CaptureComposerSelection(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullCalled || result.CaptureMode != "selection" || result.Cancelled ||
		result.Attachment == nil || !strings.HasPrefix(result.Attachment.Name, "screen-selection-") {
		t.Fatalf("selection capture = %#v, fullCalled=%v", result, fullCalled)
	}
}

func TestCaptureComposerSelectionCancellationIsNotAnError(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Selection cancel")
	if err := s.SetProjectComputerUse(info.ID, true); err != nil {
		t.Fatal(err)
	}
	s.testInteractiveCapture = func(context.Context) ([]byte, error) {
		return nil, tools.ErrDesktopCaptureCancelled
	}

	result, err := s.CaptureComposerSelection(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Cancelled || result.CaptureMode != "selection" || result.Attachment != nil || result.SavedPath != "" {
		t.Fatalf("cancelled selection = %#v", result)
	}
}

func TestCaptureComposerScreenForGLMSavesPrivateVisionInput(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "GLM capture")
	if err := s.SetProjectComputerUse(info.ID, true); err != nil {
		t.Fatal(err)
	}
	png := testPNGBytes(t)
	s.testDesktopCapture = func(context.Context) ([]byte, error) {
		return append([]byte(nil), png...), nil
	}
	result, err := s.CaptureComposerScreen(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "glm" || result.Attachment != nil ||
		result.SavedPath != ".gokin/computer-use/latest-screen.png" ||
		!strings.Contains(result.Guidance, "Vision MCP") {
		t.Fatalf("GLM capture = %#v", result)
	}
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	fullPath := filepath.Join(project.Directory, filepath.FromSlash(result.SavedPath))
	data, err := os.ReadFile(fullPath)
	if err != nil || !bytes.Equal(data, png) {
		t.Fatalf("saved GLM capture = %d bytes, %v", len(data), err)
	}
	infoOnDisk, err := os.Stat(fullPath)
	if err != nil || infoOnDisk.Mode().Perm() != 0o600 {
		t.Fatalf("capture permissions = %v, %v", infoOnDisk, err)
	}
}

func TestCaptureComposerScreenRequiresScreenToggleAndPNG(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Capture guard")
	s.testDesktopCapture = func(context.Context) ([]byte, error) {
		return []byte("not a PNG"), nil
	}
	if _, err := s.CaptureComposerScreen(info.ID); err == nil || !strings.Contains(err.Error(), "enable Screen") {
		t.Fatalf("disabled capture error = %v", err)
	}
	if err := s.SetProjectComputerUse(info.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CaptureComposerScreen(info.ID); err == nil || !strings.Contains(err.Error(), "instead of image/png") {
		t.Fatalf("invalid capture error = %v", err)
	}
}
