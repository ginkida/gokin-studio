package studio

import (
	"context"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/plan"
	"github.com/ginkida/gokin-studio/internal/engine/tasks"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"github.com/google/uuid"
	"google.golang.org/genai"
)

// ChatSession represents an independent chat conversation within a project.
type ChatSession struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	lastUsedAt int64  // unix millis of the last agent turn; 0 if never run
	ParentID   string // session ID this was forked from; "" for top-level sessions
	Pinned     bool   // true = anchor to top of tab list regardless of LastUsedAt

	client   client.Client
	history  []*genai.Content
	active   bool
	cancelFn context.CancelFunc
	mu       sync.RWMutex

	// queuedTurns are follow-up prompts submitted while this session's agent is
	// running. queueWorker stays true across the tiny idle gap between turns so
	// a second SendMessage cannot race the worker that is about to start the
	// next queued item.
	queuedTurns []*queuedTurn
	queueWorker bool
	queueHalt   bool

	// incomingDelegation is the chain stamp for the turn that is about to run
	// (or is running) in this session. It is set inside the same critical
	// section that claims queueWorker, snapshotted at turn start, and cleared
	// immediately afterwards so it can never leak into a later human turn.
	incomingDelegation *delegationStamp

	// execution* are ephemeral, session-scoped overrides used by scheduled
	// task runs. They deliberately never mutate the parent Project: a Kimi
	// scheduled run can execute alongside a normal GLM chat without either
	// client, prompt, or permission policy leaking into the other.
	executionProvider       string
	executionModel          string
	executionPermissionMode string
	executionSystemPrompt   string
	executionAllowedTools   map[string]bool
	pluginAgentChild        bool
	// delegateChild marks a chat created to service a cross-project
	// delegation. Like a scheduled or plugin-agent child it may not originate
	// further cross-agent work, so a relay cannot be built out of children.
	delegateChild bool

	// deniedTools and mutatedThisTurn are per-turn observations the delegation
	// monitor reads: a caller must be told when the target finished but some
	// tool calls were blocked, and a cancelled delegation that already wrote
	// something is not a rolled-back delegation.
	deniedTools     []string
	mutatedThisTurn bool

	// permissionMode is an ephemeral user-selected override for this chat.
	// Only "plan" is stored here: Manual/Accept edits/Auto/Skip remain durable folder
	// defaults on Project, while Plan intentionally ends with this session/app.
	permissionMode string
	// ArchivedAt hides an idle session from the active tab catalog without
	// deleting its history, worktree, drafts, pins, artifacts, or recovery data.
	// Zero means active; positive values are Unix milliseconds.
	ArchivedAt int64

	// usage accumulates per-session billing data (cost, tokens, turn count).
	// Loaded from disk on NewProject; bumped after every chat:complete and
	// re-persisted via SaveHistoryWithUsage. Read under mu.RLock(); mutated
	// under mu.Lock(). nil means "no usage recorded yet" (legacy file or
	// never-run session); the agent loop lazy-allocates on first turn.
	usage *SessionUsage

	// Git sessions created after worktree isolation was enabled keep their own
	// checkout and branch. Runtime objects are rebuilt lazily after restart.
	WorktreePath     string
	WorktreeWorkDir  string
	WorktreeBranch   string
	WorktreeBaseHead string
	WorktreeError    string
	// IsolationSkippedReason explains why this chat has no private checkout —
	// currently only WSL projects, whose repository lives inside the distro
	// while the worktree would land on the Windows drive.
	IsolationSkippedReason string
	registry               *tools.Registry
	taskManager            *tasks.Manager
	planManager            *plan.Manager

	// historyEpoch changes when the transcript is explicitly cleared. An
	// ephemeral side question captures the epoch with its context snapshot and
	// skips its usage save if that conversation was reset while it was running.
	historyEpoch uint64
}

type queuedTurn struct {
	ID              string
	Message         string
	AttachmentParts []*genai.Part
	QueuedAt        int64
	// Delegation travels with the queued item, not with the session. Two
	// delegated messages can sit in one session's queue; if the stamp lived
	// only on the session, the first turn's start would clear it and the
	// second would run as if a human had typed it — escaping the depth guard.
	Delegation *delegationStamp
}

// Stop cancels any in-progress generation for this session.
// It also clears queued follow-ups: a user pressing Stop expects the session
// to become idle, not to immediately start the next unattended request.
func (s *ChatSession) Stop() []string {
	s.mu.Lock()
	cancel := s.cancelFn
	taskManager := s.taskManager
	removed := make([]string, 0, len(s.queuedTurns))
	for _, turn := range s.queuedTurns {
		if turn != nil {
			removed = append(removed, turn.ID)
		}
	}
	s.queuedTurns = nil
	s.queueHalt = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if taskManager != nil {
		for _, info := range taskManager.ListRunning() {
			_ = taskManager.Cancel(info.ID)
		}
	}
	return removed
}

// ChatSessionInfo is the JSON-friendly representation for the frontend.
type ChatSessionInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Active     bool   `json:"active"`
	Messages   int    `json:"messages"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt,omitempty"`
	// Lineage for forked sessions. ParentID is empty for top-level
	// (non-forked) sessions; ParentName is filled in by ListChatSessions
	// from a sibling-lookup so the UI can show "↳ <name>" without an extra
	// RPC. ParentName falls back to "(deleted)" when the parent is gone.
	ParentID               string `json:"parentID,omitempty"`
	ParentName             string `json:"parentName,omitempty"`
	Pinned                 bool   `json:"pinned,omitempty"`
	WorktreeIsolated       bool   `json:"worktreeIsolated,omitempty"`
	WorktreePath           string `json:"worktreePath,omitempty"`
	WorktreeBranch         string `json:"worktreeBranch,omitempty"`
	WorktreeError          string `json:"worktreeError,omitempty"`
	PermissionMode         string `json:"permissionMode,omitempty"`
	IsolationSkippedReason string `json:"isolationSkippedReason,omitempty"`
	Archived               bool   `json:"archived,omitempty"`
	ArchivedAt             int64  `json:"archivedAt,omitempty"`
}

// NewChatSession creates a new session with a generated ID.
func NewChatSession(name string) *ChatSession {
	return &ChatSession{
		ID:        uuid.New().String()[:8],
		Name:      name,
		CreatedAt: time.Now(),
	}
}

// Info returns a JSON-safe snapshot.
func (s *ChatSession) Info() *ChatSessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgCount := 0
	for _, c := range s.history {
		for _, p := range c.Parts {
			if p.Text != "" {
				msgCount++
				break
			}
		}
	}
	return &ChatSessionInfo{
		ID:                     s.ID,
		Name:                   s.Name,
		Active:                 s.active,
		Messages:               msgCount,
		CreatedAt:              s.CreatedAt.UnixMilli(),
		LastUsedAt:             s.lastUsedAt,
		ParentID:               s.ParentID,
		Pinned:                 s.Pinned,
		WorktreeIsolated:       s.WorktreePath != "",
		WorktreePath:           s.WorktreeWorkDir,
		WorktreeBranch:         s.WorktreeBranch,
		WorktreeError:          s.WorktreeError,
		IsolationSkippedReason: s.IsolationSkippedReason,
		PermissionMode:         s.permissionMode,
		Archived:               s.ArchivedAt > 0,
		ArchivedAt:             s.ArchivedAt,
		// ParentName is populated by ListChatSessions's sibling-lookup, not here.
	}
}
