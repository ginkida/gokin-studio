package studio

import (
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// ModelSwitchWarning describes any lossy/expensive consequence of changing a
// project's model. An empty string means the switch is safe to apply directly.
func (s *Studio) ModelSwitchWarning(projectID, provider, model string) (string, error) {
	if err := s.validateAvailableStudioProviderModel(provider, model); err != nil {
		return "", err
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}

	p.mu.RLock()
	currentProvider, currentModel := p.Provider, p.Model
	sessions := make([]*ChatSession, 0, len(p.sessions))
	for _, session := range p.sessions {
		sessions = append(sessions, session)
	}
	p.mu.RUnlock()
	if currentProvider == provider && currentModel == model {
		return "", nil
	}

	targetWindow := contextWindowForProvider(provider, model)
	compactSessions := 0
	mediaSessions := 0
	for _, session := range sessions {
		session.mu.RLock()
		estimatedChars := 0
		hasMedia := false
		for _, content := range session.history {
			estimatedChars += contentSize(content)
			for _, part := range content.Parts {
				if part != nil && part.InlineData != nil {
					if _, ok := supportedImageMIMEs[normalizeImageMIME(part.InlineData.MIMEType)]; !ok {
						continue
					}
					hasMedia = true
				}
			}
		}
		session.mu.RUnlock()
		if targetWindow > 0 && estimatedChars > targetWindow*3 {
			compactSessions++
		}
		if provider != "kimi" && hasMedia {
			mediaSessions++
		}
	}

	var warnings []string
	if compactSessions > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d chat(s) exceed the target model's safe context budget and will be compacted automatically on their next turn",
			compactSessions,
		))
	}
	if mediaSessions > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d chat(s) contain image attachments; GLM is text-only, so those image blocks will be omitted from model context (they remain visible and saved in chat history)",
			mediaSessions,
		))
	}
	if len(warnings) == 0 && currentModel != model {
		return "Switching models invalidates the provider prompt cache. Starting a new chat gives the best latency and quota usage.", nil
	}
	return strings.Join(warnings, ". ") + ".", nil
}

// historyForProvider removes content blocks unsupported by the target
// provider while retaining a textual marker, so switching from Kimi to GLM
// cannot produce an invalid multimodal request or silently erase the turn.
func historyForProvider(history []*genai.Content, provider string) []*genai.Content {
	out := make([]*genai.Content, 0, len(history))
	for _, content := range history {
		if content == nil {
			continue
		}
		parts := make([]*genai.Part, 0, len(content.Parts))
		removedImage := false
		for _, part := range content.Parts {
			if part != nil && part.InlineData != nil {
				_, isImage := supportedImageMIMEs[normalizeImageMIME(part.InlineData.MIMEType)]
				if provider == "kimi" && isImage {
					parts = append(parts, part)
					continue
				}
				removedImage = removedImage || isImage
				continue
			}
			parts = append(parts, part)
		}
		if removedImage {
			parts = append(parts, genai.NewPartFromText("[Image attachment omitted for this text-only model.]"))
		}
		if len(parts) > 0 {
			out = append(out, &genai.Content{Role: content.Role, Parts: parts})
		}
	}
	return out
}
