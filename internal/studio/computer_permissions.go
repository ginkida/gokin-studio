package studio

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

const maxComputerAppPermissions = 64

type ComputerAppPermissions struct {
	Allowed []string `json:"allowed"`
	Blocked []string `json:"blocked"`
}

func sanitizeComputerAppIDs(values []string) []string {
	out := make([]string, 0, min(len(values), maxComputerAppPermissions))
	seen := make(map[string]bool)
	for _, value := range values {
		id := tools.NormalizeComputerAppID(value)
		if !validComputerAppID(id) || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) == maxComputerAppPermissions {
			break
		}
	}
	return out
}

func validComputerAppID(id string) bool {
	return id != "" &&
		len(id) <= tools.ComputerAppIDMaxBytes &&
		utf8.ValidString(id) &&
		strings.IndexFunc(id, unicode.IsControl) < 0
}

func containsComputerApp(values []string, id string) bool {
	for _, value := range values {
		if value == id {
			return true
		}
	}
	return false
}

func removeComputerApp(values []string, id string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != id {
			out = append(out, value)
		}
	}
	return out
}

func (s *Studio) ListProjectComputerPermissions(projectID string) (*ComputerAppPermissions, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return &ComputerAppPermissions{
		Allowed: append([]string(nil), p.ComputerAllowedApps...),
		Blocked: append([]string(nil), p.ComputerBlockedApps...),
	}, nil
}

// SetProjectComputerAppPermission stores an OS-observed application identity.
// mode is "allow", "block", or "remove"; block always wins over allow.
func (s *Studio) SetProjectComputerAppPermission(projectID, appID, mode string) error {
	appID = tools.NormalizeComputerAppID(appID)
	if !validComputerAppID(appID) {
		return fmt.Errorf("invalid computer application identity")
	}
	if mode != "allow" && mode != "block" && mode != "remove" {
		return fmt.Errorf("invalid computer application permission mode %q", mode)
	}
	if mode == "allow" && tools.IsSensitiveComputerApplication(tools.ComputerApplication{ID: appID}) {
		return fmt.Errorf("sensitive credential or wallet applications cannot be allowed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[projectID]
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	allowed, blocked := sanitizeComputerAppIDs(p.ComputerAllowedApps), sanitizeComputerAppIDs(p.ComputerBlockedApps)
	p.mu.RUnlock()
	if mode == "allow" && !containsComputerApp(allowed, appID) && len(allowed) >= maxComputerAppPermissions {
		return fmt.Errorf("computer application allowlist may contain at most %d entries", maxComputerAppPermissions)
	}
	if mode == "block" && !containsComputerApp(blocked, appID) && len(blocked) >= maxComputerAppPermissions {
		return fmt.Errorf("computer application blocklist may contain at most %d entries", maxComputerAppPermissions)
	}
	return s.persistProjectMutationLocked(projectID, func(pc *ProjectConfig) {
		pc.ComputerAllowedApps, pc.ComputerBlockedApps = mutateComputerPermissions(
			pc.ComputerAllowedApps, pc.ComputerBlockedApps, appID, mode,
		)
	}, func(p *Project) {
		p.mu.Lock()
		p.ComputerAllowedApps, p.ComputerBlockedApps = mutateComputerPermissions(
			p.ComputerAllowedApps, p.ComputerBlockedApps, appID, mode,
		)
		p.mu.Unlock()
	})
}

func mutateComputerPermissions(allowed, blocked []string, appID, mode string) ([]string, []string) {
	allowed = removeComputerApp(sanitizeComputerAppIDs(allowed), appID)
	blocked = removeComputerApp(sanitizeComputerAppIDs(blocked), appID)
	switch mode {
	case "allow":
		if len(allowed) < maxComputerAppPermissions {
			allowed = append(allowed, appID)
		}
	case "block":
		if len(blocked) < maxComputerAppPermissions {
			blocked = append(blocked, appID)
		}
	}
	return allowed, blocked
}
