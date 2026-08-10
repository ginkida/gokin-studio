package studio

import (
	"fmt"
	"strings"
)

// SetSessionPermissionMode applies the only session-scoped permission mode:
// Plan. Passing an empty mode returns the chat to its project's durable
// Manual/Accept edits/Auto/Skip default. It cannot change an already-running turn because
// that turn has snapshotted its policy and advertised tool schema.
func (s *Studio) SetSessionPermissionMode(projectID, sessionID, mode string) error {
	_, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return err
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "" && mode != "plan" {
		return fmt.Errorf("invalid session permission mode %q: must be plan or empty", mode)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.active {
		return fmt.Errorf("cannot change permission mode while this chat is running")
	}
	session.permissionMode = mode
	return nil
}
