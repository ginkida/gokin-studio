//go:build windows

package security

import "fmt"

// applySandbox applies basic process isolation for Windows
func (sc *SandboxedCommand) applySandbox(workDir string) error {
	return fmt.Errorf("workspace filesystem isolation is unavailable on Windows")
}

func DetectWorkspaceIsolation() WorkspaceIsolationStatus {
	return WorkspaceIsolationStatus{
		Available: false,
		Mode:      "host",
		Detail:    "Windows host execution is not filesystem-isolated; every agent command requires explicit approval.",
	}
}
