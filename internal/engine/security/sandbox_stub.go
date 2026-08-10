//go:build !linux && !darwin && unix

package security

import (
	"fmt"
)

// applySandbox refuses to mislabel process-group isolation as a filesystem
// sandbox on Unix platforms without a supported containment backend.
func (sc *SandboxedCommand) applySandbox(workDir string) error {
	return fmt.Errorf("workspace filesystem isolation is unavailable on this Unix platform")
}

func DetectWorkspaceIsolation() WorkspaceIsolationStatus {
	return WorkspaceIsolationStatus{
		Available: false,
		Mode:      "host",
		Detail:    "No supported workspace filesystem sandbox is available; commands require explicit host-execution approval.",
	}
}
