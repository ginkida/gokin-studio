package studio

import (
	"context"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
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

	// usage accumulates per-session billing data (cost, tokens, turn count).
	// Loaded from disk on NewProject; bumped after every chat:complete and
	// re-persisted via SaveHistoryWithUsage. Read under mu.RLock(); mutated
	// under mu.Lock(). nil means "no usage recorded yet" (legacy file or
	// never-run session); the agent loop lazy-allocates on first turn.
	usage *SessionUsage
}

// Stop cancels any in-progress generation for this session.
func (s *ChatSession) Stop() {
	s.mu.RLock()
	cancel := s.cancelFn
	s.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
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
	ParentID   string `json:"parentID,omitempty"`
	ParentName string `json:"parentName,omitempty"`
	Pinned     bool   `json:"pinned,omitempty"`
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
		ID:         s.ID,
		Name:       s.Name,
		Active:     s.active,
		Messages:   msgCount,
		CreatedAt:  s.CreatedAt.UnixMilli(),
		LastUsedAt: s.lastUsedAt,
		ParentID:   s.ParentID,
		Pinned:     s.Pinned,
		// ParentName is populated by ListChatSessions's sibling-lookup, not here.
	}
}
