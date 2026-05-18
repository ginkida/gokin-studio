package studio

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genai"
)

// session_export.go (iter 550+) -- export and import a chat session as JSON.
// Useful for backup, sharing a session with a teammate, or migrating to
// another machine. The format is intentionally similar to the on-disk
// historyFile (so we can read either) but adds a version + exportedAt
// envelope to make future format changes detectable.

const sessionExportVersion = 1

// SessionExportEnvelope is the canonical JSON shape produced by
// ExportSessionJSON and consumed by ImportSessionJSON.
type SessionExportEnvelope struct {
	Version    int            `json:"version"`
	ExportedAt int64          `json:"exportedAt"` // unix millis
	Name       string         `json:"name"`
	ParentID   string         `json:"parentSessionID,omitempty"`
	Usage      *SessionUsage  `json:"usage,omitempty"`
	Entries    []HistoryEntry `json:"entries"`
}

// ExportSessionJSON returns a JSON dump of the session's full state.
// The format is stable across restarts and can be re-imported via
// ImportSessionJSON. Thinking/tool turns are stripped (they're not in
// the persisted format anyway).
func (s *Studio) ExportSessionJSON(projectID, sessionID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("projectID cannot be empty")
	}
	if sessionID == "" {
		sessionID = "default"
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	sess, exists := p.sessions[sessionID]
	p.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}

	// Snapshot under read lock then release before marshalling.
	sess.mu.RLock()
	name := sess.Name
	parent := sess.ParentID
	var usage *SessionUsage
	if sess.usage != nil {
		u := *sess.usage
		usage = &u
	}
	historySnap := make([]*genai.Content, len(sess.history))
	copy(historySnap, sess.history)
	sess.mu.RUnlock()

	// Convert to text-only entries, matching the on-disk save format.
	entries := make([]HistoryEntry, 0, len(historySnap))
	for _, c := range historySnap {
		text := ""
		for _, p := range c.Parts {
			if p.Thought {
				continue
			}
			if p.Text != "" {
				text += p.Text
			}
		}
		if text == "" {
			continue
		}
		entries = append(entries, HistoryEntry{
			Role: c.Role,
			Text: text,
		})
	}

	env := SessionExportEnvelope{
		Version:    sessionExportVersion,
		ExportedAt: time.Now().UnixMilli(),
		Name:       name,
		ParentID:   parent,
		Usage:      usage,
		Entries:    entries,
	}
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal session export: %w", err)
	}
	return string(out), nil
}

// ImportSessionJSON creates a NEW session in the target project from an
// exported JSON blob. The new session gets a fresh ID (no collision with
// any existing one) and an "(imported)" suffix in its name (so the user
// can tell it apart from a session of the same name they may already have).
//
// The parent session ID from the export is intentionally NOT preserved —
// the source session may not exist in the target project, so the lineage
// link would be broken. Frontend can show "(imported)" as the lineage hint.
func (s *Studio) ImportSessionJSON(projectID, jsonBlob string) (*ChatSessionInfo, error) {
	if projectID == "" {
		return nil, fmt.Errorf("projectID cannot be empty")
	}
	jsonBlob = strings.TrimSpace(jsonBlob)
	if jsonBlob == "" {
		return nil, fmt.Errorf("import payload cannot be empty")
	}
	if len(jsonBlob) > ImportPayloadMaxBytes {
		return nil, fmt.Errorf("import payload exceeds %d bytes", ImportPayloadMaxBytes)
	}
	var env SessionExportEnvelope
	if err := json.Unmarshal([]byte(jsonBlob), &env); err != nil {
		return nil, fmt.Errorf("invalid session JSON: %w", err)
	}
	if env.Version > sessionExportVersion {
		return nil, fmt.Errorf("session export version %d is newer than this build supports (max %d)", env.Version, sessionExportVersion)
	}
	// The version field is the only schema check; missing or zero version
	// is treated as version 1 (forward-friendly default).

	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	// Build the in-memory history from the exported entries. We use
	// genai.NewPartFromText for each entry so the structure matches what
	// the agent loop expects on subsequent turns.
	history := make([]*genai.Content, 0, len(env.Entries))
	for _, e := range env.Entries {
		if e.Role == "" || e.Text == "" {
			continue
		}
		history = append(history, &genai.Content{
			Role:  e.Role,
			Parts: []*genai.Part{genai.NewPartFromText(e.Text)},
		})
	}

	name := strings.TrimSpace(env.Name)
	if name == "" {
		name = "Imported chat"
	}
	if !strings.Contains(strings.ToLower(name), "imported") {
		name = name + " (imported)"
	}
	if len(name) > 60 {
		name = name[:60]
	}

	// Generate a fresh session ID — never collide with the source's.
	newSID := uuid.New().String()[:8]
	now := time.Now()

	sess := &ChatSession{
		ID:        newSID,
		Name:      name,
		CreatedAt: now,
		history:   history,
	}
	// Bump lastUsedAt so it lands at the top of the recent-first sidebar
	// — matches user expectation that an imported session is the
	// most-recently-touched thing.
	sess.lastUsedAt = now.UnixMilli()

	p.mu.Lock()
	p.sessions[newSID] = sess
	p.mu.Unlock()

	// Persist immediately so the imported session survives restart even
	// before the user runs an agent in it. Fork-style: explicit metadata
	// (no parent — see comment above), no usage (fresh start).
	if err := SaveHistoryWithMetadata(projectID+"_"+newSID, name, "", history); err != nil {
		return nil, fmt.Errorf("persist imported session: %w", err)
	}

	return sess.Info(), nil
}
