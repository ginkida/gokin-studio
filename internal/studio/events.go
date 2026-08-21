package studio

// Wails event names (Go -> Frontend).
const (
	EventChatDelta            = "chat:delta"
	EventChatText             = "chat:text"
	EventChatThinking         = "chat:thinking"
	EventChatThinkingDelta    = "chat:thinking_delta"
	EventChatToolCall         = "chat:tool_call"
	EventChatToolProgress     = "chat:tool_progress"
	EventChatToolResult       = "chat:tool_result"
	EventCodeReviewReady      = "code-review:ready"
	EventChatComplete         = "chat:complete"
	EventPreviewBrowser       = "preview:browser_request"
	EventExternalBrowserAgent = "external-browser:agent_request"
	EventChatError            = "chat:error"
	EventChatRetry            = "chat:retry"
	EventChatUsage            = "chat:usage"
	EventChatQueueStarted     = "chat:queue_started"
	EventChatQueueAdded       = "chat:queue_added"
	EventChatQueueCleared     = "chat:queue_cleared"
	// EventChatStreamStatus surfaces stream-liveness hints (the model is thinking,
	// or a slow/stall-prone provider's stream has gone quiet, or it resumed) so the
	// UI can show "still working…" instead of looking frozen during a long pause.
	EventChatStreamStatus = "chat:stream_status"

	EventSideChatDelta    = "sidechat:delta"
	EventSideChatComplete = "sidechat:complete"
	EventSideChatError    = "sidechat:error"

	EventTerminalOutput = "terminal:output"
	EventTerminalExit   = "terminal:exit"

	EventProjectStatus = "project:status"

	EventSessionRenamed  = "session:renamed"
	EventSessionsChanged = "sessions:changed"

	EventDispatchComplete = "dispatch:complete"

	// Cross-project delegation. These carry status and bounded progress, never
	// the task text or the full answer — the record is durable and backups
	// bundle the config directory.
	EventDelegationStarted  = "delegation:started"
	EventDelegationProgress = "delegation:progress"
	EventDelegationComplete = "delegation:complete"

	EventAskUser       = "chat:ask_user"
	EventAskUserClosed = "chat:ask_user_closed"

	EventQuickEntry       = "quick-entry:show"
	EventDeepLink         = "deep-link:open"
	EventNativeCommand    = "app:native_command"
	EventUpdateAvailable  = "app:update_available"
	EventOpenUpdateCenter = "app:open_update_center"

	EventSpeechDictation = "speech-dictation:event"
)

// DelegationEvent is the live view of a cross-project delegation. Task text and
// the full answer are deliberately absent: this payload reaches the frontend
// and the event log, while the body stays in the durable run record behind
// FetchDelegationAnswer.
type DelegationEvent struct {
	RunID             string   `json:"runID"`
	BatchID           string   `json:"batchID,omitempty"`
	FromProjectID     string   `json:"fromProjectID"`
	FromSessionID     string   `json:"fromSessionID"`
	ToProjectID       string   `json:"toProjectID"`
	ToProjectName     string   `json:"toProjectName,omitempty"`
	ToSessionID       string   `json:"toSessionID,omitempty"`
	Kind              string   `json:"kind"`
	Goal              string   `json:"goal,omitempty"`
	Status            string   `json:"status"`
	ErrorType         string   `json:"errorType,omitempty"`
	Error             string   `json:"error,omitempty"`
	Summary           string   `json:"summary,omitempty"`
	Tail              []string `json:"tail,omitempty"`
	LastTool          string   `json:"lastTool,omitempty"`
	DeniedTools       []string `json:"deniedTools,omitempty"`
	MutatedBeforeStop bool     `json:"mutatedBeforeStop,omitempty"`
	ElapsedMs         int64    `json:"elapsedMs,omitempty"`
	EstimatedCostUSD  float64  `json:"estimatedCostUSD,omitempty"`
	// LegacyDispatch marks a run that ALSO emits the older dispatch:complete
	// event. Both carry the same completion, and both frontend handlers raise a
	// desktop notification, so one of them has to stand down.
	LegacyDispatch bool `json:"legacyDispatch,omitempty"`
}

// SideChatEvent belongs to an ephemeral, read-only side question. Side-chat
// messages are intentionally never folded into ChatSession.history; the
// request ID lets the frontend discard a late event after the drawer closes.
type SideChatEvent struct {
	ProjectID        string  `json:"projectID"`
	SessionID        string  `json:"sessionID"`
	RequestID        string  `json:"requestID"`
	Text             string  `json:"text,omitempty"`
	Error            string  `json:"error,omitempty"`
	Provider         string  `json:"provider,omitempty"`
	Model            string  `json:"model,omitempty"`
	InputTokens      int     `json:"inputTokens,omitempty"`
	OutputTokens     int     `json:"outputTokens,omitempty"`
	CacheReadTokens  int     `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int     `json:"cacheWriteTokens,omitempty"`
	EstimatedCostUSD float64 `json:"estimatedCostUSD,omitempty"`
}

// UpdateStatus is a notify-only desktop release check result. ReleaseURL is
// constructed locally for the fixed Gokin Studio GitHub repository; arbitrary
// URLs from a remote response never cross into the frontend.
type UpdateStatus struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	Available      bool   `json:"available"`
	CheckedAt      int64  `json:"checkedAt,omitempty"`
	PublishedAt    int64  `json:"publishedAt,omitempty"`
	ReleaseURL     string `json:"releaseURL,omitempty"`
}

// DeepLinkEvent is a validated, local navigation request. Prompt is always
// delivered as an editable composer draft; no deep-link route can send it.
type DeepLinkEvent struct {
	Sequence  uint64 `json:"sequence"`
	Action    string `json:"action"`
	ProjectID string `json:"projectID,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	View      string `json:"view,omitempty"`
}

// SpeechDictationEvent is process-local and contains transcript text only.
// Raw microphone audio never crosses the native Speech.framework boundary or
// enters the GLM/Kimi request path.
type SpeechDictationEvent struct {
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Final     bool   `json:"final,omitempty"`
	Error     string `json:"error,omitempty"`
	Sequence  uint64 `json:"sequence"`
}

// ChatQueueEvent keeps the frontend's ephemeral queue in sync with the
// backend-owned per-session worker. IDs are generated by the frontend before
// enqueueing, which makes a queue-start event safe even if it races the RPC
// response.
type ChatQueueEvent struct {
	ProjectID string   `json:"projectID"`
	SessionID string   `json:"sessionID"`
	ID        string   `json:"id,omitempty"`
	IDs       []string `json:"ids,omitempty"`
	Text      string   `json:"text,omitempty"`
	Scheduled bool     `json:"scheduled,omitempty"`
}

// ChatTextEvent is emitted for text deltas and full text.
type ChatTextEvent struct {
	ProjectID string `json:"projectID"`
	SessionID string `json:"sessionID"`
	Text      string `json:"text"`
}

// ChatThinkingEvent is emitted for extended thinking.
type ChatThinkingEvent struct {
	ProjectID string `json:"projectID"`
	SessionID string `json:"sessionID"`
	Text      string `json:"text"`
}

// ChatToolCallEvent is emitted when the agent calls a tool.
type ChatToolCallEvent struct {
	ProjectID string         `json:"projectID"`
	SessionID string         `json:"sessionID"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
}

// ChatToolResultEvent is emitted when a tool finishes.
type ChatToolResultEvent struct {
	ProjectID string         `json:"projectID"`
	SessionID string         `json:"sessionID"`
	Tool      string         `json:"tool"`
	Success   bool           `json:"success"`
	Content   string         `json:"content"`
	MCPApp    *MCPAppPayload `json:"mcpApp,omitempty"`
}

// ChatToolProgressEvent carries an incremental chunk of output from a
// long-running tool (currently only bash via the engine's ProgressCallback).
// Text is the NEW bytes since the previous event — the frontend
// concatenates into a streamingOutput buffer on the matching pending tool
// message. Emitted at ~100ms cadence (engine's StreamingFlushInterval) so
// users see live progress on long builds/tests instead of an indefinite
// spinner. Cleared when the corresponding chat:tool_result lands.
type ChatToolProgressEvent struct {
	ProjectID string `json:"projectID"`
	SessionID string `json:"sessionID"`
	Tool      string `json:"tool"`
	Text      string `json:"text"`
}

// ChatRetryEvent is emitted when an LLM call is being retried after a transient error.
type ChatRetryEvent struct {
	ProjectID string `json:"projectID"`
	SessionID string `json:"sessionID"`
	Attempt   int    `json:"attempt"`
	Max       int    `json:"max"`
	DelayMs   int    `json:"delayMs"`
	Reason    string `json:"reason"`
}

// ChatStreamStatusEvent surfaces a stream-liveness hint to the UI during a long
// pause. Status is one of: "thinking" (model in its silent reasoning phase),
// "stalled" (a slow/stall-prone provider's stream has gone quiet mid-response),
// or "resumed" (data started flowing again — clear the hint).
type ChatStreamStatusEvent struct {
	ProjectID string `json:"projectID"`
	SessionID string `json:"sessionID"`
	Status    string `json:"status"`
	Provider  string `json:"provider,omitempty"`
	ElapsedMs int    `json:"elapsedMs,omitempty"`
}

// ChatCompleteEvent is emitted when the agent finishes. InputTokens/OutputTokens
// are summed across every LLM round-trip in this turn (i.e. what the user was
// billed). "Last*" fields reflect just the final round — useful for context
// occupancy. Zero for providers that don't report usage (e.g. some Ollama models).
type ChatCompleteEvent struct {
	ProjectID  string `json:"projectID"`
	SessionID  string `json:"sessionID"`
	Text       string `json:"text"`
	Turns      int    `json:"turns"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`

	// Model + provider that generated the final response. Surfaced in the
	// assistant message footer so the user can tell which backend answered
	// when they've been switching providers mid-conversation.
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`

	InputTokens      int `json:"inputTokens,omitempty"`
	OutputTokens     int `json:"outputTokens,omitempty"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`

	LastInputTokens      int `json:"lastInputTokens,omitempty"`
	LastOutputTokens     int `json:"lastOutputTokens,omitempty"`
	LastCacheReadTokens  int `json:"lastCacheReadTokens,omitempty"`
	LastCacheWriteTokens int `json:"lastCacheWriteTokens,omitempty"`

	// EstimatedCostUSD is the computed dollar cost for the entire turn (sum of
	// every LLM round) using the pricing table in pricing.go. 0 means the
	// model is unknown to the table or the provider is local (Ollama). The
	// frontend treats 0 as "don't display a chip"; non-zero values are
	// rendered with a "≈" prefix to signal these are approximations, not
	// authoritative billing.
	EstimatedCostUSD float64 `json:"estimatedCostUSD,omitempty"`

	// PinnedContext carries the current pinned context value after the turn so
	// the frontend can update its display without a separate GetProject call.
	PinnedContext string `json:"pinnedContext,omitempty"`
}

// ChatUsageEvent is emitted incrementally as usage arrives from each LLM round
// within a turn. Carries two views:
//
//   - "last*" — the most recent round's numbers. Represents what the model
//     sees in its context window RIGHT NOW — use for the context-usage gauge.
//   - "total*" — summed across every round in this turn. What the user is
//     billed — use for per-turn cost display.
type ChatUsageEvent struct {
	ProjectID string `json:"projectID"`
	SessionID string `json:"sessionID"`

	LastInputTokens      int `json:"lastInputTokens"`
	LastOutputTokens     int `json:"lastOutputTokens"`
	LastCacheReadTokens  int `json:"lastCacheReadTokens"`
	LastCacheWriteTokens int `json:"lastCacheWriteTokens"`

	TotalInputTokens      int `json:"totalInputTokens"`
	TotalOutputTokens     int `json:"totalOutputTokens"`
	TotalCacheReadTokens  int `json:"totalCacheReadTokens"`
	TotalCacheWriteTokens int `json:"totalCacheWriteTokens"`
}

// AskUserEvent is emitted for an agent clarification or a runtime approval
// gate. The frontend must render the matching card and eventually call
// Studio.AnswerQuestion so the blocked turn can continue.
type AskUserEvent struct {
	ProjectID  string               `json:"projectID"`
	SessionID  string               `json:"sessionID"`
	QuestionID string               `json:"questionID"`
	Question   string               `json:"question"`
	Options    []string             `json:"options,omitempty"`
	Default    string               `json:"default,omitempty"`
	Kind       string               `json:"kind,omitempty"`
	Tool       string               `json:"tool,omitempty"`
	Scope      string               `json:"scope,omitempty"` // current_turn, current_turn_or_project_tool, or single_action
	Details    []ToolApprovalDetail `json:"details,omitempty"`
}

// AskUserClosedEvent retires one exact question card. It is intentionally
// question-owned: a completion/error from one concurrent operation must not
// clear a newer approval belonging to the same chat.
type AskUserClosedEvent struct {
	ProjectID  string `json:"projectID"`
	SessionID  string `json:"sessionID"`
	QuestionID string `json:"questionID"`
}

// ToolApprovalDetail is an allowlisted, display-only summary of a mutating
// tool call. It deliberately excludes file content, environment variables,
// headers, and arbitrary MCP argument values.
type ToolApprovalDetail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// TerminalOutputEvent is emitted when a terminal produces output.
type TerminalOutputEvent struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}
