package studio

import (
	"encoding/base64"
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

const sessionExportVersion = 2

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

// ExportSessionJSON returns a portable JSON dump of the session's full state.
// Text, image, and native-document attachments round-trip; thinking/tool
// turns are stripped.
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

	entries, err := historyEntriesForExport(historySnap)
	if err != nil {
		return "", err
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
	if len(out) > ImportPayloadMaxBytes {
		return "", fmt.Errorf("session export exceeds the %d MiB portable-export limit", ImportPayloadMaxBytes>>20)
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
	if len(env.Entries) > MaxHistoryEntries {
		return nil, fmt.Errorf("session has too many entries (%d, maximum %d)", len(env.Entries), MaxHistoryEntries)
	}
	// The version field is the only schema check; missing or zero version
	// is treated as version 1 (forward-friendly default).

	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	p.metadataMu.Lock()
	defer p.metadataMu.Unlock()

	// Build the in-memory history from the exported entries. We use
	// genai.NewPartFromText for each entry so the structure matches what
	// the agent loop expects on subsequent turns.
	history, err := historyEntriesFromExport(env.Entries)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(env.Name)
	if name == "" {
		name = "Imported chat"
	}
	if !strings.Contains(strings.ToLower(name), "imported") {
		name = name + " (imported)"
	}
	if len(name) > 60 {
		name = truncateUTF8(name, 60)
	}

	// Generate a fresh session ID — never collide with the source's.
	newSID := uuid.New().String()[:8]
	p.mu.RLock()
	for {
		if _, exists := p.sessions[newSID]; !exists {
			break
		}
		newSID = uuid.New().String()[:8]
	}
	p.mu.RUnlock()
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

	// Persist immediately so the imported session survives restart even
	// before the user runs an agent in it. Fork-style: explicit metadata
	// (no parent — see comment above), no usage (fresh start).
	if err := SaveNewHistoryWithMetadata(projectSessionStorageKey(projectID, newSID), name, "", history); err != nil {
		return nil, fmt.Errorf("persist imported session: %w", err)
	}
	p.mu.Lock()
	p.sessions[newSID] = sess
	p.mu.Unlock()

	return sess.Info(), nil
}

func historyEntriesForExport(history []*genai.Content) ([]HistoryEntry, error) {
	entries := make([]HistoryEntry, 0, len(history))
	totalMedia := 0
	for _, content := range history {
		text := ""
		var attachments []PersistedHistoryAttachment
		for _, part := range content.Parts {
			if part == nil || part.Thought {
				continue
			}
			if part.Text != "" {
				text += part.Text
			}
			if part.InlineData != nil && len(part.InlineData.Data) > 0 {
				mimeType := normalizeAttachmentMIME(part.InlineData.MIMEType)
				_, isImage := supportedImageMIMEs[mimeType]
				_, isDocument := supportedDocumentMIMEs[mimeType]
				if !isImage && !isDocument {
					return nil, fmt.Errorf("cannot export unsupported attachment type %q", part.InlineData.MIMEType)
				}
				maxBytes := MessageAttachmentMaxBytes
				if isImage {
					maxBytes = MessageImageAttachmentMaxBytes
				}
				if len(part.InlineData.Data) > maxBytes {
					return nil, fmt.Errorf("cannot export attachment larger than %d MiB", maxBytes>>20)
				}
				totalMedia += len(part.InlineData.Data)
				if totalMedia > MessageAttachmentsTotalMaxBytes {
					return nil, fmt.Errorf("session media exceeds the %d MiB portable-export limit", MessageAttachmentsTotalMaxBytes>>20)
				}
				attachments = append(attachments, PersistedHistoryAttachment{
					Name:     attachmentDisplayName(len(attachments), part.InlineData),
					MIMEType: mimeType,
					Size:     len(part.InlineData.Data),
					Data:     base64.StdEncoding.EncodeToString(part.InlineData.Data),
				})
			}
		}
		if text == "" && len(attachments) == 0 {
			continue
		}
		entries = append(entries, HistoryEntry{Role: content.Role, Text: text, Attachments: attachments})
	}
	return entries, nil
}

func historyEntriesFromExport(entries []HistoryEntry) ([]*genai.Content, error) {
	history := make([]*genai.Content, 0, len(entries))
	totalMedia := 0
	for i, entry := range entries {
		role := entry.Role
		if role == "assistant" {
			role = "model"
		}
		if role != "user" && role != "model" {
			return nil, fmt.Errorf("invalid session entry role %q", entry.Role)
		}
		parts := make([]*genai.Part, 0, 1+len(entry.Attachments))
		if entry.Text != "" {
			parts = append(parts, genai.NewPartFromText(entry.Text))
		}
		for j, attachment := range entry.Attachments {
			if attachment.Data == "" || attachment.Blob != "" {
				return nil, fmt.Errorf("invalid portable attachment at entry %d attachment %d", i, j)
			}
			decoded, err := decodeMessageAttachments("kimi", "k3", []MessageAttachment{{
				Name:     attachment.Name,
				MIMEType: attachment.MIMEType,
				Data:     attachment.Data,
			}})
			if err != nil {
				return nil, fmt.Errorf("invalid portable attachment at entry %d attachment %d: %w", i, j, err)
			}
			blobPart := decoded[len(decoded)-1]
			if blobPart.InlineData == nil || attachment.Size != len(blobPart.InlineData.Data) {
				return nil, fmt.Errorf("portable attachment size mismatch at entry %d attachment %d", i, j)
			}
			totalMedia += attachment.Size
			if totalMedia > MessageAttachmentsTotalMaxBytes {
				return nil, fmt.Errorf("session media exceeds the %d MiB portable-import limit", MessageAttachmentsTotalMaxBytes>>20)
			}
			// Normal exports already contain the bounded extraction context.
			// Reconstruct it for older or externally-authored portable sessions
			// so a validated document never becomes a silent, unusable blob.
			if _, isDocument := supportedDocumentMIMEs[normalizeAttachmentMIME(attachment.MIMEType)]; isDocument &&
				!strings.Contains(entry.Text, "<<<GOKIN_DOCUMENT_CONTEXT:") {
				parts = append(parts, decoded[:len(decoded)-1]...)
			}
			parts = append(parts, blobPart)
		}
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty session entry at index %d", i)
		}
		history = append(history, &genai.Content{Role: role, Parts: parts})
	}
	return history, nil
}
