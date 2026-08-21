package studio

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Project groups.
//
// A group is a DESCRIPTIVE layer, not an execution engine: no router, no state
// machine. It bundles related projects (a few repos plus capability gateways)
// and carries two text fields aimed at two different audiences:
//
//   - Description  — orchestrator-facing. How to coordinate the group. It is
//     shown in the UI and to a caller listing groups, and is NEVER injected
//     into a member's prompt.
//   - SharedContext — member-facing facts (stack names, IDs, conventions) that
//     hold regardless of which member reads them. Folded into the delegation
//     envelope under the same untrusted-context footer as everything else.
//
// A delegation with no group is byte-identical to one made before groups
// existed, so nothing regresses for users who never create one.

const (
	maxProjectGroups         = 20
	maxProjectGroupMembers   = 20
	maxProjectGroupName      = 80
	maxProjectGroupDescBytes = 1 << 10
	maxGroupSharedContext    = 4 << 10
	maxGroupMemberUseFor     = 120
)

// DelegationPolicy values control who may delegate INTO a project.
const (
	// DelegationPolicyAny is the default and reproduces the reachability that
	// existed before policies, so upgrading changes nothing.
	DelegationPolicyAny = "any"
	// DelegationPolicyGroup restricts callers to projects sharing a group.
	DelegationPolicyGroup = "group"
	// DelegationPolicyOff removes the project as a delegation target.
	DelegationPolicyOff = "off"
)

func normalizeDelegationPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case DelegationPolicyGroup:
		return DelegationPolicyGroup
	case DelegationPolicyOff:
		return DelegationPolicyOff
	default:
		return DelegationPolicyAny
	}
}

// GroupMemberConfig links a project into a group with an orchestrator-facing
// hint about what that member is for.
type GroupMemberConfig struct {
	ProjectID string `yaml:"project_id" json:"projectID"`
	UseFor    string `yaml:"use_for,omitempty" json:"useFor,omitempty"`
}

// ProjectGroupConfig is persisted inside StudioConfig, so it inherits the
// existing atomic 0600 save and needs no new file or lock.
type ProjectGroupConfig struct {
	ID            string              `yaml:"id" json:"id"`
	Name          string              `yaml:"name" json:"name"`
	Description   string              `yaml:"description,omitempty" json:"description,omitempty"`
	SharedContext string              `yaml:"shared_context,omitempty" json:"sharedContext,omitempty"`
	Members       []GroupMemberConfig `yaml:"members,omitempty" json:"members,omitempty"`
}

// ProjectGroupInfo is the frontend-facing view. Members whose project has been
// deleted are flagged rather than dropped, so the user can see and fix it.
type ProjectGroupInfo struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	SharedContext string            `json:"sharedContext,omitempty"`
	Members       []GroupMemberInfo `json:"members"`
}

type GroupMemberInfo struct {
	ProjectID string `json:"projectID"`
	Name      string `json:"name,omitempty"`
	UseFor    string `json:"useFor,omitempty"`
	Unknown   bool   `json:"unknown,omitempty"`
}

// sanitizeProjectGroups bounds everything that can reach a prompt or the
// config file. Unknown members are kept: deleting a project must not make the
// config unparseable, and the user should be able to see the dangling row.
func sanitizeProjectGroups(groups []ProjectGroupConfig) []ProjectGroupConfig {
	if len(groups) > maxProjectGroups {
		groups = groups[:maxProjectGroups]
	}
	cleaned := make([]ProjectGroupConfig, 0, len(groups))
	seenID := make(map[string]bool, len(groups))
	for _, group := range groups {
		group.ID = strings.TrimSpace(group.ID)
		if group.ID == "" || len(group.ID) > 128 || seenID[group.ID] {
			continue
		}
		seenID[group.ID] = true
		group.Name = truncateUTF8(collapseProfileWhitespace(group.Name), maxProjectGroupName)
		if group.Name == "" {
			continue
		}
		group.Description = truncateUTF8(strings.TrimSpace(group.Description), maxProjectGroupDescBytes)
		group.SharedContext = truncateUTF8(strings.TrimSpace(group.SharedContext), maxGroupSharedContext)

		members := make([]GroupMemberConfig, 0, len(group.Members))
		seenMember := make(map[string]bool, len(group.Members))
		for _, member := range group.Members {
			member.ProjectID = strings.TrimSpace(member.ProjectID)
			if member.ProjectID == "" || seenMember[member.ProjectID] {
				continue
			}
			seenMember[member.ProjectID] = true
			member.UseFor = truncateUTF8(collapseProfileWhitespace(member.UseFor), maxGroupMemberUseFor)
			members = append(members, member)
			if len(members) >= maxProjectGroupMembers {
				break
			}
		}
		group.Members = members
		cleaned = append(cleaned, group)
	}
	return cleaned
}

// ListProjectGroups returns every group with member names resolved.
func (s *Studio) ListProjectGroups() []ProjectGroupInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProjectGroupInfo, 0, len(s.config.Groups))
	for _, group := range s.config.Groups {
		info := ProjectGroupInfo{
			ID: group.ID, Name: group.Name,
			Description: group.Description, SharedContext: group.SharedContext,
			Members: make([]GroupMemberInfo, 0, len(group.Members)),
		}
		for _, member := range group.Members {
			entry := GroupMemberInfo{ProjectID: member.ProjectID, UseFor: member.UseFor}
			if project := s.projects[member.ProjectID]; project != nil {
				project.mu.RLock()
				entry.Name = project.Name
				project.mu.RUnlock()
			} else {
				entry.Unknown = true
			}
			info.Members = append(info.Members, entry)
		}
		out = append(out, info)
	}
	return out
}

// SaveProjectGroup creates or updates a group. Groups are user-only: no tool
// may create, edit, join or leave one, because a group auto-injects text into
// a member's prompt and widens the set of projects a caller can reach.
func (s *Studio) SaveProjectGroup(group ProjectGroupConfig) (ProjectGroupConfig, error) {
	if !utf8.ValidString(group.Name) || !utf8.ValidString(group.Description) || !utf8.ValidString(group.SharedContext) {
		return ProjectGroupConfig{}, fmt.Errorf("group text must be valid UTF-8")
	}
	if strings.TrimSpace(group.Name) == "" {
		return ProjectGroupConfig{}, fmt.Errorf("group name is required")
	}
	if len(group.Members) > maxProjectGroupMembers {
		return ProjectGroupConfig{}, fmt.Errorf("a group holds at most %d projects", maxProjectGroupMembers)
	}
	if group.ID == "" {
		group.ID = uuid.NewString()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	groups := append([]ProjectGroupConfig(nil), s.config.Groups...)
	replaced := false
	for i := range groups {
		if groups[i].ID == group.ID {
			groups[i] = group
			replaced = true
			break
		}
	}
	if !replaced {
		if len(groups) >= maxProjectGroups {
			return ProjectGroupConfig{}, fmt.Errorf("at most %d project groups are supported", maxProjectGroups)
		}
		groups = append(groups, group)
	}
	s.config.Groups = sanitizeProjectGroups(groups)
	s.saveConfig()
	for _, saved := range s.config.Groups {
		if saved.ID == group.ID {
			return saved, nil
		}
	}
	return ProjectGroupConfig{}, fmt.Errorf("group was rejected during validation")
}

// DeleteProjectGroup removes a group. Member projects are untouched.
func (s *Studio) DeleteProjectGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := make([]ProjectGroupConfig, 0, len(s.config.Groups))
	found := false
	for _, group := range s.config.Groups {
		if group.ID == id {
			found = true
			continue
		}
		groups = append(groups, group)
	}
	if !found {
		return fmt.Errorf("project group not found: %s", id)
	}
	s.config.Groups = groups
	s.saveConfig()
	return nil
}

// SetProjectDelegationPolicy controls who may delegate into a project.
func (s *Studio) SetProjectDelegationPolicy(projectID, policy string) error {
	normalized := normalizeDelegationPolicy(policy)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[projectID]; !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	return s.persistProjectMutationLocked(projectID, func(pc *ProjectConfig) {
		pc.DelegationPolicy = normalized
	}, func(p *Project) {
		p.mu.Lock()
		p.DelegationPolicy = normalized
		p.mu.Unlock()
	})
}

// sharedGroupLocked reports the first group both projects belong to. The caller
// must hold s.mu.
func (s *Studio) sharedGroupLocked(fromProjectID, toProjectID string) (ProjectGroupConfig, bool) {
	for _, group := range s.config.Groups {
		fromMember, toMember := false, false
		for _, member := range group.Members {
			if member.ProjectID == fromProjectID {
				fromMember = true
			}
			if member.ProjectID == toProjectID {
				toMember = true
			}
		}
		if fromMember && toMember {
			return group, true
		}
	}
	return ProjectGroupConfig{}, false
}

// delegationPolicyAllowsLocked decides whether fromProjectID may delegate into
// target, and returns the shared group whose facts should ride along. The
// caller must hold s.mu.
func (s *Studio) delegationPolicyAllowsLocked(fromProjectID string, target *Project) (ProjectGroupConfig, error) {
	target.mu.RLock()
	policy := normalizeDelegationPolicy(target.DelegationPolicy)
	targetName := target.Name
	target.mu.RUnlock()

	group, shared := s.sharedGroupLocked(fromProjectID, target.ID)
	switch policy {
	case DelegationPolicyOff:
		return ProjectGroupConfig{}, newDelegationError(DelegationErrorPolicy,
			"project %q does not accept delegated work", targetName)
	case DelegationPolicyGroup:
		if !shared {
			return ProjectGroupConfig{}, newDelegationError(DelegationErrorPolicy,
				"project %q only accepts delegation from projects in the same group", targetName)
		}
	}
	if shared {
		return group, nil
	}
	return ProjectGroupConfig{}, nil
}
