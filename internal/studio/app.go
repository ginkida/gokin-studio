package studio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"google.golang.org/genai"
)

// Ensure all background goroutines (SendMessage, Dispatch) are tracked
// so Shutdown can wait for them to finish.

// Studio is the main Wails-bound struct.
// All public methods are exposed as bindings to the React frontend.
type Studio struct {
	ctx          context.Context
	config       *StudioConfig
	projects     map[string]*Project
	terminals    map[string]*Terminal
	sharedMemory *SharedMemory    // process-wide cross-project scratchpad for agents
	askUsers     *askUserRegistry // pending ask_user questions awaiting frontend answers
	eventLog     *EventLog        // ring buffer of recent backend events (errors, warnings) — exposed via Diagnostics UI
	eventLogOnce sync.Once        // guards lazy-init of eventLog for tests using bare &Studio{}
	mu           sync.RWMutex
	wg           sync.WaitGroup // tracks background goroutines (SendMessage, Dispatch)
	// testDispatchFn, if non-nil, is called instead of dispatchInternal in tests.
	testDispatchFn func(from, to *Project, fromSid, task string, settings Settings)
}

// NewStudio creates a new Studio instance.
func NewStudio() *Studio {
	return &Studio{
		projects:     make(map[string]*Project),
		terminals:    make(map[string]*Terminal),
		sharedMemory: NewSharedMemory(),
		askUsers:     newAskUserRegistry(),
		eventLog:     NewEventLog(),
	}
}

// --- Wails lifecycle ---

// Startup is called by Wails when the app starts.
func (s *Studio) Startup(ctx context.Context) {
	s.ctx = ctx
	s.config = LoadConfig()
	for _, pc := range s.config.Projects {
		p := NewProject(pc)
		p.studio = s
		s.projects[pc.ID] = p
	}
	// Persist any migrations applied by LoadConfig.
	s.saveConfig()

	// iter 760+: hydrate the event log from disk + enable ongoing persistence.
	// Order matters: LoadFromDisk first (so replay doesn't re-persist), then
	// SetPersistPath so subsequent logs land on disk.
	s.ensureEventLog()
	eventLogPath := filepath.Join(configDir(), "events.log")
	if err := s.eventLog.LoadFromDisk(eventLogPath); err != nil {
		// Log the failure but don't crash; events.log is a debugging
		// convenience, not a critical path. Goes to stderr too so headless
		// runs see it.
		fmt.Fprintf(os.Stderr, "gokin-studio: event log replay failed: %v\n", err)
	}
	s.eventLog.SetPersistPath(eventLogPath)

	// iter 790+: background auto-cleanup pass — once per 24h, conservative
	// thresholds (replays >30d, pre-import >90d). Runs in a goroutine so a
	// slow file walk on a giant config dir doesn't block UI bring-up. Errors
	// are logged but never crash. Skipped entirely when the user has set
	// Settings.AutoCleanupDisabled.
	//
	// iter 970+: safeGoFn replaces inline defer/recover so panics surface in
	// the event log (visible via Diagnostics → View Logs) instead of only
	// stderr, which is invisible to users launching from a desktop launcher.
	safeGoFn("auto-cleanup", s.LogEvent, func() {
		_ = s.RunAutoCleanupIfDue()
	})

	// iter 840+: background auto-backup pass — once per 24h, opt-in via
	// Settings.AutoBackupEnabled. Writes a tar.gz snapshot to configDir/backups/
	// and prunes the oldest beyond AutoBackupRetention.
	safeGoFn("auto-backup", s.LogEvent, func() {
		_, _ = s.RunAutoBackupIfDue()
	})
}

// Shutdown is called by Wails when the app closes.
func (s *Studio) Shutdown(_ context.Context) {
	// Cancel all in-progress agent runs and terminals.
	s.mu.RLock()
	for _, p := range s.projects {
		p.Stop()
	}
	for _, t := range s.terminals {
		t.Close()
	}
	s.mu.RUnlock()

	// Wait for all background goroutines (SendMessage, Dispatch) to finish.
	s.wg.Wait()

	// Prune abandoned empty "Chat N" tabs before saving so they don't come
	// back on next boot. Rule: a session with zero history entries AND a
	// default auto-generated name ("Chat N") is considered abandoned and
	// gets dropped — both the in-memory entry and its on-disk files.
	// Sessions that are empty but renamed, or that have any history at all,
	// are preserved. We always keep at least one session per project.
	s.mu.RLock()
	for _, p := range s.projects {
		p.pruneAbandonedEmptySessions()
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveConfig()
}

// --- Project Management ---

// BrowseDirectory opens a native directory picker dialog and returns the selected path.
func (s *Studio) BrowseDirectory() (string, error) {
	dir, err := wailsRuntime.OpenDirectoryDialog(s.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Project Directory",
	})
	if err != nil {
		return "", err
	}
	return dir, nil
}

// AddProject registers a new project directory.
func (s *Studio) AddProject(name, directory string) (*ProjectInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("project name cannot be empty")
	}
	if len(name) > 60 {
		name = name[:60]
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("directory does not exist: %s", abs)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for duplicate directory.
	for _, existing := range s.projects {
		if existing.Directory == abs {
			return nil, fmt.Errorf("project already registered: %s", abs)
		}
	}

	id := GenerateID()
	// Stamp lastUsedAt so the freshly-added project lands at the top of the
	// recent-first sidebar immediately, instead of falling to the alphabetical
	// tail until the user actually runs the agent.
	p := NewProject(ProjectConfig{
		ID:             id,
		Name:           name,
		Directory:      abs,
		Provider:       s.config.Settings.DefaultProvider,
		Model:          s.config.Settings.DefaultModel,
		ThinkingMode:   s.config.Settings.DefaultThinkingMode,
		ThinkingBudget: s.config.Settings.DefaultThinkingBudget,
		BudgetUSD:      s.config.Settings.DefaultBudgetUSD,
		LastUsedAt:     time.Now().UnixMilli(),
	})
	p.studio = s
	s.projects[id] = p
	s.saveConfig()
	s.auditProjectAdded(name, abs)
	return p.Info(), nil
}

// RemoveProject removes a project from the studio.
func (s *Studio) RemoveProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	removedName := p.Name
	p.Stop()
	// Close any terminals owned by this project so their PTY fd + child
	// process + read-loop goroutine don't outlive the project. Without this,
	// backend cleanup depended entirely on the frontend firing CloseTerminal
	// on unmount, which can be missed (split view, lost termID reference).
	// Mirrors Shutdown's terminal loop; t.Close() is idempotent. Safe under
	// the held s.mu write lock — the read loop releases t.mu before taking
	// s.mu, so there's no lock-order cycle.
	for tid, t := range s.terminals {
		if t.ProjectID == id {
			delete(s.terminals, tid)
			t.Close()
		}
	}
	// Collect every session ID before we drop the project so we can clean up
	// both the persisted history and any replay buffers on disk.
	sessionIDs := make([]string, 0, len(p.sessions))
	for sid := range p.sessions {
		sessionIDs = append(sessionIDs, sid)
	}
	delete(s.projects, id)
	// Legacy single-file history (pre-sessions).
	DeleteHistory(id)
	// Per-session history + replay. Explicitly include "default" in case the
	// sessions map was empty (never opened) but legacy files exist.
	hasDefault := false
	for _, sid := range sessionIDs {
		DeleteHistory(id + "_" + sid)
		DiscardReplay(id, sid)
		if sid == "default" {
			hasDefault = true
		}
	}
	if !hasDefault {
		DeleteHistory(id + "_default")
		DiscardReplay(id, "default")
	}
	// Remove every persisted draft for this project so they don't outlive the
	// project that owned them.
	removeProjectDrafts(id)
	// Same for pinned messages — orphan pins are useless.
	removeProjectPins(id)
	// Drop the per-project session-pin file so a deleted project doesn't
	// leak its pinned-tab state. Symmetric with the message-pin cleanup.
	removeProjectSessionPins(id)
	// Same for the manual session-order file (iter 540+).
	removeProjectSessionOrder(id)
	s.saveConfig()
	s.auditProjectRemoved(removedName)
	return nil
}

// ListProjects returns all registered projects, sorted most-recently-used
// first. Never-used projects (LastUsedAt == 0) fall back to alphabetical.
func (s *Studio) ListProjects() []*ProjectInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*ProjectInfo
	for _, p := range s.projects {
		result = append(result, p.Info())
	}
	sort.SliceStable(result, func(i, j int) bool {
		ai, aj := result[i].LastUsedAt, result[j].LastUsedAt
		if ai != aj {
			// Non-zero always beats zero; among non-zero, newer first.
			if ai == 0 {
				return false
			}
			if aj == 0 {
				return true
			}
			return ai > aj
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// GetProject returns info for a single project.
func (s *Studio) GetProject(id string) (*ProjectInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, fmt.Errorf("project not found: %s", id)
	}
	return p.Info(), nil
}

// SetProjectProvider changes the LLM provider and model for a project.
func (s *Studio) SetProjectProvider(id, provider, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	oldProv, oldModel, name := p.Provider, p.Model, p.Name
	p.Provider = provider
	p.Model = model
	p.resetClientLocked() // close + clear so the next send re-inits
	p.mu.Unlock()
	s.saveConfig()
	s.auditProjectProvider(name, oldProv, oldModel, provider, model)
	return nil
}

// SetProjectSystemPrompt changes the system prompt for a project.
func (s *Studio) SetProjectSystemPrompt(id, prompt string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	oldPrompt, name := p.SystemPrompt, p.Name
	p.SystemPrompt = prompt
	p.resetClientLocked() // close + clear so the next send re-inits with the new prompt
	p.mu.Unlock()
	s.saveConfig()
	s.auditProjectSystemPrompt(name, oldPrompt, prompt)
	return nil
}

// SetProjectModelParams changes model parameters for a project.
func (s *Studio) SetProjectModelParams(id string, temperature float32, maxTokens int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	oldTemp, oldMax, name := p.Temperature, p.MaxTokens, p.Name
	p.Temperature = temperature
	p.MaxTokens = maxTokens
	p.resetClientLocked() // close + clear so the next send re-inits
	p.mu.Unlock()
	s.saveConfig()
	s.auditProjectModelParams(name, oldTemp, temperature, oldMax, maxTokens)
	return nil
}

// SetProjectThinking configures extended thinking for a project.
// mode: "" = auto (provider default), "enabled" = on, "disabled" = off.
// budget: max reasoning tokens; 0 means use provider default (4096).
func (s *Studio) SetProjectThinking(id, mode string, budget int32) error {
	if mode != "" && mode != "enabled" && mode != "disabled" {
		return fmt.Errorf("invalid thinking mode %q: must be empty, \"enabled\", or \"disabled\"", mode)
	}
	if budget < 0 {
		return fmt.Errorf("invalid thinking budget %d: must be >= 0", budget)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	oldMode, oldBudget, name := p.ThinkingMode, p.ThinkingBudget, p.Name
	p.ThinkingMode = mode
	p.ThinkingBudget = budget
	p.resetClientLocked() // close + clear so the next send re-inits
	p.mu.Unlock()
	s.saveConfig()
	s.auditProjectThinking(name, oldMode, oldBudget, mode, budget)
	return nil
}

// SetProjectPermissionMode sets a project's change-confirmation mode. "" / "auto"
// proceeds without asking; "ask" makes the agent confirm via the ask_user tool
// before file/git/destructive changes (soft enforcement via a system-prompt
// directive — the agent loop has no hard approval gate). Stored as "" for auto
// so it round-trips with omitempty.
func (s *Studio) SetProjectPermissionMode(id, mode string) error {
	if mode == "auto" {
		mode = ""
	}
	if mode != "" && mode != "ask" {
		return fmt.Errorf("invalid permission mode %q: must be \"\", \"auto\", or \"ask\"", mode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	p.PermissionMode = mode
	p.resetClientLocked() // rebuild client so the directive is added/removed next send
	p.mu.Unlock()
	s.saveConfig()
	return nil
}

// SetProjectBudget sets the per-project monthly USD spend cap. The frontend
// uses it to draw a progress bar in the usage modal and warn at 80%/100%.
// Pass 0 to remove the budget. Capped at $100,000 to defend against typos.
// Does not invalidate the cached client (no model state depends on this).
func (s *Studio) SetProjectBudget(id string, budgetUSD float64) error {
	if budgetUSD < 0 {
		return fmt.Errorf("invalid budget %.2f: must be >= 0", budgetUSD)
	}
	const maxBudget = 100000.0
	if budgetUSD > maxBudget {
		return fmt.Errorf("budget %.2f exceeds maximum of %.2f", budgetUSD, maxBudget)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	oldBudget, name := p.BudgetUSD, p.Name
	p.BudgetUSD = budgetUSD
	p.mu.Unlock()
	s.saveConfig()
	s.auditProjectBudget(name, oldBudget, budgetUSD)
	return nil
}

// SetProjectEnforceBudget toggles the iter 1040+ strict budget enforcement
// flag. When enabled AND BudgetUSD > 0, SendMessage blocks new turns once
// cumulative cost meets/exceeds the budget. Off (the default) keeps the
// historical behavior: only warning toasts fire at 80%/100%.
//
// Setting to true does NOT retroactively stop an already-active turn —
// only future SendMessage calls. To stop an active runaway, use the
// frontend's Stop button OR set the budget to 0 first.
func (s *Studio) SetProjectEnforceBudget(id string, enforce bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	p.EnforceBudget = enforce
	p.mu.Unlock()
	s.saveConfig()
	return nil
}

// SetProjectPinned anchors a project to the top of the sidebar (or unanchors
// it). Pinned projects keep their lastUsedAt-desc order among themselves; the
// rest follow with the same rule. Survives restart via saveConfig.
func (s *Studio) SetProjectPinned(id string, pinned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	oldPinned, name := p.Pinned, p.Name
	p.Pinned = pinned
	p.mu.Unlock()
	s.saveConfig()
	s.auditProjectPinned(name, oldPinned, pinned)
	return nil
}

// ClearPinnedContext removes any pinned context the agent attached to this
// project, both from memory and from the .gokin/pinned_context.md disk file.
func (s *Studio) ClearPinnedContext(id string) error {
	s.mu.RLock()
	p, ok := s.projects[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	p.pinnedContext = ""
	dir := p.Directory
	p.mu.Unlock()
	// Remove the disk copy so it isn't restored on next startup. Non-fatal if
	// the file doesn't exist (pin was never persisted or already cleaned up).
	diskPath := filepath.Join(dir, ".gokin", "pinned_context.md")
	if err := os.Remove(diskPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cleared pin from memory but could not remove disk file: %w", err)
	}
	return nil
}

// --- Chat Sessions ---

// CreateChatSession creates a new chat session in a project.
func (s *Studio) CreateChatSession(projectID string) (*ChatSessionInfo, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	p.mu.Lock()
	// Use max existing "Chat N" number + 1 so deletions don't create duplicates.
	// sess.Name is read under sess.mu: the agent loop auto-renames a session on
	// its first user turn under session.mu, so reading it under p.mu alone is a
	// data race (and could corrupt the Sscanf parse on a torn string read).
	maxNum := 0
	for _, sess := range p.sessions {
		sess.mu.RLock()
		nm := sess.Name
		sess.mu.RUnlock()
		var n int
		if _, err := fmt.Sscanf(nm, "Chat %d", &n); err == nil && n > maxNum {
			maxNum = n
		}
	}
	session := NewChatSession(fmt.Sprintf("Chat %d", maxNum+1))
	p.sessions[session.ID] = session
	name := session.Name
	sid := session.ID
	p.mu.Unlock()

	// Persist the (empty) session immediately so it survives an app restart
	// even if the user never sends a message in it — a fresh "Chat N" tab
	// the user opens to jot notes in tomorrow shouldn't disappear.
	_ = SaveHistoryWithName(projectID+"_"+sid, name, nil)

	return session.Info(), nil
}

// ListChatSessions returns all sessions for a project.
func (s *Studio) ListChatSessions(projectID string) ([]*ChatSessionInfo, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	// Build a quick id→name index so ParentName can be filled in without
	// nested locking. Worst case O(N) per ListChatSessions call, but N is
	// small (sessions per project rarely exceed dozens).
	nameByID := make(map[string]string, len(p.sessions))
	for sid, sess := range p.sessions {
		// Name is owned by session.mu (the agent loop's first-turn auto-rename
		// writes it under session.mu); read it there, not under p.mu alone.
		sess.mu.RLock()
		nameByID[sid] = sess.Name
		sess.mu.RUnlock()
	}
	var result []*ChatSessionInfo
	for _, sess := range p.sessions {
		info := sess.Info()
		if info.ParentID != "" {
			if pname, ok := nameByID[info.ParentID]; ok {
				info.ParentName = pname
			} else {
				// Parent was deleted — surface that to the UI rather than
				// silently dropping the lineage indicator. The frontend
				// renders this as an italic "(deleted)" placeholder so the
				// user knows the link was broken.
				info.ParentName = "(deleted)"
			}
		}
		result = append(result, info)
	}
	// Recent-first ordering matches the project sidebar: most-recently-used
	// session at the top, "default" (never-used) at the bottom unless it was
	// actively used. Pinned sessions anchor above unpinned regardless of
	// LastUsedAt — symmetric with project pinning (iter 430+). Stable so
	// ties preserve insertion order. Within each pin group, an explicit
	// user-defined order (iter 540+) takes precedence over LastUsedAt.
	order, _ := loadSessionOrder(projectID)
	orderIdx := make(map[string]int, len(order))
	for i, id := range order {
		orderIdx[id] = i
	}
	sort.SliceStable(result, func(i, j int) bool {
		// Pinned beats unpinned. Within each pin group, fall through to the
		// order rules so ordering inside the pinned set still feels natural.
		if result[i].Pinned != result[j].Pinned {
			return result[i].Pinned
		}
		// User-defined order (iter 540+): if both sessions are in the order
		// array, sort by index. If only one is, that one comes first
		// (explicitly ordered before lastUsedAt-default). Default fallback
		// rules apply only when neither has an explicit position.
		oi, oiOK := orderIdx[result[i].ID]
		oj, ojOK := orderIdx[result[j].ID]
		if oiOK && ojOK {
			return oi < oj
		}
		if oiOK != ojOK {
			return oiOK
		}
		ai, aj := result[i].LastUsedAt, result[j].LastUsedAt
		if ai != aj {
			if ai == 0 {
				return false
			}
			if aj == 0 {
				return true
			}
			return ai > aj
		}
		// Keep "default" stable at the bottom when both are unused.
		if result[i].ID == "default" {
			return false
		}
		if result[j].ID == "default" {
			return true
		}
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// SetSessionPinned anchors a session tab to the top of the tab list (or
// unanchors it). Pinned sessions sort above unpinned regardless of
// LastUsedAt; within each group, existing rules apply. Persisted via a
// per-project session-pins file. Symmetric with SetProjectPinned.
func (s *Studio) SetSessionPinned(projectID, sessionID string, pinned bool) error {
	if projectID == "" {
		return fmt.Errorf("projectID cannot be empty")
	}
	if sessionID == "" {
		sessionID = "default"
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	sess, exists := p.sessions[sessionID]
	p.mu.RUnlock()
	if !exists {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	// Update in-memory pin state under the session lock.
	sess.mu.Lock()
	sess.Pinned = pinned
	sess.mu.Unlock()
	// Read the current pin set, mutate it, write it back. We hold no project
	// lock here so two concurrent SetSessionPinned calls for different
	// sessions can race — last writer wins on the file. Acceptable: the
	// in-memory truth (sess.Pinned) is what drives the sort; the file is
	// only used to hydrate state on restart, and the user can fix any drift
	// by clicking again. To minimize the race, we rebuild from the live
	// session map rather than the on-disk file.
	p.mu.RLock()
	live := make(map[string]bool, len(p.sessions))
	for sid, ss := range p.sessions {
		ss.mu.RLock()
		if ss.Pinned {
			live[sid] = true
		}
		ss.mu.RUnlock()
	}
	p.mu.RUnlock()
	if err := savePinnedSessions(projectID, live); err != nil {
		return fmt.Errorf("failed to persist session pins: %w", err)
	}
	return nil
}

// RenameChatSession changes a session's display name.
func (s *Studio) RenameChatSession(projectID, sessionID, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("session name cannot be empty")
	}
	if len(newName) > 60 {
		newName = newName[:60]
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	session, exists := p.sessions[sessionID]
	p.mu.RUnlock()
	if !exists {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	session.mu.Lock()
	session.Name = newName
	histSnapshot := make([]*genai.Content, len(session.history))
	copy(histSnapshot, session.history)
	session.mu.Unlock()
	// Persist immediately so a rename survives even if the session isn't
	// touched again before restart. Ignore errors — same call path as send.
	_ = SaveHistoryWithName(projectID+"_"+sessionID, newName, histSnapshot)
	return nil
}

// EditLastUserMessage trims session history back to just before the last user
// turn and re-sends the edited text. Kept for backward compatibility with the
// frontend binding — delegates to EditUserMessage with index 0.
func (s *Studio) EditLastUserMessage(projectID, sessionID, newText string) error {
	return s.EditUserMessage(projectID, sessionID, 0, newText)
}

// EditUserMessage trims session history back to just before the Nth user turn
// (counted from the end: 0 = last, 1 = second-to-last) and re-sends the edited
// text from that point. This is the engine for both "edit & re-send" and
// "re-run as-is" flows from the message UI.
func (s *Studio) EditUserMessage(projectID, sessionID string, userIndexFromEnd int, newText string) error {
	newText = strings.TrimSpace(newText)
	if newText == "" {
		return fmt.Errorf("message cannot be empty")
	}
	if userIndexFromEnd < 0 {
		return fmt.Errorf("userIndexFromEnd must be >= 0")
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	session, exists := p.sessions[sid]
	p.mu.RUnlock()
	if !exists {
		return fmt.Errorf("session not found: %s", sid)
	}

	// Single lock across the active-check AND the trim so a concurrent
	// SendMessage can't slip in between them and add a user turn we'd then
	// trim unintentionally.
	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		return fmt.Errorf("agent is running in this chat, stop it first")
	}
	trimTo := -1
	seen := 0
	for i := len(session.history) - 1; i >= 0; i-- {
		c := session.history[i]
		if c == nil || c.Role != "user" {
			continue
		}
		hasText := false
		for _, part := range c.Parts {
			if part != nil && part.Text != "" {
				hasText = true
				break
			}
		}
		if !hasText {
			continue
		}
		if seen == userIndexFromEnd {
			trimTo = i
			break
		}
		seen++
	}
	if trimTo < 0 {
		session.mu.Unlock()
		return fmt.Errorf("user turn #%d from end not found in history", userIndexFromEnd)
	}
	session.history = session.history[:trimTo]
	histSnapshot := make([]*genai.Content, len(session.history))
	copy(histSnapshot, session.history)
	name := session.Name
	session.mu.Unlock()
	_ = SaveHistoryWithName(projectID+"_"+sid, name, histSnapshot)

	// Kick off a fresh send via the normal path with the edited (or identical) text.
	return s.SendMessage(projectID, newText, sid)
}

// ForkChatSession branches a new session from an existing one at a specific
// user message. The new session inherits all history up to AND including
// the chosen user turn, plus any preceding model/tool turns — exactly the
// state the model would have seen when answering. The forked session is
// independent: subsequent edits in either side don't affect the other.
//
// Use case: user has a long conversation and wants to try a different
// approach without losing the original thread. Without forking, the only
// options were "keep typing in the same session" (loses the original
// continuation) or "/clear" (loses everything).
//
// `userIndexFromEnd` matches EditUserMessage semantics: 0 = most recent
// user turn, 1 = the one before, etc. The fork includes that user turn
// (so the new session is ready for the model to respond to it again with
// a different approach). `newName` is optional — empty means auto-generate
// "<source name> (branch)" or fall back to "Chat N".
func (s *Studio) ForkChatSession(projectID, sessionID string, userIndexFromEnd int, newName string) (*ChatSessionInfo, error) {
	if userIndexFromEnd < 0 {
		return nil, fmt.Errorf("userIndexFromEnd must be >= 0")
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	source, exists := p.sessions[sid]
	sourceName := ""
	if exists {
		sourceName = source.Name
	}
	p.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("session not found: %s", sid)
	}

	// Snapshot source history under its lock so a concurrent SendMessage
	// can't mutate the slice while we're cloning it.
	source.mu.RLock()
	srcHistory := make([]*genai.Content, len(source.history))
	copy(srcHistory, source.history)
	source.mu.RUnlock()

	// Find the cutoff: the index AFTER the chosen user turn (so we include it).
	cutoff := -1
	seen := 0
	for i := len(srcHistory) - 1; i >= 0; i-- {
		c := srcHistory[i]
		if c == nil || c.Role != "user" {
			continue
		}
		hasText := false
		for _, part := range c.Parts {
			if part != nil && part.Text != "" {
				hasText = true
				break
			}
		}
		if !hasText {
			continue
		}
		if seen == userIndexFromEnd {
			cutoff = i + 1 // include this turn
			break
		}
		seen++
	}
	if cutoff < 0 {
		return nil, fmt.Errorf("user turn #%d from end not found in history", userIndexFromEnd)
	}

	// Deep-copy the prefix into the new session. Sharing *genai.Content
	// pointers between sessions would couple them — a Part appended in
	// one session would surface in the other on the next save.
	forkedHistory := make([]*genai.Content, cutoff)
	for i := range cutoff {
		src := srcHistory[i]
		if src == nil {
			continue
		}
		dup := &genai.Content{Role: src.Role}
		if len(src.Parts) > 0 {
			dup.Parts = make([]*genai.Part, len(src.Parts))
			for j, part := range src.Parts {
				if part == nil {
					continue
				}
				cp := *part
				dup.Parts[j] = &cp
			}
		}
		forkedHistory[i] = dup
	}

	// Pick a name. Fall back to "<source> (branch)" when caller didn't give
	// one; truncate to the same 60-char cap as RenameChatSession so a long
	// source name + suffix doesn't blow the limit.
	name := strings.TrimSpace(newName)
	if name == "" {
		if sourceName != "" {
			name = sourceName + " (branch)"
		} else {
			name = "Chat (branch)"
		}
	}
	if len(name) > 60 {
		name = name[:60]
	}

	p.mu.Lock()
	newSession := NewChatSession(name)
	newSession.history = forkedHistory
	newSession.ParentID = sid // remember which session we forked from
	p.sessions[newSession.ID] = newSession
	newID := newSession.ID
	p.mu.Unlock()

	// Persist immediately with explicit parent ID so the fork survives a
	// restart even if the user never sends a new message in it. Use the
	// metadata variant rather than SaveHistoryWithName so the parent ID
	// is stamped on the FIRST write (not preserved from a non-existent
	// previous file).
	_ = SaveHistoryWithMetadata(projectID+"_"+newID, name, sid, forkedHistory)

	return newSession.Info(), nil
}

// ReplaySessionEvent is the JSON representation of a recovery event sent to
// the frontend. Mirrors internal ReplayEvent but uses stable field names for
// the Wails binding.
type ReplaySessionEvent struct {
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	Success *bool          `json:"success,omitempty"`
	Ts      int64          `json:"ts"`
}

// GetRecoveryEvents returns replay events for a session if an interrupted
// turn was detected (and automatically cleans up empty/completed logs).
func (s *Studio) GetRecoveryEvents(projectID, sessionID string) ([]*ReplaySessionEvent, error) {
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	events, err := LoadReplay(projectID, sid)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	// If the log ends with a "complete" marker, the turn actually finished —
	// this can happen if cleanup was interrupted. Drop it silently.
	if HasCompleteMarker(events) {
		DiscardReplay(projectID, sid)
		return nil, nil
	}
	out := make([]*ReplaySessionEvent, 0, len(events))
	for _, e := range events {
		out = append(out, &ReplaySessionEvent{
			Type: e.Type, Text: e.Text, Tool: e.Tool, Args: e.Args,
			Success: e.Success, Ts: e.TimestampMs,
		})
	}
	return out, nil
}

// DiscardRecoveryEvents removes the replay log for a session, used when the
// user chooses to dismiss an interrupted turn from the recovery UI.
func (s *Studio) DiscardRecoveryEvents(projectID, sessionID string) error {
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	DiscardReplay(projectID, sid)
	return nil
}

// GetClipboardText returns the system clipboard's current text content via
// the Wails native bridge. Necessary because WebKitGTK on Linux sometimes
// refuses browser-side Ctrl+V into `type="password"` inputs and even blocks
// navigator.clipboard.readText in non-secure contexts. The frontend calls
// this from the "Paste" button in the API-keys UI.
func (s *Studio) GetClipboardText() (string, error) {
	if s.ctx == nil {
		return "", fmt.Errorf("studio not initialised")
	}
	return wailsRuntime.ClipboardGetText(s.ctx)
}

// DeleteChatSession removes a session from a project, cancelling any active run.
// Refuses to remove the last remaining session so the project is never left
// with zero chats. Any session (including "default") can be deleted as long
// as at least one other session remains.
func (s *Studio) DeleteChatSession(projectID, sessionID string) error {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}

	// Hold the write lock for the guard check AND the delete so two concurrent
	// deletion calls can't both pass the "at least 2 sessions" guard.
	p.mu.Lock()
	sessionCount := len(p.sessions)
	session, exists := p.sessions[sessionID]
	if !exists {
		p.mu.Unlock()
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if sessionCount <= 1 {
		p.mu.Unlock()
		return fmt.Errorf("cannot delete the last remaining session")
	}
	delete(p.sessions, sessionID)
	p.mu.Unlock()

	// Cancel any active generation after releasing the lock so Stop() doesn't
	// need to acquire any project-level lock itself.
	session.Stop()
	DeleteHistory(projectID + "_" + sessionID)
	DiscardReplay(projectID, sessionID)
	// Drop the persisted draft for this session — once the session is gone,
	// keeping its draft on disk just consumes inodes.
	_ = s.ClearDraft(projectID, sessionID)
	// Same for pinned messages — pins anchor to a session that no longer exists.
	removeSessionPins(projectID, sessionID)
	return nil
}

// --- Chat ---

// SendMessage sends a message to a project's agent (async -- results via events).
// sessionID can be empty for the default session.
func (s *Studio) SendMessage(projectID, message, sessionID string) error {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	settings := s.config.Settings
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	sid := sessionID
	// Go 1.25's WaitGroup.Go subsumes the Add(1) + defer Done() boilerplate
	// and scopes the goroutine to the wg lifecycle in one call.
	//
	// iter 970+: defense-in-depth panic barrier. SendMessage has its own
	// internal recover at function entry (project.go:565), but if the
	// closure itself panics before SendMessage starts (extremely rare but
	// possible if `p`/`settings` capture a poisoned value), this catches it
	// and surfaces in the event log instead of killing the process.
	s.wg.Go(func() {
		defer recoverPanic("send-message", s.LogEvent)
		p.SendMessage(s.ctx, message, settings, sid)
	})
	return nil
}

// StopGeneration cancels the current agent run for a specific session (or all if empty).
func (s *Studio) StopGeneration(projectID, sessionID string) error {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	if sessionID == "" {
		p.Stop()
	} else {
		p.StopSession(sessionID)
	}
	return nil
}

// ClearHistory resets chat history for a session.
func (s *Studio) ClearHistory(projectID, sessionID string) error {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	session := p.GetSession(sid)
	if session == nil {
		return fmt.Errorf("session not found: %s", sid)
	}
	// Stop any active generation first. Clearing mid-turn otherwise leaves
	// the agent goroutine appending a model response into a freshly-emptied
	// history, ending with "model" as the only turn — which then fails the
	// next LLM call. The Stop call is synchronous via cancelFn; the goroutine
	// will exit on its next ctx.Err() check and skip its final SaveHistory.
	session.Stop()
	session.mu.Lock()
	session.history = nil
	session.mu.Unlock()
	DeleteHistory(projectID + "_" + sid)
	// Any in-flight replay buffer references a history we just wiped — drop
	// it so the recovery banner doesn't resurrect a turn the user wanted gone.
	DiscardReplay(projectID, sid)
	// /clear also implies "I'm done with whatever I was drafting" — drop the
	// persisted draft so a stale half-typed message doesn't reappear next time.
	_ = s.ClearDraft(projectID, sid)
	// Reset per-session file trackers so the continuation hint after the next
	// compaction doesn't suggest files from the now-cleared session.
	p.mu.Lock()
	if rt, ok := p.readTrackers[sid]; ok {
		rt.Reset()
	}
	if wt, ok := p.writeTrackers[sid]; ok {
		wt.Reset()
	}
	p.mu.Unlock()
	return nil
}

// GetHistory returns the persisted chat messages for a specific session.
func (s *Studio) GetHistory(projectID, sessionID string) ([]ChatMessage, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	session := p.GetSession(sid)
	if session == nil {
		return nil, nil
	}

	session.mu.RLock()
	defer session.mu.RUnlock()

	var msgs []ChatMessage
	for _, c := range session.history {
		text := ""
		for _, part := range c.Parts {
			// Exclude thinking (reasoning) parts — they are internal model
			// deliberation and should not appear as regular assistant text when
			// history is reloaded into the chat panel.
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
		role := c.Role
		if role == "model" {
			role = "assistant"
		}
		msgs = append(msgs, ChatMessage{
			ID:        GenerateID(),
			Role:      role,
			Content:   text,
			Timestamp: 0,
		})
	}
	return msgs, nil
}

// MemoryEntryInfo is a JSON-friendly projection of a memory.Entry for the
// frontend. Exposes only the fields useful to a human browsing what the
// agent has remembered; drops audit fields like LastAccessed / AccessCount
// that aren't worth the screen space yet.
type MemoryEntryInfo struct {
	ID            string   `json:"id"`
	Key           string   `json:"key,omitempty"`
	Content       string   `json:"content"`
	Type          string   `json:"type"`
	Tags          []string `json:"tags,omitempty"`
	Timestamp     int64    `json:"timestamp"`
	Project       string   `json:"project,omitempty"`
	Reinforcement int      `json:"reinforcement,omitempty"`
}

// SearchHit is a single match returned by SearchProjectHistory: it tells
// the frontend which session the match is in, what role spoke it, and a
// snippet centered on the matched substring so the result list can render
// preview text without sending the full message back.
type SearchHit struct {
	SessionID   string `json:"sessionID"`
	SessionName string `json:"sessionName"`
	MessageIdx  int    `json:"messageIdx"`  // index of the matched message within the session's filtered history
	Role        string `json:"role"`        // "user" or "assistant"
	Snippet     string `json:"snippet"`     // ~120-char window around the first match, with the match preserved
	MatchOffset int    `json:"matchOffset"` // index of the match within Snippet (for highlighting)
}

// SearchProjectHistory does a case-insensitive substring search of every
// chat session's text history within a project. Empty/whitespace queries
// return no hits. Each session contributes at most 5 hits so a noisy match
// can't overwhelm the UI. Caps total result count at 200.
func (s *Studio) SearchProjectHistory(projectID, query string) ([]SearchHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []SearchHit{}, nil
	}
	needle := strings.ToLower(q)

	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	// Snapshot session IDs + names under p.mu so we can release the lock before
	// taking each session's lock — avoids holding two locks at once.
	p.mu.RLock()
	type sessRef struct {
		id   string
		sess *ChatSession
	}
	sessions := make([]sessRef, 0, len(p.sessions))
	for sid, sess := range p.sessions {
		// Don't read sess.Name here under p.mu — it's owned by session.mu.
		// Captured inside the per-session lock below instead.
		sessions = append(sessions, sessRef{id: sid, sess: sess})
	}
	p.mu.RUnlock()

	const (
		perSessionCap = 5
		totalCap      = 200
		snippetWindow = 60 // chars of context on each side of the match
	)
	hits := make([]SearchHit, 0, 32)

	for _, ref := range sessions {
		ref.sess.mu.RLock()
		sessName := ref.sess.Name
		filteredIdx := -1
		count := 0
		for _, c := range ref.sess.history {
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
			filteredIdx++
			lo := strings.ToLower(text)
			matchAt := strings.Index(lo, needle)
			if matchAt < 0 {
				continue
			}
			role := c.Role
			if role == "model" {
				role = "assistant"
			}
			start := max(matchAt-snippetWindow, 0)
			end := min(matchAt+len(needle)+snippetWindow, len(text))
			snippet := text[start:end]
			if start > 0 {
				snippet = "…" + snippet
			}
			if end < len(text) {
				snippet += "…"
			}
			matchOff := matchAt - start
			if start > 0 {
				matchOff += len("…") // 3 bytes in UTF-8; matchOff is byte-indexed
			}
			hits = append(hits, SearchHit{
				SessionID:   ref.id,
				SessionName: sessName,
				MessageIdx:  filteredIdx,
				Role:        role,
				Snippet:     snippet,
				MatchOffset: matchOff,
			})
			count++
			if count >= perSessionCap {
				break
			}
			if len(hits) >= totalCap {
				break
			}
		}
		ref.sess.mu.RUnlock()
		if len(hits) >= totalCap {
			break
		}
	}
	return hits, nil
}

// ListProjectMemory returns all project-scoped memory entries plus global
// entries the agent has stored for this project. Returns an empty list if
// the agent has not initialised a memory store yet (hasn't run once).
// SessionUsageInfo is the JSON-friendly per-session breakdown row.
type SessionUsageInfo struct {
	SessionID         string  `json:"sessionID"`
	SessionName       string  `json:"sessionName"`
	TotalCostUSD      float64 `json:"totalCostUSD"`
	TotalInputTokens  int     `json:"totalInputTokens"`
	TotalOutputTokens int     `json:"totalOutputTokens"`
	TotalCacheTokens  int     `json:"totalCacheTokens"`
	TurnCount         int     `json:"turnCount"`
	LastTurnAt        int64   `json:"lastTurnAt,omitempty"`
}

// ProjectUsageStatsInfo aggregates billing/cost across every session in a
// project. Used by the frontend's "usage" modal to show a per-project
// total + a per-session breakdown.
type ProjectUsageStatsInfo struct {
	TotalCostUSD      float64            `json:"totalCostUSD"`
	TotalInputTokens  int                `json:"totalInputTokens"`
	TotalOutputTokens int                `json:"totalOutputTokens"`
	TotalCacheTokens  int                `json:"totalCacheTokens"`
	TotalTurns        int                `json:"totalTurns"`
	TotalSessions     int                `json:"totalSessions"`
	Sessions          []SessionUsageInfo `json:"sessions"`
}

// ProjectUsageStats aggregates the per-session usage totals across every
// chat session in a project. Sessions with no recorded usage (never run)
// still appear in the breakdown with zero values so users can see the
// full session list. Sessions are sorted by TotalCostUSD desc then by
// TurnCount desc so the heaviest hitters surface first.
func (s *Studio) ProjectUsageStats(projectID string) (*ProjectUsageStatsInfo, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	out := &ProjectUsageStatsInfo{Sessions: []SessionUsageInfo{}}

	p.mu.RLock()
	type sessRef struct {
		sess *ChatSession
		id   string
	}
	refs := make([]sessRef, 0, len(p.sessions))
	for sid, sess := range p.sessions {
		refs = append(refs, sessRef{sess: sess, id: sid})
	}
	p.mu.RUnlock()

	for _, r := range refs {
		r.sess.mu.RLock()
		row := SessionUsageInfo{SessionID: r.id, SessionName: r.sess.Name}
		if r.sess.usage != nil {
			row.TotalCostUSD = r.sess.usage.TotalCostUSD
			row.TotalInputTokens = r.sess.usage.TotalInputTokens
			row.TotalOutputTokens = r.sess.usage.TotalOutputTokens
			row.TotalCacheTokens = r.sess.usage.TotalCacheTokens
			row.TurnCount = r.sess.usage.TurnCount
			row.LastTurnAt = r.sess.usage.LastTurnAt
		}
		r.sess.mu.RUnlock()
		out.TotalCostUSD += row.TotalCostUSD
		out.TotalInputTokens += row.TotalInputTokens
		out.TotalOutputTokens += row.TotalOutputTokens
		out.TotalCacheTokens += row.TotalCacheTokens
		out.TotalTurns += row.TurnCount
		out.Sessions = append(out.Sessions, row)
	}
	out.TotalSessions = len(out.Sessions)

	// Sort: highest cost first, then highest turn count, then by session
	// name for stable ordering when both totals are zero. Stable so map-
	// iteration randomness doesn't reshuffle equal-cost rows on every call.
	sort.SliceStable(out.Sessions, func(i, j int) bool {
		if out.Sessions[i].TotalCostUSD != out.Sessions[j].TotalCostUSD {
			return out.Sessions[i].TotalCostUSD > out.Sessions[j].TotalCostUSD
		}
		if out.Sessions[i].TurnCount != out.Sessions[j].TurnCount {
			return out.Sessions[i].TurnCount > out.Sessions[j].TurnCount
		}
		return out.Sessions[i].SessionName < out.Sessions[j].SessionName
	})
	return out, nil
}

func (s *Studio) ListProjectMemory(projectID string) ([]MemoryEntryInfo, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	p.mu.RLock()
	store := p.memoryStore
	p.mu.RUnlock()

	out := []MemoryEntryInfo{}
	if store == nil {
		return out, nil
	}
	// false = include globals too; the UI filters/sorts client-side.
	entries := store.List(false)
	for _, e := range entries {
		if e == nil {
			continue
		}
		out = append(out, MemoryEntryInfo{
			ID:            e.ID,
			Key:           e.Key,
			Content:       e.Content,
			Type:          string(e.Type),
			Tags:          e.Tags,
			Timestamp:     e.Timestamp.UnixMilli(),
			Project:       e.Project,
			Reinforcement: e.Reinforcement,
		})
	}
	return out, nil
}

// DeleteMemoryEntry removes a specific memory entry by ID. Returns an error
// if the memory store isn't initialised or the entry doesn't exist.
func (s *Studio) DeleteMemoryEntry(projectID, entryID string) error {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	store := p.memoryStore
	p.mu.RUnlock()
	if store == nil {
		return fmt.Errorf("memory not initialised for this project")
	}
	if !store.Remove(entryID) {
		return fmt.Errorf("memory entry not found: %s", entryID)
	}
	return nil
}

// ExportChat exports a single session's chat history as markdown. sessionID
// defaults to "default" if empty.
func (s *Studio) ExportChat(projectID, sessionID string) (string, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}

	p.mu.RLock()
	session, exists := p.sessions[sid]
	pName := p.Name // capture under RLock — RenameProject writes p.Name under p.mu.Lock()
	p.mu.RUnlock()
	if !exists {
		return "", fmt.Errorf("session not found: %s", sid)
	}

	session.mu.RLock()
	defer session.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("# Chat: " + pName + " / " + session.Name + "\n\n")

	for _, c := range session.history {
		text := ""
		for _, part := range c.Parts {
			// Skip thinking/reasoning parts — they contain raw model
			// deliberation that belongs in the internal loop, not in a
			// human-readable export.
			if part.Thought {
				continue
			}
			if part.Text != "" {
				text += part.Text
			}
		}
		if text == "" {
			// Skip function calls / responses — export is the human-readable
			// conversation view, not a full machine trace.
			continue
		}

		role := c.Role
		if role == "model" {
			role = "Assistant"
		} else {
			role = "User"
		}

		sb.WriteString("## " + role + "\n\n")
		sb.WriteString(text + "\n\n---\n\n")
	}

	return sb.String(), nil
}

// ExportProjectAllSessions returns a single markdown document containing
// EVERY session in the project, sorted most-recently-used first (matching
// the tab-bar order). Each session is delimited by a top-level header
// with the session name + date. Sessions with no visible history (only
// tool calls / function responses) are skipped — those would render as
// an empty section, which is just noise.
//
// Use case: archiving a full project's conversation snapshot for offline
// review or handoff. The single-blob format keeps it pasteable into a
// note-taking tool without juggling many files.
func (s *Studio) ExportProjectAllSessions(projectID string) (string, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}

	p.mu.RLock()
	pName := p.Name
	// Snapshot ordered by lastUsedAt desc (matches the sidebar / tab bar).
	type sessRef struct {
		sess       *ChatSession
		lastUsedAt int64
	}
	refs := make([]sessRef, 0, len(p.sessions))
	for _, sess := range p.sessions {
		// lastUsedAt is owned by session.mu (the agent loop bumps it under
		// session.mu at the start of every turn); read it there, not under
		// p.mu alone, to avoid a torn int64 read on 32-bit / a -race trip.
		sess.mu.RLock()
		lu := sess.lastUsedAt
		sess.mu.RUnlock()
		refs = append(refs, sessRef{sess: sess, lastUsedAt: lu})
	}
	p.mu.RUnlock()

	// Sort: highest lastUsedAt first; "default"-named never-used at bottom
	// matches ListChatSessions.
	sort.SliceStable(refs, func(i, j int) bool {
		ai, aj := refs[i].lastUsedAt, refs[j].lastUsedAt
		if ai != aj {
			if ai == 0 {
				return false
			}
			if aj == 0 {
				return true
			}
			return ai > aj
		}
		return refs[i].sess.CreatedAt.Before(refs[j].sess.CreatedAt)
	})

	var sb strings.Builder
	sb.WriteString("# " + pName + " — all sessions\n\n")
	fmt.Fprintf(&sb, "_Exported %s · %d session%s_\n\n",
		time.Now().Format("2006-01-02 15:04"), len(refs),
		plural2(len(refs), ""))

	included := 0
	for _, r := range refs {
		r.sess.mu.RLock()
		sessName := r.sess.Name
		// Materialise the per-session block locally so we can release the
		// session lock before appending to the outer builder.
		var local strings.Builder
		hadAny := false
		for _, c := range r.sess.history {
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
			role := c.Role
			if role == "model" {
				role = "Assistant"
			} else {
				role = "User"
			}
			local.WriteString("### " + role + "\n\n")
			local.WriteString(text + "\n\n")
			hadAny = true
		}
		createdAt := r.sess.CreatedAt
		r.sess.mu.RUnlock()
		if !hadAny {
			continue
		}
		sb.WriteString("---\n\n")
		sb.WriteString("## " + sessName + "\n\n")
		sb.WriteString("_Created " + createdAt.Format("2006-01-02") + "_\n\n")
		sb.WriteString(local.String())
		included++
	}

	if included == 0 {
		sb.WriteString("_No sessions with visible history yet._\n")
	}
	return sb.String(), nil
}

// plural2 is a no-frills English pluraliser for the export header. Doesn't
// share with git_status.go's plural() because that one prepends the count
// while this one only appends "s" when N != 1.
func plural2(n int, _ string) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// --- Terminal ---

// OpenTerminal opens a PTY terminal for a project.
func (s *Studio) OpenTerminal(projectID string) (string, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}

	termID := "term-" + uuid.New().String()[:8]
	// onExit drops the registry entry when the shell exits on its own so a
	// long session of spawn/exit cycles doesn't accumulate dead *Terminal
	// entries. The read loop has already reaped the child + closed the fd by
	// the time this fires, so this only frees the (tiny) map slot. Deleting a
	// missing key is a no-op, so the rare exit-before-insert case is benign.
	onExit := func(id string) {
		s.mu.Lock()
		delete(s.terminals, id)
		s.mu.Unlock()
	}
	t, err := newTerminalWithLogger(s.ctx, p.Directory, projectID, termID, s.LogEvent, onExit)
	if err != nil {
		return "", fmt.Errorf("open terminal: %w", err)
	}

	s.mu.Lock()
	s.terminals[termID] = t
	s.mu.Unlock()
	return termID, nil
}

// WriteTerminal sends input to a terminal.
func (s *Studio) WriteTerminal(termID, data string) error {
	s.mu.RLock()
	t, ok := s.terminals[termID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("terminal not found: %s", termID)
	}
	return t.Write(data)
}

// ResizeTerminal changes terminal dimensions.
func (s *Studio) ResizeTerminal(termID string, cols, rows int) error {
	s.mu.RLock()
	t, ok := s.terminals[termID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("terminal not found: %s", termID)
	}
	return t.Resize(uint16(cols), uint16(rows))
}

// CloseTerminal shuts down a terminal.
func (s *Studio) CloseTerminal(termID string) error {
	s.mu.Lock()
	t, ok := s.terminals[termID]
	if ok {
		delete(s.terminals, termID)
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("terminal not found: %s", termID)
	}
	t.Close()
	return nil
}

// --- Files ---

// FileEntry represents a file or directory in the project tree.
type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// ListDirectory returns the contents of a directory within a project.
func (s *Studio) ListDirectory(projectID, subPath string) ([]FileEntry, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	dir := filepath.Join(p.Directory, subPath)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	projAbs, _ := filepath.Abs(p.Directory)
	// Proper prefix check: must be the project root itself or have the
	// separator immediately after. Plain prefix matching would treat
	// "/home/user/foobar" as "inside" "/home/user/foo".
	if !isInsidePath(abs, projAbs) {
		return nil, fmt.Errorf("path outside project directory")
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}

	var result []FileEntry
	for _, e := range entries {
		// Skip hidden files and common noise directories.
		name := e.Name()
		if len(name) == 0 || name[0] == '.' || name == "node_modules" || name == "__pycache__" {
			continue
		}
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		result = append(result, FileEntry{
			Name:  name,
			Path:  filepath.Join(subPath, name),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}

	// Sort: directories first, then alphabetical.
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// ReadFileContent reads a file's text content within a project directory.
func (s *Studio) ReadFileContent(projectID, subPath string) (string, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}

	filePath := filepath.Join(p.Directory, subPath)
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}
	projAbs, _ := filepath.Abs(p.Directory)
	if !isInsidePath(abs, projAbs) {
		return "", fmt.Errorf("path outside project directory")
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}

	// Limit to 100KB to avoid sending huge files.
	if len(data) > 100*1024 {
		return string(data[:100*1024]) + "\n\n[truncated at 100KB]", nil
	}
	return string(data), nil
}

// RenameProject changes a project's display name.
func (s *Studio) RenameProject(id, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if len(newName) > 60 {
		newName = newName[:60]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.Lock()
	oldName := p.Name
	p.Name = newName
	p.mu.Unlock()
	s.saveConfig()
	s.auditProjectRenamed(oldName, newName)
	return nil
}

// --- Providers ---

// ProviderInfo describes a provider and its models for the frontend dropdown.
type ProviderInfo struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

// GetProviders returns the list of available LLM providers and models.
func (s *Studio) GetProviders() []*ProviderInfo {
	// Lineup mirrors internal/engine/client.AvailableModels (cloud providers)
	// so the picker never drifts from what the engine can actually construct.
	// glm-5.2 is the current flagship/default.
	return []*ProviderInfo{
		{ID: "glm", Name: "GLM (Zhipu AI)", Models: []string{
			"glm-5.2", "glm-5.1", "glm-5", "glm-5-turbo", "glm-4.7", "glm-4.5",
		}},
		{ID: "minimax", Name: "MiniMax", Models: []string{
			"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed",
		}},
		{ID: "kimi", Name: "Kimi for Coding", Models: []string{
			"kimi-for-coding",
		}},
		{ID: "deepseek", Name: "DeepSeek", Models: []string{
			"deepseek-v4-pro", "deepseek-v4-flash",
		}},
		{ID: "ollama", Name: "Ollama (Local)", Models: []string{
			"qwen3", "llama3.3", "deepseek-r1", "gemma3", "codellama", "phi4",
		}},
	}
}

// --- Settings ---

// GetSettings returns a copy of the current settings. Returning a copy (not
// the live pointer) avoids a data race: Wails serializes the return value
// after the function returns, by which point the lock has already been
// released, so any concurrent UpdateSettings could race on the same struct.
func (s *Studio) GetSettings() *StudioConfig {
	s.mu.RLock()
	c := *s.config
	s.mu.RUnlock()
	return &c
}

// UpdateSettings saves new settings. Also invalidates every project's cached
// client so freshly-entered API keys / URLs take effect on the next send —
// without this, users reported "I added my key but the agent still says
// 'configure key in settings'" until they restarted the app.
func (s *Studio) UpdateSettings(cfg StudioConfig) error {
	// Trim whitespace from keys/URLs so a paste with leading/trailing spaces
	// doesn't silently break authentication.
	cfg.Settings.GLMKey = strings.TrimSpace(cfg.Settings.GLMKey)
	cfg.Settings.MiniMaxKey = strings.TrimSpace(cfg.Settings.MiniMaxKey)
	cfg.Settings.KimiKey = strings.TrimSpace(cfg.Settings.KimiKey)
	cfg.Settings.DeepSeekKey = strings.TrimSpace(cfg.Settings.DeepSeekKey)
	cfg.Settings.OllamaURL = strings.TrimSpace(cfg.Settings.OllamaURL)
	if cfg.Settings.DefaultThinkingBudget < 0 {
		cfg.Settings.DefaultThinkingBudget = 0
	}
	// Mirror the per-project budget validation: clamp negatives to 0 and
	// reject typo-sized values (10000000 instead of 100). Both are silent
	// clamps rather than errors so a malformed UI input doesn't block save.
	if cfg.Settings.DefaultBudgetUSD < 0 {
		cfg.Settings.DefaultBudgetUSD = 0
	}
	if cfg.Settings.DefaultBudgetUSD > 100000 {
		cfg.Settings.DefaultBudgetUSD = 100000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// iter 800+: audit each changed field to the event log so users can
	// answer "wait, why is X enabled now?" after a settings change. API key
	// VALUES are never logged — see diffSettings for the secrets policy.
	oldSettings := s.config.Settings
	s.config.Settings = cfg.Settings
	for _, p := range s.projects {
		p.mu.Lock()
		p.resetClientLocked() // close + clear so the next send re-inits with new settings
		p.mu.Unlock()
	}
	if err := s.config.Save(); err != nil {
		return err
	}
	s.logSettingsChanges(oldSettings, cfg.Settings)
	return nil
}

// ApplyDefaultToProjects updates all projects to use the current default provider and model.
// Called from the frontend after the user changes the default provider in Settings.
func (s *Studio) ApplyDefaultToProjects() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	provider := s.config.Settings.DefaultProvider
	model := s.config.Settings.DefaultModel

	for _, p := range s.projects {
		p.mu.Lock()
		p.Provider = provider
		p.Model = model
		p.resetClientLocked() // close + clear so the next send re-inits with the new provider
		p.mu.Unlock()
	}

	s.saveConfig()
	return nil
}

// --- Dispatch ---

// Dispatch sends a task from one project to another (async -- results via events).
// fromSessionID identifies which chat session the dispatch originated from so
// the result can route back to the same chat, not the default one. Empty
// string falls back to "default" for backward compat with older bindings.
func (s *Studio) Dispatch(fromID, toID, fromSessionID, task string) error {
	if fromID == toID {
		return fmt.Errorf("cannot dispatch to self; pick a different target project")
	}
	if strings.TrimSpace(task) == "" {
		return fmt.Errorf("task description cannot be empty")
	}
	fromSid := fromSessionID
	if fromSid == "" {
		fromSid = "default"
	}
	s.mu.RLock()
	from, okFrom := s.projects[fromID]
	to, okTo := s.projects[toID]
	settings := s.config.Settings
	s.mu.RUnlock()
	if !okFrom {
		return fmt.Errorf("source project not found: %s", fromID)
	}
	if !okTo {
		return fmt.Errorf("target project not found: %s", toID)
	}
	dispatchFn := s.dispatchInternal
	if s.testDispatchFn != nil {
		dispatchFn = s.testDispatchFn
	}
	// Go 1.25 wg.Go: same lifecycle scoping in one call.
	//
	// iter 970+: panic barrier. dispatchInternal does network I/O against a
	// second project's LLM client; any panic there (provider library bug,
	// nil-deref on a race) previously killed the whole app. Now surfaces in
	// the event log.
	s.wg.Go(func() {
		defer recoverPanic("dispatch", s.LogEvent)
		dispatchFn(from, to, fromSid, task, settings)
	})
	return nil
}

func (s *Studio) dispatchInternal(from, to *Project, fromSid, task string, settings Settings) {
	// Capture names under the project locks so we don't race with RenameProject,
	// which writes Name under p.mu.Lock(). Directory is set once in AddProject
	// and never mutated, so it's safe to read without a lock.
	from.mu.RLock()
	fromName := from.Name
	from.mu.RUnlock()
	to.mu.RLock()
	toName := to.Name
	to.mu.RUnlock()

	emitError := func(err error) {
		// Humanize the error (401/429/context length → friendlier guidance)
		// so the dispatch-result card in the source chat isn't a raw stack
		// like "API error 401". Mirrors the main SendMessage error path.
		wailsRuntime.EventsEmit(s.ctx, EventDispatchComplete, map[string]any{
			"from": from.ID, "to": to.ID, "toName": toName,
			"sessionID": fromSid,
			"success":   false, "error": humanizeAPIError(err),
		})
	}

	if err := to.initClient(settings); err != nil {
		emitError(err)
		return
	}

	prompt := fmt.Sprintf("## Dispatched by\n%s (%s)\n\n## Task\n%s", fromName, from.Directory, task)

	to.mu.RLock()
	c := to.client
	to.mu.RUnlock()

	if c == nil {
		emitError(fmt.Errorf("client not initialized for project %s", toName))
		return
	}

	// Use the Wails context so dispatch is cancelled on app shutdown.
	resp, err := c.SendMessage(s.ctx, prompt)
	if err != nil {
		emitError(err)
		return
	}
	if resp == nil {
		emitError(fmt.Errorf("nil response from %s", toName))
		return
	}
	collected, err := resp.Collect()
	if err != nil {
		emitError(err)
		return
	}
	wailsRuntime.EventsEmit(s.ctx, EventDispatchComplete, map[string]any{
		"from": from.ID, "to": to.ID, "toName": toName,
		"sessionID": fromSid,
		"success":   true, "result": collected.Text,
	})
}

// --- Internal ---

// isInsidePath returns true if path is equal to root or a descendant of it,
// using a proper path-separator-aware prefix check. Plain string prefix
// matching would incorrectly accept "/home/user/foobar" for root
// "/home/user/foo" — use this instead.
func isInsidePath(path, root string) bool {
	if path == root {
		return true
	}
	sep := string(filepath.Separator)
	// Ensure root has a trailing separator so /foo doesn't match /foobar.
	if !strings.HasSuffix(root, sep) {
		root += sep
	}
	return strings.HasPrefix(path, root)
}

// saveConfig persists project configs to disk. Caller MUST hold s.mu (Lock
// or RLock) — every existing caller in this file acquires s.mu.Lock itself
// or runs from Startup before any concurrency exists. Background paths that
// don't hold s.mu (e.g. agent goroutines bumping lastUsedAt) must use
// saveConfigAsync, which takes its own read lock and writes outside of it.
func (s *Studio) saveConfig() {
	s.config.Projects = s.config.Projects[:0]
	for _, p := range s.projects {
		s.config.Projects = append(s.config.Projects, p.ToConfig())
	}
	if err := s.config.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "gokin-studio: failed to save config: %v\n", err)
		s.logf("error", "config", "failed to save config: %v", err)
	}
}

// saveConfigAsync is the safe, unlocked entry point. It acquires s.mu.RLock
// itself to snapshot, then writes the file with the lock released. Intended
// for background paths (agent goroutines) that don't already hold s.mu.
func (s *Studio) saveConfigAsync() {
	s.mu.RLock()
	projects := make([]ProjectConfig, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, p.ToConfig())
	}
	// Read Settings under the lock — UpdateSettings can race on s.config.Settings
	// concurrently (struct assignment is not atomic for multi-field structs).
	settings := s.config.Settings
	s.mu.RUnlock()
	// Clone the entire StudioConfig so we don't race with readers that read
	// s.config (e.g. GetSettings) while the yaml.Marshal goroutine touches
	// the Projects slice. Also avoids a race where another saveConfigAsync
	// run updates s.config.Projects concurrently.
	cfg := &StudioConfig{
		Settings: settings,
		Projects: projects,
	}
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "gokin-studio: failed to save config: %v\n", err)
		s.logf("error", "config", "failed to save config (async): %v", err)
	}
}
