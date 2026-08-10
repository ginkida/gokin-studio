//go:build !darwin && !windows

package tools

import (
	"context"
	"fmt"
)

func captureDesktopPNG(context.Context) ([]byte, error) {
	return nil, fmt.Errorf("computer use is currently supported on macOS and Windows")
}

func captureInteractiveDesktopPNG(context.Context) ([]byte, error) {
	return nil, fmt.Errorf("interactive screen capture is currently supported on macOS and Windows")
}
