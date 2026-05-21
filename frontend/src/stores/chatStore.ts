import { create } from 'zustand'

export interface ChatMessage {
  id: string
  role: 'user' | 'assistant' | 'tool' | 'thinking' | 'dispatch'
  content: string
  toolName?: string
  toolArgs?: Record<string, unknown>
  toolSuccess?: boolean
  // iter 1030+: streamingOutput is the accumulated live stdout for a still-
  // running tool (currently only bash). Appended to on every chat:tool_progress
  // event. Rendered inline during isPending so users see a live preview of
  // a long build instead of just a spinner. Untouched once the tool resolves —
  // the authoritative content field replaces this view in the rendered card.
  streamingOutput?: string
  dispatchTarget?: string
  dispatchSuccess?: boolean
  durationMs?: number
  usage?: TokenUsage
  // Model + provider that produced this message (assistant only). Shown in
  // the footer so the user can tell which backend answered when they've
  // been switching providers mid-conversation.
  model?: string
  provider?: string
  timestamp: number
}

export interface RetryStatus {
  attempt: number
  max: number
  delayMs: number
  reason: string
  startedAt: number
}

export interface AskUserQuestion {
  questionID: string
  question: string
  options: string[]
  default: string
  askedAt: number
}

export interface TokenUsage {
  // Per-turn totals (summed across every LLM round). Use for cost/billing display.
  totalInputTokens: number
  totalOutputTokens: number
  totalCacheReadTokens: number
  totalCacheWriteTokens: number
  // Last round only. Use for the context gauge ("how full is my context window").
  lastInputTokens: number
  lastOutputTokens: number
  lastCacheReadTokens: number
  lastCacheWriteTokens: number
  // Approximate USD cost for the entire turn, computed server-side from the
  // pricing table in `pricing.go`. 0 means the model is unknown to the table
  // or the provider is local (Ollama). Frontend renders with a "≈" prefix
  // since these are approximations, not authoritative billing.
  estimatedCostUSD?: number
}

interface ChatState {
  messages: Record<string, ChatMessage[]>  // chatKey → messages (chatKey = projectID_sessionID)
  streaming: Record<string, string>        // chatKey → partial text
  thinkingStream: Record<string, string>   // chatKey → live reasoning text (for thinking-capable models)
  sessionActive: Record<string, boolean>   // chatKey → is agent running in this session
  retrying: Record<string, RetryStatus | null>  // chatKey → current retry state, null if none
  drafts: Record<string, string>           // chatKey → unsent input draft
  currentUsage: Record<string, TokenUsage | null>  // chatKey → live usage for the in-progress turn
  lastTurnUsage: Record<string, TokenUsage | null>  // chatKey → usage from the most recently completed turn
  askUser: Record<string, AskUserQuestion | null>   // chatKey → pending ask_user question (one at a time)
  activeSession: Record<string, string>    // projectID → current active sessionID
  // Per-tab unread counter. Bumped when a session finishes a turn or errors
  // while the user isn't currently viewing it. Cleared when the user switches
  // to that tab. Drives the amber dot/count badge on inactive session tabs.
  unread: Record<string, number>           // chatKey → unread turn count
  // iter 1010+: scroll position per session so flipping tabs doesn't lose
  // the user's reading position in a long chat. Saved on session switch,
  // restored when returning. -1 sentinel means "user was at bottom, scroll
  // to bottom on return so new content shows up".
  scrollPositions: Record<string, number>  // chatKey → scrollTop, or -1 = at bottom
}

interface ChatStore extends ChatState {
  setActiveSession: (projectId: string, sessionId: string) => void
  setMessages: (chatKey: string, messages: ChatMessage[]) => void
  addUserMessage: (chatKey: string, content: string) => void
  appendStreamText: (chatKey: string, text: string) => void
  appendThinkingStream: (chatKey: string, text: string) => void
  addThinking: (chatKey: string, text: string) => void
  finalizeAssistant: (chatKey: string, text: string) => void
  addToolCall: (chatKey: string, tool: string, args: Record<string, unknown>) => void
  addToolProgress: (chatKey: string, tool: string, text: string) => void
  addToolResult: (chatKey: string, tool: string, success: boolean, content: string) => void
  addDispatchResult: (chatKey: string, target: string, success: boolean, content: string) => void
  setSessionActive: (chatKey: string, active: boolean) => void
  setRetrying: (chatKey: string, status: RetryStatus | null) => void
  setDraft: (chatKey: string, draft: string) => void
  setCurrentUsage: (chatKey: string, usage: TokenUsage | null) => void
  finalizeUsage: (chatKey: string, usage: TokenUsage | null) => void
  setAskUser: (chatKey: string, q: AskUserQuestion | null) => void
  bumpUnread: (chatKey: string) => void
  clearUnread: (chatKey: string) => void
  // Drop every unread entry whose key starts with `${projectID}_`. Used by
  // "Mark all sessions as read" — wipes badges across the active project's
  // tabs in one click without touching session content.
  clearAllUnreadForProject: (projectID: string) => void
  clearChat: (chatKey: string) => void
  // Remove ALL store entries for a specific session so they don't accumulate.
  // Use this when a session is deleted (vs clearChat which just empties them).
  dropSession: (chatKey: string) => void
  // Drop every chatStore entry (messages, streaming, drafts, usage, etc.)
  // whose key starts with the given projectID prefix. Called when a project
  // is removed so its per-session state doesn't leak indefinitely.
  dropProject: (projectID: string) => void
  // iter 1010+: persist the per-session scroll position so switching tabs
  // and back doesn't reset reading position. Pass -1 to record "at bottom".
  setScrollPosition: (chatKey: string, pos: number) => void
}

let counter = 0
const genId = () => `msg-${Date.now()}-${++counter}`

export const useChatStore = create<ChatStore>((set) => ({
  messages: {},
  streaming: {},
  thinkingStream: {},
  sessionActive: {},
  retrying: {},
  drafts: {},
  currentUsage: {},
  lastTurnUsage: {},
  askUser: {},
  activeSession: {},
  unread: {},
  scrollPositions: {},

  bumpUnread: (chatKey) =>
    set((s) => ({
      unread: { ...s.unread, [chatKey]: (s.unread[chatKey] || 0) + 1 },
    })),

  clearUnread: (chatKey) =>
    set((s) => {
      if (!s.unread[chatKey]) return s
      const next = { ...s.unread }
      delete next[chatKey]
      return { unread: next }
    }),

  clearAllUnreadForProject: (projectID) =>
    set((s) => {
      const prefix = projectID + '_'
      let touched = false
      const next: Record<string, number> = {}
      for (const k of Object.keys(s.unread)) {
        if (k === projectID || k.startsWith(prefix)) {
          touched = true
          continue
        }
        next[k] = s.unread[k]
      }
      return touched ? { unread: next } : s
    }),

  setActiveSession: (projectId, sessionId) =>
    set((s) => ({
      activeSession: { ...s.activeSession, [projectId]: sessionId },
    })),

  setSessionActive: (chatKey, active) =>
    set((s) => ({
      sessionActive: { ...s.sessionActive, [chatKey]: active },
    })),

  setRetrying: (chatKey, status) =>
    set((s) => ({
      retrying: { ...s.retrying, [chatKey]: status },
    })),

  setDraft: (chatKey, draft) =>
    set((s) => ({
      drafts: { ...s.drafts, [chatKey]: draft },
    })),

  setCurrentUsage: (chatKey, usage) =>
    set((s) => ({
      currentUsage: { ...s.currentUsage, [chatKey]: usage },
    })),

  finalizeUsage: (chatKey, usage) =>
    set((s) => ({
      currentUsage: { ...s.currentUsage, [chatKey]: null },
      lastTurnUsage: { ...s.lastTurnUsage, [chatKey]: usage },
    })),

  setAskUser: (chatKey, q) =>
    set((s) => ({
      askUser: { ...s.askUser, [chatKey]: q },
    })),

  setMessages: (projectId, messages) =>
    set((s) => ({
      messages: { ...s.messages, [projectId]: messages },
    })),

  addUserMessage: (projectId, content) =>
    set((s) => ({
      messages: {
        ...s.messages,
        [projectId]: [
          ...(s.messages[projectId] || []),
          { id: genId(), role: 'user', content, timestamp: Date.now() },
        ],
      },
      streaming: { ...s.streaming, [projectId]: '' },
      thinkingStream: { ...s.thinkingStream, [projectId]: '' },
      currentUsage: { ...s.currentUsage, [projectId]: null },
    })),

  appendStreamText: (projectId, text) =>
    set((s) => ({
      streaming: {
        ...s.streaming,
        [projectId]: (s.streaming[projectId] || '') + text,
      },
      retrying: { ...s.retrying, [projectId]: null },
    })),

  appendThinkingStream: (projectId, text) =>
    set((s) => ({
      thinkingStream: {
        ...s.thinkingStream,
        [projectId]: (s.thinkingStream[projectId] || '') + text,
      },
    })),

  // Called at end-of-turn with the fully accumulated thinking text. If we were
  // streaming, the live buffer already has it — just finalize as a message and
  // clear the buffer. If no streaming happened (provider without OnThinking
  // deltas), fall back to the text from the event.
  addThinking: (projectId, text) =>
    set((s) => {
      const streamed = s.thinkingStream[projectId] || ''
      const content = streamed || text
      if (!content) return s
      return {
        messages: {
          ...s.messages,
          [projectId]: [
            ...(s.messages[projectId] || []),
            { id: genId(), role: 'thinking', content, timestamp: Date.now() },
          ],
        },
        thinkingStream: { ...s.thinkingStream, [projectId]: '' },
      }
    }),

  finalizeAssistant: (projectId, text) =>
    set((s) => {
      const content = text || s.streaming[projectId] || ''
      if (!content) return s
      return {
        messages: {
          ...s.messages,
          [projectId]: [
            ...(s.messages[projectId] || []),
            { id: genId(), role: 'assistant', content, timestamp: Date.now() },
          ],
        },
        streaming: { ...s.streaming, [projectId]: '' },
      }
    }),

  addToolCall: (projectId, tool, args) =>
    set((s) => ({
      messages: {
        ...s.messages,
        [projectId]: [
          ...(s.messages[projectId] || []),
          {
            id: genId(),
            role: 'tool',
            content: `Calling ${tool}...`,
            toolName: tool,
            toolArgs: args,
            timestamp: Date.now(),
          },
        ],
      },
    })),

  addToolProgress: (projectId, tool, text) =>
    set((s) => {
      if (!text) return s
      const msgs = [...(s.messages[projectId] || [])]
      // Append partial output to the latest PENDING tool call of this name.
      // Capped at 100 KB so a runaway log doesn't bloat memory; the
      // authoritative full content arrives via addToolResult anyway.
      const MAX_STREAM_BYTES = 100_000
      for (let i = msgs.length - 1; i >= 0; i--) {
        const m = msgs[i]
        if (m.toolName === tool && m.role === 'tool' && m.toolSuccess === undefined) {
          const next = (m.streamingOutput || '') + text
          const trimmed = next.length > MAX_STREAM_BYTES
            ? '…' + next.slice(next.length - MAX_STREAM_BYTES)
            : next
          msgs[i] = { ...m, streamingOutput: trimmed }
          break
        }
      }
      return { messages: { ...s.messages, [projectId]: msgs } }
    }),

  addToolResult: (projectId, tool, success, content) =>
    set((s) => {
      const msgs = [...(s.messages[projectId] || [])]
      // Update last PENDING tool call for this tool (toolSuccess is undefined until resolved).
      for (let i = msgs.length - 1; i >= 0; i--) {
        if (msgs[i].toolName === tool && msgs[i].role === 'tool' && msgs[i].toolSuccess === undefined) {
          // iter 1030+: drop streamingOutput once authoritative content lands.
          // The rendered tool card switches to the regular bash-output view.
          msgs[i] = { ...msgs[i], content, toolSuccess: success, streamingOutput: undefined }
          break
        }
      }
      return { messages: { ...s.messages, [projectId]: msgs } }
    }),

  addDispatchResult: (projectId, target, success, content) =>
    set((s) => ({
      messages: {
        ...s.messages,
        [projectId]: [
          ...(s.messages[projectId] || []),
          {
            id: genId(),
            role: 'dispatch',
            content,
            dispatchTarget: target,
            dispatchSuccess: success,
            timestamp: Date.now(),
          },
        ],
      },
    })),

  clearChat: (projectId) =>
    set((s) => {
      // Drop unread count too — clearing the chat means the user has dealt
      // with whatever was unread, no point keeping a stale count around.
      const nextUnread = { ...s.unread }
      delete nextUnread[projectId]
      // iter 1010+: same for scroll position — after /clear the content is
      // empty so any saved position is meaningless.
      const nextScroll = { ...s.scrollPositions }
      delete nextScroll[projectId]
      return {
        messages: { ...s.messages, [projectId]: [] },
        streaming: { ...s.streaming, [projectId]: '' },
        thinkingStream: { ...s.thinkingStream, [projectId]: '' },
        askUser: { ...s.askUser, [projectId]: null },
        currentUsage: { ...s.currentUsage, [projectId]: null },
        lastTurnUsage: { ...s.lastTurnUsage, [projectId]: null },
        retrying: { ...s.retrying, [projectId]: null },
        drafts: { ...s.drafts, [projectId]: '' },
        sessionActive: { ...s.sessionActive, [projectId]: false },
        unread: nextUnread,
        scrollPositions: nextScroll,
      }
    }),

  dropSession: (chatKey) =>
    set((s) => {
      const del = (rec: Record<string, any>) => { const n = { ...rec }; delete n[chatKey]; return n }
      return {
        messages: del(s.messages),
        streaming: del(s.streaming),
        thinkingStream: del(s.thinkingStream),
        sessionActive: del(s.sessionActive),
        retrying: del(s.retrying),
        drafts: del(s.drafts),
        currentUsage: del(s.currentUsage),
        lastTurnUsage: del(s.lastTurnUsage),
        askUser: del(s.askUser),
        unread: del(s.unread),
        scrollPositions: del(s.scrollPositions),
      }
    }),

  dropProject: (projectID) =>
    set((s) => {
      const prefix = projectID + '_'
      // Also match a bare projectID key (legacy, pre-composite-key paths).
      const drop = (rec: Record<string, any>): Record<string, any> => {
        const next: Record<string, any> = {}
        for (const k of Object.keys(rec)) {
          if (k === projectID || k.startsWith(prefix)) continue
          next[k] = rec[k]
        }
        return next
      }
      const dropExact = (rec: Record<string, any>): Record<string, any> => {
        const next = { ...rec }
        delete next[projectID]
        return next
      }
      return {
        messages: drop(s.messages),
        streaming: drop(s.streaming),
        thinkingStream: drop(s.thinkingStream),
        sessionActive: drop(s.sessionActive),
        retrying: drop(s.retrying),
        drafts: drop(s.drafts),
        currentUsage: drop(s.currentUsage),
        lastTurnUsage: drop(s.lastTurnUsage),
        askUser: drop(s.askUser),
        unread: drop(s.unread),
        scrollPositions: drop(s.scrollPositions),
        activeSession: dropExact(s.activeSession),
      }
    }),

  setScrollPosition: (chatKey, pos) =>
    set((s) => ({
      scrollPositions: { ...s.scrollPositions, [chatKey]: pos },
    })),
}))
