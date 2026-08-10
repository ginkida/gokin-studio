//go:build !darwin && !windows

package tools

import (
	"context"
	"fmt"
)

func foregroundApplication(context.Context) (ComputerApplication, error) {
	return ComputerApplication{}, fmt.Errorf("computer use is currently supported on macOS and Windows")
}
