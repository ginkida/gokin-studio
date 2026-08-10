package studio

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

const maxProjectToolPermissions = 32

// ToolPermissionRule is deliberately argument-free. Persisting arbitrary
// command lines, environment values, connector payloads, or file contents in
// config would turn a convenience feature into a secret-retention surface.
type ToolPermissionRule struct {
	Tool      string `yaml:"tool" json:"tool"`
	CreatedAt int64  `yaml:"created_at,omitempty" json:"createdAt,omitempty"`
}

type ProjectToolPermissionInfo struct {
	Tool        string `json:"tool"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	CreatedAt   int64  `json:"createdAt,omitempty"`
}

// Only root-confined local mutations with well-understood semantics can be
// remembered. Shell/process, connector, delegation, SSH, screen, browser,
// scheduling, deletion, and unknown tools remain outside this list.
var persistableProjectTools = map[string]string{
	"write":           "Create project files",
	"atomicwrite":     "Write project files atomically",
	"edit":            "Edit project files",
	"move":            "Move project files",
	"copy":            "Copy project files",
	"mkdir":           "Create project directories",
	"refactor":        "Apply bounded project refactors",
	"document_create": "Create project documents",
	"git_add":         "Stage project changes",
	"git_commit":      "Create local Git commits",
	"git_branch":      "Create or switch local Git branches",
}

func normalizeToolPermissionName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sanitizeToolPermissionRules(values []ToolPermissionRule) []ToolPermissionRule {
	out := make([]ToolPermissionRule, 0, min(len(values), maxProjectToolPermissions))
	seen := make(map[string]bool)
	for _, value := range values {
		toolName := normalizeToolPermissionName(value.Tool)
		if _, ok := persistableProjectTools[toolName]; !ok || seen[toolName] {
			continue
		}
		createdAt := value.CreatedAt
		if createdAt < 0 || createdAt > time.Now().Add(24*time.Hour).UnixMilli() {
			createdAt = 0
		}
		seen[toolName] = true
		out = append(out, ToolPermissionRule{Tool: toolName, CreatedAt: createdAt})
		if len(out) == maxProjectToolPermissions {
			break
		}
	}
	return out
}

func persistentToolPermissionEligible(toolName string, args map[string]any) bool {
	toolName = normalizeToolPermissionName(toolName)
	if _, ok := persistableProjectTools[toolName]; !ok {
		return false
	}
	// Re-run the call-level classifiers before every match. A grant for
	// document_create must not cover replace=true, and a git_branch grant must
	// not cover delete, even though the stored rule has the same tool name.
	return tools.RequiresUserApproval(toolName, args) && !hardGatedTool(toolName, args)
}

func (p *Project) hasPersistentToolPermission(toolName string, args map[string]any) bool {
	toolName = normalizeToolPermissionName(toolName)
	if !persistentToolPermissionEligible(toolName, args) {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, rule := range p.ToolPermissions {
		if rule.Tool == toolName {
			return true
		}
	}
	return false
}

func (s *Studio) ListProjectToolPermissions(projectID string) ([]ProjectToolPermissionInfo, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	rules := append([]ToolPermissionRule(nil), p.ToolPermissions...)
	p.mu.RUnlock()
	rules = sanitizeToolPermissionRules(rules)
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].CreatedAt == rules[j].CreatedAt {
			return rules[i].Tool < rules[j].Tool
		}
		return rules[i].CreatedAt > rules[j].CreatedAt
	})
	out := make([]ProjectToolPermissionInfo, 0, len(rules))
	for _, rule := range rules {
		out = append(out, ProjectToolPermissionInfo{
			Tool:        rule.Tool,
			Scope:       "This project",
			Description: persistableProjectTools[rule.Tool],
			CreatedAt:   rule.CreatedAt,
		})
	}
	return out, nil
}

func (s *Studio) grantProjectToolPermission(projectID, toolName string, args map[string]any) error {
	toolName = normalizeToolPermissionName(toolName)
	if !persistentToolPermissionEligible(toolName, args) {
		return fmt.Errorf("%s cannot receive a persistent project permission", toolName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[projectID]
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	rules := sanitizeToolPermissionRules(p.ToolPermissions)
	p.mu.RUnlock()
	for _, rule := range rules {
		if rule.Tool == toolName {
			return nil
		}
	}
	if len(rules) >= maxProjectToolPermissions {
		return fmt.Errorf("project may remember at most %d tool permissions", maxProjectToolPermissions)
	}
	rules = append(rules, ToolPermissionRule{Tool: toolName, CreatedAt: time.Now().UnixMilli()})
	return s.persistProjectMutationLocked(projectID, func(pc *ProjectConfig) {
		pc.ToolPermissions = append([]ToolPermissionRule(nil), rules...)
	}, func(p *Project) {
		p.mu.Lock()
		p.ToolPermissions = append([]ToolPermissionRule(nil), rules...)
		p.mu.Unlock()
	})
}

// RevokeProjectToolPermission is intentionally the only public mutation RPC.
// Grants can be created only by answering the backend-owned approval card.
func (s *Studio) RevokeProjectToolPermission(projectID, toolName string) error {
	toolName = normalizeToolPermissionName(toolName)
	if _, ok := persistableProjectTools[toolName]; !ok {
		return fmt.Errorf("invalid project tool permission")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[projectID]
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	rules := sanitizeToolPermissionRules(p.ToolPermissions)
	p.mu.RUnlock()
	next := make([]ToolPermissionRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Tool != toolName {
			next = append(next, rule)
		}
	}
	return s.persistProjectMutationLocked(projectID, func(pc *ProjectConfig) {
		pc.ToolPermissions = append([]ToolPermissionRule(nil), next...)
	}, func(p *Project) {
		p.mu.Lock()
		p.ToolPermissions = append([]ToolPermissionRule(nil), next...)
		p.mu.Unlock()
	})
}
