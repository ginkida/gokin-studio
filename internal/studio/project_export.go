package studio

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/genai"
)

// project_export.go (iter 560+) -- export and import an entire project as
// a JSON envelope. Extends iter 550+ session export to the project level
// for full backup, sharing, or migration. The format bundles project
// metadata + every session; import creates a fresh project (the user
// must pick a directory for it).

const projectExportVersion = 1

// ImportPayloadMaxBytes caps both ExportSessionJSON and ExportProjectJSON
// payloads on the import path so a malformed/giant blob can't OOM the
// process. 5 MB is generous — a project with 100 active sessions
// totalling many thousands of turns fits comfortably below this.
const ImportPayloadMaxBytes = 5 * 1024 * 1024

// ProjectExportEnvelope is the canonical JSON shape for a full project
// export. The project Directory is intentionally NOT included — paths
// don't transfer between machines, so the importer picks a fresh one.
type ProjectExportEnvelope struct {
	Version      int                       `json:"version"`
	ExportedAt   int64                     `json:"exportedAt"`
	Name         string                    `json:"name"`
	SystemPrompt string                    `json:"systemPrompt,omitempty"`
	Provider     string                    `json:"provider,omitempty"`
	Model        string                    `json:"model,omitempty"`
	Temperature  float32                   `json:"temperature,omitempty"`
	MaxTokens    int                       `json:"maxTokens,omitempty"`
	BudgetUSD    float64                   `json:"budgetUSD,omitempty"`
	Sessions     []SessionExportEnvelope   `json:"sessions"`
}

// ExportProjectJSON snapshots the project + every session into one JSON
// envelope. Each session is rendered via the same logic as the per-
// session export (text-only entries, thinking parts stripped).
func (s *Studio) ExportProjectJSON(projectID string) (string, error) {
	if projectID == "" {
		return "", fmt.Errorf("projectID cannot be empty")
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}

	p.mu.RLock()
	name := p.Name
	systemPrompt := p.SystemPrompt
	provider := p.Provider
	model := p.Model
	temperature := p.Temperature
	maxTokens := p.MaxTokens
	budget := p.BudgetUSD
	// Snapshot session pointers so we can release the project lock
	// before walking each session (each session has its own RWMutex).
	sessRefs := make([]*ChatSession, 0, len(p.sessions))
	for _, sess := range p.sessions {
		sessRefs = append(sessRefs, sess)
	}
	p.mu.RUnlock()

	sessions := make([]SessionExportEnvelope, 0, len(sessRefs))
	for _, sess := range sessRefs {
		sess.mu.RLock()
		sName := sess.Name
		parent := sess.ParentID
		var usage *SessionUsage
		if sess.usage != nil {
			u := *sess.usage
			usage = &u
		}
		histSnap := make([]*genai.Content, len(sess.history))
		copy(histSnap, sess.history)
		sess.mu.RUnlock()

		entries := make([]HistoryEntry, 0, len(histSnap))
		for _, c := range histSnap {
			text := ""
			for _, part := range c.Parts {
				if part.Thought {
					continue
				}
				if part.Text != "" {
					text += part.Text
				}
			}
			if text == "" {
				continue
			}
			entries = append(entries, HistoryEntry{Role: c.Role, Text: text})
		}
		sessions = append(sessions, SessionExportEnvelope{
			Version:    sessionExportVersion,
			ExportedAt: time.Now().UnixMilli(),
			Name:       sName,
			ParentID:   parent,
			Usage:      usage,
			Entries:    entries,
		})
	}

	env := ProjectExportEnvelope{
		Version:      projectExportVersion,
		ExportedAt:   time.Now().UnixMilli(),
		Name:         name,
		SystemPrompt: systemPrompt,
		Provider:     provider,
		Model:        model,
		Temperature:  temperature,
		MaxTokens:    maxTokens,
		BudgetUSD:    budget,
		Sessions:     sessions,
	}
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal project export: %w", err)
	}
	return string(out), nil
}

// ImportProjectJSON creates a NEW project from an exported envelope. The
// caller must provide the target directory (paths don't transfer between
// machines). Each session in the envelope is restored as a fresh session
// in the new project. Lineage between sessions is dropped because the
// session IDs were regenerated and the lineage map can't be rewritten
// reliably from the export alone.
func (s *Studio) ImportProjectJSON(jsonBlob, directory string) (*ProjectInfo, error) {
	jsonBlob = strings.TrimSpace(jsonBlob)
	if jsonBlob == "" {
		return nil, fmt.Errorf("import payload cannot be empty")
	}
	if len(jsonBlob) > ImportPayloadMaxBytes {
		return nil, fmt.Errorf("import payload exceeds %d bytes", ImportPayloadMaxBytes)
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("target directory cannot be empty")
	}
	var env ProjectExportEnvelope
	if err := json.Unmarshal([]byte(jsonBlob), &env); err != nil {
		return nil, fmt.Errorf("invalid project JSON: %w", err)
	}
	if env.Version > projectExportVersion {
		return nil, fmt.Errorf("project export version %d is newer than this build supports (max %d)", env.Version, projectExportVersion)
	}

	// Resolve directory now so AddProject's checks (exists, is-dir, no
	// duplicate) run with absolute path. Reuse AddProject's logic to keep
	// validation in one place.
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("invalid directory: %w", err)
	}

	// Build a project name with "(imported)" suffix matching iter 550+
	// session import semantics. Cap at 60 chars per AddProject's contract.
	name := strings.TrimSpace(env.Name)
	if name == "" {
		name = "Imported project"
	}
	if !strings.Contains(strings.ToLower(name), "imported") {
		name = name + " (imported)"
	}
	if len(name) > 60 {
		name = name[:60]
	}

	// Reuse AddProject for the heavy lifting (id gen, default-session
	// creation, persistence). It handles duplicate-directory rejection
	// for us — important so the importer can't silently overwrite an
	// existing project at the same path.
	info, err := s.AddProject(name, abs)
	if err != nil {
		return nil, fmt.Errorf("create imported project: %w", err)
	}

	// Apply project-level settings from the envelope. The set methods
	// individually validate input and persist via saveConfig. Failures
	// are non-fatal — the project still exists, the user can fix later.
	if env.SystemPrompt != "" {
		_ = s.SetProjectSystemPrompt(info.ID, env.SystemPrompt)
	}
	if env.Provider != "" && env.Model != "" {
		_ = s.SetProjectProvider(info.ID, env.Provider, env.Model)
	}
	if env.Temperature != 0 || env.MaxTokens != 0 {
		_ = s.SetProjectModelParams(info.ID, env.Temperature, env.MaxTokens)
	}
	if env.BudgetUSD > 0 {
		_ = s.SetProjectBudget(info.ID, env.BudgetUSD)
	}

	// Restore each session from the envelope. We reuse the per-session
	// import path so the same name-suffix + lineage-drop semantics apply
	// uniformly. Failures per-session are logged but don't abort.
	importedCount := 0
	for _, sess := range env.Sessions {
		sessJSON, err := json.Marshal(sess)
		if err != nil {
			continue
		}
		if _, err := s.ImportSessionJSON(info.ID, string(sessJSON)); err != nil {
			// Per-session failure is non-fatal — keep importing the rest
			// so the user gets as many of their sessions back as possible.
			continue
		}
		importedCount++
	}

	// Cleanup: AddProject created an empty "Chat 1" default session. If the
	// envelope brought in real sessions, that default is just clutter —
	// delete it so the user lands on a project containing only their
	// imported sessions. Skip when nothing was imported (the default
	// session is the user's only entry point in that case).
	if importedCount > 0 {
		s.mu.RLock()
		newProj, ok := s.projects[info.ID]
		s.mu.RUnlock()
		if ok {
			newProj.mu.RLock()
			defaultSess, hasDefault := newProj.sessions["default"]
			newProj.mu.RUnlock()
			if hasDefault {
				defaultSess.mu.RLock()
				isEmpty := !hasUserMessage(defaultSess.history)
				defaultSess.mu.RUnlock()
				if isEmpty {
					// DeleteChatSession enforces the "can't delete last
					// session" rule; we already have ≥1 imported session, so
					// this is safe.
					_ = s.DeleteChatSession(info.ID, "default")
				}
			}
		}
	}

	// Refresh the projectInfo so its messages count + session metadata
	// reflect the imported sessions.
	return s.GetProject(info.ID)
}
