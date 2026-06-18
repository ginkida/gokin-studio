package studio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	"github.com/ginkida/gokin-studio/internal/engine/config"
	"github.com/ginkida/gokin-studio/internal/engine/memory"
	"github.com/ginkida/gokin-studio/internal/engine/plan"
	"github.com/ginkida/gokin-studio/internal/engine/tasks"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"google.golang.org/genai"
)

// Project represents a single project workspace.
type Project struct {
	ID             string
	Name           string
	Directory      string
	Provider       string
	Model          string
	SystemPrompt   string
	Temperature    float32
	MaxTokens      int
	ThinkingMode   string  // "" = auto, "enabled", "disabled"
	ThinkingBudget int32   // 0 = use default (4096) when enabled
	PermissionMode string  // "" / "auto" = proceed; "ask" = confirm before changes
	BudgetUSD      float64 // 0 = no budget set; otherwise per-month spend cap in USD
	// EnforceBudget, when true, blocks new SendMessage calls once cumulative
	// cost (cached, seeded from ProjectUsageStats on first need, bumped on
	// chat:complete) reaches BudgetUSD. Requires BudgetUSD > 0 to take effect.
	EnforceBudget bool
	Pinned        bool // true = anchor to top of sidebar regardless of LastUsedAt

	// testEmitter, when non-nil, replaces wailsRuntime.EventsEmit so unit tests
	// can record emitted events without a running Wails application.
	testEmitter func(event string, data any)
	// retryInitialDelay overrides the 2s first-retry backoff in sendWithRetry.
	// Set to a small value (e.g. time.Millisecond) in tests to keep them fast.
	retryInitialDelay time.Duration
	// testTurnTimeout overrides the 30-minute per-turn ceiling. Test-only;
	// set to e.g. 50*time.Millisecond to exercise the DeadlineExceeded
	// codepath without actually waiting 30 minutes. Production callers
	// leave it at zero and get the default 30-minute ceiling.
	testTurnTimeout time.Duration

	studio     *Studio // back-reference for inter-project communication
	client     client.Client
	registry   *tools.Registry
	sessions   map[string]*ChatSession // sessionID → session
	lastUsedAt int64                   // unix millis, bumped on every agent turn

	// Long-lived memory and plan state, shared across all sessions of this
	// project. Lazy-initialized on first client setup so they only exist for
	// projects that actually run the agent.
	memoryStore     *memory.Store
	projectLearning *memory.ProjectLearning
	planManager     *plan.Manager
	taskManager     *tasks.Manager // background-shell registry for bash/kill_shell/task_output/task_stop

	// pinnedContext holds the text most recently pinned via the pin_context tool.
	// It is appended to the system instruction at the start of every SendMessage
	// so it survives history compaction. Protected by mu.
	pinnedContext string

	// iter 1040+: cached cumulative cost across every session in this project,
	// in USD. Seeded LAZILY on first need (via ProjectUsageStats which walks
	// every session JSON file — O(N) but happens once), then bumped O(1) on
	// each chat:complete. Used by the strict budget enforcement path so the
	// pre-flight check in SendMessage doesn't pay the cost of a full disk
	// walk on every send. Protected by costMu (not mu) so the SendMessage
	// pre-flight doesn't contend with the long-running mu read locks.
	cachedTotalCostUSD float64
	costSeeded         bool
	costMu             sync.Mutex

	// semanticValidators checks written/edited files for logical issues after
	// each successful write tool call (go_quality, security, shell, test_quality).
	// Warnings are appended to the tool result so the model can self-correct.
	semanticValidators *tools.SemanticValidatorRegistry

	// Per-session file trackers for post-compaction continuation hints.
	// keyed by sessionID; allocated on demand in SendMessage.
	readTrackers  map[string]*tools.FileReadTracker
	writeTrackers map[string]*tools.FileWriteTracker

	mu sync.RWMutex
}

// ProjectInfo is the JSON-friendly project representation sent to the frontend.
type ProjectInfo struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Directory      string  `json:"directory"`
	DirectoryOK    bool    `json:"directoryOK"`
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	Active         bool    `json:"active"`
	LastUsedAt     int64   `json:"lastUsedAt,omitempty"`
	GitBranch      string  `json:"gitBranch"`
	SystemPrompt   string  `json:"systemPrompt,omitempty"`
	Temperature    float32 `json:"temperature,omitempty"`
	MaxTokens      int     `json:"maxTokens,omitempty"`
	ThinkingMode   string  `json:"thinkingMode,omitempty"`
	ThinkingBudget int32   `json:"thinkingBudget,omitempty"`
	PermissionMode string  `json:"permissionMode,omitempty"`
	BudgetUSD      float64 `json:"budgetUSD,omitempty"`
	EnforceBudget  bool    `json:"enforceBudget,omitempty"`
	Pinned         bool    `json:"pinned,omitempty"`
	ContextWindow  int     `json:"contextWindow"`
	PinnedContext  string  `json:"pinnedContext,omitempty"`
}

// ChatMessage is a single chat entry for the frontend.
type ChatMessage struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	ToolName  string `json:"toolName,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// NewProject creates a project from config, loading all persisted sessions.
func NewProject(pc ProjectConfig) *Project {
	p := &Project{
		ID:             pc.ID,
		Name:           pc.Name,
		Directory:      pc.Directory,
		Provider:       pc.Provider,
		Model:          pc.Model,
		SystemPrompt:   pc.SystemPrompt,
		Temperature:    pc.Temperature,
		MaxTokens:      pc.MaxTokens,
		ThinkingMode:   pc.ThinkingMode,
		ThinkingBudget: pc.ThinkingBudget,
		PermissionMode: pc.PermissionMode,
		BudgetUSD:      pc.BudgetUSD,
		EnforceBudget:  pc.EnforceBudget,
		Pinned:         pc.Pinned,
		lastUsedAt:     pc.LastUsedAt,
		sessions:       make(map[string]*ChatSession),
	}

	// Load any persisted sessions from disk, preserving display names.
	// Pre-load the per-project session-pin map once so each session's Pinned
	// field can be set during the initial loop without a per-session disk hit.
	pinned, _ := loadPinnedSessions(pc.ID)
	diskSessions := ListHistoryFilesForProject(pc.ID)
	defaultOnDisk := false
	for _, sid := range diskSessions {
		hist, err := LoadHistory(pc.ID + "_" + sid)
		if err != nil || hist == nil {
			continue
		}
		name := LoadHistoryName(pc.ID + "_" + sid)
		if name == "" {
			if sid == "default" {
				name = "Chat 1"
			} else {
				name = "Chat " + sid[:4]
			}
		}
		sess := NewChatSession(name)
		sess.ID = sid
		sess.history = hist
		// Restore fork lineage so the UI can show "↳ source name" after a
		// restart, not just within the session that did the fork.
		sess.ParentID = LoadHistoryParent(pc.ID + "_" + sid)
		// Restore aggregated usage so per-project stats survive restart.
		sess.usage = LoadHistoryUsage(pc.ID + "_" + sid)
		// Restore pin state so the tab list comes up with the user's
		// previous ordering on reopen, not lastUsedAt-default.
		sess.Pinned = pinned[sid]
		p.sessions[sid] = sess
		if sid == "default" {
			defaultOnDisk = true
		}
	}

	// If nothing was loaded, create an empty "default" session so the project
	// always has at least one chat. If the user had previously deleted default
	// but kept other sessions, we respect that — no phantom "Chat 1" returns.
	if len(p.sessions) == 0 {
		defaultSession := NewChatSession("Chat 1")
		defaultSession.ID = "default"
		// Backfill from legacy single-file history if present (pre-sessions
		// format). Migrate it to the per-session path AND remove the legacy
		// file so it never re-adopts on the next boot.
		if legacy, _ := LoadHistory(pc.ID); legacy != nil {
			defaultSession.history = legacy
			_ = SaveHistoryWithName(pc.ID+"_default", "Chat 1", legacy)
			DeleteHistory(pc.ID)
		}
		p.sessions["default"] = defaultSession
	} else if legacy, _ := LoadHistory(pc.ID); legacy != nil && !defaultOnDisk {
		// Legacy single-file history exists but no "default" session file.
		// If the user explicitly deleted the default session in a previous
		// boot, they don't want Chat 1 to reappear every time — so we only
		// adopt the legacy file the FIRST time we encounter it, then drop
		// it from disk so the next boot respects the user's state.
		defaultSession := NewChatSession("Chat 1")
		defaultSession.ID = "default"
		defaultSession.history = legacy
		_ = SaveHistoryWithName(pc.ID+"_default", "Chat 1", legacy)
		DeleteHistory(pc.ID)
		p.sessions["default"] = defaultSession
	}

	// Pre-load pinned context from disk so the badge shows on first ListProjects,
	// before the agent's initClient has run for this project.
	pinPath := filepath.Join(pc.Directory, ".gokin", "pinned_context.md")
	if data, err := os.ReadFile(pinPath); err == nil && len(data) > 0 {
		p.pinnedContext = string(data)
	}

	return p
}

// GetSession returns a session by ID, or the default session.
func (p *Project) GetSession(sessionID string) *ChatSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if sessionID == "" {
		sessionID = "default"
	}
	if s, ok := p.sessions[sessionID]; ok {
		return s
	}
	return p.sessions["default"]
}

// Info returns a JSON-safe snapshot.
func (p *Project) Info() *ProjectInfo {
	// Read git branch without holding the lock since it spawns a subprocess.
	branch := p.gitBranch()
	// Verify the project directory still exists. Users often move/delete
	// folders outside the app; we need to flag this so the UI can warn
	// instead of letting every tool call fail mysteriously.
	dirOK := false
	if info, err := os.Stat(p.Directory); err == nil && info.IsDir() {
		dirOK = true
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	// Active = true if any session is active.
	anyActive := false
	for _, s := range p.sessions {
		s.mu.RLock()
		if s.active {
			anyActive = true
		}
		s.mu.RUnlock()
		if anyActive {
			break
		}
	}

	return &ProjectInfo{
		ID:             p.ID,
		Name:           p.Name,
		Directory:      p.Directory,
		DirectoryOK:    dirOK,
		Provider:       p.Provider,
		Model:          p.Model,
		Active:         anyActive,
		LastUsedAt:     p.lastUsedAt,
		SystemPrompt:   p.SystemPrompt,
		Temperature:    p.Temperature,
		MaxTokens:      p.MaxTokens,
		ThinkingMode:   p.ThinkingMode,
		ThinkingBudget: p.ThinkingBudget,
		PermissionMode: p.PermissionMode,
		BudgetUSD:      p.BudgetUSD,
		EnforceBudget:  p.EnforceBudget,
		Pinned:         p.Pinned,
		GitBranch:      branch,
		ContextWindow:  contextWindowForProvider(p.Provider, p.Model),
		PinnedContext:  p.pinnedContext,
	}
}

// initMemoryAndPlan builds the project's persistent memory store, plan
// manager, and project-learning store, then injects them into the
// corresponding tools in the registry so the agent can actually use them.
// Idempotent: safe to call on every initClient because the setters overwrite
// existing references with the same value.
func (p *Project) initMemoryAndPlan(reg *tools.Registry) {
	cfgDir := configDir()

	if p.memoryStore == nil {
		store, err := memory.NewStore(cfgDir, p.Directory, 10000)
		if err == nil {
			p.memoryStore = store
		}
	}
	if p.projectLearning == nil {
		learning, err := memory.NewProjectLearning(p.Directory)
		if err == nil {
			p.projectLearning = learning
		}
	}
	if p.planManager == nil {
		// enabled=true, requireApproval=false — studio UI surfaces plans as
		// informational cards, no out-of-band approval flow yet.
		p.planManager = plan.NewManager(true, false)
	}
	if p.taskManager == nil {
		// Per-project task registry for backgrounded bash runs. Keeps shell
		// processes isolated between projects and lets kill_shell /
		// task_output / task_stop reference them by ID.
		p.taskManager = tasks.NewManager(p.Directory)
	}

	if p.memoryStore != nil {
		if t, ok := reg.Get("memory"); ok {
			if mt, ok := t.(*tools.MemoryTool); ok {
				mt.SetStore(p.memoryStore)
			}
		}
	}
	if p.projectLearning != nil {
		if t, ok := reg.Get("memorize"); ok {
			if mt, ok := t.(*tools.MemorizeTool); ok {
				mt.SetLearning(p.projectLearning)
			}
		}
	}
	if p.planManager != nil {
		for _, name := range []string{"enter_plan_mode", "update_plan_progress", "get_plan_status", "exit_plan_mode"} {
			t, ok := reg.Get(name)
			if !ok {
				continue
			}
			switch pt := t.(type) {
			case *tools.EnterPlanModeTool:
				pt.SetManager(p.planManager)
			case *tools.UpdatePlanProgressTool:
				pt.SetManager(p.planManager)
			case *tools.GetPlanStatusTool:
				pt.SetManager(p.planManager)
			case *tools.ExitPlanModeTool:
				pt.SetManager(p.planManager)
			}
		}
	}

	// Cross-project shared memory (studio-scoped, in-process). Lets an agent
	// in project A leave a note that an agent in project B can read via the
	// shared_memory tool. Lives on the Studio struct so every project wires
	// to the same instance.
	if p.studio != nil && p.studio.sharedMemory != nil {
		if t, ok := reg.Get("shared_memory"); ok {
			if sm, ok := t.(*tools.SharedMemoryTool); ok {
				sm.SetMemory(p.studio.sharedMemory)
				sm.SetAgentID(p.ID)
			}
		}
	}

	// Background-task manager: shared by bash (for `run_in_background`),
	// kill_shell, task_output, and task_stop so they all reference the
	// same per-project registry of backgrounded shell processes.
	if p.taskManager != nil {
		if t, ok := reg.Get("bash"); ok {
			if bt, ok := t.(*tools.BashTool); ok {
				bt.SetTaskManager(p.taskManager)
			}
		}
		if t, ok := reg.Get("kill_shell"); ok {
			if kt, ok := t.(*tools.KillShellTool); ok {
				kt.SetManager(p.taskManager)
			}
		}
		if t, ok := reg.Get("task_output"); ok {
			if tot, ok := t.(*tools.TaskOutputTool); ok {
				tot.SetManager(p.taskManager)
			}
		}
		if t, ok := reg.Get("task_stop"); ok {
			if tst, ok := t.(*tools.TaskStopTool); ok {
				tst.SetManager(p.taskManager)
			}
		}
	}

	// Build the semantic validator registry once per project. Validators run
	// after every successful write/edit call and append warnings to the tool
	// result so the model can self-correct without a separate build step.
	if p.semanticValidators == nil {
		svr := tools.NewSemanticValidatorRegistry()
		for _, v := range tools.DefaultSemanticValidators() {
			svr.Register(v)
		}
		p.semanticValidators = svr
	}

	// Wire pin_context so the agent can attach a persistent note to the system
	// prompt that survives history compaction. The updater stores the pinned text
	// in p.pinnedContext; SendMessage applies it to the client's system instruction
	// before every agent run.
	if t, ok := reg.Get("pin_context"); ok {
		if pct, ok := t.(*tools.PinContextTool); ok {
			pct.SetWorkDir(p.Directory)
			pct.SetUpdater(func(content string) {
				p.mu.Lock()
				p.pinnedContext = content
				p.mu.Unlock()
			})
			// Restore any pin written by a prior session for this project.
			pct.LoadPersistedPin()
		}
	}
}

func (p *Project) gitBranch() string {
	cmd := exec.Command("git", "-C", p.Directory, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// emitEvent is a thin wrapper around wailsRuntime.EventsEmit. When
// testEmitter is set (non-nil), events are routed there instead so
// unit tests can run without a live Wails application.
//
// Tee'd side-effect: chat:error and chat:retry events are also appended to
// the studio event log so users can review backend failures via the
// Diagnostics UI. We sniff the data for a Text field; anything else logs
// just the event name. Best-effort — log failure here must not crash the
// caller, hence the defer/recover guard around the log call.
func (p *Project) emitEvent(wailsCtx context.Context, event string, data any) {
	if event == EventChatError || event == EventChatRetry {
		func() {
			defer func() { _ = recover() }()
			if p.studio != nil {
				msg := summarizeEventForLog(data)
				level := "error"
				if event == EventChatRetry {
					level = "warn"
				}
				// iter 985+: snapshot p.Name under RLock. emitEvent is
				// called from many code paths including ones that don't
				// hold p.mu (e.g. inside the agent loop after a partial
				// snapshot). RenameProject writes p.Name under p.mu.Lock,
				// so an unlocked read is a documented race.
				p.mu.RLock()
				pName := p.Name
				p.mu.RUnlock()
				p.studio.LogEvent(level, "agent", fmt.Sprintf("[%s] %s", pName, msg))
			}
		}()
	}
	if p.testEmitter != nil {
		p.testEmitter(event, data)
		return
	}
	wailsRuntime.EventsEmit(wailsCtx, event, data)
}

// summarizeEventForLog extracts a human-readable message from a chat
// event payload. Falls back to a generic placeholder if the shape is
// unfamiliar. Used by emitEvent for the event-log tee.
func summarizeEventForLog(data any) string {
	if cte, ok := data.(ChatTextEvent); ok {
		return cte.Text
	}
	if m, ok := data.(map[string]any); ok {
		if v, ok := m["text"].(string); ok {
			return v
		}
		if v, ok := m["message"].(string); ok {
			return v
		}
		if v, ok := m["error"].(string); ok {
			return v
		}
	}
	return fmt.Sprintf("(payload type %T)", data)
}

// initClient creates the LLM client for this project.
// resetClientLocked closes the current per-project client (releasing its idle
// HTTP connections) and clears it so the next SendMessage rebuilds it with the
// new config. Caller MUST hold p.mu. With NewClientNoPool each project owns its
// client instance and there's no shared pool to clean it up, so invalidation
// has to close it explicitly. Close() only touches the client's own transport
// (CloseIdleConnections / nil-out), so it's safe under p.mu, and an in-flight
// turn keeps its own snapshotted client reference (idle-conn close leaves an
// active streaming request alone).
func (p *Project) resetClientLocked() {
	if p.client != nil {
		_ = p.client.Close()
		p.client = nil
	}
}

func (p *Project) initClient(settings Settings) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		return nil
	}

	provider := p.Provider
	if provider == "" {
		provider = settings.DefaultProvider
	}
	model := p.Model
	if model == "" {
		model = settings.DefaultModel
	}
	// Migrate legacy Kimi model names that pointed at the old moonshot endpoint.
	// The new api.kimi.com/coding backend only serves "kimi-for-coding".
	if provider == "kimi" {
		switch model {
		case "", "kimi-k2.5", "kimi-k2-thinking-turbo", "kimi-k2-turbo", "kimi-k2-turbo-preview",
			"kimi-latest", "moonshot-v1-auto", "moonshot-v1-128k", "moonshot-v1-8k", "moonshot-v1-32k":
			model = "kimi-for-coding"
		}
	}

	cfg := &config.Config{}
	cfg.API.ActiveProvider = provider
	cfg.Model.Name = model
	cfg.Model.Temperature = p.Temperature
	cfg.Model.MaxOutputTokens = int32(p.MaxTokens)

	switch provider {
	case "glm":
		cfg.API.GLMKey = firstNonEmpty(settings.GLMKey, os.Getenv("GLM_API_KEY"))
	case "minimax":
		cfg.API.MiniMaxKey = firstNonEmpty(settings.MiniMaxKey, os.Getenv("MINIMAX_API_KEY"))
	case "kimi":
		cfg.API.KimiKey = firstNonEmpty(settings.KimiKey, os.Getenv("KIMI_API_KEY"))
	case "deepseek":
		cfg.API.DeepSeekKey = firstNonEmpty(settings.DeepSeekKey, os.Getenv("DEEPSEEK_API_KEY"))
	case "ollama":
		cfg.API.OllamaBaseURL = firstNonEmpty(settings.OllamaURL, os.Getenv("OLLAMA_HOST"), "http://localhost:11434")
	}

	// Apply thinking configuration. ThinkingMode="" (auto) defaults to enabled
	// for Kimi (always reasoning) and for DeepSeek V4 Pro (the pro variant's
	// distinguishing feature — flash variant doesn't support thinking, so
	// auto stays off there). All other providers/models off in auto mode.
	switch p.ThinkingMode {
	case "enabled":
		cfg.Model.EnableThinking = true
		cfg.Model.ThinkingBudget = p.ThinkingBudget
		if cfg.Model.ThinkingBudget <= 0 {
			cfg.Model.ThinkingBudget = 4096
		}
	case "disabled":
		// Explicitly off — nothing to set.
	default: // "" auto
		if provider == "kimi" || (provider == "deepseek" && model == "deepseek-v4-pro") {
			cfg.Model.EnableThinking = true
			cfg.Model.ThinkingBudget = 4096
		}
	}

	// NewClientNoPool (not NewClient): each project owns a dedicated client
	// instance. The shared connection pool is keyed only by provider:model, so
	// two projects on the same model would otherwise alias to ONE client and
	// clobber each other's system prompt / pinned context (project B's
	// SetSystemInstruction overwriting project A's). Studio caches p.client per
	// project and rebuilds it on settings/provider changes, so it never needs
	// the pool.
	c, err := client.NewClientNoPool(context.Background(), cfg, model)
	if err != nil {
		return fmt.Errorf("init client (%s/%s): %w", provider, model, err)
	}

	// Set available tools on the client, filtered by provider capability.
	reg := tools.DefaultRegistry(p.Directory)
	p.registry = reg
	p.client = c

	// Wire messenger for inter-project agent coordination.
	if p.studio != nil {
		if askAgent, ok := reg.Get("ask_agent"); ok {
			if aat, ok := askAgent.(*tools.AskAgentTool); ok {
				aat.SetMessenger(NewStudioMessenger(p.studio, p.ID))
			}
		}
		// Wire ask_user so the agent's clarification questions bubble up as
		// chat:ask_user events the frontend can render as an interactive card.
		// The handler reads project/session from the per-turn context.
		if p.studio.ctx != nil {
			if askUser, ok := reg.Get("ask_user"); ok {
				if aut, ok := askUser.(*tools.AskUserTool); ok {
					aut.SetHandler(p.studio.makeAskUserHandler(p.studio.ctx))
				}
			}
		}
	}

	// Wire persistent memory + plan state. Errors creating the stores are
	// non-fatal — the tools themselves return "store not configured" and the
	// agent proceeds without them, same as before this hook-up.
	p.initMemoryAndPlan(reg)

	// Apply system prompt (user-configured or sensible default), plus the
	// "ask before changes" directive when the project is in that mode.
	sysPrompt := p.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = defaultSystemPrompt(p.Directory, p.Name)
	}
	c.SetSystemInstruction(sysPrompt + permissionDirective(p.PermissionMode))

	sets := toolSetsForProvider(provider)
	toolDecls := reg.FilteredDeclarations(sets...)
	if len(toolDecls) > 0 {
		c.SetTools([]*genai.Tool{{FunctionDeclarations: toolDecls}})
	}

	return nil
}

// SendMessage runs the agent loop and emits events to the frontend.
func (p *Project) SendMessage(wailsCtx context.Context, message string, settings Settings, sessionID ...string) {
	sid := "default"
	if len(sessionID) > 0 && sessionID[0] != "" {
		sid = sessionID[0]
	}
	// Top-level panic barrier so an unexpected crash in this function or any
	// of its helpers (stream parsing, history manipulation, event emit) can't
	// take down the Wails process. safeToolExecute covers tool panics; this
	// handles everything else. On panic we surface a chat:error so the user
	// at least sees something and the session unsticks.
	defer func() {
		if r := recover(); r != nil {
			p.emitEvent(wailsCtx, EventChatError, ChatTextEvent{
				ProjectID: p.ID,
				SessionID: sid,
				Text:      fmt.Sprintf("Internal error: %v — please check the application logs and report it", r),
			})
			// Leave session.active=false via existing defer below. If we
			// panicked before that defer registered, force it here.
			if s := p.GetSession(sid); s != nil {
				s.mu.Lock()
				s.active = false
				s.cancelFn = nil
				s.mu.Unlock()
			}
			// Emit project:status idle so the sidebar/status dot goes grey
			// instead of staying green forever.
			p.emitEvent(wailsCtx, EventProjectStatus, map[string]any{
				"id": p.ID, "sessionID": sid, "status": "idle", "gitBranch": p.gitBranch(),
			})
		}
	}()
	session := p.GetSession(sid)
	if session == nil {
		p.emitEvent(wailsCtx, EventChatError, ChatTextEvent{
			ProjectID: p.ID, SessionID: sid, Text: "session not found: " + sid,
		})
		return
	}
	// iter 1040+: strict budget enforcement. Opt-in via Project.EnforceBudget.
	// Pre-flight check refuses new turns once cumulative cost across every
	// session in this project meets/exceeds BudgetUSD. The cache is seeded
	// lazily from ProjectUsageStats on first need (O(N sessions), happens
	// once per app run) and bumped at chat:complete (O(1)). This catches
	// the AFK-during-long-agent-run case: warning toasts (iter 610+) help
	// when the user is watching, but a hard block is the only thing that
	// stops a runaway during sleep.
	p.mu.RLock()
	enforce := p.EnforceBudget
	budget := p.BudgetUSD
	p.mu.RUnlock()
	if enforce && budget > 0 {
		spent := p.totalCostUSD()
		if spent >= budget {
			p.emitEvent(wailsCtx, EventChatError, ChatTextEvent{
				ProjectID: p.ID,
				SessionID: sid,
				Text: fmt.Sprintf(
					"Budget reached: spent $%.4f of $%.2f limit. Raise the budget in the project's budget editor, disable strict enforcement, or reset usage to continue.",
					spent, budget),
			})
			return
		}
	}
	if err := p.initClient(settings); err != nil {
		p.emitEvent(wailsCtx, EventChatError, ChatTextEvent{
			ProjectID: p.ID, SessionID: sid, Text: err.Error(),
		})
		return
	}

	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		p.emitEvent(wailsCtx, EventChatError, ChatTextEvent{
			ProjectID: p.ID, SessionID: sid, Text: "Agent is already running in this chat. Wait for it to finish or stop it first.",
		})
		return
	}
	session.active = true
	now := time.Now().UnixMilli()
	session.lastUsedAt = now
	// 30-minute hard ceiling for an entire agent run. Long tasks (refactors,
	// large explorations) can legitimately take this long. User can always
	// stop manually via the chat header.
	turnTimeout := 30 * time.Minute
	if p.testTurnTimeout > 0 {
		turnTimeout = p.testTurnTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnTimeout)
	session.cancelFn = cancel
	session.mu.Unlock()

	// Snapshot project state + bump the project's lastUsedAt under p.mu.
	// CRITICAL: this runs OUTSIDE the session.mu section above. Readers like
	// ListChatSessions / Info() / CreateChatSession lock p.mu THEN session.mu,
	// so taking p.mu while holding session.mu here would be a lock-order
	// inversion (a latent AB-BA deadlock against any of those readers).
	// Acquiring the two locks separately keeps one global p.mu→session.mu
	// order everywhere.
	//
	// iter 985+: p.Name is snapshotted here too so the post-lock
	// defaultSystemPrompt() call below doesn't race with RenameProject.
	p.mu.Lock()
	p.lastUsedAt = now
	c := p.client
	reg := p.registry
	pinnedCtx := p.pinnedContext
	sysPr := p.SystemPrompt
	pName := p.Name
	permMode := p.PermissionMode
	p.mu.Unlock()

	// Persist the lastUsedAt bump so sidebar ordering survives restarts. Uses
	// the async variant because we don't hold s.mu here; saveConfig (the sync
	// variant) is only safe under s.mu, and using it here risks double-lock
	// deadlock.
	//
	// iter 980+: was a bare `go p.studio.saveConfigAsync()` — most reachable
	// goroutine launch in the app (every agent turn), so a panic here (yaml
	// edge case, full disk causing os.WriteFile to behave oddly) had the
	// highest blast radius. safeGoFn surfaces the panic in the event log
	// instead of crashing.
	if p.studio != nil {
		safeGoFn("save-config-on-turn", p.studio.LogEvent, p.studio.saveConfigAsync)
	}

	// If the agent has previously pinned context via the pin_context tool,
	// incorporate it into the system instruction for this run so it survives
	// history compaction. The instruction is re-applied each send so a new pin
	// from the previous turn is visible immediately.
	// Re-assemble the system instruction when there's pinned context OR the
	// project is in "ask" permission mode, so both survive history compaction
	// and reflect the current setting (the cached client was built once at
	// init). No-pin + auto-mode keeps the init-time instruction untouched.
	if pinnedCtx != "" || permMode == "ask" {
		base := sysPr
		if base == "" {
			base = defaultSystemPrompt(p.Directory, pName)
		}
		if pinnedCtx != "" {
			base += "\n\n## Pinned Context\n" + pinnedCtx
		}
		c.SetSystemInstruction(base + permissionDirective(permMode))
	}

	// Replay log captures every event so an abrupt shutdown doesn't lose the
	// turn. Each new turn truncates the log; Complete() clears it on normal
	// finish so the authoritative state lives only in history.json afterwards.
	replay := NewReplayLogger(p.ID, sid)

	// Per-session file trackers: allocated on first use and reused across
	// turns within the same session so read/write history accumulates.
	p.mu.Lock()
	if p.readTrackers == nil {
		p.readTrackers = make(map[string]*tools.FileReadTracker)
	}
	if p.writeTrackers == nil {
		p.writeTrackers = make(map[string]*tools.FileWriteTracker)
	}
	if _, ok := p.readTrackers[sid]; !ok {
		p.readTrackers[sid] = tools.NewFileReadTracker()
	}
	if _, ok := p.writeTrackers[sid]; !ok {
		p.writeTrackers[sid] = tools.NewFileWriteTracker()
	}
	readTracker := p.readTrackers[sid]
	writeTracker := p.writeTrackers[sid]
	p.mu.Unlock()

	// retryDelay and notifyRetry are shared by both sendWithRetry call sites
	// inside the agent loop below.
	retryDelay := p.retryInitialDelay
	if retryDelay == 0 {
		retryDelay = 2 * time.Second
	}
	notifyRetry := func(attempt, max, delayMs int, reason string) {
		p.emitEvent(wailsCtx, EventChatRetry, ChatRetryEvent{
			ProjectID: p.ID,
			SessionID: sid,
			Attempt:   attempt,
			Max:       max,
			DelayMs:   delayMs,
			Reason:    reason,
		})
	}

	defer func() {
		cancel()
		// Ensure no further appends hit the replay file after we leave this
		// function, so concurrent cleanup paths (RemoveProject,
		// DeleteChatSession) can safely remove the file without racing a
		// lingering write. If Complete() already ran, Close is a no-op.
		replay.Close()
		session.mu.Lock()
		session.active = false
		session.cancelFn = nil
		session.mu.Unlock()
	}()

	p.emitEvent(wailsCtx, EventProjectStatus, map[string]any{
		"id": p.ID, "sessionID": sid, "status": "active",
	})

	// Add user message to session history. If this is the first user turn
	// and the session still has its default "Chat N" label, derive a better
	// name from the message itself.
	session.mu.Lock()
	wasFirstUserTurn := !hasUserMessage(session.history)
	session.history = append(session.history, &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{genai.NewPartFromText(message)},
	})
	var renamedTo string
	if wasFirstUserTurn && isDefaultSessionName(session.Name) {
		renamedTo = deriveSessionName(message)
		if renamedTo != "" {
			session.Name = renamedTo
		}
	}
	session.mu.Unlock()
	if renamedTo != "" {
		p.emitEvent(wailsCtx, EventSessionRenamed, map[string]any{
			"projectID": p.ID,
			"sessionID": sid,
			"name":      renamedTo,
		})
	}

	// Persist history immediately after the user message is added so it survives
	// a crash or abrupt shutdown before the agent finishes. The final save at
	// the end of this function will include the model response.
	session.mu.RLock()
	earlySnapshot := make([]*genai.Content, len(session.history))
	copy(earlySnapshot, session.history)
	earlyName := session.Name
	session.mu.RUnlock()
	_ = SaveHistoryWithName(p.ID+"_"+sid, earlyName, earlySnapshot)

	// Log the user turn start to the replay buffer.
	replay.Append(ReplayEvent{Type: "user", Text: message})

	start := time.Now()
	turns := 0
	var finalText string
	// Provider-reported usage. Two views:
	//   total*   — summed across every LLM round in this turn (what you're billed).
	//   last*    — the most recent round's numbers (what the model actually sees
	//              in its context window right now; used for the context gauge).
	var totalInputTokens, totalOutputTokens, totalCacheRead, totalCacheWrite int
	var lastInputTokens, lastOutputTokens, lastCacheRead, lastCacheWrite int

	// streamEmitted tracks whether the CURRENT stream attempt pushed any
	// user-visible content (text/thinking) to the frontend. sendAndStream reads
	// it to decide whether a mid-stream failure can be retried safely — only
	// when nothing was shown yet, so a retry can't duplicate output.
	var streamEmitted bool
	// streamAndProcess streams an LLM response, emitting text deltas to the
	// frontend in real time, and returns the fully accumulated response.
	streamAndProcess := func(sr *client.StreamingResponse) (*client.Response, error) {
		return client.ProcessStream(ctx, sr, &client.StreamHandler{
			OnText: func(text string) {
				streamEmitted = true
				p.emitEvent(wailsCtx, EventChatDelta, ChatTextEvent{
					ProjectID: p.ID, SessionID: sid, Text: text,
				})
			},
			OnThinking: func(text string) {
				// Stream reasoning deltas so the user sees the model think in
				// real time. The accumulated thinking is also persisted at
				// end-of-turn via recordResponse (for replay + history).
				streamEmitted = true
				p.emitEvent(wailsCtx, EventChatThinkingDelta, ChatTextEvent{
					ProjectID: p.ID, SessionID: sid, Text: text,
				})
			},
			// NOTE: the hard chat:error emit is NOT here anymore — it lives at
			// the call site (via sendAndStream). OnError fires synchronously
			// mid-stream, before we know whether the error is retryable with
			// nothing emitted (in which case we retry silently rather than flash
			// a scary error that then recovers). ProcessStream still returns the
			// error, so no information is lost.
		})
	}

	// recordResponse saves the model response in conversation history and
	// emits the final text + thinking to the frontend.
	recordResponse := func(collected *client.Response) {
		// Provider-reported usage for this round. Remember the latest numbers
		// (for context gauge) and accumulate totals (for per-turn cost).
		if collected.InputTokens > 0 || collected.OutputTokens > 0 {
			lastInputTokens = collected.InputTokens
			lastOutputTokens = collected.OutputTokens
			lastCacheRead = collected.CacheReadInputTokens
			lastCacheWrite = collected.CacheCreationInputTokens
			totalInputTokens += collected.InputTokens
			totalOutputTokens += collected.OutputTokens
			totalCacheRead += collected.CacheReadInputTokens
			totalCacheWrite += collected.CacheCreationInputTokens
			p.emitEvent(wailsCtx, EventChatUsage, ChatUsageEvent{
				ProjectID:             p.ID,
				SessionID:             sid,
				LastInputTokens:       lastInputTokens,
				LastOutputTokens:      lastOutputTokens,
				LastCacheReadTokens:   lastCacheRead,
				LastCacheWriteTokens:  lastCacheWrite,
				TotalInputTokens:      totalInputTokens,
				TotalOutputTokens:     totalOutputTokens,
				TotalCacheReadTokens:  totalCacheRead,
				TotalCacheWriteTokens: totalCacheWrite,
			})
		}
		if collected.Thinking != "" {
			p.emitEvent(wailsCtx, EventChatThinking, ChatThinkingEvent{
				ProjectID: p.ID, SessionID: sid, Text: collected.Thinking,
			})
			replay.Append(ReplayEvent{Type: "thinking", Text: collected.Thinking})
		}
		if collected.Text != "" {
			finalText = collected.Text
			p.emitEvent(wailsCtx, EventChatText, ChatTextEvent{
				ProjectID: p.ID, SessionID: sid, Text: collected.Text,
			})
			replay.Append(ReplayEvent{Type: "assistant_text", Text: collected.Text})
		}

		var parts []*genai.Part
		// Preserve any thinking parts (with signatures) that the provider
		// returned — Kimi and native Anthropic require them on round-trip
		// once extended thinking is enabled.
		for _, p := range collected.Parts {
			if p != nil && p.Thought && p.Text != "" {
				parts = append(parts, p)
			}
		}
		modelText := collected.Text
		if modelText == "" && len(collected.FunctionCalls) == 0 && len(parts) == 0 {
			modelText = " " // Gemini requires non-empty parts
		}
		if modelText != "" {
			parts = append(parts, genai.NewPartFromText(modelText))
		}
		for _, fc := range collected.FunctionCalls {
			parts = append(parts, &genai.Part{FunctionCall: fc})
		}
		if len(parts) > 0 {
			session.mu.Lock()
			session.history = append(session.history, &genai.Content{
				Role:  "model",
				Parts: parts,
			})
			session.mu.Unlock()
		}
	}

	// sendAndStream issues one LLM request and consumes its stream as a single
	// retryable unit. sendWithRetry alone only covers the pre-stream call, which
	// returns at the 200 OK BEFORE any token streams; a transient failure that
	// lands mid-stream (dropped connection, idle timeout) would otherwise abort
	// the entire turn with no retry — losing a multi-minute refactor on one
	// blip. Here we retry the whole request when the failure is transient AND
	// nothing was streamed to the UI yet, so a retry cannot duplicate output or
	// corrupt history (recordResponse runs only on the returned success). If
	// content was already shown, the error isn't retryable, or ctx is cancelled,
	// the error is returned for the caller to surface as a hard chat:error.
	sendAndStream := func(mkSend func() (*client.StreamingResponse, error)) (*client.Response, error) {
		const maxStreamAttempts = 3
		delay := retryDelay
		for attempt := 1; ; attempt++ {
			resp, err := sendWithRetry(ctx, notifyRetry, retryDelay, mkSend)
			if err != nil {
				return nil, err
			}
			if resp == nil {
				return nil, nil
			}
			streamEmitted = false
			collected, serr := streamAndProcess(resp)
			if serr == nil {
				// A completely empty 200 (no text/tools/thinking) is usually a
				// transient provider glitch. Retry it the way the upstream engine
				// classifies it (client.EmptyModelResponseError is retryable), but
				// only while nothing has been streamed (so a retry can't duplicate
				// output). If retries are exhausted, fall through and return the
				// empty response so the turn still completes rather than erroring.
				if responseIsEmpty(collected) && attempt < maxStreamAttempts && !streamEmitted && ctx.Err() == nil {
					emptyErr := &client.EmptyModelResponseError{}
					notifyRetry(attempt, maxStreamAttempts, int(delay/time.Millisecond), summarizeRetryReason(emptyErr))
					select {
					case <-time.After(delay):
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					delay *= 2
					continue
				}
				return collected, nil
			}
			if ctx.Err() != nil {
				return nil, serr
			}
			if attempt < maxStreamAttempts && !streamEmitted && client.IsRetryableError(serr) {
				notifyRetry(attempt, maxStreamAttempts, int(delay/time.Millisecond), summarizeRetryReason(serr))
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				delay *= 2
				continue
			}
			return nil, serr
		}
	}

	// Agent loop: send -> stream -> if tool calls: execute -> send results -> repeat.
	// The outer loop handles retries / multi-message sessions (turns cap = 50).
	// The inner tool loop keeps executing tool-call rounds without re-entering
	// SendMessageWithHistory, so chained tool calls are handled correctly: the
	// function responses stay paired with their function calls in history and
	// don't get stripped by sanitizeToolPairs on a spurious second top-level send.
outer:
	for turns < 50 {
		if ctx.Err() != nil {
			break
		}
		turns++

		// Snapshot session history under lock and compact if needed.
		session.mu.RLock()
		historySnapshot := make([]*genai.Content, len(session.history))
		copy(historySnapshot, session.history)
		session.mu.RUnlock()
		p.mu.RLock()
		provider := p.Provider
		model := p.Model
		p.mu.RUnlock()

		maxCtx := contextWindowForProvider(provider, model)
		lenBefore := len(historySnapshot)
		historySnapshot = compactHistory(historySnapshot, maxCtx)
		if len(historySnapshot) < lenBefore {
			historySnapshot = injectContinuationHint(historySnapshot, message, readTracker, writeTracker)
		}

		collected, err := sendAndStream(func() (*client.StreamingResponse, error) {
			return c.SendMessageWithHistory(ctx, historySnapshot, "")
		})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			p.emitEvent(wailsCtx, EventChatError, ChatTextEvent{
				ProjectID: p.ID, SessionID: sid, Text: humanizeAPIError(err),
			})
			break
		}
		if collected == nil {
			break
		}

		recordResponse(collected)

		// Tool loop: keep executing tool rounds until the model returns a plain
		// text response with no further tool calls (or until toolRound cap).
		// recentToolPatterns records the stagnation key of each executed call
		// in order; checkStagnation fires when the last stagnationLimit are
		// identical (ported from gokin's executor loop guard).
		var recentToolPatterns []string
		for toolRound := 0; len(collected.FunctionCalls) > 0 && toolRound < 40; toolRound++ {
			if ctx.Err() != nil {
				break outer
			}

			// Execute tools and collect responses.
			var funcParts []*genai.Part
			for _, fc := range collected.FunctionCalls {
				if ctx.Err() != nil {
					break
				}

				p.emitEvent(wailsCtx, EventChatToolCall, ChatToolCallEvent{
					ProjectID: p.ID, SessionID: sid, Tool: fc.Name, Args: fc.Args,
				})
				replay.Append(ReplayEvent{Type: "tool_call", Tool: fc.Name, Args: fc.Args})

				tool, ok := reg.Get(fc.Name)
				if !ok {
					errMsg := tools.FormatUnknownToolError(fc.Name, reg.Names())
					funcParts = append(funcParts, &genai.Part{
						FunctionResponse: &genai.FunctionResponse{
							ID:       fc.ID,
							Name:     fc.Name,
							Response: map[string]any{"error": errMsg},
						},
					})
					p.emitEvent(wailsCtx, EventChatToolResult, ChatToolResultEvent{
						ProjectID: p.ID, SessionID: sid, Tool: fc.Name, Success: false,
						Content: errMsg,
					})
					notSuccess := false
					replay.Append(ReplayEvent{Type: "tool_result", Tool: fc.Name, Success: &notSuccess, Text: errMsg})
					continue
				}

				// Seed routing so tools that escalate to the user (ask_user) can
				// emit events against the correct project+session without needing
				// a global handler per call. Also seed a per-session history getter
				// for history_search so concurrent sessions don't race on the shared
				// tool's stored field.
				//
				// ReadTrackerCtxKey lets edit.go enforce the Read-before-Edit
				// safety invariant: refuse to edit a file that hasn't been read in
				// this session (prevents blind grep-based edits clobbering context).
				toolCtx := withAskUserRouting(ctx, p.ID, sid)
				toolCtx = context.WithValue(toolCtx, tools.ReadTrackerCtxKey{}, readTracker)
				toolCtx = context.WithValue(toolCtx, tools.HistoryGetterCtxKey{}, func() []*genai.Content {
					session.mu.RLock()
					defer session.mu.RUnlock()
					out := make([]*genai.Content, len(session.history))
					copy(out, session.history)
					return out
				})
				// iter 1030+: wire the engine's ProgressCallback so partial output
				// from long-running tools (bash via stdout pipe) streams to the
				// frontend as chat:tool_progress events. progress=0 means "here's
				// the next chunk of stdout/stderr text"; progress=-1 means "just
				// a byte counter update" (we ignore those — the frontend's
				// elapsed-time chip already conveys "still running"). Skipping the
				// counter path keeps event volume low (1 event per ~100ms of
				// stdout, not 1 per 32KB AND 1 per 100ms).
				toolName := fc.Name
				toolCtx = tools.ContextWithProgressCallback(toolCtx, func(progress float64, step string) {
					if progress != 0 || step == "" {
						return
					}
					p.emitEvent(wailsCtx, EventChatToolProgress, ChatToolProgressEvent{
						ProjectID: p.ID, SessionID: sid, Tool: toolName, Text: step,
					})
				})
				// Loop guard: detect stuck repetition before executing.
				toolPattern := stagnationKey(fc.Name, fc.Args)
				recentToolPatterns = append(recentToolPatterns, toolPattern)
				if checkStagnation(recentToolPatterns, toolPattern) {
					guardMsg := buildStagnationMessage(fc.Name, fc.Args, stagnationLimit)
					funcParts = append(funcParts, &genai.Part{
						FunctionResponse: &genai.FunctionResponse{
							ID:       fc.ID,
							Name:     fc.Name,
							Response: map[string]any{"result": guardMsg},
						},
					})
					p.emitEvent(wailsCtx, EventChatToolResult, ChatToolResultEvent{
						ProjectID: p.ID, SessionID: sid, Tool: fc.Name, Success: false,
						Content: guardMsg,
					})
					notSuccess := false
					replay.Append(ReplayEvent{Type: "tool_result", Tool: fc.Name, Success: &notSuccess, Text: guardMsg})
					continue
				}

				result, toolErr := safeToolExecute(toolCtx, tool, fc.Args)
				success := toolErr == nil && result.Success

				// Run semantic validators after successful write operations so the
				// model sees warnings inline (go_quality, security, shell, test_quality).
				if success && toolErr == nil && p.semanticValidators != nil && tools.IsWriteTool(fc.Name) {
					for _, fp := range tools.ExtractFilePaths(fc.Args) {
						if data, readErr := os.ReadFile(fp); readErr == nil {
							if warns := p.semanticValidators.RunAll(toolCtx, fp, data, p.Directory); len(warns) > 0 {
								if formatted := tools.FormatWarnings(warns); formatted != "" {
									result.Content += "\n\n" + formatted
								}
							}
						}
					}
				}

				// On failure, COMBINE the captured output with the error rather
				// than replacing it. A failing `go build`/`go test` returns its
				// compiler/test diagnostics in result.Content (incl. stderr) and
				// a terse "command exited with code N" in result.Error; sending
				// only the latter stranded the agent with nothing to fix. A
				// panic/exec error (toolErr) has no useful content, so use it
				// directly.
				content := result.Content
				if toolErr != nil {
					content = toolErr.Error()
				} else if result.Error != "" {
					if content != "" {
						content = content + "\n" + result.Error
					} else {
						content = result.Error
					}
				}

				// Record reads and writes for the continuation hint.
				if success {
					switch fc.Name {
					case "read":
						if fp, _ := fc.Args["file_path"].(string); fp != "" {
							readOffset := stagnationFingerprintArg(fc.Args, "offset")
							readLimit := stagnationFingerprintArg(fc.Args, "limit")
							readTracker.CheckAndRecord(fp, readOffset, readLimit, len(result.Content))
						}
					case "write", "edit", "delete", "mkdir", "copy", "move", "batch":
						if fp, _ := fc.Args["path"].(string); fp != "" {
							writeTracker.Record(fp)
						}
						if fp, _ := fc.Args["file_path"].(string); fp != "" {
							writeTracker.Record(fp)
						}
					}
				}

				p.emitEvent(wailsCtx, EventChatToolResult, ChatToolResultEvent{
					ProjectID: p.ID, SessionID: sid, Tool: fc.Name, Success: success, Content: content,
				})
				sBool := success
				replay.Append(ReplayEvent{Type: "tool_result", Tool: fc.Name, Success: &sBool, Text: content})

				funcParts = append(funcParts, &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						ID:       fc.ID,
						Name:     fc.Name,
						Response: map[string]any{"result": content},
					},
				})
			}

			if ctx.Err() != nil {
				break outer
			}

			// Snapshot history BEFORE appending function responses. SendFunctionResponse
			// receives them via funcResponses (not from history) — passing them in both
			// would cause every provider to send duplicate tool_result blocks.
			session.mu.RLock()
			historySnapshot = make([]*genai.Content, len(session.history))
			copy(historySnapshot, session.history)
			session.mu.RUnlock()

			funcResponses := make([]*genai.FunctionResponse, 0, len(funcParts))
			for _, part := range funcParts {
				if part.FunctionResponse != nil {
					funcResponses = append(funcResponses, part.FunctionResponse)
				}
			}

			collected, err = sendAndStream(func() (*client.StreamingResponse, error) {
				return c.SendFunctionResponse(ctx, historySnapshot, funcResponses)
			})
			if err != nil {
				if ctx.Err() != nil {
					break outer
				}
				p.emitEvent(wailsCtx, EventChatError, ChatTextEvent{
					ProjectID: p.ID, SessionID: sid, Text: humanizeAPIError(err),
				})
				break outer
			}
			if collected == nil {
				break outer
			}

			// Append function responses to history AFTER the send+stream
			// succeeds (and after any internal stream-retry), and before
			// recordResponse, so history stays in causal order:
			// [... model{tool_calls}, user{tool_results}, model{reply}].
			// Appending only on success keeps a failed round from leaving an
			// orphaned tool-results turn in history. SendFunctionResponse
			// receives the responses via funcResponses (not from history), so
			// the snapshot taken above already excludes them.
			session.mu.Lock()
			session.history = append(session.history, &genai.Content{
				Role:  "user",
				Parts: funcParts,
			})
			session.mu.Unlock()

			recordResponse(collected)
		}

		// Tool loop exited — no more FCs (or toolRound cap hit). Done.
		break
	}

	// iter 980+: surface ctx.DeadlineExceeded as a real chat:error so the
	// user understands why the agent stopped. Previously the loop broke
	// silently and chat:complete fired with empty text — the spinner
	// cleared but no message explained the 30-minute cap was hit. Users
	// reported "agent just stopped doing anything" on long refactors.
	//
	// context.Canceled (user clicked Stop) intentionally falls through
	// without a synthesized error — the user just initiated that stop,
	// they don't need a banner explaining their own action.
	if ctx.Err() == context.DeadlineExceeded {
		p.emitEvent(wailsCtx, EventChatError, ChatTextEvent{
			ProjectID: p.ID,
			SessionID: sid,
			Text:      "Agent stopped after the 30-minute per-turn limit. Long tasks can be broken into smaller steps, or you can re-send to continue from here.",
		})
	}

	p.mu.RLock()
	completedProvider := p.Provider
	completedModel := p.Model
	p.mu.RUnlock()
	if completedProvider == "" {
		completedProvider = settings.DefaultProvider
	}
	if completedModel == "" {
		completedModel = settings.DefaultModel
	}
	p.mu.RLock()
	currentPin := p.pinnedContext
	p.mu.RUnlock()
	// Cost estimation uses the SUM of every round's tokens since billing is
	// per-token, not per-round. The frontend gets this as a $-figure so it
	// doesn't have to duplicate the pricing table in TS.
	estCost := EstimateCost(completedProvider, completedModel, totalInputTokens, totalOutputTokens, totalCacheRead, totalCacheWrite)
	// A turn "persisted" its work when it finished normally OR hit the 30-minute
	// deadline — BOTH produced legitimate output that belongs on disk. Only an
	// explicit cancel (context.Canceled from user Stop, /clear, or session
	// delete) skips persistence: /clear and DeleteChatSession own the on-disk
	// state (a racing save here would resurrect a wiped chat), while a plain
	// Stop stays recoverable via the preserved replay log below. The in-memory
	// budget cache (bumpTotalCostUSD) is bumped in lockstep with the on-disk
	// usage save inside the persist block, so the two never desync.
	persisted := ctx.Err() != context.Canceled
	p.emitEvent(wailsCtx, EventChatComplete, ChatCompleteEvent{
		ProjectID:            p.ID,
		SessionID:            sid,
		Text:                 finalText,
		Turns:                turns,
		DurationMs:           time.Since(start).Milliseconds(),
		Model:                completedModel,
		Provider:             completedProvider,
		InputTokens:          totalInputTokens,
		OutputTokens:         totalOutputTokens,
		CacheReadTokens:      totalCacheRead,
		CacheWriteTokens:     totalCacheWrite,
		LastInputTokens:      lastInputTokens,
		LastOutputTokens:     lastOutputTokens,
		LastCacheReadTokens:  lastCacheRead,
		LastCacheWriteTokens: lastCacheWrite,
		EstimatedCostUSD:     estCost,
		PinnedContext:        currentPin,
	})

	// Drop the replay recovery log ONLY when we persisted to history.json
	// (clean finish or 30-min deadline) — history is then the authoritative
	// copy. On an explicit cancel, leave the log: the deferred replay.Close()
	// preserves it so a plain user Stop stays recoverable on next load, and
	// /clear / DeleteChatSession each call DiscardReplay so a wiped chat can't
	// be resurrected from it.
	if persisted {
		replay.Complete()
	}

	p.emitEvent(wailsCtx, EventProjectStatus, map[string]any{
		"id": p.ID, "sessionID": sid, "status": "idle", "gitBranch": p.gitBranch(),
	})

	// Bump per-session usage stats and persist them alongside history — but
	// ONLY when the turn persisted (normal finish or 30-min deadline). /clear
	// (ClearHistory) and session delete (DeleteChatSession) cancel ctx via
	// session.cancelFn and THEN wipe session.history + remove the history file;
	// if the still-running agent goroutine saved here regardless, it would
	// resurrect a just-cleared conversation or recreate a deleted session's
	// ghost file (re-discovered by ListHistoryFilesForProject on next startup).
	// On an explicit cancel those callers own the on-disk state, so we stay out.
	//
	// A 30-minute DeadlineExceeded, by contrast, produced real work — we MUST
	// save it (the pre-iter-1070 code saved unconditionally; gating on Canceled
	// preserves that for the timeout path while still ceding clear/delete).
	//
	// Counts are accumulated atomically under session.mu so concurrent
	// SendMessage calls (different turns of the same session — shouldn't
	// happen, but defensive) don't race on the running totals.
	if persisted {
		var (
			doSave         bool
			usageSnapshot  SessionUsage
			parentSnapshot string
			histSnapshot   []*genai.Content
			sessionName    string
		)
		session.mu.Lock()
		// Re-check under the lock: a /clear can race in between the persisted
		// check above and acquiring the lock, flipping ctx to Canceled (it
		// cancels before wiping). If so, don't persist.
		if ctx.Err() != context.Canceled {
			if session.usage == nil {
				session.usage = &SessionUsage{}
			}
			session.usage.TotalCostUSD += estCost
			session.usage.TotalInputTokens += totalInputTokens
			session.usage.TotalOutputTokens += totalOutputTokens
			session.usage.TotalCacheTokens += totalCacheRead + totalCacheWrite
			session.usage.TurnCount++
			session.usage.LastTurnAt = time.Now().UnixMilli()
			usageSnapshot = *session.usage // copy for the save
			parentSnapshot = session.ParentID
			histSnapshot = make([]*genai.Content, len(session.history))
			copy(histSnapshot, session.history)
			sessionName = session.Name
			doSave = true
		}
		session.mu.Unlock()
		if doSave {
			// Use SaveHistoryWithUsage so the new totals are stamped onto disk
			// (not preserved-from-previous-write, which would leave them stale
			// for the very first turn of a session).
			_ = SaveHistoryWithUsage(p.ID+"_"+sid, sessionName, parentSnapshot, &usageSnapshot, histSnapshot)
			// Bump the in-memory budget cache in lockstep with the on-disk
			// usage we just wrote, so strict-budget enforcement stays
			// deterministic across restarts (the cache re-seeds from disk).
			// bumpTotalCostUSD short-circuits when estCost <= 0.
			p.bumpTotalCostUSD(estCost)
		}
	}
}

// Stop cancels an in-progress generation in all sessions.
func (p *Project) Stop() {
	p.mu.RLock()
	sessions := make([]*ChatSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		sessions = append(sessions, s)
	}
	tm := p.taskManager
	memStore := p.memoryStore
	learning := p.projectLearning
	cl := p.client
	p.mu.RUnlock()
	for _, s := range sessions {
		s.Stop()
	}
	// Release the per-project client's idle HTTP connections on teardown.
	// NewClientNoPool gives each project a dedicated instance with no shared
	// pool to reap it; RemoveProject and Shutdown both call Stop, so this is
	// the cleanup point. Closing only drops idle keep-alives, so any turn still
	// finishing on its own snapshotted client reference is unaffected.
	if cl != nil {
		_ = cl.Close()
	}
	// Cancel every still-running background bash task. Without this, shell
	// processes launched via bash(run_in_background=true) survive project
	// removal and keep consuming resources until they exit on their own.
	if tm != nil {
		for _, info := range tm.ListRunning() {
			_ = tm.Cancel(info.ID)
		}
	}
	// Flush any pending memory-store writes — the store uses a 2s debounce
	// so a fresh `memory remember` call moments before shutdown would
	// otherwise be lost. Same treatment for ProjectLearning, which also
	// debounces saves to batch rapid `memorize` calls.
	if memStore != nil {
		_ = memStore.Flush()
	}
	if learning != nil {
		_ = learning.Flush()
	}
}

// StopSession cancels generation for a specific session only.
func (p *Project) StopSession(sessionID string) {
	session := p.GetSession(sessionID)
	if session != nil {
		session.Stop()
	}
}

// pruneAbandonedEmptySessions removes sessions that appear to have been
// created but never used: zero history entries AND an auto-generated
// "Chat N" name. Without this, every Ctrl+T or Plus-click leaves a
// persisted empty tab that reappears on the next app boot — the user
// reported "I close with one chat and it keeps reopening with others".
// Renamed empty sessions and any session with a single message are kept.
// We always preserve at least one session per project so the UI never
// renders a project with zero tabs.
func (p *Project) pruneAbandonedEmptySessions() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.sessions) <= 1 {
		return
	}

	var drop []string
	kept := 0
	for id, sess := range p.sessions {
		sess.mu.RLock()
		hasHistory := len(sess.history) > 0
		autoNamed := isDefaultSessionName(sess.Name)
		active := sess.active
		sess.mu.RUnlock()

		if !hasHistory && autoNamed && !active {
			drop = append(drop, id)
		} else {
			kept++
		}
	}

	// Guardrail: never delete every session; leave at least one — either an
	// explicitly-kept one, or the first candidate we'd otherwise drop.
	if kept == 0 && len(drop) > 0 {
		drop = drop[1:]
	}

	for _, id := range drop {
		delete(p.sessions, id)
		DeleteHistory(p.ID + "_" + id)
		DiscardReplay(p.ID, id)
	}
}

// totalCostUSD returns the cached cumulative cost across every session in
// this project. iter 1040+: seeded lazily from ProjectUsageStats on first
// need so cold starts pay the O(N sessions) disk walk only once; subsequent
// calls are O(1). Used by SendMessage's strict-budget pre-flight check.
//
// Returns 0 if the studio back-reference is missing (test paths that
// construct Project directly without wiring a *Studio) or if the underlying
// stats call fails — in either case the budget check effectively no-ops,
// which matches the documented opt-in semantics.
func (p *Project) totalCostUSD() float64 {
	p.costMu.Lock()
	defer p.costMu.Unlock()
	if !p.costSeeded {
		if p.studio != nil {
			stats, err := p.studio.ProjectUsageStats(p.ID)
			if err == nil && stats != nil {
				p.cachedTotalCostUSD = stats.TotalCostUSD
			}
		}
		p.costSeeded = true
	}
	return p.cachedTotalCostUSD
}

// bumpTotalCostUSD adds delta to the cached total. Called from the
// chat:complete path with the per-turn cost. Seeds the cache from disk on
// first call so the initial bump combines disk-state plus this turn — a
// rare but real scenario when the user adds a budget mid-session after
// already accumulating cost.
func (p *Project) bumpTotalCostUSD(delta float64) {
	if delta <= 0 {
		return
	}
	p.costMu.Lock()
	defer p.costMu.Unlock()
	if !p.costSeeded {
		if p.studio != nil {
			stats, err := p.studio.ProjectUsageStats(p.ID)
			if err == nil && stats != nil {
				p.cachedTotalCostUSD = stats.TotalCostUSD
			}
		}
		p.costSeeded = true
	}
	p.cachedTotalCostUSD += delta
}

// ToConfig converts to persistable config.
//
// iter 980+: takes p.mu.RLock() internally because all callers (saveConfig,
// saveConfigAsync) hold s.mu but NOT p.mu. Without this lock, ToConfig
// raced with Set* mutations (RenameProject writing Name, SetProjectProvider
// writing Provider+Model, etc.) which happen under p.mu.Lock(). The race
// produced torn YAML on disk — e.g. Provider=kimi but Model=glm-5.1 if a
// concurrent agent turn called saveConfigAsync between the two field
// writes. Manifested as "I switched provider but after restart the model
// went back to the previous one".
//
// Safe because no current caller holds p.mu (verified by grep). If a future
// caller does, it must read fields manually instead of calling ToConfig.
func (p *Project) ToConfig() ProjectConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return ProjectConfig{
		ID:             p.ID,
		Name:           p.Name,
		Directory:      p.Directory,
		Provider:       p.Provider,
		Model:          p.Model,
		SystemPrompt:   p.SystemPrompt,
		Temperature:    p.Temperature,
		MaxTokens:      p.MaxTokens,
		ThinkingMode:   p.ThinkingMode,
		ThinkingBudget: p.ThinkingBudget,
		PermissionMode: p.PermissionMode,
		BudgetUSD:      p.BudgetUSD,
		EnforceBudget:  p.EnforceBudget,
		Pinned:         p.Pinned,
		LastUsedAt:     p.lastUsedAt,
	}
}

// GenerateID creates a short unique project ID.
func GenerateID() string {
	return uuid.New().String()[:8]
}

// firstNonEmpty returns the first non-empty string from the arguments.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// askBeforeChangesDirective is appended to the system prompt when a project's
// PermissionMode == "ask". Soft enforcement: the agent loop has no hard
// approval gate (project.go initMemoryAndPlan uses requireApproval=false), so
// this instructs the model to confirm via ask_user before mutating anything.
const askBeforeChangesDirective = "\n\n## Permission mode: ask before changes\n" +
	"Before making any change to the user's files or repository — file " +
	"writes/edits/deletes/moves, `git` mutations (commit, reset, checkout, " +
	"rebase, push), or destructive shell commands (rm, overwriting files, " +
	"dropping data) — FIRST use the ask_user tool to briefly describe what you " +
	"intend to do and get confirmation. Read-only operations (reading files, " +
	"search, git status/diff, running tests) do NOT need confirmation. Batch " +
	"related changes into a single confirmation rather than asking per file."

// permissionDirective returns the system-prompt addendum for a permission mode.
// Only "ask" adds anything; "" / "auto" return the empty string.
func permissionDirective(mode string) string {
	if mode == "ask" {
		return askBeforeChangesDirective
	}
	return ""
}

// defaultSystemPrompt returns a baseline instruction for the agent when the
// user hasn't configured a project-specific one. Tuned for GLM-5.2 (the default
// model: 1M-token context window, strong tool use) — explicit tool-selection
// directives, evidence-first discipline, and an architecture-first sketch for
// new features (the latter three merged from the gokin upstream's
// baseSystemPrompt in internal/context/prompt.go).
func defaultSystemPrompt(directory, name string) string {
	return `You are a senior software engineer working inside the project "` + name + `" at ` + directory + `.

# Tool use is mandatory
You have access to file tools (read, write, edit, copy, move, delete, mkdir, diff, list_dir, tree, glob, grep), shell (bash, run_tests for smart test execution, task for background processes), git (git_status, git_diff, git_log, git_blame, git_add, git_commit, git_branch, git_pr, review_changes), web (web_fetch, web_search), planning (enter_plan_mode, update_plan_progress, get_plan_status, exit_plan_mode), persistent memory (memory, memorize, pin_context, history_search), inter-project coordination (ask_agent, coordinate), and clarification (ask_user).

Prefer run_tests over bare "bash go test ./..." — run_tests auto-detects the framework, parses JSON output, surfaces only failed test names and their file:line assertion locations.
Prefer review_changes over bare "git diff" — review_changes also shows untracked (newly-created) files in the same view and truncates sensibly for long diffs.

Always prefer the dedicated tool over a bash equivalent — it returns structured, safer results:
- Find files → glob (NOT bash find/ls)
- Search content → grep (NOT bash grep/rg or cat | grep)
- Read a file → read (NOT bash cat/head/tail)
- Targeted change → edit (NOT rewriting the whole file with write)
- New file → write
- Builds / tests / commands → bash, only when no dedicated tool fits
When several independent operations are needed, call the tools in parallel in one step.

NEVER describe what you would do — just do it with tools. If asked "what files are here?", call list_dir or tree, do not guess. If asked to fix a bug, read the relevant files first, then edit them.

# Project-context-first workflow
At the START of a new conversation, before answering any non-trivial question:
1. Run list_dir or tree on the project root to understand structure
2. Look for and read context files in this priority order: CLAUDE.md, AGENTS.md, README.md, then language manifests (package.json, go.mod, Cargo.toml, pyproject.toml, requirements.txt)
3. Run git_status to see current state
4. Call memory with action=search to check if you've learned anything about this project before (past decisions, user preferences, known gotchas)

This grounds your responses in the actual project. Skip this step ONLY for trivial follow-ups in the same conversation.

# Persistent memory — use it actively
- memory (action=store/search/retrieve/delete) — durable key/value notes across sessions. Store: architectural decisions, user preferences, non-obvious constraints, gotchas the user had to correct you on.
- memorize — one-shot "remember this fact/preference/pattern about the project". Use when the user says "remember that..." or when you notice a project convention worth keeping.
- pin_context — pin a persistent note that is prepended to every new turn's system prompt and survives both history compaction and restarts. Use for key constraints or reminders.
- history_search — grep the current session's in-memory history (useful after compaction to recover details you know were mentioned earlier).

Rule of thumb: if you learn something the user would be annoyed to re-explain next week, memorize it. Before proposing an approach, search memory for prior context on the same area.

# Workflow for non-trivial tasks
1. PLAN — for tasks with 3+ steps, use enter_plan_mode to lay out the plan. For ambiguous tasks, use ask_user to clarify scope before starting (options + default keep it snappy).
2. EXECUTE — work through steps; after each meaningful step, update_plan_progress.
3. VERIFY — after edits: re-read modified files, run relevant commands (tests, builds, lints), check git_diff. Don't claim "done" without verification.

# Evidence over assumption
- Identify the smallest relevant slice first: entry points, interfaces, tests, config, and existing conventions for the area. GLM-5.2 has a large (1M-token) context window — read broadly (the defining files AND a nearby caller/test) before editing rather than guessing.
- Prefer existing patterns, helpers, error handling, and naming over inventing new abstractions.
- Keep changes scoped to the request; don't refactor unrelated code or rename public APIs unless the task requires it.
- If a tool result disproves your assumption, revise immediately — never keep coding from stale assumptions.

# Architecture-first for new features
When asked to build something NEW (a feature, module, or major refactor) and plan mode is NOT active, first output a short sketch (3-6 lines) — Design / Components / Flow / Order — then implement it. This lets the user redirect before you invest in the wrong direction. Skip the sketch for bug fixes, single-file edits, or questions. If the user interrupts with new direction, revise the sketch first ("Revised design: ...") before continuing.

# Quality bar
- Make small focused edits, not sweeping rewrites.
- Match existing code style (read nearby code first).
- Keep replies concise. Show results in markdown with proper code blocks (language tag).
- If something fails, investigate the root cause before patching symptoms.
- For destructive operations (rm, git reset --hard, force push, dropping data), use ask_user first.

# Communication
- Lead with the answer or result, not preamble.
- When showing code, use fenced blocks with language identifiers.
- Reference files as ` + "`file_path:line_number`" + ` so the user can jump to them.
- Don't apologize or hedge unnecessarily.`
}

// responseIsEmpty reports whether a model response carried no usable content
// (no text, no tool calls, no thinking). Such empty 200s are usually a
// transient provider glitch worth retrying rather than ending the turn on.
func responseIsEmpty(r *client.Response) bool {
	if r == nil {
		return true
	}
	if r.Text != "" || len(r.FunctionCalls) > 0 || r.Thinking != "" {
		return false
	}
	// A response carrying thinking-signature parts (Kimi / native Anthropic on
	// round-trip) is not "empty" even with no plain text — don't retry it away.
	for _, part := range r.Parts {
		if part != nil && part.Thought && part.Text != "" {
			return false
		}
	}
	return true
}

// contextWindowForProvider returns an approximate token budget for a provider.
func contextWindowForProvider(provider, model string) int {
	switch provider {
	case "ollama":
		profile := client.GetModelProfile(model)
		if profile.ContextWindow > 0 {
			return profile.ContextWindow
		}
		return 8192
	case "glm":
		if strings.HasPrefix(model, "glm-5.2") {
			return 1000000 // GLM-5.2 ships a 1M input context window (Z.AI)
		}
		if strings.HasPrefix(model, "glm-5") {
			return 200000 // GLM-5/5.1/5-turbo — 200K context
		}
		return 128000 // older GLM-4.x families
	case "kimi":
		return 262144 // kimi-for-coding (Kimi-k2.6) has 262K context
	case "deepseek":
		return 128000 // DeepSeek V4 (both pro + flash) — 128K context
	default:
		return 204800 // MiniMax M2.x — 200K context
	}
}

// contentSize estimates the character size of a Content entry, accounting for
// text, function calls, and function responses (which can carry large tool output).
func contentSize(c *genai.Content) int {
	if c == nil {
		return 0
	}
	total := 0
	for _, p := range c.Parts {
		if p == nil {
			continue
		}
		total += len(p.Text)
		if p.FunctionCall != nil {
			total += len(p.FunctionCall.Name)
			for k, v := range p.FunctionCall.Args {
				total += len(k)
				if s, ok := v.(string); ok {
					total += len(s)
				} else {
					total += 32 // rough estimate for non-string args
				}
			}
		}
		if p.FunctionResponse != nil {
			total += len(p.FunctionResponse.Name)
			for k, v := range p.FunctionResponse.Response {
				total += len(k)
				if s, ok := v.(string); ok {
					total += len(s)
				} else {
					total += 64
				}
			}
		}
	}
	return total
}

// findExchangeStarts returns indices in history where a "user" content begins
// a new exchange. Each exchange spans from one user message up to (but not
// including) the next user message — so it includes the model's response and
// any function call/response pairs that followed.
func findExchangeStarts(history []*genai.Content) []int {
	var starts []int
	for i, c := range history {
		if c != nil && c.Role == "user" {
			// Skip "user" entries that are pure function responses — they belong
			// to the previous model exchange, not a new user turn.
			isFuncResponse := false
			if len(c.Parts) > 0 {
				isFuncResponse = true
				for _, p := range c.Parts {
					if p == nil || p.FunctionResponse == nil {
						isFuncResponse = false
						break
					}
				}
			}
			if !isFuncResponse {
				starts = append(starts, i)
			}
		}
	}
	return starts
}

// compactHistory trims history to fit within ~75% of the model's context
// window while keeping function call/response pairs intact. Drops the oldest
// MIDDLE exchanges, always preserving the first exchange (project context)
// and the last few exchanges (recent tool results the agent needs).
func compactHistory(history []*genai.Content, maxTokens int) []*genai.Content {
	if len(history) == 0 {
		return history
	}

	budget := maxTokens * 3 / 4 // 75% of context
	charBudget := budget * 4    // ~4 chars per token

	total := 0
	for _, c := range history {
		total += contentSize(c)
	}
	if total <= charBudget {
		return history
	}

	starts := findExchangeStarts(history)
	if len(starts) <= 2 {
		// Only one exchange; cannot trim safely.
		return history
	}

	// Always keep:
	//   - exchange[0] (initial context — project files read, etc.)
	//   - last KEEP_RECENT exchanges (recent tool results the agent needs)
	const keepRecent = 3
	keepLastFrom := max(1, len(starts)-keepRecent)

	// Drop middle exchanges from oldest to newest until we fit.
	dropFrom := 1
	for dropFrom < keepLastFrom && total > charBudget {
		exchangeStart := starts[dropFrom]
		exchangeEnd := starts[dropFrom+1]
		for i := exchangeStart; i < exchangeEnd; i++ {
			total -= contentSize(history[i])
		}
		dropFrom++
	}

	if dropFrom == 1 {
		return history // nothing trimmed
	}

	// Build result: first exchange + remaining exchanges.
	firstEnd := starts[1]
	dropEnd := starts[dropFrom]

	result := make([]*genai.Content, 0, len(history)-(dropEnd-firstEnd))
	result = append(result, history[:firstEnd]...)
	result = append(result, history[dropEnd:]...)
	return result
}

// toolSetsForProvider returns the tool sets appropriate for a given provider.
// Ollama gets a minimal set; cloud providers get full capabilities.
func toolSetsForProvider(provider string) []tools.ToolSet {
	switch provider {
	case "ollama":
		return []tools.ToolSet{
			tools.ToolSetOllamaCore,
			tools.ToolSetGit,
		}
	default:
		// Cloud providers: full tool suite except semantic (requires embeddings).
		return []tools.ToolSet{
			tools.ToolSetCore,
			tools.ToolSetGit,
			tools.ToolSetWeb,
			tools.ToolSetFileOps,
			tools.ToolSetAdvanced,
			tools.ToolSetPlanning,
			tools.ToolSetMemory,
			tools.ToolSetAgent,
		}
	}
}

// sendWithRetry calls fn with exponential backoff on transient errors
// (rate limit 429, server errors 5xx, network/timeout). notify, if non-nil,
// is called before each retry so the UI can surface a warning banner.
// initialDelay sets the first backoff (doubled each attempt: d, 2d, 4d…).
//
// iter 1020+: when the provider returns 429 with a Retry-After header, the
// engine wraps it in HTTPError.RetryAfter. We extract that via
// client.RetryAfterFromError and use it as the next delay if greater than
// our exponential value. Capped at RetryAfterMaxDelay so a misbehaving
// provider can't lock the UI for minutes — at that point the user is
// better served by a clean failure they can act on.
func sendWithRetry(
	ctx context.Context,
	notify func(attempt, max, delayMs int, reason string),
	initialDelay time.Duration,
	fn func() (*client.StreamingResponse, error),
) (*client.StreamingResponse, error) {
	const maxAttempts = 3
	delay := initialDelay

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := fn()
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err

		// Stop on non-retryable errors (auth, bad request, etc.)
		if !client.IsRetryableError(err) {
			return resp, err
		}
		if attempt == maxAttempts {
			break
		}

		// Honor the server's Retry-After hint when it's longer than our own
		// backoff. Capped so a hostile/misbehaving provider can't park the
		// UI in a "retrying in 3600s" state — past the cap the user should
		// be shown a failure they can manually retry.
		next := delay
		if hint := client.RetryAfterFromError(err); hint > 0 && hint > next {
			if hint > RetryAfterMaxDelay {
				hint = RetryAfterMaxDelay
			}
			next = hint
		}

		// Notify UI: retry pending. Frontend renders this as a transient warning.
		if notify != nil {
			notify(attempt, maxAttempts, int(next/time.Millisecond), summarizeRetryReason(err))
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(next):
		}
		delay *= 2 // exponential backoff: d, 2d, 4d
	}

	return nil, fmt.Errorf("failed after %d attempts: %w", maxAttempts, lastErr)
}

// RetryAfterMaxDelay caps the Retry-After hint we'll wait for. A provider
// telling us to wait 30 minutes (e.g. daily-quota-exceeded soft-rate-limit)
// is better surfaced as a failure than as a hung UI — the user can /clear,
// switch providers, or come back later. 30 seconds is the sweet spot: long
// enough to ride out most genuine rate-limit windows, short enough to keep
// the UX from feeling broken.
const RetryAfterMaxDelay = 30 * time.Second

// summarizeRetryReason maps an error to a short human-readable label.
func summarizeRetryReason(err error) string {
	if err == nil {
		return ""
	}
	if client.IsRateLimitError(err) {
		return "rate limit"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "connection refused"):
		return "network"
	case strings.Contains(msg, "503"), strings.Contains(msg, "overloaded"):
		return "service overloaded"
	case strings.Contains(msg, "502"), strings.Contains(msg, "504"):
		return "gateway error"
	case strings.Contains(msg, "eof"):
		return "connection dropped"
	}
	return "transient error"
}

// hasUserMessage returns true if any Content in history carries a user turn
// with non-empty text (function responses with role "user" don't count).
func hasUserMessage(history []*genai.Content) bool {
	for _, c := range history {
		if c == nil || c.Role != "user" {
			continue
		}
		for _, part := range c.Parts {
			if part != nil && part.Text != "" {
				return true
			}
		}
	}
	return false
}

// isDefaultSessionName returns true for auto-generated names like "Chat 1"
// (CreateChatSession, `Chat %d`) or "Chat abcd" (NewChatSession, first 4 hex
// chars of a UUID). User-renamed sessions keep their own label.
func isDefaultSessionName(name string) bool {
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, "Chat ") {
		return false
	}
	rest := strings.TrimSpace(name[len("Chat "):])
	if rest == "" {
		return false
	}
	// Accept all-digit ("Chat 1", "Chat 12") ...
	allDigits := true
	for _, r := range rest {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return true
	}
	// ... or exactly 4 hex chars ("Chat abcd"). Anything else (e.g. "Chat first")
	// is a user-chosen name we must not clobber.
	if len(rest) != 4 {
		return false
	}
	for _, r := range rest {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// deriveSessionName extracts a short descriptive name from the first user
// message. Strips leading slash commands, collapses whitespace, and caps length.
func deriveSessionName(msg string) string {
	s := strings.TrimSpace(msg)
	if s == "" {
		return ""
	}
	// Drop leading slash-command
	if strings.HasPrefix(s, "/") {
		if idx := strings.Index(s, " "); idx >= 0 {
			s = strings.TrimSpace(s[idx+1:])
		} else {
			return ""
		}
	}
	// Strip code fences / file attachment blocks
	if idx := strings.Index(s, "```"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	// First line only
	if idx := strings.IndexAny(s, "\n\r"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	// Collapse internal whitespace
	s = strings.Join(strings.Fields(s), " ")
	const maxLen = 48
	if len(s) > maxLen {
		// Truncate on a word boundary if possible.
		cut := s[:maxLen]
		if sp := strings.LastIndex(cut, " "); sp > maxLen/2 {
			cut = cut[:sp]
		}
		s = cut + "…"
	}
	return s
}

// safeToolExecute runs tool.Execute with a recover() barrier so a panic in
// tool code (or one of its dependencies) doesn't take down the agent
// goroutine — or, worse, the entire Wails app. On panic it returns an
// error-shaped ToolResult that the agent can incorporate as context.
func safeToolExecute(ctx context.Context, tool tools.Tool, args map[string]any) (result tools.ToolResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %s panicked: %v", tool.Name(), r)
			result = tools.ToolResult{
				Error:   err.Error(),
				Success: false,
			}
		}
	}()
	return tool.Execute(ctx, args)
}

// humanizeAPIError turns a raw API error into a friendlier message with hints.
func humanizeAPIError(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	low := strings.ToLower(raw)
	switch {
	case strings.Contains(low, "401"), strings.Contains(low, "unauthorized"), strings.Contains(low, "invalid_api_key"):
		return "Authentication failed: check your API key in Settings. (" + raw + ")"
	case strings.Contains(low, "403"), strings.Contains(low, "forbidden"):
		return "Access denied by the provider. Verify your account has access to this model. (" + raw + ")"
	case strings.Contains(low, "404"), strings.Contains(low, "model_not_found"):
		return "Model not found. The selected model may be unavailable for your account. (" + raw + ")"
	case strings.Contains(low, "429"), strings.Contains(low, "rate limit"), strings.Contains(low, "too many requests"):
		return "Rate limited by the provider. Wait a moment and try again. (" + raw + ")"
	case strings.Contains(low, "context") && strings.Contains(low, "length"):
		return "Conversation is too long for this model. Use /clear to start a new chat. (" + raw + ")"
	case strings.Contains(low, "no such host"), strings.Contains(low, "connection refused"):
		return "Network error: cannot reach the provider. Check your internet connection. (" + raw + ")"
	case strings.Contains(low, "timeout"), strings.Contains(low, "deadline"):
		return "Request timed out. The provider may be slow — try again or stop and rephrase. (" + raw + ")"
	}
	return "Error: " + raw
}

// injectContinuationHint appends a synthetic user message after history compaction
// so the model can continue its task without re-reading already-loaded files.
// Mirrors gokin's agent.injectContinuationHint but adapted for studio's simpler loop.
func injectContinuationHint(
	history []*genai.Content,
	originalPrompt string,
	readTracker *tools.FileReadTracker,
	writeTracker *tools.FileWriteTracker,
) []*genai.Content {
	if len(history) == 0 {
		return history
	}

	var b strings.Builder
	b.WriteString("[System: Conversation was automatically compacted to free context space.")

	if originalPrompt != "" {
		task := originalPrompt
		if runes := []rune(task); len(runes) > 500 {
			task = string(runes[:500]) + "..."
		}
		b.WriteString("\nYour original task: ")
		b.WriteString(task)
	}

	if readTracker != nil {
		if files := readTracker.RecentlyReadFiles(15); len(files) > 0 {
			b.WriteString("\n\nAlready-read files in this session (content was compacted; re-read only if you need specific details):")
			for _, f := range files {
				b.WriteString("\n- ")
				b.WriteString(f)
			}
		}
	}

	if writeTracker != nil {
		if files := writeTracker.RecentlyModifiedFiles(10); len(files) > 0 {
			b.WriteString("\n\nFiles you already modified in this task (do not overwrite unless the user asked for a change):")
			for _, f := range files {
				b.WriteString("\n- ")
				b.WriteString(f)
			}
		}
	}

	b.WriteString("\nContinue with your current task.]")
	hint := b.String()

	// Append to the last user message if possible (avoids consecutive same-role
	// issues on strict providers), otherwise add a new user turn.
	last := history[len(history)-1]
	if last.Role == genai.RoleUser {
		last.Parts = append(last.Parts, genai.NewPartFromText(hint))
	} else {
		history = append(history, genai.NewContentFromText(hint, genai.RoleUser))
	}
	return history
}
