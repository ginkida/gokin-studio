//go:build !darwin && !windows

package tools

import (
	"context"
	"fmt"
)

func performComputerClick(context.Context, int, int, string) error {
	return fmt.Errorf("computer use is currently supported on macOS and Windows")
}
func performComputerType(context.Context, string) error {
	return fmt.Errorf("computer use is currently supported on macOS and Windows")
}
func performComputerKey(context.Context, string) error {
	return fmt.Errorf("computer use is currently supported on macOS and Windows")
}
