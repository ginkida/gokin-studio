package studio

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A project's delegation profile: how OTHER projects' agents recognise it.
//
// This is the replacement for ask_agent's `target_role` enum
// ("explore"/"bash"/"general"/"plan"), which the studio layer matched against
// project NAMES. Nothing matched, so routing silently fell through to "the
// first other project" and the model had no way to address anyone.
//
// The text is deliberately user-owned and never generated: it rides into
// another project's prompt as attributed context, so the user must be the
// author.

const (
	projectDescriptionMaxBytes = 240
	projectCapabilityMaxBytes  = 40
	projectMaxCapabilities     = 8
)

// SetProjectProfile stores the description and capability hints other agents
// see for this project.
func (s *Studio) SetProjectProfile(id, description string, capabilities []string) error {
	if !utf8.ValidString(description) {
		return fmt.Errorf("description must be valid UTF-8")
	}
	description = truncateUTF8(strings.TrimSpace(collapseProfileWhitespace(description)), projectDescriptionMaxBytes)
	cleaned, err := sanitizeProjectCapabilities(capabilities)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[id]; !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	return s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.Description = description
		pc.Capabilities = cleaned
	}, func(p *Project) {
		p.mu.Lock()
		p.Description = description
		p.Capabilities = cleaned
		p.mu.Unlock()
	})
}

// sanitizeProjectCapabilities normalises the short "good for" hints. They are
// rendered into another project's prompt, so they are bounded, single-line and
// free of control characters.
func sanitizeProjectCapabilities(capabilities []string) ([]string, error) {
	cleaned := make([]string, 0, len(capabilities))
	seen := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		if !utf8.ValidString(capability) {
			return nil, fmt.Errorf("capability must be valid UTF-8")
		}
		capability = strings.ToLower(strings.TrimSpace(collapseProfileWhitespace(capability)))
		capability = truncateUTF8(capability, projectCapabilityMaxBytes)
		if capability == "" || seen[capability] {
			continue
		}
		seen[capability] = true
		cleaned = append(cleaned, capability)
		if len(cleaned) >= projectMaxCapabilities {
			break
		}
	}
	return cleaned, nil
}

// collapseProfileWhitespace flattens newlines and control characters so a
// profile cannot inject structure into the delegation envelope.
func collapseProfileWhitespace(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	lastSpace := false
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
