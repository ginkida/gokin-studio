package studio

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
)

// summarizePrompt is the user-facing instruction sent alongside the
// session history. Kept short so it doesn't dominate small conversations.
// Asks for a structured response so the frontend modal can render it
// reliably (3-5 bullets is short enough to fit in a popup).
const summarizePrompt = `Summarize the conversation above in 3-5 concise bullet points. Cover:
1. What the user is working on / asked about.
2. Key decisions or solutions reached.
3. Outstanding questions or next steps, if any.

Be terse. No preamble. Plain markdown bullets.`

// summarizeTimeout caps the LLM call so a hung provider can't block the
// summarize-modal forever. 60 s is generous — typical summarisation
// takes 5-15 s with reasoning models, but slow networks + thinking budgets
// can push past 30 s.
const summarizeTimeout = 60 * time.Second

// SummarizeSession requests a TL;DR of the session's conversation from the
// LLM and returns it as plain markdown. Useful for long sessions where
// scrolling through the full history is slow, or for handoff between
// engineers / contexts. The summary is NOT inserted into the session
// history — it's returned to the caller for display only. Frontend can
// optionally pin the result as context via the existing pin_context tool.
//
// Cost notes: this is an LLM call, so it consumes tokens proportional to
// the history size. The frontend should warn the user before invoking,
// especially on long sessions.
//
// Returns an error when:
//   - project / session not found
//   - the session has no visible history (nothing to summarise)
//   - the LLM call fails (network, auth, etc.)
func (s *Studio) SummarizeSession(projectID, sessionID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("projectID required")
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}

	s.mu.RLock()
	p, ok := s.projects[projectID]
	settings := s.config.Settings
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	session, exists := p.sessions[sid]
	p.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("session not found: %s", sid)
	}

	// Snapshot history under session lock so a concurrent SendMessage
	// can't mutate the slice mid-summarisation. We strip thought parts
	// (model deliberation isn't useful for summarisation) and snapshot
	// only turns with visible text — function-call turns are noise here.
	session.mu.RLock()
	history := make([]*genai.Content, 0, len(session.history))
	for _, c := range session.history {
		if c == nil {
			continue
		}
		dup := &genai.Content{Role: c.Role}
		hasText := false
		for _, p := range c.Parts {
			if p == nil || p.Thought {
				continue
			}
			if p.Text != "" {
				hasText = true
				cp := *p
				dup.Parts = append(dup.Parts, &cp)
			}
		}
		if hasText {
			history = append(history, dup)
		}
	}
	session.mu.RUnlock()

	if len(history) == 0 {
		return "", fmt.Errorf("no visible history to summarise — send a message first")
	}

	// Ensure the project's client is initialised (it might not be if no
	// turn has been sent yet in this session — though if history is non-
	// empty, a previous session must have existed). Use the same lazy
	// initClient path as SendMessage.
	if err := p.initClient(settings); err != nil {
		return "", fmt.Errorf("init client: %w", err)
	}
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()
	if c == nil {
		return "", fmt.Errorf("LLM client not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), summarizeTimeout)
	defer cancel()

	stream, err := c.SendMessageWithHistory(ctx, history, summarizePrompt)
	if err != nil {
		return "", fmt.Errorf("summarize call failed: %s", humanizeAPIError(err))
	}
	resp, err := stream.Collect()
	if err != nil {
		// Even on partial-collect errors we may have some text — return
		// it with the error so the user can see what got through.
		if resp != nil && resp.Text != "" {
			return resp.Text, fmt.Errorf("summary partial (truncated): %s", humanizeAPIError(err))
		}
		return "", fmt.Errorf("summary failed: %s", humanizeAPIError(err))
	}
	out := strings.TrimSpace(resp.Text)
	if out == "" {
		return "", fmt.Errorf("model returned empty summary")
	}
	return out, nil
}
