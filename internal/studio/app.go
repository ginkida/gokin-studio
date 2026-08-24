package studio

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/ginkida/gokin-studio/internal/engine/wsl"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/memory"
	"github.com/ginkida/gokin-studio/internal/engine/security"
	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"google.golang.org/genai"
)

// Ensure all background goroutines (SendMessage, Dispatch) are tracked
// so Shutdown can wait for them to finish.

// Studio is the main Wails-bound struct.
// All public methods are exposed as bindings to the React frontend.
type Studio struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
	config                 *StudioConfig
	projects               map[string]*Project
	archived               map[string]ArchivedProjectRecord
	terminals              map[string]*Terminal
	previewMu              sync.Mutex
	previewServers         map[string]*previewServerRun
	previewStaticEpoch     map[string]uint64
	externalBrowserMu      sync.Mutex
	externalBrowserTabs    map[string]*externalBrowserRun
	externalBrowserActive  map[string]string
	externalBrowserAgent   *externalBrowserAgentRegistry
	browserPermissionMu    sync.Mutex
	browserPermissions     map[string]bool
	browserPermissionsRead bool
	sharedMemory           *SharedMemory    // process-wide cross-project scratchpad for agents
	askUsers               *askUserRegistry // pending ask_user questions awaiting frontend answers
	previewBrowser         *previewBrowserRegistry
	mcpApps                *mcpAppRegistry // ephemeral same-server sessions for sandboxed MCP Apps
	mcpAppsOnce            sync.Once
	eventLog               *EventLog // ring buffer of recent backend events (errors, warnings) — exposed via Diagnostics UI
	eventLogOnce           sync.Once // guards lazy-init of eventLog for tests using bare &Studio{}
	mu                     sync.RWMutex
	configSaveMu           sync.Mutex // serializes config snapshots through durable commit
	sessionFileSaveMu      sync.Mutex // makes each optimistic editor compare-and-replace indivisible from another editor save
	nativeRestoreSelectMu  sync.Mutex // keeps native file dialogs/staging latest-selection-wins
	nativeRestoreMu        sync.Mutex
	nativeRestoreCandidate *nativeRestoreCandidate
	lifecycleMu            sync.Mutex // makes background registration atomic with shutdown
	shuttingDown           bool
	shutdownOnce           sync.Once
	quitPromptMu           sync.Mutex // prevents duplicate native dialogs from repeated Quit commands
	quitPromptOpen         bool
	wg                     sync.WaitGroup // tracks every Studio-owned background goroutine
	scheduleWake           chan struct{}  // wakes the persistent scheduled-task loop after config changes
	quickEntryMu           sync.Mutex
	quickEntry             quickEntryController
	quickEntryWindowMu     sync.Mutex
	quickEntryWindow       bool
	deepLinkMu             sync.Mutex
	deepLinkPending        []DeepLinkEvent
	deepLinkReady          bool
	deepLinkSequence       uint64
	deepLinkRecent         map[[32]byte]time.Time
	nativeCommandMu        sync.Mutex
	nativeCommandPending   []string
	nativeCommandReady     bool
	sideChatMu             sync.Mutex
	sideChatRuns           map[string]sideChatRun

	// delegationMu normally nests no other application locks. Initial
	// publication is the narrow exception: while both endpoint metadata locks
	// are held it may acquire delegationRunsMu and synchronously claim the
	// target session queue (metadata -> delegationMu -> delegationRunsMu ->
	// Project/session). Deletion follows the same metadata -> delegation order;
	// run-store code never acquires delegationMu, preserving the graph.
	delegationMu           sync.Mutex
	delegations            map[string]delegationHandle
	updateMu               sync.Mutex
	pullRequestMu          sync.Mutex
	pullRequestArchiveOnce sync.Once
	pullRequestArchiveNext atomic.Uint64
	voiceShortcutMu        sync.Mutex
	voiceShortcut          quickEntryController
	speechMu               sync.Mutex
	speechSession          *studioSpeechSession
	mcpOAuthMu             sync.Mutex
	mcpOAuthRuns           map[string]bool
	localEnvironmentMu     sync.Mutex
	localEnvironmentError  string
	codeReviewMu           sync.Mutex
	codeReviewFindings     map[string]storedCodeReview
	wakeEnabled            atomic.Bool
	wakeScheduled          atomic.Bool
	archivedIDs            atomic.Value // map[string]bool; lock-free scheduler/wake snapshot
	wakeMu                 sync.Mutex
	wakeRuns               int
	wakeLease              wakeLease
	wakeError              string
	discoveredModels       map[string]map[string]bool // provider model IDs confirmed by this process's authenticated /models probe
	discoveredModelsAt     map[string]time.Time       // freshness boundary mirrors the frontend capability snapshot TTL
	// Test seams keep OS shortcut registration/window activation out of unit
	// tests while exercising the full settings lifecycle.
	testQuickEntryStarter      func(string, func()) (quickEntryController, error)
	testVoiceShortcutStarter   func(string, func()) (quickEntryController, error)
	testQuickEntryActivation   func(string)
	testQuickEntryWindowShow   func() error
	testQuickEntryWindowHide   func(bool) error
	testDeepLinkEmitter        func(DeepLinkEvent)
	testNativeCommandEmitter   func(string)
	testSideChatEmitter        func(string, SideChatEvent)
	testDelegationEmitter      func(string, DelegationEvent)
	testWindowActivator        func()
	testUpdateHTTPClient       *http.Client
	testUpdateEndpoint         string
	testUpdateEmitter          func(UpdateStatus)
	testUpdateNow              func() time.Time
	testGHCommand              func(context.Context, string, ...string) ([]byte, error)
	testBackupSaveDialog       func(string) (string, error)
	testRestoreOpenDialog      func() (string, error)
	testSpeechStarter          func(string, string, func(nativeSpeechEvent)) (nativeSpeechController, error)
	testSpeechStatus           func(string) nativeSpeechStatus
	testSpeechPermissions      func(context.Context) (nativeSpeechStatus, error)
	testSpeechEmitter          func(SpeechDictationEvent)
	testDesktopCapture         func(context.Context) ([]byte, error)
	testInteractiveCapture     func(context.Context) ([]byte, error)
	testMCPOAuthHTTPClient     *http.Client
	testMCPOAuthOpenBrowser    func(string) error
	testMCPOAuthSave           func(string, []byte) error
	testLocalEnvironmentLoad   func() ([]byte, error)
	testLocalEnvironmentSave   func([]byte) error
	testLocalEnvironmentDelete func() error
	testKnowledgeURLFetcher    func(context.Context, string) (string, error)
	testKnowledgeURLValidate   func(string) error
	testWakeAcquire            func(string) (wakeLease, error)
	// testDispatchFn, if non-nil, is called instead of dispatchInternal in tests.
	testDispatchFn func(from, to *Project, fromSid, task string, settings Settings)
	// testMCPAppApproval bypasses the Wails approval card in MCP App tests.
	testMCPAppApproval              func(context.Context, string, string, string, string, map[string]any) (bool, error)
	testPreviewBrowserEmitter       func(map[string]any)
	testCodeReviewEmitter           func(map[string]any)
	testExternalBrowserClient       *http.Client
	testExternalBrowserValidate     func(string) error
	testExternalBrowserAgentEmitter func(map[string]any)
	testQuitConfirmation            func(QuitWorkSummary) (bool, error)
}

// NewStudio creates a new Studio instance.
func NewStudio() *Studio {
	s := &Studio{
		projects:              make(map[string]*Project),
		archived:              make(map[string]ArchivedProjectRecord),
		terminals:             make(map[string]*Terminal),
		previewServers:        make(map[string]*previewServerRun),
		previewStaticEpoch:    make(map[string]uint64),
		externalBrowserTabs:   make(map[string]*externalBrowserRun),
		externalBrowserActive: make(map[string]string),
		externalBrowserAgent:  newExternalBrowserAgentRegistry(),
		browserPermissions:    make(map[string]bool),
		sharedMemory:          NewSharedMemory(),
		askUsers:              newAskUserRegistry(),
		previewBrowser:        newPreviewBrowserRegistry(),
		mcpApps:               newMCPAppRegistry(),
		eventLog:              NewEventLog(),
		scheduleWake:          make(chan struct{}, 1),
		discoveredModels:      make(map[string]map[string]bool),
		discoveredModelsAt:    make(map[string]time.Time),
		sideChatRuns:          make(map[string]sideChatRun),
		codeReviewFindings:    make(map[string]storedCodeReview),
	}
	s.archivedIDs.Store(map[string]bool{})
	return s
}

// GetWorkspaceIsolationStatus reports the real code-execution boundary used
// by bash, background shell tasks, and run_tests. Unsupported platforms do not
// pretend that process groups are a filesystem sandbox.
func (s *Studio) GetWorkspaceIsolationStatus() security.WorkspaceIsolationStatus {
	return security.DetectWorkspaceIsolation()
}

// --- Wails lifecycle ---

// Startup is called by Wails when the app starts.
func (s *Studio) Startup(ctx context.Context) {
	// Install the macOS WKNavigationDelegate guard before React can create any
	// preview/browser iframe. Unsupported hosts keep the script-disabled proxy.
	_ = externalBrowserActiveScriptsSupported()
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.config = LoadConfig()
	s.wakeEnabled.Store(s.config.Settings.KeepAwakeEnabled)
	archived, err := loadArchivedProjectsRaw()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gokin-studio: archived projects unavailable: %v\n", err)
		archived = make(map[string]ArchivedProjectRecord)
	}
	s.archived = archived
	for _, pc := range s.config.Projects {
		// Active config wins after a crash between the two durable commits of
		// archive/restore. This is the only reconciliation rule that cannot
		// hide an otherwise usable project.
		delete(s.archived, pc.ID)
		p := NewProject(pc)
		p.studio = s
		s.projects[pc.ID] = p
	}
	s.syncArchivedIDsLocked()
	if err := saveArchivedProjectsRaw(s.archived); err != nil {
		fmt.Fprintf(os.Stderr, "gokin-studio: reconcile archived projects: %v\n", err)
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
	if removed, cleanupErrors := cleanupStaleNativeRestoreCandidates(time.Now(), os.TempDir()); len(cleanupErrors) > 0 {
		s.LogEvent("warn", "backup", fmt.Sprintf("native restore candidate cleanup had %d error(s): %v", len(cleanupErrors), cleanupErrors[0]))
	} else if removed > 0 {
		s.LogEvent("info", "backup", fmt.Sprintf("removed %d stale native restore candidate(s)", removed))
	}
	if err := s.loadLocalEnvironment(); err != nil {
		fmt.Fprintf(os.Stderr, "gokin-studio: local environment unavailable: %v\n", err)
		s.LogEvent("warn", "local-environment", fmt.Sprintf("secure local environment unavailable: %v", err))
	}
	if s.config.Settings.QuickEntryEnabled {
		if err := s.setQuickEntryEnabled(true, s.config.Settings.QuickEntryShortcut); err != nil {
			s.LogEvent("warn", "quick-entry", fmt.Sprintf("global shortcut unavailable: %v", err))
		}
	}
	if s.config.Settings.VoiceShortcutEnabled {
		if err := s.setVoiceShortcutEnabled(true, s.config.Settings.VoiceShortcut); err != nil {
			s.LogEvent("warn", "quick-entry", fmt.Sprintf("voice shortcut unavailable: %v", err))
		}
	}
	if err := s.refreshScheduledWakeNeed(); err != nil {
		s.LogEvent("warn", "wake", fmt.Sprintf("scheduled wake status unavailable: %v", err))
	}

	// Surface any session-history files quarantined during project load (a
	// corrupt/unreadable file was moved aside rather than silently dropping the
	// session tab). Done here, after the event log is ready — NewProject runs
	// too early to log. Visible in Diagnostics → View Logs so the user knows a
	// session was affected and where the recoverable bytes went.
	for _, p := range s.projects {
		for _, q := range p.corruptHistory {
			s.LogEvent("warn", "history", fmt.Sprintf("quarantined corrupt session history in %q: %s", p.Name, q))
		}
		p.corruptHistory = nil
	}

	// iter 790+: background auto-cleanup pass — once per 24h, conservative
	// thresholds (replays >30d, pre-import >90d). Runs in a goroutine so a
	// slow file walk on a giant config dir doesn't block UI bring-up. Errors
	// are logged but never crash. Skipped entirely when the user has set
	// Settings.AutoCleanupDisabled.
	//
	// iter 970+: safeGoFn replaces inline defer/recover so panics surface in
	// the event log (visible via Diagnostics → View Logs) instead of only
	// stderr, which is invisible to users launching from a desktop launcher.
	s.startBackground("auto-cleanup", func() {
		_ = s.RunAutoCleanupIfDue()
	})

	// iter 840+: background auto-backup pass — once per 24h, opt-in via
	// Settings.AutoBackupEnabled. Writes a tar.gz snapshot to configDir/backups/
	// and prunes the oldest beyond AutoBackupRetention.
	s.startBackground("auto-backup", func() {
		_, _ = s.RunAutoBackupIfDue()
	})

	// A delegation left "running" by a previous process has no monitor any
	// more. Flip it so the UI stops showing it as live and cleanup can collect
	// it, mirroring what scheduled runs do.
	if reconciled, evicted, err := reconcileInterruptedDelegationRuns(); err != nil {
		s.LogEvent("warn", "delegation", "reconcile interrupted delegation runs: "+err.Error())
	} else {
		s.reapEvictedDelegationSessions(evicted)
		if reconciled > 0 {
			s.LogEvent("info", "delegation", fmt.Sprintf(
				"marked %d interrupted delegation run(s) as stopped", reconciled))
		}
	}

	// Local recurring prompts are intentionally tied to the desktop lifecycle.
	// Every attempt gets a separate child session with its own selected
	// GLM/Kimi model, approval mode, transcript, and retained run status.
	s.startBackground("scheduled-tasks", s.runScheduledTasks)
	if s.config.Settings.AutoArchivePRAfterClose {
		s.ensurePullRequestArchiveMonitor()
	}
}

// Shutdown is called by Wails when the app closes.
func (s *Studio) Shutdown(_ context.Context) {
	s.shutdownOnce.Do(s.shutdown)
}

func (s *Studio) shutdown() {
	// Close the registration gate before Wait. sync.WaitGroup forbids a new
	// Add/Go racing with Wait when its counter may reach zero. The gate also
	// prevents UI callbacks delivered during teardown from starting fresh work.
	s.lifecycleMu.Lock()
	s.shuttingDown = true
	cancel := s.cancel
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = s.setQuickEntryEnabled(false, "")
	_ = s.setVoiceShortcutEnabled(false, "")
	s.closeQuickEntryWindowForShutdown()
	s.cancelSpeechDictationForShutdown()
	s.setWakeEnabled(false)
	s.stopPreviewServers("", "", true)
	s.stopExternalBrowserTabs("", "")
	s.clearNativeRestoreCandidate()

	// Snapshot owners under the global lock, then close them without it. Task
	// completion callbacks and terminal read loops may re-enter Studio; waiting
	// for them while holding s.mu would create a shutdown-only lock cycle.
	s.mu.RLock()
	projects := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, p)
	}
	terminals := make([]*Terminal, 0, len(s.terminals))
	for _, t := range s.terminals {
		terminals = append(terminals, t)
	}
	s.mu.RUnlock()

	// Cancel all in-progress agent runs and terminals.
	for _, p := range projects {
		p.Stop()
	}
	for _, t := range terminals {
		t.Close()
	}

	// Wait for all background goroutines (SendMessage, Dispatch) to finish.
	s.wg.Wait()

	// Permanently close background-task start gates and completion observers,
	// then release provider/MCP transports. All tracked turns are finished, so
	// no caller can still be using these clients.
	for _, p := range projects {
		p.Close()
	}

	// Prune abandoned empty "Chat N" tabs before saving so they don't come
	// back on next boot. Rule: a session with zero history entries AND a
	// default auto-generated name ("Chat N") is considered abandoned and
	// gets dropped — both the in-memory entry and its on-disk files.
	// Sessions that are empty but renamed, or that have any history at all,
	// are preserved. We always keep at least one session per project.
	for _, p := range projects {
		p.pruneAbandonedEmptySessions()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveConfig()
}

// startBackground is the single lifecycle gate for Studio-owned goroutines.
// It returns false after shutdown has begun. The panic barrier is inside the
// tracked function so Done always runs (WaitGroup.Go defers it itself).
func (s *Studio) startBackground(label string, fn func()) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.shuttingDown {
		return false
	}
	s.wg.Go(func() {
		defer recoverPanic(label, s.LogEvent)
		fn()
	})
	return true
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
	if !utf8.ValidString(name) || !utf8.ValidString(directory) {
		return nil, fmt.Errorf("project name and directory must be valid UTF-8")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("project name cannot be empty")
	}
	if len(name) > 60 {
		name = truncateUTF8(name, 60)
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("project directory cannot be empty")
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("directory does not exist: %s", abs)
	}
	// One repository must be one project however the user spells its path.
	// \\wsl$\Ubuntu\x and \\wsl.localhost\Ubuntu\x name the same directory, and
	// registering both would give one repo two projects with two histories.
	remote := remoteProjectDirectory(abs)
	if remote {
		if canonicalUNC, ok := wsl.CanonicalWindowsPath(abs); ok {
			abs = canonicalUNC
		}
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Go maps only IO_REPARSE_TAG_SYMLINK to ModeSymlink; anything else the
		// 9P redirector reports becomes ModeIrregular and EvalSymlinks fails
		// with ENOTDIR. Refusing the project over that would make WSL
		// directories unusable, so fall back to the canonical spelling. A
		// non-WSL path still fails with the identical message.
		if !remote {
			return nil, fmt.Errorf("resolve project directory: %w", err)
		}
		canonical = abs
	}
	// Resolve for identity/validity checks, but preserve the user's absolute
	// spelling for display (on macOS /var commonly resolves to /private/var).
	info, err = os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("directory does not exist: %s", abs)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for duplicate directory.
	for _, existing := range s.projects {
		if sameProjectDirectory(existing.Directory, abs) {
			return nil, fmt.Errorf("project already registered: %s", abs)
		}
	}
	for _, archived := range s.archived {
		if sameProjectDirectory(archived.Project.Directory, abs) {
			return nil, fmt.Errorf("project is archived: %s", abs)
		}
	}
	if len(s.projects)+len(s.archived) >= StudioConfigMaxProjects {
		return nil, fmt.Errorf("project limit reached (%d)", StudioConfigMaxProjects)
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
	// Claude-style Git isolation starts with the very first chat, not only
	// chats created later through the + button. Persist the empty history too,
	// so the worktree metadata always points at a session that survives restart.
	defaultSession := p.sessions["default"]
	if defaultSession != nil {
		if err := provisionSessionWorktree(p, defaultSession, p.Directory); err != nil {
			p.Close()
			return nil, err
		}
		if err := SaveNewHistoryWithMetadata(projectSessionStorageKey(id, "default"), defaultSession.Name, "", nil); err != nil {
			_ = removeSessionWorktree(p, defaultSession)
			p.Close()
			return nil, fmt.Errorf("persist initial chat session: %w", err)
		}
	}
	projects := make([]ProjectConfig, 0, len(s.projects)+1)
	for _, existing := range s.projects {
		projects = append(projects, existing.ToConfig())
	}
	projects = append(projects, p.ToConfig())
	candidate := &StudioConfig{Projects: projects, Groups: s.config.Groups, Settings: s.config.Settings}
	// Publish only after the candidate config is durable. Otherwise the UI
	// would show a project that disappears on restart after a disk failure.
	s.configSaveMu.Lock()
	err = candidate.Save()
	s.configSaveMu.Unlock()
	if err != nil {
		if defaultSession != nil {
			_ = removeSessionWorktree(p, defaultSession)
			_ = deleteHistoryChecked(projectSessionStorageKey(id, "default"))
		}
		p.Close()
		return nil, fmt.Errorf("persist new project: %w", err)
	}
	s.projects[id] = p
	s.config.Projects = projects
	s.auditProjectAdded(name, abs)
	return p.Info(), nil
}

// RemoveProject removes a project from the studio.
func (s *Studio) RemoveProject(id string) error {
	s.mu.Lock()
	p, ok := s.projects[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("project not found: %s", id)
	}
	// Managed worktrees are app-owned directories but may contain valuable
	// uncommitted user changes. Fail before mutating config so project removal
	// can never silently discard them.
	p.mu.RLock()
	for _, session := range p.sessions {
		status := sessionWorktreeStatus(session)
		session.mu.RLock()
		sessionName := session.Name
		session.mu.RUnlock()
		if status.Error != "" {
			p.mu.RUnlock()
			s.mu.Unlock()
			return fmt.Errorf("cannot remove project while session %q has an unavailable worktree: %s", sessionName, status.Error)
		}
		if status.Dirty {
			p.mu.RUnlock()
			s.mu.Unlock()
			return fmt.Errorf("cannot remove project while session %q has %d uncommitted worktree change(s)", sessionName, status.ChangedFiles)
		}
	}
	p.mu.RUnlock()
	// Remove the project from the durable config before closing transports or
	// deleting any app-owned data. If the config write fails, the operation is
	// a clean no-op and the project remains fully usable after this call and
	// after restart. Leftover data after a later cleanup error is recoverable;
	// deleting data before a failed config write is not.
	projects := make([]ProjectConfig, 0, len(s.projects)-1)
	for projectID, existing := range s.projects {
		if projectID != id {
			projects = append(projects, existing.ToConfig())
		}
	}
	candidate := &StudioConfig{Projects: projects, Groups: s.config.Groups, Settings: s.config.Settings}
	s.configSaveMu.Lock()
	err := candidate.Save()
	s.configSaveMu.Unlock()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("persist project removal: %w", err)
	}
	s.config.Projects = projects
	p.mu.RLock()
	removedName := p.Name
	// Snapshot cleanup identities before detaching the project. Cleanup below
	// deliberately runs without s.mu so event hooks and process exit callbacks
	// may safely re-enter Studio.
	sessionIDs := make([]string, 0, len(p.sessions))
	for sid := range p.sessions {
		sessionIDs = append(sessionIDs, sid)
	}
	p.mu.RUnlock()
	delete(s.projects, id)
	terminals := make([]*Terminal, 0)
	for terminalID, terminal := range s.terminals {
		if terminal.ProjectID == id {
			delete(s.terminals, terminalID)
			terminals = append(terminals, terminal)
		}
	}
	s.mu.Unlock()

	// Mark every source/target delegation terminal before p.Close stops child
	// sessions. Otherwise its monitor could observe an idle child first and
	// commit a misleading completed result while the project is being removed.
	// The project is already absent from s.projects, so no new owner can appear
	// while this cancellation emits terminal events without a global lock.
	s.CancelDelegationsForSession(id, "")
	s.cancelSideQuestions(id, "")
	s.stopPreviewServers(id, "", true)
	s.stopExternalBrowserTabs(id, "")
	p.Close()
	p.mu.RLock()
	for _, session := range p.sessions {
		if err := removeSessionWorktreeAt(p, session, p.Directory); err != nil {
			s.logf("warn", "worktree", "cleanup while removing project %q session %q: %v", id, session.ID, err)
		}
	}
	p.mu.RUnlock()
	// Close any terminals owned by this project so their PTY fd + child
	// process + read-loop goroutine don't outlive the project. Without this,
	// backend cleanup depended entirely on the frontend firing CloseTerminal
	// on unmount, which can be missed (split view, lost termID reference).
	// Mirrors Shutdown's terminal loop; t.Close() is idempotent. Entries were
	// detached atomically with the project above, and processes are closed now
	// without s.mu so their exit callbacks may re-enter safely.
	for _, terminal := range terminals {
		terminal.Close()
	}
	// Legacy single-file history (pre-sessions).
	DeleteHistory(id)
	// Per-session history + replay. Explicitly include "default" in case the
	// sessions map was empty (never opened) but legacy files exist.
	hasDefault := false
	for _, sid := range sessionIDs {
		DeleteHistory(projectSessionStorageKey(id, sid))
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
	// Archived chats are project-owned metadata. The histories themselves were
	// removed above; drop the archive index as well so a later project with the
	// same generated ID cannot inherit stale hidden-session flags.
	removeAllSessionArchiveRecords(id)
	removeProjectSessionSuggestions(id)
	// Recurring prompts belong to the removed local project and must not keep
	// waking up as orphaned scheduler errors.
	if _, err := removeScheduledTasksFor(id, ""); err != nil {
		s.logf("warn", "scheduler", "cleanup while removing project %q: %v", id, err)
	}
	_ = s.refreshScheduledWakeNeed()
	// Versioned live artifacts can contain complete copies of project HTML/SVG.
	// Remove those private app-owned snapshots with the project.
	_ = removeArtifactVersionsFor(id)
	// Persisted preview sessions may contain login cookies and localStorage.
	// They are app-owned, project-scoped secrets and must not become orphans.
	removePreviewSessionProfiles(id, "")
	// Project knowledge is app-owned data, unlike the user's project directory.
	// Remove it with the project so deleted workspaces do not leave orphaned
	// copies of potentially sensitive documents under the config directory.
	_ = os.RemoveAll(knowledgeDir(id))
	invalidateKnowledgeCache(id)
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

// RelinkProjectDirectory reconnects an existing project to a folder that was
// moved or renamed outside Studio. The project ID and all app-owned state
// (sessions, drafts, pins, schedules, knowledge, artifacts, settings) stay the
// same. Path-keyed memory is cloned before the durable config commit so a
// successful relink cannot make saved project memory disappear on restart.
func (s *Studio) RelinkProjectDirectory(id, directory string) (*ProjectInfo, error) {
	if !utf8.ValidString(directory) {
		return nil, fmt.Errorf("project directory must be valid UTF-8")
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("project directory cannot be empty")
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("directory does not exist: %s", abs)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve project directory: %w", err)
	}
	info, err = os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("directory does not exist: %s", abs)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, fmt.Errorf("project not found: %s", id)
	}
	if projectHasActiveSession(p) {
		return nil, fmt.Errorf("stop all running chats in this project before reconnecting its folder")
	}
	for projectID, existing := range s.projects {
		if projectID == id {
			continue
		}
		existing.mu.RLock()
		existingDir := existing.Directory
		existing.mu.RUnlock()
		existingInfo, statErr := os.Stat(existingDir)
		if filepath.Clean(existingDir) == filepath.Clean(abs) || (statErr == nil && os.SameFile(existingInfo, info)) {
			return nil, fmt.Errorf("project already registered: %s", abs)
		}
	}
	for _, archived := range s.archived {
		archivedInfo, statErr := os.Stat(archived.Project.Directory)
		if filepath.Clean(archived.Project.Directory) == filepath.Clean(abs) || (statErr == nil && os.SameFile(archivedInfo, info)) {
			return nil, fmt.Errorf("project is archived: %s", abs)
		}
	}

	p.mu.RLock()
	oldDirectory := p.Directory
	projectName := p.Name
	memStore := p.memoryStore
	taskManager := p.taskManager
	p.mu.RUnlock()
	if filepath.Clean(oldDirectory) == filepath.Clean(abs) {
		return p.Info(), nil
	}
	if memStore != nil {
		if err := memStore.Flush(); err != nil {
			return nil, fmt.Errorf("flush project memory before reconnecting folder: %w", err)
		}
	}
	if err := memory.CloneProjectMemory(configDir(), oldDirectory, abs); err != nil {
		return nil, fmt.Errorf("preserve project memory: %w", err)
	}

	projects := make([]ProjectConfig, 0, len(s.projects))
	for projectID, existing := range s.projects {
		pc := existing.ToConfig()
		if projectID == id {
			pc.Directory = abs
		}
		projects = append(projects, pc)
	}
	candidate := &StudioConfig{Projects: projects, Groups: s.config.Groups, Settings: s.config.Settings}
	s.configSaveMu.Lock()
	err = candidate.Save()
	s.configSaveMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("persist project folder: %w", err)
	}

	// The durable path now owns the project. Tear down every path-bound runtime
	// object so the next GLM/Kimi turn rebuilds tools, sandbox, MCP, learning,
	// and background-task managers against the selected directory.
	p.Stop()
	if err := closeBackgroundTaskManager(taskManager); err != nil {
		s.logf("warn", "tasks", "timed out settling background tasks while reconnecting project %q: %v", id, err)
	}
	for terminalID, terminal := range s.terminals {
		if terminal.ProjectID == id {
			delete(s.terminals, terminalID)
			terminal.Close()
		}
	}
	pinnedContext, _ := tools.ReadPersistedPin(abs)
	p.mu.Lock()
	p.Directory = abs
	p.resetClientLocked()
	p.registry = nil
	p.memoryStore = nil
	p.projectLearning = nil
	p.taskManager = nil
	p.readTrackers = make(map[string]*tools.FileReadTracker)
	p.writeTrackers = make(map[string]*tools.FileWriteTracker)
	p.pinnedContext = pinnedContext
	p.mu.Unlock()
	s.config.Projects = projects
	s.auditProjectDirectory(projectName, oldDirectory, abs)
	return p.Info(), nil
}

// SetProjectProvider changes the LLM provider and model for a project.
func (s *Studio) SetProjectProvider(id, provider, model string) error {
	provider, model, err := s.validatedStudioProviderModel(provider, model)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	if projectHasActiveSession(p) {
		return fmt.Errorf("stop all running chats in this project before changing model settings")
	}
	p.mu.RLock()
	oldProv, oldModel, name := p.Provider, p.Model, p.Name
	p.mu.RUnlock()
	if err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.Provider, pc.Model = provider, model
	}, func(p *Project) {
		p.mu.Lock()
		p.Provider, p.Model = provider, model
		p.resetClientLocked()
		p.mu.Unlock()
	}); err != nil {
		return err
	}
	s.auditProjectProvider(name, oldProv, oldModel, provider, model)
	return nil
}

// ConfigureProjectModel atomically updates every setting exposed by the
// project model popover. Validating and persisting one future snapshot avoids
// the partial state that three sequential RPC calls could leave behind when a
// later validation or disk write failed.
func (s *Studio) ConfigureProjectModel(id, provider, model string, temperature float32, maxTokens int, mode string, budget int32) (*ProjectInfo, error) {
	provider, model, err := s.validatedStudioProviderModel(provider, model)
	if err != nil {
		return nil, err
	}
	if err := validateProjectModelParams(provider, model, temperature, maxTokens); err != nil {
		return nil, err
	}
	if err := validateProjectThinking(mode, budget); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, fmt.Errorf("project not found: %s", id)
	}
	if projectHasActiveSession(p) {
		return nil, fmt.Errorf("stop all running chats in this project before changing model settings")
	}
	p.mu.RLock()
	oldProvider, oldModel, name := p.Provider, p.Model, p.Name
	oldTemperature, oldMaxTokens := p.Temperature, p.MaxTokens
	oldMode, oldBudget := p.ThinkingMode, p.ThinkingBudget
	p.mu.RUnlock()

	if err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.Provider, pc.Model = provider, model
		pc.Temperature, pc.MaxTokens = temperature, maxTokens
		pc.ThinkingMode, pc.ThinkingBudget = mode, budget
	}, func(p *Project) {
		p.mu.Lock()
		p.Provider, p.Model = provider, model
		p.Temperature, p.MaxTokens = temperature, maxTokens
		p.ThinkingMode, p.ThinkingBudget = mode, budget
		p.resetClientLocked()
		p.mu.Unlock()
	}); err != nil {
		return nil, err
	}

	s.auditProjectProvider(name, oldProvider, oldModel, provider, model)
	s.auditProjectModelParams(name, oldTemperature, temperature, oldMaxTokens, maxTokens)
	s.auditProjectThinking(name, oldMode, oldBudget, mode, budget)
	return p.Info(), nil
}

func (s *Studio) validatedStudioProviderModel(provider, model string) (string, string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if provider == "" || model == "" || !utf8.ValidString(provider) || !utf8.ValidString(model) {
		return "", "", fmt.Errorf("provider and model must be non-empty valid UTF-8")
	}
	if len(provider) > 128 || len(model) > 256 {
		return "", "", fmt.Errorf("provider or model identifier is too long")
	}
	if err := s.validateAvailableStudioProviderModel(provider, model); err != nil {
		return "", "", err
	}
	return provider, model, nil
}

// SetProjectSystemPrompt changes the system prompt for a project.
func (s *Studio) SetProjectSystemPrompt(id, prompt string) error {
	if !utf8.ValidString(prompt) {
		return fmt.Errorf("system prompt must be valid UTF-8")
	}
	prompt = truncateUTF8(prompt, 64<<10)
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.RLock()
	oldPrompt, name := p.SystemPrompt, p.Name
	p.mu.RUnlock()
	if err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.SystemPrompt = prompt
	}, func(p *Project) {
		p.mu.Lock()
		p.SystemPrompt = prompt
		p.resetClientLocked()
		p.mu.Unlock()
	}); err != nil {
		return err
	}
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
	if projectHasActiveSession(p) {
		return fmt.Errorf("stop all running chats in this project before changing model settings")
	}
	p.mu.RLock()
	oldTemp, oldMax, name := p.Temperature, p.MaxTokens, p.Name
	provider, model := p.Provider, p.Model
	p.mu.RUnlock()
	if err := validateProjectModelParams(provider, model, temperature, maxTokens); err != nil {
		return err
	}
	if err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.Temperature, pc.MaxTokens = temperature, maxTokens
	}, func(p *Project) {
		p.mu.Lock()
		p.Temperature, p.MaxTokens = temperature, maxTokens
		p.resetClientLocked()
		p.mu.Unlock()
	}); err != nil {
		return err
	}
	s.auditProjectModelParams(name, oldTemp, temperature, oldMax, maxTokens)
	return nil
}

// SetProjectThinking configures extended thinking for a project.
// mode: "" = auto (provider default), "enabled" = on, "disabled" = off.
// budget: compatibility reasoning control; 0 uses the selected model's tuned
// default (GLM 5.2 max effort, Kimi K3 high effort).
func (s *Studio) SetProjectThinking(id, mode string, budget int32) error {
	if err := validateProjectThinking(mode, budget); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	if projectHasActiveSession(p) {
		return fmt.Errorf("stop all running chats in this project before changing model settings")
	}
	p.mu.RLock()
	oldMode, oldBudget, name := p.ThinkingMode, p.ThinkingBudget, p.Name
	p.mu.RUnlock()
	if err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.ThinkingMode, pc.ThinkingBudget = mode, budget
	}, func(p *Project) {
		p.mu.Lock()
		p.ThinkingMode, p.ThinkingBudget = mode, budget
		p.resetClientLocked()
		p.mu.Unlock()
	}); err != nil {
		return err
	}
	s.auditProjectThinking(name, oldMode, oldBudget, mode, budget)
	return nil
}

func validateProjectModelParams(provider, model string, temperature float32, maxTokens int) error {
	if temperature < 0 || temperature > 2 || math.IsNaN(float64(temperature)) || math.IsInf(float64(temperature), 0) {
		return fmt.Errorf("temperature must be finite and between 0 and 2")
	}
	if maxTokens < 0 {
		return fmt.Errorf("max tokens must be zero (model default) or positive")
	}
	limit := int(maxOutputTokens(provider, model))
	if limit <= 0 {
		return fmt.Errorf("unsupported provider/model: %s/%s", provider, model)
	}
	if maxTokens > limit {
		return fmt.Errorf("max tokens for %s/%s must be between 0 and %d", provider, model, limit)
	}
	return nil
}

func validateProjectThinking(mode string, budget int32) error {
	if mode != "" && mode != "enabled" && mode != "disabled" {
		return fmt.Errorf("invalid thinking mode %q: must be empty, \"enabled\", or \"disabled\"", mode)
	}
	if budget < 0 {
		return fmt.Errorf("invalid thinking budget %d: must be >= 0", budget)
	}
	if budget > 1_000_000 {
		return fmt.Errorf("invalid thinking budget %d: exceeds 1000000", budget)
	}
	return nil
}

// SetProjectPermissionMode sets Manual, Accept edits, reviewed Auto, or Skip.
// Sensitive actions remain hard-gated in every mode.
func (s *Studio) SetProjectPermissionMode(id, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "ask" {
		mode = "manual"
	}
	if mode == "" {
		mode = "auto"
	}
	if mode == "acceptedits" || mode == "accept-edits" {
		mode = "accept_edits"
	}
	if mode != "auto" && mode != "accept_edits" && mode != "manual" && mode != "skip" {
		return fmt.Errorf("invalid permission mode %q: must be auto, accept_edits, manual, or skip", mode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.PermissionMode = mode
	}, func(p *Project) {
		p.mu.Lock()
		p.PermissionMode = mode
		p.resetClientLocked()
		p.mu.Unlock()
	})
}

// SetProjectComputerUse enables or removes OS-level screen tools for one
// project. The provider client is rebuilt so its tool declarations and
// Kimi-vs-GLM screenshot behavior cannot remain stale.
func (s *Studio) SetProjectComputerUse(id string, enabled bool) error {
	if !enabled {
		return s.disableProjectComputerUse(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.ComputerUseEnabled = enabled
	}, func(p *Project) {
		p.mu.Lock()
		p.ComputerUseEnabled = enabled
		p.resetClientLocked()
		p.mu.Unlock()
	})
}

// disableProjectComputerUse keeps the stop and durable policy commit in one
// lifecycle transaction. Without this, a new turn could claim queueWorker
// after StopGeneration returned but before the setting/client changed, and
// continue executing the computer tools the user had just disabled.
func (s *Studio) disableProjectComputerUse(id string) error {
	s.mu.Lock()
	p, ok := s.projects[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("project not found: %s", id)
	}
	p.metadataMu.Lock()
	delegationNotices := s.cancelDelegationsForSessionQuiet(id, "")
	removed := p.Stop()
	err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.ComputerUseEnabled = false
	}, func(p *Project) {
		p.mu.Lock()
		p.ComputerUseEnabled = false
		p.resetClientLocked()
		p.mu.Unlock()
	})
	p.metadataMu.Unlock()
	s.mu.Unlock()

	for _, notice := range delegationNotices {
		s.emitDelegationStopNotice(notice, " while disabling computer use")
	}
	for sid, ids := range removed {
		if len(ids) > 0 {
			p.emitEvent(s.ctx, EventChatQueueCleared, ChatQueueEvent{
				ProjectID: id, SessionID: sid, IDs: ids,
			})
		}
	}
	return err
}

// SetProjectBudget sets the per-project monthly USD spend cap. The frontend
// uses it to draw a progress bar in the usage modal and warn at 80%/100%.
// Pass 0 to remove the budget. Capped at $100,000 to defend against typos.
// Does not invalidate the cached client (no model state depends on this).
func validateProjectBudget(budgetUSD float64) error {
	if budgetUSD < 0 || math.IsNaN(budgetUSD) || math.IsInf(budgetUSD, 0) {
		return fmt.Errorf("invalid budget %.2f: must be >= 0", budgetUSD)
	}
	const maxBudget = 100000.0
	if budgetUSD > maxBudget {
		return fmt.Errorf("budget %.2f exceeds maximum of %.2f", budgetUSD, maxBudget)
	}
	return nil
}

func (s *Studio) SetProjectBudget(id string, budgetUSD float64) error {
	if err := validateProjectBudget(budgetUSD); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.RLock()
	oldBudget, name := p.BudgetUSD, p.Name
	p.mu.RUnlock()
	if err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.BudgetUSD = budgetUSD
	}, func(p *Project) {
		p.mu.Lock()
		p.BudgetUSD = budgetUSD
		p.mu.Unlock()
	}); err != nil {
		return err
	}
	s.auditProjectBudget(name, oldBudget, budgetUSD)
	return nil
}

// ConfigureProjectBudget atomically persists the spend cap and enforcement
// switch exposed by the budget dialog. Keeping both fields in one config
// transaction prevents a successful cap update followed by a failed strict-
// mode update from leaving the desktop UI and runtime out of sync.
func (s *Studio) ConfigureProjectBudget(id string, budgetUSD float64, enforce bool) (*ProjectInfo, error) {
	if err := validateProjectBudget(budgetUSD); err != nil {
		return nil, err
	}
	if budgetUSD == 0 {
		enforce = false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, fmt.Errorf("project not found: %s", id)
	}
	p.mu.RLock()
	oldBudget, name := p.BudgetUSD, p.Name
	p.mu.RUnlock()
	if err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.BudgetUSD = budgetUSD
		pc.EnforceBudget = enforce
	}, func(p *Project) {
		p.mu.Lock()
		p.BudgetUSD = budgetUSD
		p.EnforceBudget = enforce
		p.mu.Unlock()
	}); err != nil {
		return nil, err
	}
	s.auditProjectBudget(name, oldBudget, budgetUSD)
	return p.Info(), nil
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
	return s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.EnforceBudget = enforce
	}, func(p *Project) {
		p.mu.Lock()
		p.EnforceBudget = enforce
		p.mu.Unlock()
	})
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
	p.mu.RLock()
	oldPinned, name := p.Pinned, p.Name
	p.mu.RUnlock()
	if err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.Pinned = pinned
	}, func(p *Project) {
		p.mu.Lock()
		p.Pinned = pinned
		p.mu.Unlock()
	}); err != nil {
		return err
	}
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
	p.mu.RLock()
	dir := p.Directory
	p.mu.RUnlock()
	// Remove the disk copy so it isn't restored on next startup. Non-fatal if
	// the file doesn't exist (pin was never persisted or already cleaned up).
	diskPath := filepath.Join(dir, ".gokin", "pinned_context.md")
	if err := os.Remove(diskPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not remove pinned context from disk: %w", err)
	}
	// Commit the in-memory change only after disk removal succeeds. Otherwise
	// the UI would show an empty pin until restart, when the stale disk copy
	// would unexpectedly reappear.
	p.mu.Lock()
	p.pinnedContext = ""
	p.mu.Unlock()
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
	p.metadataMu.Lock()
	defer p.metadataMu.Unlock()

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
	for {
		if _, exists := p.sessions[session.ID]; !exists {
			break
		}
		session = NewChatSession(fmt.Sprintf("Chat %d", maxNum+1))
	}
	name := session.Name
	sid := session.ID
	projectDir := p.Directory
	p.mu.Unlock()
	if err := provisionSessionWorktree(p, session, projectDir); err != nil {
		return nil, err
	}

	// Persist the (empty) session immediately so it survives an app restart
	// even if the user never sends a message in it — a fresh "Chat N" tab
	// the user opens to jot notes in tomorrow shouldn't disappear.
	// Do this before publishing the session in memory: returning success for an
	// unsaved tab makes it disappear on restart and leaves the UI believing the
	// operation was durable. metadataMu serializes the create/persist/publish
	// transaction while p.mu stays free during the potentially slow Git call.
	if err := SaveNewHistoryWithMetadata(projectSessionStorageKey(projectID, sid), name, "", nil); err != nil {
		_ = removeSessionWorktree(p, session)
		return nil, fmt.Errorf("persist new chat session: %w", err)
	}
	p.mu.Lock()
	if _, exists := p.sessions[session.ID]; exists {
		p.mu.Unlock()
		_ = removeSessionWorktree(p, session)
		_ = deleteHistoryChecked(projectSessionStorageKey(projectID, sid))
		return nil, fmt.Errorf("session ID collision: %s", session.ID)
	}
	p.sessions[session.ID] = session
	p.mu.Unlock()

	return session.Info(), nil
}

// ListChatSessions returns all sessions for a project.
func (s *Studio) ListChatSessions(projectID string) ([]*ChatSessionInfo, error) {
	return s.listChatSessions(projectID, false)
}

// ListArchivedChatSessions returns only reversibly archived sessions. Their
// history/worktree-owned data remains available for restore or explicit
// permanent deletion, but they never appear in the active tab catalog.
func (s *Studio) ListArchivedChatSessions(projectID string) ([]*ChatSessionInfo, error) {
	return s.listChatSessions(projectID, true)
}

func (s *Studio) listChatSessions(projectID string, archived bool) ([]*ChatSessionInfo, error) {
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
		if info.Archived != archived {
			continue
		}
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
	if archived {
		sort.SliceStable(result, func(i, j int) bool {
			if result[i].ArchivedAt != result[j].ArchivedAt {
				return result[i].ArchivedAt > result[j].ArchivedAt
			}
			return result[i].ID < result[j].ID
		})
		return result, nil
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
	p.metadataMu.Lock()
	defer p.metadataMu.Unlock()

	// Keep the project read-locked through persistence so session deletion or
	// creation cannot invalidate the snapshot while it is being committed.
	p.mu.RLock()
	sess, exists := p.sessions[sessionID]
	if !exists {
		p.mu.RUnlock()
		return fmt.Errorf("session not found: %s", sessionID)
	}
	// Build the future persisted state from the live sessions, overriding only
	// the requested target. Memory is changed after the durable write succeeds,
	// so an I/O error cannot create a state that reverses after restart.
	live := make(map[string]bool, len(p.sessions))
	for sid, ss := range p.sessions {
		ss.mu.RLock()
		isPinned := ss.Pinned
		if sid == sessionID {
			isPinned = pinned
		}
		if isPinned {
			live[sid] = true
		}
		ss.mu.RUnlock()
	}
	if err := savePinnedSessions(projectID, live); err != nil {
		p.mu.RUnlock()
		return fmt.Errorf("failed to persist session pins: %w", err)
	}
	sess.mu.Lock()
	sess.Pinned = pinned
	sess.mu.Unlock()
	p.mu.RUnlock()
	return nil
}

// RenameChatSession changes a session's display name.
func (s *Studio) RenameChatSession(projectID, sessionID, newName string) error {
	if !utf8.ValidString(newName) {
		return fmt.Errorf("session name must be valid UTF-8")
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("session name cannot be empty")
	}
	if len(newName) > 60 {
		newName = truncateUTF8(newName, 60)
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
	histSnapshot := make([]*genai.Content, len(session.history))
	copy(histSnapshot, session.history)
	parentID := session.ParentID
	var usageSnapshot *SessionUsage
	if session.usage != nil {
		copyUsage := *session.usage
		usageSnapshot = &copyUsage
	}
	// Keep the write lock through the metadata-only commit so no turn can
	// snapshot the old name and persist it after this rename.
	// Commit the metadata-only disk update first. Failure leaves both memory
	// and the existing history file untouched, and no stale history snapshot
	// is written over turns that completed concurrently.
	if err := RenameHistory(projectSessionStorageKey(projectID, sessionID), newName, parentID, usageSnapshot, histSnapshot); err != nil {
		session.mu.Unlock()
		return fmt.Errorf("persist session rename: %w", err)
	}
	session.Name = newName
	session.mu.Unlock()
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
	if err := validateRPCText("message", newText, ChatMessageMaxBytes, true); err != nil {
		return err
	}
	newText = strings.TrimSpace(newText)
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
	histSnapshot := make([]*genai.Content, trimTo)
	copy(histSnapshot, session.history[:trimTo])
	name := session.Name
	if err := SaveHistoryWithName(projectSessionStorageKey(projectID, sid), name, histSnapshot); err != nil {
		session.mu.Unlock()
		return fmt.Errorf("persist edited session: %w", err)
	}
	session.history = histSnapshot
	session.mu.Unlock()

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
	if !utf8.ValidString(newName) {
		return nil, fmt.Errorf("session name must be valid UTF-8")
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
	p.metadataMu.Lock()
	defer p.metadataMu.Unlock()
	p.mu.RLock()
	source, exists := p.sessions[sid]
	p.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("session not found: %s", sid)
	}

	// Snapshot source history under its lock so a concurrent SendMessage
	// can't mutate the slice while we're cloning it.
	source.mu.RLock()
	sourceName := source.Name
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
		name = truncateUTF8(name, 60)
	}

	newSession := NewChatSession(name)
	p.mu.RLock()
	for {
		if _, exists := p.sessions[newSession.ID]; !exists {
			break
		}
		newSession = NewChatSession(name)
	}
	p.mu.RUnlock()
	newSession.history = forkedHistory
	newSession.ParentID = sid // remember which session we forked from
	newID := newSession.ID
	p.mu.RLock()
	sourceWorkDir := p.Directory
	p.mu.RUnlock()
	source.mu.RLock()
	if source.WorktreeWorkDir != "" && source.WorktreeError == "" {
		sourceWorkDir = source.WorktreeWorkDir
	}
	source.mu.RUnlock()
	if err := provisionSessionWorktree(p, newSession, sourceWorkDir); err != nil {
		return nil, err
	}

	// Persist immediately with explicit parent ID so the fork survives a
	// restart even if the user never sends a new message in it. Use the
	// metadata variant rather than SaveHistoryWithName so the parent ID
	// is stamped on the FIRST write (not preserved from a non-existent
	// previous file).
	if err := SaveNewHistoryWithMetadata(projectSessionStorageKey(projectID, newID), name, sid, forkedHistory); err != nil {
		_ = removeSessionWorktree(p, newSession)
		return nil, fmt.Errorf("persist forked session: %w", err)
	}
	p.mu.Lock()
	if _, exists := p.sessions[newID]; exists {
		p.mu.Unlock()
		return nil, fmt.Errorf("session ID collision: %s", newID)
	}
	p.sessions[newID] = newSession
	p.mu.Unlock()

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
	return s.deleteChatSession(projectID, sessionID, nil)
}

// deleteChatSession is the guarded internal form used by background retention.
// The guard runs while p.mu is held for writing, immediately before any
// cancellation or disk mutation, so an idle/clean preflight cannot race a new
// turn that starts in the same chat. Interactive deletion passes no guard and
// retains the public method's existing behaviour.
func (s *Studio) deleteChatSession(projectID, sessionID string, guard func(*Project, *ChatSession) error) error {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("project not found: %s", projectID)
	}
	studioLocked := true
	p.metadataMu.Lock()
	metadataLocked := true
	var delegationNotices []delegationStopNotice
	var removedQueueIDs []string
	defer func() {
		if metadataLocked {
			p.metadataMu.Unlock()
		}
		if studioLocked {
			s.mu.RUnlock()
		}
		for _, notice := range delegationNotices {
			s.emitDelegationStopNotice(notice, " while deleting session")
		}
		if len(removedQueueIDs) > 0 {
			p.emitEvent(s.ctx, EventChatQueueCleared, ChatQueueEvent{
				ProjectID: projectID, SessionID: sessionID, IDs: removedQueueIDs,
			})
		}
	}()

	// Hold the write lock for the guard check AND the delete so two concurrent
	// deletion calls can't both pass the "at least 2 sessions" guard.
	p.mu.Lock()
	session, exists := p.sessions[sessionID]
	if !exists {
		p.mu.Unlock()
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if guard != nil {
		if err := guard(p, session); err != nil {
			p.mu.Unlock()
			return err
		}
	}
	activeSessionCount := 0
	for _, candidate := range p.sessions {
		candidate.mu.RLock()
		if candidate.ArchivedAt == 0 {
			activeSessionCount++
		}
		candidate.mu.RUnlock()
	}
	session.mu.RLock()
	targetArchived := session.ArchivedAt > 0
	session.mu.RUnlock()
	if !targetArchived && activeSessionCount <= 1 {
		p.mu.Unlock()
		return fmt.Errorf("cannot delete the last remaining session")
	}
	// Claim the exact terminal outcome before Stop makes the child look like a
	// naturally completed turn to its monitor. The metadata topology lock held
	// here prevents a new source/target delegation from appearing during this
	// cancellation pass.
	delegationNotices = s.cancelDelegationsForSessionQuiet(projectID, sessionID)
	s.cancelSideQuestions(projectID, sessionID)
	s.stopPreviewServers(projectID, sessionID, true)
	s.stopExternalBrowserTabs(projectID, sessionID)
	// Cancel before taking the history-file lock. The agent loop observes the
	// cancellation and skips its final save; an already-running save completes
	// first under the same per-file lock and is then removed below.
	removedQueueIDs = session.Stop()
	session.mu.RLock()
	nameSnapshot := session.Name
	parentSnapshot := session.ParentID
	historySnapshot := append([]*genai.Content(nil), session.history...)
	var usageSnapshot *SessionUsage
	if session.usage != nil {
		copyUsage := *session.usage
		usageSnapshot = &copyUsage
	}
	session.mu.RUnlock()
	if err := deleteHistoryChecked(projectSessionStorageKey(projectID, sessionID)); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("delete session history: %w", err)
	}
	if err := removeSessionWorktreeAt(p, session, p.Directory); err != nil {
		restoreErr := SaveHistoryWithUsage(
			projectSessionStorageKey(projectID, sessionID), nameSnapshot, parentSnapshot, usageSnapshot, historySnapshot,
		)
		p.mu.Unlock()
		if restoreErr != nil {
			return fmt.Errorf("delete blocked by worktree safety (%v); restoring session history also failed: %w", err, restoreErr)
		}
		return err
	}
	delete(p.sessions, sessionID)
	p.mu.Unlock()
	// The topology barrier can now be released. Matching delegations were
	// terminalised above, before the session was stopped or removed.
	p.metadataMu.Unlock()
	metadataLocked = false
	s.mu.RUnlock()
	studioLocked = false
	_ = removeSessionArchiveRecord(projectID, sessionID)

	DiscardReplay(projectID, sessionID)
	removedRuns, scheduledCleanupErr := removeScheduledTasksFor(projectID, sessionID)
	for _, run := range removedRuns {
		if scheduledTaskRunTerminal(run.Status) {
			continue
		}
		if child := p.GetSession(run.SessionID); child != nil && child.ID == run.SessionID {
			child.Stop()
		}
	}
	if scheduledCleanupErr != nil {
		s.logf("warn", "scheduler", "cleanup after deleting session %q/%q: %v", projectID, sessionID, scheduledCleanupErr)
	}
	_ = s.refreshScheduledWakeNeed()
	// Drop the persisted draft for this session — once the session is gone,
	// keeping its draft on disk just consumes inodes.
	_ = s.ClearDraft(projectID, sessionID)
	removeSessionSuggestions(projectID, sessionID)
	// Same for pinned messages — pins anchor to a session that no longer exists.
	removeSessionPins(projectID, sessionID)
	// Session-scoped artifact snapshots can contain sensitive generated output;
	// once their worktree is gone they have no valid restore target.
	_ = removeArtifactVersionsForSession(projectID, sessionID)
	// The same ownership rule applies to opt-in preview cookies/localStorage.
	removePreviewSessionProfiles(projectID, sessionID)
	// The deleted session is gone from p.sessions; re-derive the project cost
	// cache so its usage no longer counts toward a strict-budget block.
	p.invalidateCostCache()
	return nil
}

// --- Chat ---

// SendMessage sends a message to a project's agent (async -- results via events).
// sessionID can be empty for the default session.
func (s *Studio) SendMessage(projectID, message, sessionID string) error {
	if err := validateRPCText("message", message, ChatMessageMaxBytes, true); err != nil {
		return err
	}
	return s.startMessage(projectID, message, nil, sessionID)
}

// SendMessageWithAttachments validates images/native documents and sends their
// bounded model-ready parts. The regular SendMessage RPC stays text-only.
func (s *Studio) SendMessageWithAttachments(projectID, message string, attachments []MessageAttachment, sessionID string) error {
	if err := validateRPCText("message", message, ChatMessageMaxBytes, false); err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" && len(attachments) == 0 {
		return fmt.Errorf("message or attachment is required")
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	provider, model := p.Provider, p.Model
	p.mu.RUnlock()
	parts, err := decodeMessageAttachments(provider, model, attachments)
	if err != nil {
		return err
	}
	return s.startMessage(projectID, message, parts, sessionID)
}

func (s *Studio) startMessage(projectID, message string, attachmentParts []*genai.Part, sessionID string) error {
	return s.startMessageWithQueueEvent(projectID, message, attachmentParts, sessionID, nil, nil)
}

// startMessageWithQueueEvent atomically claims an idle session and, for an
// incoming cross-session message, publishes its user-card before releasing the
// worker to call the provider. This prevents a very fast response from racing
// ahead of the attributed incoming turn in the frontend transcript.
func (s *Studio) startMessageWithQueueEvent(projectID, message string, attachmentParts []*genai.Part, sessionID string, startEvent *ChatQueueEvent, delegation *delegationStamp) error {
	return s.startMessageWithQueueEventPermission(projectID, message, attachmentParts, sessionID, startEvent, "", delegation)
}

func (s *Studio) startMessageWithQueueEventPermission(projectID, message string, attachmentParts []*genai.Part, sessionID string, startEvent *ChatQueueEvent, firstPermissionMode string, delegation *delegationStamp) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.startMessageWithQueueEventPermissionLocked(projectID, message, attachmentParts, sessionID, startEvent, firstPermissionMode, delegation)
}

// startMessageWithQueueEventPermissionLocked carries the body. The caller MUST
// already hold s.mu for reading and must keep holding it until this returns.
//
// It exists because sync.RWMutex read locks are not reentrant: a caller that
// holds s.mu.RLock and then reaches a second s.mu.RLock blocks forever as soon
// as any goroutine is waiting on s.mu.Lock (Go deliberately blocks new readers
// so a pending writer can make progress). dispatchScheduledTask holds the read
// lock across the whole run precisely so ArchiveProject cannot slip in between
// creating the child session and claiming its queue worker, so it calls this
// variant instead of the locking wrapper above.
func (s *Studio) startMessageWithQueueEventPermissionLocked(projectID, message string, attachmentParts []*genai.Part, sessionID string, startEvent *ChatQueueEvent, firstPermissionMode string, delegation *delegationStamp) error {
	p, ok := s.projects[projectID]
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	// metadataMu is the session-topology transaction barrier shared with
	// create/delete/archive/import/clear/stop. Hold it only through the
	// synchronous queue claim; the provider goroutine never inherits it.
	p.metadataMu.Lock()
	defer p.metadataMu.Unlock()
	return s.startMessageWithQueueEventPermissionTopologyLocked(
		projectID, message, attachmentParts, sessionID, startEvent,
		firstPermissionMode, delegation,
	)
}

// startMessageWithQueueEventPermissionTopologyLocked is used only when the
// caller already owns the target project's metadataMu (delegation startup).
// The caller must also hold s.mu for reading.
func (s *Studio) startMessageWithQueueEventPermissionTopologyLocked(projectID, message string, attachmentParts []*genai.Part, sessionID string, startEvent *ChatQueueEvent, firstPermissionMode string, delegation *delegationStamp) error {
	p, ok := s.projects[projectID]
	settings := s.config.Settings
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	// Resolve the explicit ID exactly while holding the project lock through
	// the synchronous claim. Falling back to "default" here can route a queued
	// or cross-session message into the wrong conversation if its target was
	// concurrently deleted.
	p.mu.RLock()
	session, exists := p.sessions[sid]
	if !exists || session == nil {
		p.mu.RUnlock()
		return fmt.Errorf("session not found: %s", sid)
	}
	session.mu.Lock()
	if session.queueWorker {
		session.mu.Unlock()
		p.mu.RUnlock()
		return fmt.Errorf("agent is already running in this chat; queue the follow-up instead")
	}
	if session.ArchivedAt > 0 {
		session.mu.Unlock()
		p.mu.RUnlock()
		return fmt.Errorf("session is archived; restore it before sending messages")
	}
	session.queueWorker = true
	session.queueHalt = false
	// Same critical section as the claim: a delegated turn must never be able
	// to start without its chain stamp attached.
	session.incomingDelegation = delegation
	session.mu.Unlock()
	p.mu.RUnlock()
	// ArchiveProject takes s.mu.Lock and checks queueWorker. The caller holds
	// s.mu for reading across this synchronous claim, which prevents a detached
	// project pointer from starting work immediately after the project was
	// archived.
	// Go 1.25's WaitGroup.Go subsumes the Add(1) + defer Done() boilerplate
	// and scopes the goroutine to the wg lifecycle in one call.
	//
	// iter 970+: defense-in-depth panic barrier. SendMessage has its own
	// internal recover at function entry (project.go:565), but if the
	// closure itself panics before SendMessage starts (extremely rare but
	// possible if `p`/`settings` capture a poisoned value), this catches it
	// and surfaces in the event log instead of killing the process.
	startGate := make(chan struct{})
	if !s.startBackground("send-message", func() {
		<-startGate
		defer func() {
			// Keep the synchronous claim self-healing even if code between
			// agent turns panics; startBackground's panic barrier reports the
			// crash, while this defer ensures the session can be used again.
			session.mu.Lock()
			session.queueWorker = false
			session.queueHalt = false
			session.mu.Unlock()
		}()
		nextMessage := message
		nextParts := attachmentParts
		nextSettings := settings
		permissionMode := firstPermissionMode
		for {
			if permissionMode != "" {
				p.sendMessageWithPermissionMode(s.ctx, nextMessage, nextSettings, permissionMode, sid)
			} else {
				p.SendMessageWithAttachments(s.ctx, nextMessage, nextParts, nextSettings, sid)
			}
			permissionMode = ""

			session.mu.Lock()
			if session.queueHalt || len(session.queuedTurns) == 0 {
				session.mu.Unlock()
				return
			}
			next := session.queuedTurns[0]
			session.queuedTurns = session.queuedTurns[1:]
			// Re-arm the stamp for the turn we are about to run. The previous
			// turn cleared it, so without this a queued delegated follow-up
			// would run as if a human had typed it.
			session.incomingDelegation = next.Delegation
			session.mu.Unlock()

			p.emitEvent(s.ctx, EventChatQueueStarted, ChatQueueEvent{
				ProjectID: projectID,
				SessionID: sid,
				ID:        next.ID,
			})
			s.mu.RLock()
			nextSettings = s.config.Settings
			s.mu.RUnlock()
			nextMessage = next.Message
			nextParts = next.AttachmentParts
		}
	}) {
		session.mu.Lock()
		session.queueWorker = false
		session.queueHalt = false
		session.mu.Unlock()
		return fmt.Errorf("studio is shutting down")
	}
	released := false
	defer func() {
		if !released {
			close(startGate)
		}
	}()
	if startEvent != nil {
		startEvent.ProjectID = projectID
		startEvent.SessionID = sid
		p.emitEvent(s.ctx, EventChatQueueStarted, *startEvent)
	}
	close(startGate)
	released = true
	return nil
}

const (
	maxQueuedTurns      = 8
	maxQueuedMediaBytes = 64 << 20
)

// QueueMessage appends a text follow-up to the active session. queueID is
// generated by the frontend so queue lifecycle events can be reconciled
// without depending on RPC/event delivery order.
func (s *Studio) QueueMessage(projectID, message, sessionID, queueID string) error {
	if err := validateRPCText("message", message, ChatMessageMaxBytes, true); err != nil {
		return err
	}
	return s.queueMessage(projectID, message, nil, sessionID, queueID)
}

// QueueMessageWithAttachments queues validated image or document attachments.
func (s *Studio) QueueMessageWithAttachments(projectID, message string, attachments []MessageAttachment, sessionID, queueID string) error {
	if err := validateRPCText("message", message, ChatMessageMaxBytes, false); err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" && len(attachments) == 0 {
		return fmt.Errorf("message or attachment is required")
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	provider, model := p.Provider, p.Model
	p.mu.RUnlock()
	parts, err := decodeMessageAttachments(provider, model, attachments)
	if err != nil {
		return err
	}
	return s.queueMessage(projectID, message, parts, sessionID, queueID)
}

func (s *Studio) queueMessage(projectID, message string, attachmentParts []*genai.Part, sessionID, queueID string) error {
	return s.queueMessageWithDelegation(projectID, message, attachmentParts, sessionID, queueID, nil)
}

// queueMessageWithDelegation is queueMessage plus the cross-agent chain stamp.
// The stamp rides on the queued item rather than on the session because a
// session can hold several queued turns, and each must keep its own depth.
func (s *Studio) queueMessageWithDelegation(projectID, message string, attachmentParts []*genai.Part, sessionID, queueID string, delegation *delegationStamp) error {
	queueID = strings.TrimSpace(queueID)
	if queueID == "" || len(queueID) > 128 {
		return fmt.Errorf("invalid queue ID")
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("project not found: %s", projectID)
	}
	defer s.mu.RUnlock()
	p.metadataMu.Lock()
	defer p.metadataMu.Unlock()
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	// Exact lookup: an expired tab/cross-session target must never enqueue into
	// the default chat merely because Project.GetSession has a UI convenience
	// fallback.
	p.mu.RLock()
	session, exists := p.sessions[sid]
	if !exists || session == nil {
		p.mu.RUnlock()
		return fmt.Errorf("session not found: %s", sid)
	}

	session.mu.Lock()
	defer p.mu.RUnlock()
	defer session.mu.Unlock()
	if session.ArchivedAt > 0 {
		return fmt.Errorf("session is archived; restore it before queueing messages")
	}
	if !session.queueWorker {
		return fmt.Errorf("agent is no longer running; send this message normally")
	}
	if session.queueHalt {
		return fmt.Errorf("agent is stopping; wait for the session to become idle")
	}
	if len(session.queuedTurns) >= maxQueuedTurns {
		return fmt.Errorf("message queue is full (maximum %d)", maxQueuedTurns)
	}
	queuedMediaBytes := 0
	for _, turn := range session.queuedTurns {
		if turn == nil {
			continue
		}
		if turn.ID == queueID {
			return fmt.Errorf("duplicate queue ID")
		}
		queuedMediaBytes += attachmentPartsBytes(turn.AttachmentParts)
	}
	queuedMediaBytes += attachmentPartsBytes(attachmentParts)
	if queuedMediaBytes > maxQueuedMediaBytes {
		return fmt.Errorf("queued attachments exceed the %d MiB limit", maxQueuedMediaBytes>>20)
	}
	session.queuedTurns = append(session.queuedTurns, &queuedTurn{
		ID:              queueID,
		Message:         message,
		AttachmentParts: attachmentParts,
		QueuedAt:        time.Now().UnixMilli(),
		Delegation:      delegation,
	})
	return nil
}

func attachmentPartsBytes(parts []*genai.Part) int {
	total := 0
	for _, part := range parts {
		if part != nil && part.InlineData != nil {
			total += len(part.InlineData.Data)
		}
	}
	return total
}

// RemoveQueuedMessage removes one not-yet-started follow-up.
func (s *Studio) RemoveQueuedMessage(projectID, sessionID, queueID string) error {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("project not found: %s", projectID)
	}
	defer s.mu.RUnlock()
	p.metadataMu.Lock()
	defer p.metadataMu.Unlock()
	sid := sessionID
	if sid == "" {
		sid = "default"
	}
	p.mu.RLock()
	session, exists := p.sessions[sid]
	p.mu.RUnlock()
	if !exists || session == nil {
		return fmt.Errorf("session not found: %s", sid)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	for i, turn := range session.queuedTurns {
		if turn != nil && turn.ID == queueID {
			session.queuedTurns = append(session.queuedTurns[:i], session.queuedTurns[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("queued message not found")
}

// StopGeneration cancels the current agent run for a specific session (or all if empty).
func (s *Studio) StopGeneration(projectID, sessionID string) error {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("project not found: %s", projectID)
	}
	// A delegation run owns more than the ChatSession.active bit: its durable
	// row is what the caller's card, cancel button and restart recovery trust.
	// Commit that row as stopped before session.Stop makes the monitor observe
	// an apparently natural completion. metadataMu is the same topology barrier
	// used by delegation startup, so no matching run can appear in the gap.
	p.metadataMu.Lock()
	delegationNotices := s.cancelDelegationsForSessionQuiet(projectID, sessionID)
	removed := make(map[string][]string)
	if sessionID == "" {
		removed = p.Stop()
	} else {
		ids := p.StopSession(sessionID)
		if len(ids) > 0 {
			removed[sessionID] = ids
		}
	}
	p.metadataMu.Unlock()
	s.mu.RUnlock()
	for _, notice := range delegationNotices {
		s.emitDelegationStopNotice(notice, " during Stop")
	}
	for sid, ids := range removed {
		if len(ids) > 0 {
			p.emitEvent(s.ctx, EventChatQueueCleared, ChatQueueEvent{
				ProjectID: projectID, SessionID: sid, IDs: ids,
			})
		}
	}
	return nil
}

// ClearHistory resets chat history for a session.
func (s *Studio) ClearHistory(projectID, sessionID string) error {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	if !ok {
		s.mu.RUnlock()
		return fmt.Errorf("project not found: %s", projectID)
	}
	studioLocked := true
	p.metadataMu.Lock()
	metadataLocked := true
	var delegationNotices []delegationStopNotice
	var removedQueueIDs []string
	var sid string
	defer func() {
		if metadataLocked {
			p.metadataMu.Unlock()
		}
		if studioLocked {
			s.mu.RUnlock()
		}
		for _, notice := range delegationNotices {
			s.emitDelegationStopNotice(notice, " while clearing history")
		}
		if len(removedQueueIDs) > 0 {
			p.emitEvent(s.ctx, EventChatQueueCleared, ChatQueueEvent{
				ProjectID: projectID, SessionID: sid, IDs: removedQueueIDs,
			})
		}
	}()
	sid = sessionID
	if sid == "" {
		sid = "default"
	}
	session := p.GetSession(sid)
	if session == nil {
		return fmt.Errorf("session not found: %s", sid)
	}
	// Claim the delegation's terminal row while the same topology barrier used
	// by startup is held. The direct ClearHistory RPC is therefore safe even if
	// a caller did not issue StopGeneration first.
	delegationNotices = s.cancelDelegationsForSessionQuiet(projectID, sid)
	s.cancelSideQuestions(projectID, sid)
	// Invalidate the snapshot held by any side question before touching disk.
	// A second bump after the clear closes the small window in which a brand-new
	// side question could snapshot the old transcript while deletion runs.
	session.mu.Lock()
	session.historyEpoch++
	session.mu.Unlock()
	// Stop any active generation first. Clearing mid-turn otherwise leaves
	// the agent goroutine appending a model response into a freshly-emptied
	// history, ending with "model" as the only turn — which then fails the
	// next LLM call. The Stop call is synchronous via cancelFn; the goroutine
	// will exit on its next ctx.Err() check and skip its final SaveHistory.
	removedQueueIDs = session.Stop()
	// Delete the durable copy before mutating memory. If the disk operation
	// fails, keep the conversation visible instead of claiming it was cleared
	// only to restore it after restart.
	if err := deleteHistoryChecked(projectSessionStorageKey(projectID, sid)); err != nil {
		return fmt.Errorf("clear session history: %w", err)
	}
	session.mu.Lock()
	session.history = nil
	// The session's recorded usage was attributed to the history we're deleting,
	// so clear it too — otherwise the cumulative cost (and a strict-budget block)
	// would keep counting tokens from a conversation that no longer exists.
	session.usage = nil
	session.historyEpoch++
	session.mu.Unlock()
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
	// We just zeroed this session's usage; re-derive the project cost cache so
	// the strict-budget gate reflects the reduction instead of staying stuck.
	p.invalidateCostCache()
	p.metadataMu.Unlock()
	metadataLocked = false
	s.mu.RUnlock()
	studioLocked = false
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
	consumedSuggestions, _ := loadConsumedSessionSuggestions(projectID, sid)

	session.mu.RLock()
	defer session.mu.RUnlock()

	var msgs []ChatMessage
	for _, c := range session.history {
		for _, part := range c.Parts {
			if part == nil || part.FunctionCall == nil || part.FunctionCall.Name != "session_agent" ||
				!strings.EqualFold(strings.TrimSpace(stringArg(part.FunctionCall.Args, "action")), "suggest") {
				continue
			}
			args := make(map[string]any, len(part.FunctionCall.Args))
			for key, value := range part.FunctionCall.Args {
				args[key] = value
			}
			title := strings.TrimSpace(stringArg(args, "name"))
			prompt := strings.TrimSpace(stringArg(args, "message"))
			if title == "" || prompt == "" {
				continue
			}
			success := true
			msgs = append(msgs, ChatMessage{
				ID: GenerateID(), Role: "tool", ToolName: "session_agent", ToolArgs: args,
				ToolSuccess: &success, Content: "Suggested as a separate task.",
				Consumed: consumedSuggestions[sessionSuggestionKey(title, prompt)],
			})
		}
		text := ""
		var attachments []ChatAttachment
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
			if part.InlineData != nil && len(part.InlineData.Data) > 0 {
				attachments = append(attachments, ChatAttachment{
					Name:     attachmentDisplayName(len(attachments), part.InlineData),
					MIMEType: part.InlineData.MIMEType,
					Data:     base64.StdEncoding.EncodeToString(part.InlineData.Data),
					Size:     len(part.InlineData.Data),
				})
			}
		}
		text = stripDocumentAttachmentContext(text)
		if text == "" && len(attachments) == 0 {
			continue
		}
		role := c.Role
		if role == "model" {
			role = "assistant"
		}
		msgs = append(msgs, ChatMessage{
			ID:          GenerateID(),
			Role:        role,
			Content:     text,
			Timestamp:   0,
			Attachments: attachments,
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
	MatchOffset int    `json:"matchOffset"` // UTF-16 index within Snippet (JavaScript String.slice compatible)
	MessageHash string `json:"messageHash"` // SHA-256 of normalized role + visible message text for reliable post-load jumps
}

// SearchProjectHistory does a case-insensitive substring search of every
// chat session's text history within a project. Empty/whitespace queries
// return no hits. Each session contributes at most 5 hits so a noisy match
// can't overwhelm the UI. Caps total result count at 200.
func (s *Studio) SearchProjectHistory(projectID, query string) ([]SearchHit, error) {
	if err := validateRPCText("search query", query, HistoryQueryMaxBytes, false); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return []SearchHit{}, nil
	}
	needle := strings.ToLower(q)
	needleRunes := utf8.RuneCountInString(needle)

	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	// Snapshot session pointers under p.mu so we can release the project lock
	// before reading session-owned metadata — avoids holding two locks at once.
	p.mu.RLock()
	type sessRef struct {
		id         string
		name       string
		lastUsedAt int64
		pinned     bool
		sess       *ChatSession
	}
	sessions := make([]sessRef, 0, len(p.sessions))
	for sid, sess := range p.sessions {
		// Name/order metadata is captured under session.mu below.
		sessions = append(sessions, sessRef{id: sid, sess: sess})
	}
	p.mu.RUnlock()
	for i := range sessions {
		sessions[i].sess.mu.RLock()
		sessions[i].name = sessions[i].sess.Name
		sessions[i].lastUsedAt = sessions[i].sess.lastUsedAt
		sessions[i].pinned = sessions[i].sess.Pinned
		sessions[i].sess.mu.RUnlock()
	}
	// Stable, useful ordering: pinned sessions first, then most recently used,
	// then name/ID. Map iteration order must never reshuffle keyboard-selected
	// search results between identical queries.
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].pinned != sessions[j].pinned {
			return sessions[i].pinned
		}
		if sessions[i].lastUsedAt != sessions[j].lastUsedAt {
			return sessions[i].lastUsedAt > sessions[j].lastUsedAt
		}
		nameI, nameJ := strings.ToLower(sessions[i].name), strings.ToLower(sessions[j].name)
		if nameI != nameJ {
			return nameI < nameJ
		}
		if sessions[i].name != sessions[j].name {
			return sessions[i].name < sessions[j].name
		}
		return sessions[i].id < sessions[j].id
	})

	const (
		perSessionCap = 5
		totalCap      = 200
		snippetWindow = 60 // chars of context on each side of the match
	)
	hits := make([]SearchHit, 0, 32)

	for _, ref := range sessions {
		ref.sess.mu.RLock()
		sessName := ref.name
		filteredIdx := -1
		count := 0
		for _, c := range ref.sess.history {
			var textBuilder strings.Builder
			hasAttachment := false
			for _, part := range c.Parts {
				if part.Thought {
					continue
				}
				if part.Text != "" {
					textBuilder.WriteString(part.Text)
				}
				if part.InlineData != nil && len(part.InlineData.Data) > 0 {
					hasAttachment = true
				}
			}
			text := stripDocumentAttachmentContext(textBuilder.String())
			if text == "" && !hasAttachment {
				continue
			}
			filteredIdx++
			if text == "" {
				continue
			}
			lo := strings.ToLower(text)
			matchByte := strings.Index(lo, needle)
			if matchByte < 0 {
				continue
			}
			// strings.ToLower applies a one-rune-to-one-rune Unicode mapping, so
			// the rune index in lo maps back to the original text even when the
			// encoded byte lengths differ. Build the window in runes to avoid
			// slicing through UTF-8, then report a UTF-16 offset because React's
			// String.slice indexes JavaScript code units, not UTF-8 bytes.
			matchRune := utf8.RuneCountInString(lo[:matchByte])
			textRunes := []rune(text)
			role := c.Role
			if role == "model" {
				role = "assistant"
			}
			messageDigest := sha256.Sum256([]byte(role + "\x00" + text))
			start := max(matchRune-snippetWindow, 0)
			end := min(matchRune+needleRunes+snippetWindow, len(textRunes))
			snippetCore := string(textRunes[start:end])
			matchPrefix := string(textRunes[start:matchRune])
			snippet := snippetCore
			prefix := ""
			if start > 0 {
				prefix = "…"
				snippet = prefix + snippet
			}
			if end < len(textRunes) {
				snippet += "…"
			}
			matchOff := len(utf16.Encode([]rune(prefix + matchPrefix)))
			hits = append(hits, SearchHit{
				SessionID:   ref.id,
				SessionName: sessName,
				MessageIdx:  filteredIdx,
				Role:        role,
				Snippet:     snippet,
				MatchOffset: matchOff,
				MessageHash: hex.EncodeToString(messageDigest[:]),
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

// UpdateMemoryEntry lets the user correct a remembered fact without changing
// its identity, scope, timestamp, reinforcement, or retrieval history. The
// memory store rebuilds automatic tags and invalidates its context cache.
func (s *Studio) UpdateMemoryEntry(projectID, entryID, content string) (MemoryEntryInfo, error) {
	if err := validateRPCText("memory entry ID", entryID, MemoryEntryIDMaxBytes, true); err != nil {
		return MemoryEntryInfo{}, err
	}
	if err := validateRPCText("memory content", content, MemoryContentMaxBytes, true); err != nil {
		return MemoryEntryInfo{}, err
	}
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return MemoryEntryInfo{}, fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	store := p.memoryStore
	p.mu.RUnlock()
	if store == nil {
		return MemoryEntryInfo{}, fmt.Errorf("memory not initialised for this project")
	}
	if err := store.Edit(entryID, content); err != nil {
		return MemoryEntryInfo{}, err
	}
	// A direct user edit should survive an immediate crash/restart instead of
	// waiting for the store's normal two-second agent-write debounce.
	if err := store.Flush(); err != nil {
		return MemoryEntryInfo{}, fmt.Errorf("persist memory update: %w", err)
	}
	entry, ok := store.GetByID(entryID)
	if !ok || entry == nil {
		return MemoryEntryInfo{}, fmt.Errorf("memory entry not found after update: %s", entryID)
	}
	return MemoryEntryInfo{
		ID:            entry.ID,
		Key:           entry.Key,
		Content:       entry.Content,
		Type:          string(entry.Type),
		Tags:          entry.Tags,
		Timestamp:     entry.Timestamp.UnixMilli(),
		Project:       entry.Project,
		Reinforcement: entry.Reinforcement,
	}, nil
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
	p.mu.RLock()
	workDir := p.Directory
	p.mu.RUnlock()
	return s.openTerminalAt(projectID, workDir)
}

// OpenSessionTerminal opens a PTY in the active chat's isolated checkout.
// Legacy/non-Git sessions intentionally resolve to the project directory.
func (s *Studio) OpenSessionTerminal(projectID, sessionID string) (string, error) {
	return s.OpenSessionTerminalAt(projectID, sessionID, "")
}

func (s *Studio) openTerminalAt(projectID, workDir string) (string, error) {

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
	t, err := newTerminalWithLogger(s.ctx, workDir, projectID, termID, s.LogEvent, onExit)
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
	if !utf8.ValidString(data) {
		return fmt.Errorf("terminal input must be valid UTF-8")
	}
	if len(data) > TerminalWriteMaxBytes {
		return fmt.Errorf("terminal input exceeds the %d-byte limit", TerminalWriteMaxBytes)
	}
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
	if cols < 1 || rows < 1 || cols > TerminalDimensionMax || rows > TerminalDimensionMax {
		return fmt.Errorf("terminal dimensions must be between 1 and %d", TerminalDimensionMax)
	}
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
	p.mu.RLock()
	workDir := p.Directory
	p.mu.RUnlock()
	return listDirectoryAt(workDir, subPath)
}

// ListSessionDirectory browses the files visible to one chat session.
func (s *Studio) ListSessionDirectory(projectID, sessionID, subPath string) ([]FileEntry, error) {
	p, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	workDir, err := sessionWorkingDirectory(p, session)
	if err != nil {
		return nil, err
	}
	return listDirectoryAt(workDir, subPath)
}

func listDirectoryAt(workDir, subPath string) ([]FileEntry, error) {
	root, rel, err := openProjectPath(workDir, subPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil {
		return nil, fmt.Errorf("stat project directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("project path is not a directory")
	}
	dir, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open project directory: %w", err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(projectDirectoryMaxEntries + 1)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read project directory: %w", err)
	}
	if len(entries) > projectDirectoryMaxEntries {
		entries = entries[:projectDirectoryMaxEntries]
	}

	var result []FileEntry
	for _, e := range entries {
		// Skip hidden files and common noise directories.
		name := e.Name()
		if len(name) == 0 || name[0] == '.' || name == "node_modules" || name == "__pycache__" {
			continue
		}
		base := rel
		if base == "." {
			base = ""
		}
		entryRel := filepath.Join(base, name)
		// Resolve through os.Root rather than DirEntry.Info so safe internal
		// symlinks get their target type, while outward/absolute symlinks are
		// rejected and omitted. Special files are not actionable in the text
		// browser and may block if opened, so hide them as well.
		entryInfo, statErr := root.Stat(entryRel)
		if statErr != nil || (!entryInfo.IsDir() && !entryInfo.Mode().IsRegular()) {
			continue
		}
		result = append(result, FileEntry{
			Name:  name,
			Path:  entryRel,
			IsDir: entryInfo.IsDir(),
			Size:  entryInfo.Size(),
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
	p.mu.RLock()
	workDir := p.Directory
	p.mu.RUnlock()
	return readProjectTextFile(workDir, subPath)
}

// ReadSessionFileContent reads an @mentioned file from the same isolated
// checkout used by the session's agent tools.
func (s *Studio) ReadSessionFileContent(projectID, sessionID, subPath string) (string, error) {
	p, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return "", err
	}
	workDir, err := sessionWorkingDirectory(p, session)
	if err != nil {
		return "", err
	}
	return readProjectTextFile(workDir, subPath)
}

// RenameProject changes a project's display name.
func (s *Studio) RenameProject(id, newName string) error {
	if !utf8.ValidString(newName) {
		return fmt.Errorf("name must be valid UTF-8")
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if len(newName) > 60 {
		newName = truncateUTF8(newName, 60)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.mu.RLock()
	oldName := p.Name
	p.mu.RUnlock()
	if err := s.persistProjectMutationLocked(id, func(pc *ProjectConfig) {
		pc.Name = newName
	}, func(p *Project) {
		p.mu.Lock()
		p.Name = newName
		p.mu.Unlock()
	}); err != nil {
		return err
	}
	s.auditProjectRenamed(oldName, newName)
	return nil
}

// --- Providers ---

// ProviderInfo describes a provider and its models for the frontend dropdown.
type ProviderInfo struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Models       []string            `json:"models"`
	ModelDetails []ProviderModelInfo `json:"modelDetails"`
}

// ProviderModelInfo exposes the verified GLM/Kimi capability contract to the
// picker. Keeping it beside the allowlist prevents context/output/reasoning
// labels in the UI from drifting away from the model actually sent on wire.
type ProviderModelInfo struct {
	ID                     string   `json:"id"`
	ContextWindow          int      `json:"contextWindow"`
	DefaultMaxOutputTokens int32    `json:"defaultMaxOutputTokens"`
	MaxOutputTokens        int32    `json:"maxOutputTokens"`
	InputModalities        []string `json:"inputModalities"`
	ReasoningControl       string   `json:"reasoningControl"`
	Description            string   `json:"description"`
	Latest                 bool     `json:"latest"`
	Recommended            bool     `json:"recommended"`
}

// GetProviders returns the list of available LLM providers and models.
func (s *Studio) GetProviders() []*ProviderInfo {
	dynamic := make(map[string]map[string]bool)
	addDynamic := func(provider, model string) {
		if !isFutureStudioModelID(provider, model) {
			return
		}
		if dynamic[provider] == nil {
			dynamic[provider] = make(map[string]bool)
		}
		dynamic[provider][model] = true
	}
	s.mu.RLock()
	if s.config != nil {
		addDynamic(s.config.Settings.DefaultProvider, s.config.Settings.DefaultModel)
	}
	for provider, models := range s.discoveredModels {
		if checkedAt := s.discoveredModelsAt[provider]; checkedAt.IsZero() || time.Since(checkedAt) > studioModelDiscoveryTTL {
			continue
		}
		for model := range models {
			addDynamic(provider, model)
		}
	}
	for _, project := range s.projects {
		project.mu.RLock()
		addDynamic(project.Provider, project.Model)
		project.mu.RUnlock()
	}
	s.mu.RUnlock()

	providers := make([]*ProviderInfo, 0, len(studioProviderCatalog))
	for i := range studioProviderCatalog {
		provider := studioProviderCatalog[i]
		provider.Models = append([]string(nil), provider.Models...)
		provider.ModelDetails = append([]ProviderModelInfo(nil), provider.ModelDetails...)
		for j := range provider.ModelDetails {
			provider.ModelDetails[j].InputModalities = append(
				[]string(nil), provider.ModelDetails[j].InputModalities...,
			)
		}
		known := make(map[string]bool, len(provider.Models))
		for _, model := range provider.Models {
			known[model] = true
		}
		extra := make([]string, 0, len(dynamic[provider.ID]))
		for model := range dynamic[provider.ID] {
			if !known[model] {
				extra = append(extra, model)
			}
		}
		sort.Slice(extra, func(i, j int) bool {
			if compared := modelVersionCompare(provider.ID, extra[i], extra[j]); compared != 0 {
				return compared > 0
			}
			return extra[i] < extra[j]
		})
		if len(extra) > 0 {
			provider.Models = append(extra, provider.Models...)
			newestIsFuture := modelVersionCompare(provider.ID, extra[0], defaultModelForProvider(provider.ID)) > 0
			if newestIsFuture {
				for j := range provider.ModelDetails {
					provider.ModelDetails[j].Latest = false
					provider.ModelDetails[j].Recommended = false
				}
			}
			for _, model := range extra {
				if detail := modelDefinition(provider.ID, model); detail != nil {
					inferred := *detail
					if newestIsFuture {
						inferred.Latest = model == extra[0]
						inferred.Recommended = model == extra[0]
					}
					inferred.InputModalities = append([]string(nil), inferred.InputModalities...)
					provider.ModelDetails = append(provider.ModelDetails, inferred)
				}
			}
		}
		providers = append(providers, &provider)
	}
	return providers
}

// --- Settings ---

// GetSettings returns a copy of the current settings. Returning a copy (not
// the live pointer) avoids a data race: Wails serializes the return value
// after the function returns, by which point the lock has already been
// released, so any concurrent UpdateSettings could race on the same struct.
// settingsSnapshot copies the current global settings. Callers must NOT already
// hold s.mu — its read locks are not reentrant, so a nested acquisition parks
// behind any queued writer and wedges the studio.
func (s *Studio) settingsSnapshot() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config == nil {
		return Settings{}
	}
	return s.config.Settings
}

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
	defaults := defaultConfig().Settings
	if cfg.Settings.Theme == "" {
		cfg.Settings.Theme = defaults.Theme
	}
	if cfg.Settings.DefaultProvider == "" {
		cfg.Settings.DefaultProvider = defaults.DefaultProvider
	}
	if cfg.Settings.DefaultModel == "" {
		cfg.Settings.DefaultModel = defaults.DefaultModel
	}
	if strings.TrimSpace(cfg.Settings.QuickEntryShortcut) == "" {
		cfg.Settings.QuickEntryShortcut = defaults.QuickEntryShortcut
	}
	normalizedShortcut, err := normalizeQuickEntryShortcut(cfg.Settings.QuickEntryShortcut)
	if err != nil {
		return fmt.Errorf("invalid Quick Entry shortcut: %w", err)
	}
	cfg.Settings.QuickEntryShortcut = normalizedShortcut
	if strings.TrimSpace(cfg.Settings.VoiceShortcut) == "" {
		cfg.Settings.VoiceShortcut = defaults.VoiceShortcut
	}
	normalizedVoiceShortcut, err := normalizeVoiceShortcut(cfg.Settings.VoiceShortcut)
	if err != nil {
		return fmt.Errorf("invalid voice shortcut: %w", err)
	}
	cfg.Settings.VoiceShortcut = normalizedVoiceShortcut
	// Trim whitespace from keys/URLs so a paste with leading/trailing spaces
	// doesn't silently break authentication.
	cfg.Settings.GLMKey = strings.TrimSpace(cfg.Settings.GLMKey)
	cfg.Settings.KimiKey = strings.TrimSpace(cfg.Settings.KimiKey)
	cfg.Settings.GlobalInstructions = strings.TrimSpace(cfg.Settings.GlobalInstructions)
	if cfg.Settings.DefaultThinkingMode != "" && cfg.Settings.DefaultThinkingMode != "enabled" && cfg.Settings.DefaultThinkingMode != "disabled" {
		return fmt.Errorf("invalid default thinking mode %q", cfg.Settings.DefaultThinkingMode)
	}
	if cfg.Settings.DefaultThinkingBudget < 0 || cfg.Settings.DefaultThinkingBudget > 1_000_000 {
		cfg.Settings.DefaultThinkingBudget = 0
	}
	// Mirror the per-project budget validation: clamp negatives to 0 and
	// reject typo-sized values (10000000 instead of 100). Both are silent
	// clamps rather than errors so a malformed UI input doesn't block save.
	if cfg.Settings.DefaultBudgetUSD < 0 || math.IsNaN(cfg.Settings.DefaultBudgetUSD) || math.IsInf(cfg.Settings.DefaultBudgetUSD, 0) {
		cfg.Settings.DefaultBudgetUSD = 0
	}
	if cfg.Settings.DefaultBudgetUSD > 100000 {
		cfg.Settings.DefaultBudgetUSD = 100000
	}
	if cfg.Settings.Theme != "dark" && cfg.Settings.Theme != "light" && cfg.Settings.Theme != "system" {
		return fmt.Errorf("invalid theme %q", cfg.Settings.Theme)
	}
	stringFields := []string{
		cfg.Settings.DefaultProvider, cfg.Settings.DefaultModel, cfg.Settings.GLMKey,
		cfg.Settings.KimiKey, cfg.Settings.GlobalInstructions,
	}
	for _, value := range stringFields {
		if !utf8.ValidString(value) {
			return fmt.Errorf("settings must contain valid UTF-8")
		}
	}
	if strings.TrimSpace(cfg.Settings.DefaultProvider) == "" || strings.TrimSpace(cfg.Settings.DefaultModel) == "" {
		return fmt.Errorf("default provider and model cannot be empty")
	}
	cfg.Settings.DefaultProvider = truncateUTF8(strings.ToLower(strings.TrimSpace(cfg.Settings.DefaultProvider)), 128)
	cfg.Settings.DefaultModel = truncateUTF8(strings.TrimSpace(cfg.Settings.DefaultModel), 256)
	if err := s.validateAvailableStudioProviderModel(cfg.Settings.DefaultProvider, cfg.Settings.DefaultModel); err != nil {
		return err
	}
	cfg.Settings.GLMKey = truncateUTF8(cfg.Settings.GLMKey, 64<<10)
	cfg.Settings.KimiKey = truncateUTF8(cfg.Settings.KimiKey, 64<<10)
	cfg.Settings.GlobalInstructions = truncateUTF8(cfg.Settings.GlobalInstructions, GlobalInstructionsMaxBytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	// iter 800+: audit each changed field to the event log so users can
	// answer "wait, why is X enabled now?" after a settings change. API key
	// VALUES are never logged — see diffSettings for the secrets policy.
	oldSettings := s.config.Settings
	quickEntryAffected := oldSettings.QuickEntryEnabled != cfg.Settings.QuickEntryEnabled ||
		(oldSettings.QuickEntryEnabled && cfg.Settings.QuickEntryEnabled && oldSettings.QuickEntryShortcut != cfg.Settings.QuickEntryShortcut)
	voiceShortcutAffected := oldSettings.VoiceShortcutEnabled != cfg.Settings.VoiceShortcutEnabled ||
		(oldSettings.VoiceShortcutEnabled && cfg.Settings.VoiceShortcutEnabled && oldSettings.VoiceShortcut != cfg.Settings.VoiceShortcut)
	quickEntryOldStopped := false
	voiceShortcutOldStopped := false
	quickEntryNewStarted := false
	voiceShortcutNewStarted := false
	rollbackShortcuts := func() error {
		var failures []string
		if voiceShortcutNewStarted {
			if stopErr := s.setVoiceShortcutEnabled(false, ""); stopErr != nil {
				failures = append(failures, "stop new voice shortcut: "+stopErr.Error())
			}
		}
		if quickEntryNewStarted {
			if stopErr := s.setQuickEntryEnabled(false, ""); stopErr != nil {
				failures = append(failures, "stop new Quick Entry shortcut: "+stopErr.Error())
			}
		}
		if quickEntryOldStopped {
			if startErr := s.setQuickEntryEnabled(true, oldSettings.QuickEntryShortcut); startErr != nil {
				failures = append(failures, "restore old Quick Entry shortcut: "+startErr.Error())
			}
		}
		if voiceShortcutOldStopped {
			if startErr := s.setVoiceShortcutEnabled(true, oldSettings.VoiceShortcut); startErr != nil {
				failures = append(failures, "restore old voice shortcut: "+startErr.Error())
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("%s", strings.Join(failures, "; "))
		}
		return nil
	}
	shortcutFailure := func(primary error) error {
		if rollbackErr := rollbackShortcuts(); rollbackErr != nil {
			return fmt.Errorf("%w; shortcut rollback also failed: %v", primary, rollbackErr)
		}
		return primary
	}
	// Stop every affected old registration before starting replacements. This
	// permits disabling one shortcut in favour of the same chord, and even
	// swapping the text and voice chords, without a transient OS collision.
	if quickEntryAffected && oldSettings.QuickEntryEnabled {
		if err := s.setQuickEntryEnabled(false, ""); err != nil {
			return fmt.Errorf("stop previous Quick Entry shortcut: %w", err)
		}
		quickEntryOldStopped = true
	}
	if voiceShortcutAffected && oldSettings.VoiceShortcutEnabled {
		if err := s.setVoiceShortcutEnabled(false, ""); err != nil {
			return shortcutFailure(fmt.Errorf("stop previous voice shortcut: %w", err))
		}
		voiceShortcutOldStopped = true
	}
	if quickEntryAffected && cfg.Settings.QuickEntryEnabled {
		if err := s.setQuickEntryEnabled(true, cfg.Settings.QuickEntryShortcut); err != nil {
			return shortcutFailure(fmt.Errorf("register Quick Entry shortcut: %w", err))
		}
		quickEntryNewStarted = true
	}
	if voiceShortcutAffected && cfg.Settings.VoiceShortcutEnabled {
		if err := s.setVoiceShortcutEnabled(true, cfg.Settings.VoiceShortcut); err != nil {
			return shortcutFailure(fmt.Errorf("register voice shortcut: %w", err))
		}
		voiceShortcutNewStarted = true
	}
	projects := make([]ProjectConfig, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, p.ToConfig())
	}
	// Groups come from the authoritative in-memory config, not from cfg, for
	// the same reason Projects is rebuilt from s.projects above: the caller
	// sends a settings payload, not a whole config. SettingsPage posts
	// {projects: [], settings: …} with no groups field at all, so trusting cfg
	// here would still write an empty groups list over the stored one.
	candidate := &StudioConfig{Projects: projects, Groups: s.config.Groups, Settings: cfg.Settings}
	s.configSaveMu.Lock()
	err = candidate.Save()
	s.configSaveMu.Unlock()
	if err != nil {
		return shortcutFailure(err)
	}
	if oldSettings.GLMKey != cfg.Settings.GLMKey {
		delete(s.discoveredModels, "glm")
		delete(s.discoveredModelsAt, "glm")
	}
	if oldSettings.KimiKey != cfg.Settings.KimiKey {
		delete(s.discoveredModels, "kimi")
		delete(s.discoveredModelsAt, "kimi")
	}
	s.config.Settings = cfg.Settings
	s.config.Projects = projects
	for _, p := range s.projects {
		p.mu.Lock()
		p.resetClientLocked() // close + clear so the next send re-inits with new settings
		p.mu.Unlock()
	}
	s.setWakeEnabled(cfg.Settings.KeepAwakeEnabled)
	if cfg.Settings.AutoArchivePRAfterClose {
		s.ensurePullRequestArchiveMonitor()
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
// Dispatch is the user-facing "send this task to another project" action. It
// is now a thin wrapper over StartDelegation.
//
// The previous implementation called client.SendMessage directly against the
// target's shared client: no child session, no tool loop, no budget preflight,
// no recordResponse, and a raw EventsEmit that bypassed the event-log tee. A
// delegation is a real turn in the target project, so all of that works.
func (s *Studio) Dispatch(fromID, toID, fromSessionID, task string) error {
	if err := validateRPCText("task description", task, DispatchTaskMaxBytes, true); err != nil {
		return err
	}
	if fromID == toID {
		return fmt.Errorf("cannot dispatch to self; pick a different target project")
	}
	fromSid := fromSessionID
	if fromSid == "" {
		fromSid = "default"
	}
	// Checked first so existing tests can substitute the whole path.
	if s.testDispatchFn != nil {
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
		if !s.startBackground("dispatch", func() {
			s.testDispatchFn(from, to, fromSid, task, settings)
		}) {
			return fmt.Errorf("studio is shutting down")
		}
		return nil
	}
	_, err := s.startDelegation(delegationRequest{
		FromProjectID:  fromID,
		FromSessionID:  fromSid,
		ToProjectID:    toID,
		Kind:           "run",
		Task:           task,
		LegacyDispatch: true,
	})
	return err
}

// --- Internal ---

// persistProjectMutationLocked commits one project's future config before
// publishing the corresponding in-memory state. Caller must hold s.mu.Lock.
// Keeping all project-setting APIs on this path gives them the same contract:
// success survives restart; failure changes neither the project nor its cached
// provider/MCP transports.
func (s *Studio) persistProjectMutationLocked(id string, mutate func(*ProjectConfig), publish func(*Project)) error {
	p, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	projects := make([]ProjectConfig, 0, len(s.projects))
	found := false
	for projectID, existing := range s.projects {
		pc := existing.ToConfig()
		if projectID == id {
			mutate(&pc)
			found = true
		}
		projects = append(projects, pc)
	}
	if !found {
		return fmt.Errorf("project not found: %s", id)
	}
	candidate := &StudioConfig{Projects: projects, Groups: s.config.Groups, Settings: s.config.Settings}
	s.configSaveMu.Lock()
	err := candidate.Save()
	s.configSaveMu.Unlock()
	if err != nil {
		return fmt.Errorf("persist project settings: %w", err)
	}
	publish(p)
	s.config.Projects = projects
	return nil
}

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
	s.configSaveMu.Lock()
	defer s.configSaveMu.Unlock()
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
	// Acquire in s.mu -> configSaveMu order, matching saveConfig callers that
	// already hold s.mu. Holding the commit lock through Save prevents an older
	// async snapshot from finishing after and overwriting a newer one.
	s.configSaveMu.Lock()
	defer s.configSaveMu.Unlock()
	projects := make([]ProjectConfig, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, p.ToConfig())
	}
	// Read Settings under the lock — UpdateSettings can race on s.config.Settings
	// concurrently (struct assignment is not atomic for multi-field structs).
	settings := s.config.Settings
	// Groups are read here for the same reason as Settings, and carried below
	// for a sharper one: Save() marshals exactly the struct it is handed and
	// `groups` is omitempty, so omitting the field does not leave the on-disk
	// groups alone — it deletes them. This is the hot path (every turn bumps
	// lastUsedAt), so the omission erased them on the first message in any chat.
	groups := s.config.Groups
	s.mu.RUnlock()
	// Clone the entire StudioConfig so we don't race with readers that read
	// s.config (e.g. GetSettings) while the yaml.Marshal goroutine touches
	// the Projects slice. Also avoids a race where another saveConfigAsync
	// run updates s.config.Projects concurrently.
	cfg := &StudioConfig{
		Settings: settings,
		Projects: projects,
		Groups:   groups,
	}
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "gokin-studio: failed to save config: %v\n", err)
		s.logf("error", "config", "failed to save config (async): %v", err)
	}
}
