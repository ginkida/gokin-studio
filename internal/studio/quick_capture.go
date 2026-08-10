package studio

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

type ComposerScreenCapture struct {
	Provider    string             `json:"provider"`
	CaptureMode string             `json:"captureMode"`
	Attachment  *MessageAttachment `json:"attachment,omitempty"`
	SavedPath   string             `json:"savedPath,omitempty"`
	Guidance    string             `json:"guidance,omitempty"`
	Cancelled   bool               `json:"cancelled,omitempty"`
}

// CaptureComposerScreen is an explicit user action from the composer. It
// requires the project's Screen toggle, but does not require a second agent
// approval because the user is clicking the capture control themselves.
func (s *Studio) CaptureComposerScreen(projectID string) (*ComposerScreenCapture, error) {
	return s.captureComposerScreen(projectID, false)
}

// CaptureComposerSelection opens the operating system's interactive region /
// window picker. Like full-screen composer capture, this is driven directly by
// the user and only prepares a reviewable draft; it never sends a model turn.
func (s *Studio) CaptureComposerSelection(projectID string) (*ComposerScreenCapture, error) {
	return s.captureComposerScreen(projectID, true)
}

func (s *Studio) captureComposerScreen(projectID string, interactive bool) (*ComposerScreenCapture, error) {
	s.mu.RLock()
	project := s.projects[projectID]
	s.mu.RUnlock()
	if project == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	project.mu.RLock()
	enabled := project.ComputerUseEnabled
	provider := project.Provider
	model := project.Model
	workDir := project.Directory
	project.mu.RUnlock()
	if !enabled {
		return nil, fmt.Errorf("enable Screen for this project before capturing the desktop")
	}
	if err := validateStudioProviderModelRuntime(provider, model); err != nil {
		return nil, err
	}
	capture := s.testDesktopCapture
	mode := "desktop"
	timeout := 30 * time.Second
	if interactive {
		mode = "selection"
		timeout = 2 * time.Minute
		capture = s.testInteractiveCapture
		if capture == nil {
			capture = tools.CaptureInteractiveDesktopPNG
		}
	} else if capture == nil {
		capture = tools.CaptureDesktopPNG
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	data, err := capture(ctx)
	if err != nil {
		if errors.Is(err, tools.ErrDesktopCaptureCancelled) {
			return &ComposerScreenCapture{
				Provider:    provider,
				CaptureMode: mode,
				Cancelled:   true,
				Guidance:    "Screen capture cancelled. No image was added or sent.",
			}, nil
		}
		return nil, fmt.Errorf("capture %s: %w", mode, err)
	}
	if len(data) == 0 || len(data) > MessageImageAttachmentMaxBytes {
		return nil, fmt.Errorf("desktop capture must be between 1 byte and %d MiB", MessageImageAttachmentMaxBytes>>20)
	}
	if detected := normalizeImageMIME(http.DetectContentType(data)); detected != "image/png" {
		return nil, fmt.Errorf("desktop capture returned %s instead of image/png", detected)
	}
	if provider == "kimi" {
		namePrefix := "screen-"
		if interactive {
			namePrefix = "screen-selection-"
		}
		return &ComposerScreenCapture{
			Provider:    provider,
			CaptureMode: mode,
			Attachment: &MessageAttachment{
				Name:     namePrefix + time.Now().Format("20060102-150405") + ".png",
				MIMEType: "image/png",
				Data:     base64.StdEncoding.EncodeToString(data),
			},
			Guidance: "Screenshot attached locally. Review it before sending to Kimi K3.",
		}, nil
	}
	path, err := tools.SaveDesktopCapture(workDir, data)
	if err != nil {
		return nil, fmt.Errorf("save desktop capture: %w", err)
	}
	relativePath, err := filepath.Rel(workDir, path)
	if err != nil {
		return nil, fmt.Errorf("resolve desktop capture path: %w", err)
	}
	return &ComposerScreenCapture{
		Provider:    provider,
		CaptureMode: mode,
		SavedPath:   filepath.ToSlash(relativePath),
		Guidance:    fmt.Sprintf("%s is text-only. Ask it to inspect this saved screenshot through an enabled Z.AI Vision MCP connector.", model),
	}, nil
}
