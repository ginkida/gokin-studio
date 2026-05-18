package studio

// Wails event names (Go -> Frontend).
const (
	EventChatDelta         = "chat:delta"
	EventChatText          = "chat:text"
	EventChatThinking      = "chat:thinking"
	EventChatThinkingDelta = "chat:thinking_delta"
	EventChatToolCall      = "chat:tool_call"
	EventChatToolResult = "chat:tool_result"
	EventChatComplete   = "chat:complete"
	EventChatError      = "chat:error"
	EventChatRetry      = "chat:retry"
	EventChatUsage      = "chat:usage"

	EventTerminalOutput = "terminal:output"
	EventTerminalExit   = "terminal:exit"

	EventProjectStatus = "project:status"

	EventSessionRenamed = "session:renamed"

	EventDispatchComplete = "dispatch:complete"

	EventAskUser = "chat:ask_user"
)

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
	ProjectID string `json:"projectID"`
	SessionID string `json:"sessionID"`
	Tool      string `json:"tool"`
	Success   bool   `json:"success"`
	Content   string `json:"content"`
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

// AskUserEvent is emitted when the agent calls the ask_user tool. The frontend
// must render a question card and eventually call Studio.AnswerQuestion with
// the matching QuestionID so the tool can return to the agent.
type AskUserEvent struct {
	ProjectID  string   `json:"projectID"`
	SessionID  string   `json:"sessionID"`
	QuestionID string   `json:"questionID"`
	Question   string   `json:"question"`
	Options    []string `json:"options,omitempty"`
	Default    string   `json:"default,omitempty"`
}

// TerminalOutputEvent is emitted when a terminal produces output.
type TerminalOutputEvent struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}
