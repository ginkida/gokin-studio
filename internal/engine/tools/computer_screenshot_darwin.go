//go:build darwin

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func captureDesktopPNG(ctx context.Context) ([]byte, error) {
	return runMacOSScreenCapture(ctx, false)
}

func captureInteractiveDesktopPNG(ctx context.Context) ([]byte, error) {
	return runMacOSScreenCapture(ctx, true)
}

func runMacOSScreenCapture(ctx context.Context, interactive bool) ([]byte, error) {
	binary, err := exec.LookPath("screencapture")
	if err != nil {
		return nil, fmt.Errorf("macOS screencapture utility not found")
	}
	f, err := os.CreateTemp("", "gokin-screen-*.png")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return nil, err
	}
	defer os.Remove(path)
	args := []string{"-x", "-t", "png"}
	if interactive {
		// The native picker selects a region; pressing Space switches it to
		// window selection. Escape cancels without producing an image.
		args = append(args, "-i")
	}
	args = append(args, path)
	if output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if interactive && len(output) == 0 {
			return nil, ErrDesktopCaptureCancelled
		}
		return nil, fmt.Errorf("%w: %s", err, string(output))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if interactive && len(data) == 0 {
		return nil, ErrDesktopCaptureCancelled
	}
	return data, nil
}
