import React, { useState, useRef, useEffect, useLayoutEffect, useMemo } from 'react'
import ReactMarkdown from 'react-markdown'
import rehypeHighlight from 'rehype-highlight'
import 'highlight.js/styles/github-dark-dimmed.min.css'
import { useChatStore, ChatMessage, AskUserQuestion } from '../../stores/chatStore'
import { TerminalPanel } from '../terminal/Terminal'
import { useProjectStore, type ProjectInfo } from '../../stores/projectStore'
import { useSettingsStore } from '../../stores/settingsStore'
import { DispatchModal } from '../dispatch/DispatchModal'
import { FilePicker } from '../files/FilePicker'
import { Send, Square, ChevronRight, CheckCircle, XCircle, Trash2, ArrowRightLeft, AlertTriangle, Brain, ExternalLink, ListChecks, Circle, Database, FileText, Download, Search, X, MessageSquare, Zap, Bot, User, Terminal, TerminalSquare, Pencil, Eye, EyeOff, GitBranch, FolderSearch, Loader2, Copy, Check, MoreHorizontal, RotateCcw, FolderTree, Pin, GitFork, Bookmark, BookmarkPlus, BookmarkMinus, Activity, DollarSign } from 'lucide-react'
import { SendMessage, StopGeneration, ClearHistory, GetHistory, SetProjectSystemPrompt, ExportChat, ListDirectory, EditUserMessage, ReadFileContent, GetRecoveryEvents, DiscardRecoveryEvents, AnswerQuestion, CancelQuestion, ListProjectMemory, DeleteMemoryEntry, ClearPinnedContext, SearchProjectHistory, SaveDraft, GetDraft, ForkChatSession, PinMessage, UnpinMessage, ListPinnedMessages, ListPromptTemplates, SaveUserPromptTemplate, DeleteUserPromptTemplate, ListUserPromptTemplates, GetProjectGitContext, ProjectUsageStats, ExportProjectAllSessions, SummarizeSession, SetProjectBudget, SetProjectEnforceBudget, SetProjectProvider, GetProject, ListUserSnippets, ListChatSessions, DeleteChatSession, ListProjectFiles, ExportSessionJSON, ImportSessionJSON, ExportProjectUsageCSV, GetModelPricing } from '../../../wailsjs/go/studio/Studio'
import { ClipboardSetText, EventsOn } from '../../../wailsjs/runtime/runtime'
import { isProjectMuted } from '../../lib/mutedProjects'

// ChatPanel is a thin wrapper: it renders the empty state when no project is
// selected, and otherwise mounts ChatPanelBody. This split is REQUIRED for
// Rules-of-Hooks correctness. ChatPanelBody declares ~14 hooks; a single
// component must run the SAME number of hooks on every render of an instance.
// Previously the no-project early return sat ABOVE those trailing hooks, so
// deleting the last project (activeProjectId→null) or adding the first one
// flipped the hook count on the same mounted instance → React error #310
// ("rendered fewer/more hooks than during the previous render"), caught by the
// ErrorBoundary as a full-app white screen. Keeping the project check in this
// parent means ChatPanelBody only ever renders WITH a project, so its hook
// list is constant. activeProject is passed as a guaranteed-defined prop so the
// body never dereferences an undefined project.
export function ChatPanel({ sessionId, sessionName }: { sessionId?: string; sessionName?: string }) {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const activeProject = useProjectStore((s) => s.projects.find((p) => p.id === s.activeProjectId))
  if (!activeProjectId || !activeProject) {
    return (
      <div className="chat-panel">
        <div className="chat-empty">
          <MessageSquare size={32} style={{ opacity: 0.3, marginBottom: 8 }} />
          <p>Select a project from the sidebar</p>
          <p style={{ fontSize: 11, marginTop: 4 }}>or add a new one with the + button</p>
        </div>
      </div>
    )
  }
  return (
    <ChatPanelBody
      sessionId={sessionId}
      sessionName={sessionName}
      activeProjectId={activeProjectId}
      activeProject={activeProject}
    />
  )
}

function ChatPanelBody({
  sessionId,
  sessionName,
  activeProjectId,
  activeProject,
}: {
  sessionId?: string
  sessionName?: string
  activeProjectId: string
  activeProject: ProjectInfo
}) {
  const currentSessionId = sessionId || 'default'
  const chatKey = activeProjectId ? activeProjectId + '_' + currentSessionId : ''
  const messages = useChatStore((s) => chatKey ? s.messages[chatKey] || [] : [])
  const streamingText = useChatStore((s) => chatKey ? s.streaming[chatKey] || '' : '')
  const thinkingStreamText = useChatStore((s) => chatKey ? s.thinkingStream[chatKey] || '' : '')
  const retryStatus = useChatStore((s) => chatKey ? s.retrying[chatKey] : null)
  const persistedDraft = useChatStore((s) => chatKey ? s.drafts[chatKey] || '' : '')
  const setDraft = useChatStore((s) => s.setDraft)
  const liveUsage = useChatStore((s) => chatKey ? s.currentUsage[chatKey] : null)
  const lastTurnUsage = useChatStore((s) => chatKey ? s.lastTurnUsage[chatKey] : null)
  const askUserQ = useChatStore((s) => chatKey ? s.askUser[chatKey] : null)
  // Per-session active flag. project.active is project-wide (true if ANY
  // session is running), which wrongly keeps the "Generating" pill up in
  // sibling sessions where no turn is in flight.
  const thisSessionActive = useChatStore((s) => chatKey ? !!s.sessionActive[chatKey] : false)
  const addUserMessage = useChatStore((s) => s.addUserMessage)
  const clearChat = useChatStore((s) => s.clearChat)
  const settings = useSettingsStore((s) => s.settings)

  const setMessages = useChatStore((s) => s.setMessages)
  const setScrollPosition = useChatStore((s) => s.setScrollPosition)

  const updateProject = useProjectStore((s) => s.updateProject)

  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const [confirmClear, setConfirmClear] = useState(false)
  const [draggingFile, setDraggingFile] = useState(false)
  const [showTerminal, setShowTerminal] = useState(false)
  const [showMenu, setShowMenu] = useState(false)
  const [showFilePicker, setShowFilePicker] = useState(false)
  const [projectLang, setProjectLang] = useState<string | null>(null)
  // Git context for the smart welcome screen — branch / changed files /
  // recent commits. Loaded once per project switch; null while loading.
  const [gitCtx, setGitCtx] = useState<any | null>(null)
  const [showDispatch, setShowDispatch] = useState(false)
  const [showSysPrompt, setShowSysPrompt] = useState(false)
  const [sysPromptDraft, setSysPromptDraft] = useState('')
  const [sysPromptError, setSysPromptError] = useState<string | null>(null)
  const [showSysPromptTemplates, setShowSysPromptTemplates] = useState(false)
  const [sysPromptTemplates, setSysPromptTemplates] = useState<any[] | null>(null)
  const [showMemory, setShowMemory] = useState(false)
  const [memoryEntries, setMemoryEntries] = useState<any[] | null>(null)
  const [memError, setMemError] = useState<string | null>(null)
  const [deletingMemId, setDeletingMemId] = useState<string | null>(null)
  // Pinned messages: session-scoped bookmarks. The set holds the *content
  // hash* (just role+content) of currently pinned messages so the context
  // menu can flip "Pin" / "Unpin" without a round-trip per message.
  const [showPins, setShowPins] = useState(false)
  const [pinnedList, setPinnedList] = useState<any[] | null>(null)
  const [pinsError, setPinsError] = useState<string | null>(null)
  // Set of `<role>:<content>` strings for fast membership-test in the
  // context menu so the rename Pin <-> Unpin can be decided without
  // searching the array on every render.
  const [pinnedKeys, setPinnedKeys] = useState<Set<string>>(new Set())
  // Agent activity timeline modal (Ctrl+Shift+A): chronological view of every
  // tool call in the current session. Lets users scan "what did the agent do"
  // without scrolling the whole transcript.
  const [showActivity, setShowActivity] = useState(false)
  const [activityFilter, setActivityFilter] = useState('') // optional substring filter
  // Per-project usage statistics modal: aggregates cost/tokens/turn count
  // across every session in the project. Lazy-loaded on open since the
  // backend has to walk every session under lock.
  const [showUsageStats, setShowUsageStats] = useState(false)
  const [usageStats, setUsageStats] = useState<any | null>(null)
  const [usageStatsError, setUsageStatsError] = useState<string | null>(null)
  // Per-project budget: live total cost across all sessions, refreshed on
  // project switch and on every chat:complete. The header chip and the
  // usage modal both read this so they don't need separate fetches.
  const [projectTotalCostUSD, setProjectTotalCostUSD] = useState(0)
  // Budget editor modal: small inline form to set the per-project USD cap.
  const [showBudget, setShowBudget] = useState(false)
  const [budgetDraft, setBudgetDraft] = useState('')
  const [budgetError, setBudgetError] = useState<string | null>(null)
  const [budgetSaving, setBudgetSaving] = useState(false)
  // iter 1040+: strict-mode toggle. When on AND budget > 0, the backend
  // blocks new SendMessage calls once cumulative cost reaches the budget.
  const [budgetEnforceDraft, setBudgetEnforceDraft] = useState(false)
  // Conversation summary modal: triggers an LLM call against the current
  // session's history and shows a 3-5 bullet TL;DR. Useful for long
  // sessions or handoff between contexts. Costs tokens, so the modal
  // shows a brief warning before the user confirms.
  const [showSummary, setShowSummary] = useState(false)
  const [summaryText, setSummaryText] = useState<string | null>(null)
  const [summaryLoading, setSummaryLoading] = useState(false)
  const [summaryError, setSummaryError] = useState<string | null>(null)
  // In-app help modal (iter 440+). Comprehensive list of slash commands,
  // keyboard shortcuts, and gestures. Searchable so users with many entries
  // can find one quickly. Opens via /help, Ctrl+/, ?, or the chat header menu.
  const [showHelp, setShowHelp] = useState(false)
  const [helpQuery, setHelpQuery] = useState('')
  // Chat input history recall (iter 460+). Up/Down walk through previous
  // user messages terminal-style. -1 = not recalling. savedDraftRef holds
  // whatever the user was typing before recall started, so Esc/Down-past-0
  // can restore it. Reset on send + session switch.
  const [historyIdx, setHistoryIdx] = useState(-1)
  const savedDraftRef = useRef<string | null>(null)
  // Quick model switcher (iter 470+). Ctrl+M opens a Spotlight-style picker
  // listing every provider×model combo. Search filters; ↑↓ navigates;
  // Enter applies. Faster than clicking through ProviderSelect popup.
  const [showModelSwitcher, setShowModelSwitcher] = useState(false)
  const [modelSwitcherQuery, setModelSwitcherQuery] = useState('')
  const [modelSwitcherIdx, setModelSwitcherIdx] = useState(0)
  const [modelSwitcherSaving, setModelSwitcherSaving] = useState(false)
  const [modelSwitcherError, setModelSwitcherError] = useState<string | null>(null)
  const providers = useSettingsStore((s) => s.providers)
  // User-defined slash snippets (iter 490+). Loaded once per project switch
  // and merged into the slash autocomplete. Picking a snippet inserts its
  // body into the chat input (does NOT send) so the user can tweak before
  // sending. Refreshed whenever the user saves/deletes via Settings.
  const [userSnippets, setUserSnippets] = useState<{ id: string; name: string; body: string }[]>([])
  // Bulk session management modal (iter 500+). Lists all sessions with
  // checkboxes + bulk-delete. Helps users with 20+ sessions clean up.
  const [showSessionsMgr, setShowSessionsMgr] = useState(false)
  const [sessionsMgrList, setSessionsMgrList] = useState<any[] | null>(null)
  const [sessionsMgrSelected, setSessionsMgrSelected] = useState<Set<string>>(new Set())
  const [sessionsMgrConfirm, setSessionsMgrConfirm] = useState(false)
  const [sessionsMgrDeleting, setSessionsMgrDeleting] = useState(false)
  const [sessionsMgrError, setSessionsMgrError] = useState<string | null>(null)
  // @path autocomplete (iter 520+). Cached file list per project; popup
  // state tracks the @<query> the user is typing + selected suggestion.
  const [projectFiles, setProjectFiles] = useState<string[]>([])
  const [atMention, setAtMention] = useState<{ query: string; start: number } | null>(null)
  const [atMentionIdx, setAtMentionIdx] = useState(0)
  // Session export/import (iter 550+). Import opens a modal with a paste
  // textarea — Wails desktop apps can't reliably get a "browse for file"
  // dialog without OS plumbing, so paste is the most portable path.
  const [showImport, setShowImport] = useState(false)
  const [importDraft, setImportDraft] = useState('')
  const [importBusy, setImportBusy] = useState(false)
  const [importError, setImportError] = useState<string | null>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [showSearch, setShowSearch] = useState(false)
  // Cross-session search (Ctrl+Shift+F): scans every session in the active
  // project. Results jump to the matched session + scroll the message into view.
  const [showGlobalSearch, setShowGlobalSearch] = useState(false)
  const [globalQuery, setGlobalQuery] = useState('')
  const [globalHits, setGlobalHits] = useState<any[] | null>(null)
  const [globalSearchError, setGlobalSearchError] = useState<string | null>(null)
  const [globalSearchLoading, setGlobalSearchLoading] = useState(false)
  const [showScrollBtn, setShowScrollBtn] = useState(false)
  const [elapsedMs, setElapsedMs] = useState(0)
  const [retryCountdown, setRetryCountdown] = useState(0)
  const [recoveryEvents, setRecoveryEvents] = useState<any[] | null>(null)
  const [focusedMsgId, setFocusedMsgId] = useState<string | null>(null)
  const [ctxMenu, setCtxMenu] = useState<{ msgId: string; x: number; y: number } | null>(null)
  const [slashIdx, setSlashIdx] = useState(-1)
  // Quiet mode (iter 510+): hide tool/dispatch/thinking messages so users
  // can focus on the conversation. Persisted in localStorage per-project so
  // the preference survives restart but doesn't bleed across projects.
  // Initialized eagerly from localStorage on mount, then synced back on toggle.
  const quietModeKey = activeProjectId ? `gokin:quietmode:${activeProjectId}` : ''
  const [quietMode, setQuietMode] = useState<boolean>(() => {
    try {
      return quietModeKey ? localStorage.getItem(quietModeKey) === '1' : false
    } catch { return false }
  })
  // Hydrate quiet mode whenever the active project changes — different
  // projects can have different preferences. Without this, switching from a
  // quiet project to a non-quiet one would carry the wrong state until the
  // user touches the toggle.
  useEffect(() => {
    if (!quietModeKey) { setQuietMode(false); return }
    try { setQuietMode(localStorage.getItem(quietModeKey) === '1') } catch { setQuietMode(false) }
  }, [quietModeKey])
  // Per-marker expansion: clicking a "N hidden" marker reveals just that group.
  const [expandedMarkers, setExpandedMarkers] = useState<Set<string>>(new Set())
  // Reset expansion on session switch so a stale marker doesn't carry over.
  useEffect(() => { setExpandedMarkers(new Set()) }, [chatKey])

  // Must be declared before the j/k navigation hook which reads filteredMessages.
  const filteredMessages = useMemo(() => {
    if (!searchQuery) return messages
    const needle = searchQuery.toLowerCase()
    return messages.filter((m) => m.content.toLowerCase().includes(needle))
  }, [messages, searchQuery])

  // displayMessages applies quiet-mode collapsing on top of search filtering.
  // When quiet, runs of consecutive tool/dispatch/thinking messages are
  // replaced by a single synthetic "marker" entry that the renderer can
  // detect via the role==='hidden-marker' sentinel. The marker's id contains
  // the index of the first hidden message so click-to-expand can target it.
  const displayMessages = useMemo(() => {
    if (!quietMode) return filteredMessages
    const out: any[] = []
    let runStart = -1
    let runCount = 0
    const flushRun = () => {
      if (runCount === 0) return
      const markerId = `hidden-${runStart}`
      if (expandedMarkers.has(markerId)) {
        // User explicitly expanded this run — render the original messages.
        for (let k = 0; k < runCount; k++) out.push(filteredMessages[runStart + k])
      } else {
        out.push({
          id: markerId,
          role: 'hidden-marker',
          content: '',
          count: runCount,
          timestamp: 0,
          firstIdx: runStart,
        })
      }
      runStart = -1
      runCount = 0
    }
    for (let i = 0; i < filteredMessages.length; i++) {
      const m = filteredMessages[i]
      const isNoise = m.role === 'tool' || m.role === 'dispatch' || m.role === 'thinking'
      if (isNoise) {
        if (runStart === -1) runStart = i
        runCount++
        continue
      }
      flushRun()
      out.push(m)
    }
    flushRun()
    return out
  }, [filteredMessages, quietMode, expandedMarkers])

  const chatContainerRef = useRef<HTMLDivElement>(null)
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)

  // Sync textarea height to content without flicker.
  useLayoutEffect(() => {
    const el = inputRef.current
    if (!el) return
    // Measure natural height by resetting to 0 via a style attribute without
    // triggering an intermediate 'auto' layout. Then apply the new height.
    el.style.height = '0'
    el.style.height = Math.min(el.scrollHeight, 200) + 'px'
  }, [input])

  // Persist the current draft to the in-memory store (immediate) AND to disk
  // (debounced 400 ms). Disk persistence means a long unsent message survives
  // an app restart, crash, or accidental Cmd+Q. The 400 ms debounce keeps
  // disk I/O off the typing hot-path.
  useEffect(() => {
    if (!chatKey) return
    setDraft(chatKey, input)
    if (!activeProjectId) return
    const timer = window.setTimeout(() => {
      // Fire-and-forget: a failed write is logged but doesn't surface to the
      // user — they'd just type into the void otherwise. The in-memory copy
      // is still authoritative for the current session.
      SaveDraft(activeProjectId, currentSessionId, input).catch((e) => {
        console.warn('SaveDraft failed:', e)
      })
    }, 400)
    return () => window.clearTimeout(timer)
  }, [input, chatKey, activeProjectId, currentSessionId, setDraft])

  // On session switch, hydrate the input from the persisted disk draft if the
  // in-memory store has nothing for this chat key. The chatStore's draft
  // wins when set (most recent edit lives there); the disk fallback is for
  // a fresh app start where the in-memory store is empty.
  useEffect(() => {
    if (!activeProjectId || !chatKey) return
    if (persistedDraft) return // in-memory draft already set
    let cancelled = false
    GetDraft(activeProjectId, currentSessionId).then((text) => {
      if (cancelled || !text) return
      // Only hydrate if the user hasn't started typing in the interim — guard
      // against a slow disk read clobbering a fresh keystroke.
      const live = useChatStore.getState().drafts[chatKey] || ''
      if (live) return
      setDraft(chatKey, text)
      setInput(text)
    }).catch(() => {})
    return () => { cancelled = true }
    // currentSessionId / activeProjectId in deps so the effect re-runs on switch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeProjectId, currentSessionId, chatKey])



  // Load persisted history when switching sessions (only if chat is empty).
  //
  // iter 1010+: after content is loaded, restore the saved scroll position
  // for this chatKey if any. Save-on-switch can't live in this effect's
  // cleanup because by the time cleanup runs, React has already committed
  // the NEW chatKey's render — DOM scrollTop is meaningless for the OLD
  // chatKey. Instead, scroll position is saved live in handleScroll (see
  // below). Sentinel -1 means "user was at bottom" → scroll to bottom on
  // return so any new content that arrived in the interim is visible.
  const AT_BOTTOM_PX = 80
  useEffect(() => {
    if (!activeProjectId || !chatKey) return
    const applyScroll = () => {
      const el = chatContainerRef.current
      if (!el) return
      const saved = useChatStore.getState().scrollPositions[chatKey]
      if (saved !== undefined && saved >= 0) {
        // Clamp to current scrollable range — content may have changed
        // between saves so the literal saved pixel can exceed the new max.
        const maxScroll = Math.max(0, el.scrollHeight - el.clientHeight)
        el.scrollTop = Math.min(saved, maxScroll)
        const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
        userScrolledUpRef.current = distanceFromBottom > AT_BOTTOM_PX
      } else {
        messagesEndRef.current?.scrollIntoView({ behavior: 'auto' as ScrollBehavior })
        userScrolledUpRef.current = false
      }
    }
    const existing = useChatStore.getState().messages[chatKey]
    if (existing && existing.length > 0) {
      requestAnimationFrame(applyScroll)
      return
    }
    let cancelled = false
    GetHistory(activeProjectId, currentSessionId).then((hist) => {
      if (cancelled) return
      if (hist && hist.length > 0) {
        setMessages(chatKey, hist.map((m: any) => ({
          id: m.id || `hist-${Date.now()}-${Math.random()}`,
          role: m.role === 'model' ? 'assistant' : m.role,
          content: m.content,
          timestamp: m.timestamp || 0,
        })))
        requestAnimationFrame(applyScroll)
      }
    }).catch(() => {})
    return () => { cancelled = true }
  }, [activeProjectId, chatKey, setMessages, currentSessionId])

  // Check for interrupted turn recovery each time the session switches.
  // Clear synchronously first so stale banner from the old session never
  // flickers in while the async call is in flight.
  useEffect(() => {
    setRecoveryEvents(null)
    if (!activeProjectId || !currentSessionId) return
    let cancelled = false
    GetRecoveryEvents(activeProjectId, currentSessionId).then((events) => {
      if (cancelled) return
      setRecoveryEvents(events && events.length > 0 ? events : null)
    }).catch(() => { if (!cancelled) setRecoveryEvents(null) })
    return () => { cancelled = true }
  }, [activeProjectId, currentSessionId])

  // Track whether user manually scrolled up (auto-scroll pauses while true).
  const userScrolledUpRef = useRef(false)

  // Auto-scroll on new messages / streaming updates unless user scrolled up.
  useEffect(() => {
    const el = chatContainerRef.current
    if (!el) return
    if (!userScrolledUpRef.current) {
      // Use requestAnimationFrame so we scroll AFTER the new content has layout.
      requestAnimationFrame(() => {
        messagesEndRef.current?.scrollIntoView({ behavior: 'auto' as ScrollBehavior, block: 'end' })
      })
    }
  }, [messages, streamingText, thinkingStreamText, askUserQ, retryStatus])

  // Live countdown for the retry delay banner.
  useEffect(() => {
    if (!retryStatus) { setRetryCountdown(0); return }
    const deadline = retryStatus.startedAt + retryStatus.delayMs
    const tick = () => setRetryCountdown(Math.max(0, Math.ceil((deadline - Date.now()) / 1000)))
    tick()
    const id = setInterval(tick, 250)
    return () => clearInterval(id)
  }, [retryStatus])

  // Listen for Ctrl+L from App.tsx.
  useEffect(() => {
    const handler = () => setConfirmClear(true)
    window.addEventListener('gokin:clear-chat', handler)
    return () => window.removeEventListener('gokin:clear-chat', handler)
  }, [])

  // Listen for "open global search" from the command palette / external.
  useEffect(() => {
    const handler = () => setShowGlobalSearch(true)
    window.addEventListener('gokin:open-global-search', handler)
    return () => window.removeEventListener('gokin:open-global-search', handler)
  }, [])

  // Listen for "insert @path" requests from changed-files summary chips.
  // Reads el.value (not input state) so the handler never needs to re-register.
  useEffect(() => {
    const handler = (e: Event) => {
      const path = (e as CustomEvent).detail?.path
      if (!path) return
      const token = '@' + path
      const el = inputRef.current
      if (el) {
        const cursor = el.selectionStart ?? el.value.length
        const before = el.value.slice(0, cursor)
        const after = el.value.slice(el.selectionEnd ?? cursor)
        const needsLeadingSpace = before.length > 0 && !before.endsWith(' ') && !before.endsWith('\n')
        const insertion = (needsLeadingSpace ? ' ' : '') + token + ' '
        setInput(before + insertion + after)
        requestAnimationFrame(() => {
          el.focus()
          const pos = (before + insertion).length
          el.setSelectionRange(pos, pos)
        })
      } else {
        setInput((prev) => (prev ? prev + ' ' : '') + token + ' ')
      }
    }
    window.addEventListener('gokin:insert-file-ref', handler)
    return () => window.removeEventListener('gokin:insert-file-ref', handler)
  }, [])

  // Vim-style message navigation: J/K move focus down/up, gg to first, G to
  // last, Enter scrolls focused into view. Only active when the input is not
  // focused (so typing 'j' actually types 'j'). Modal/palette/edit states
  // suppress it via `isInteractive` check.
  const ggPressedRef = useRef(false)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Never intercept IME composition — preventDefault during a dead-key /
      // Cyrillic / CJK compose would swallow the character.
      if (e.isComposing || e.keyCode === 229) return
      const target = e.target as HTMLElement | null
      const inEditor = target && (
        target.tagName === 'TEXTAREA' ||
        target.tagName === 'INPUT' ||
        target.isContentEditable
      )
      if (inEditor) return
      if (e.ctrlKey || e.metaKey || e.altKey) return
      if (showPaletteActive()) return

      const list = filteredMessages
      if (list.length === 0) return
      const currentIdx = focusedMsgId ? list.findIndex((m) => m.id === focusedMsgId) : -1

      if (e.key === 'j' || e.key === 'ArrowDown' && !e.shiftKey && target === document.body) {
        e.preventDefault()
        const nextIdx = currentIdx < 0 ? 0 : Math.min(list.length - 1, currentIdx + 1)
        setFocusedMsgId(list[nextIdx].id)
        ggPressedRef.current = false
        return
      }
      if (e.key === 'k' || e.key === 'ArrowUp' && !e.shiftKey && target === document.body) {
        e.preventDefault()
        const prevIdx = currentIdx < 0 ? list.length - 1 : Math.max(0, currentIdx - 1)
        setFocusedMsgId(list[prevIdx].id)
        ggPressedRef.current = false
        return
      }
      if (e.key === 'G' && e.shiftKey) {
        e.preventDefault()
        setFocusedMsgId(list[list.length - 1].id)
        ggPressedRef.current = false
        return
      }
      if (e.key === 'g' && !e.shiftKey) {
        if (ggPressedRef.current) {
          e.preventDefault()
          setFocusedMsgId(list[0].id)
          ggPressedRef.current = false
        } else {
          ggPressedRef.current = true
          // Expire after short window so 'g' + later 'g' doesn't accumulate.
          window.setTimeout(() => { ggPressedRef.current = false }, 600)
        }
        return
      }
      // Esc clears focus so the ring isn't left visible after a modal closes.
      if (e.key === 'Escape' && focusedMsgId) {
        setFocusedMsgId(null)
      }
    }
    // Check whether the palette (rendered at App.tsx level) currently has any
    // backdrop mounted — if so, skip nav so keys go to the palette's own input.
    // We detect via a DOM query because palette state lives in App.tsx.
    function showPaletteActive() {
      return !!document.querySelector('.palette-backdrop, .fp-backdrop')
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [filteredMessages, focusedMsgId])

  // Scroll focused message into view.
  useEffect(() => {
    if (!focusedMsgId) return
    const el = document.querySelector<HTMLElement>(`[data-msg-id="${focusedMsgId}"]`)
    if (el) el.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }, [focusedMsgId])

  // Clear focus ring when search query changes so j/k navigation always
  // starts from scratch in the current filtered list. Without this, a
  // previously-focused message that no longer appears in filtered results
  // causes currentIdx === -1 and the first j press jumps to index 0
  // instead of the next logical message.
  useEffect(() => {
    setFocusedMsgId(null)
  }, [searchQuery])

  // Ctrl+P / Cmd+P — open the file picker (VSCode "Quick Open" style). Also
  // intercepts the browser print dialog which is not useful in this desktop app.
  // Ctrl+F / Cmd+F — open in-chat search (intercepts browser find-in-page).
  // Ctrl+` — toggle the integrated terminal (VS Code / iTerm style).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      if ((e.ctrlKey || e.metaKey) && e.key === 'p') {
        e.preventDefault()
        setShowFilePicker((s) => !s)
      }
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && (e.key === 'F' || e.key === 'f')) {
        // Ctrl+Shift+F → cross-session search across the entire project.
        // Must be checked BEFORE the plain Ctrl+F handler since shift+f also
        // matches that one on some keyboard layouts.
        e.preventDefault()
        setShowGlobalSearch((s) => !s)
        return
      }
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && (e.key === 'A' || e.key === 'a')) {
        // Ctrl+Shift+A → agent activity timeline.
        e.preventDefault()
        setShowActivity((s) => !s)
        return
      }
      if ((e.ctrlKey || e.metaKey) && e.key === 'f') {
        e.preventDefault()
        // Mirror the header button: close + clear query, or open.
        if (showSearch) { setShowSearch(false); setSearchQuery('') }
        else setShowSearch(true)
      }
      if ((e.ctrlKey || e.metaKey) && e.key === '`') {
        e.preventDefault()
        setShowTerminal((s) => !s)
      }
      // Ctrl+/ → open the in-app help modal. Also accept "?" when the user
      // isn't typing in an input (textareas/inputs would otherwise lose the
      // "?" character). Avoids stealing "?" inside slash autocomplete.
      if ((e.ctrlKey || e.metaKey) && e.key === '/') {
        e.preventDefault()
        setShowHelp((s) => !s)
        return
      }
      // Ctrl+M → open the quick model switcher (iter 470+). Spotlight-style
      // picker that filters every provider×model combo.
      if ((e.ctrlKey || e.metaKey) && (e.key === 'm' || e.key === 'M')) {
        e.preventDefault()
        setModelSwitcherQuery('')
        setModelSwitcherIdx(0)
        setModelSwitcherError(null)
        setShowModelSwitcher((s) => !s)
        return
      }
      if (e.key === '?' && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) {
        const t = e.target as HTMLElement | null
        const inEditor = t && (t.tagName === 'TEXTAREA' || t.tagName === 'INPUT' || (t as HTMLElement).isContentEditable)
        if (!inEditor) {
          e.preventDefault()
          setShowHelp((s) => !s)
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [showSearch])

  // Escape handling: close modals first; if nothing is open and the agent is
  // running, stop it as a shortcut. Input-level Esc (while typing) still lets
  // the textarea receive its own key event — we only intercept when focus is
  // outside an editable control or when the modal chain is active.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      if (e.key !== 'Escape') return
      // If any overlay is open, defer to it — FilePicker/CommandPalette/etc
      // attach their own Esc handlers to internal elements, but this window-
      // level fallback used to kick in anyway and accidentally stop the agent
      // when closing an unrelated overlay.
      if (document.querySelector('.fp-backdrop, .palette-backdrop, .provider-select-backdrop, .dispatch-backdrop')) return
      // Global cross-session search has its own backdrop class; handle Esc
      // explicitly so the modal closes from anywhere, not just when the input
      // has focus.
      if (showGlobalSearch) { e.preventDefault(); setShowGlobalSearch(false); setGlobalQuery(''); setGlobalHits(null); setGlobalSearchError(null); return }
      // Pins modal — same dispatch-backdrop bail-out wouldn't reach this
      // branch from non-input focus, so handle explicitly.
      if (showPins) { e.preventDefault(); setShowPins(false); return }
      if (showActivity) { e.preventDefault(); setShowActivity(false); setActivityFilter(''); return }
      if (showUsageStats) { e.preventDefault(); setShowUsageStats(false); return }
      if (showSummary) { e.preventDefault(); setShowSummary(false); return }
      if (showHelp) { e.preventDefault(); setShowHelp(false); setHelpQuery(''); return }
      if (showModelSwitcher) { e.preventDefault(); setShowModelSwitcher(false); setModelSwitcherQuery(''); setModelSwitcherError(null); return }
      if (showSessionsMgr) { e.preventDefault(); setShowSessionsMgr(false); setSessionsMgrConfirm(false); setSessionsMgrError(null); return }
      if (showImport) { e.preventDefault(); setShowImport(false); setImportError(null); return }
      if (showBudget) { e.preventDefault(); setShowBudget(false); setBudgetError(null); return }
      // If confirm-clear bar is open, close it first.
      if (confirmClear) { e.preventDefault(); setConfirmClear(false); return }
      if (showSearch) { e.preventDefault(); setShowSearch(false); setSearchQuery(''); return }
      if (showSysPrompt) { e.preventDefault(); setShowSysPrompt(false); return }
      if (showDispatch) { e.preventDefault(); setShowDispatch(false); return }
      if (showMenu) { e.preventDefault(); setShowMenu(false); return }
      if (showFilePicker) { e.preventDefault(); setShowFilePicker(false); return }
      if (showMemory) { e.preventDefault(); setShowMemory(false); return }
      if (ctxMenu) { e.preventDefault(); setCtxMenu(null); return }
      // If an ask_user card is visible, Esc cancels the question (not the agent).
      // Read from the store so the handler isn't stale — askUserQ is not in deps.
      if (chatKey) {
        const pendingQ = useChatStore.getState().askUser[chatKey]
        if (pendingQ) {
          e.preventDefault()
          CancelQuestion(pendingQ.questionID).catch(() => {})
          useChatStore.getState().setAskUser(chatKey, null)
          return
        }
      }
      // Nothing is open — if THIS session's agent is active, Esc stops it.
      const target = e.target as HTMLElement | null
      const inEditor = target && (target.tagName === 'TEXTAREA' || target.tagName === 'INPUT')
      if (thisSessionActive && !inEditor) {
        e.preventDefault()
        handleStop()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [confirmClear, showSearch, showSysPrompt, showDispatch, showMenu, showFilePicker, showMemory, showGlobalSearch, showPins, showActivity, showUsageStats, showSummary, showHelp, showModelSwitcher, showSessionsMgr, showImport, showBudget, ctxMenu, thisSessionActive, chatKey])

  // Close the context menu on any global click, Escape, or scroll.
  useEffect(() => {
    if (!ctxMenu) return
    const close = () => setCtxMenu(null)
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      if (e.key === 'Escape') close()
    }
    const container = chatContainerRef.current
    window.addEventListener('mousedown', close)
    window.addEventListener('keydown', onKey)
    container?.addEventListener('scroll', close)
    return () => {
      window.removeEventListener('mousedown', close)
      window.removeEventListener('keydown', onKey)
      container?.removeEventListener('scroll', close)
    }
  }, [ctxMenu])

  // Restore draft input and reset transient UI when switching sessions.
  useEffect(() => {
    setInput(persistedDraft)
    setConfirmClear(false)
    setShowSearch(false)
    setSearchQuery('')
    setShowSysPrompt(false)
    setSysPromptError(null)
    setShowDispatch(false)
    setShowMemory(false)
    setShowFilePicker(false)
    setShowMenu(false)
    setCtxMenu(null)
    setFocusedMsgId(null)
    setShowGlobalSearch(false)
    setGlobalQuery('')
    setGlobalHits(null)
    setGlobalSearchError(null)
    setGlobalSearchLoading(false)
    setShowPins(false)
    setPinsError(null)
    setPinnedList(null) // force re-fetch on next open since this is a different session
    setShowSysPromptTemplates(false)
    setShowActivity(false)
    setActivityFilter('')
    setShowUsageStats(false)
    setUsageStats(null)
    setUsageStatsError(null)
    setShowSummary(false)
    setSummaryText(null)
    setSummaryLoading(false)
    setSummaryError(null)
    setShowBudget(false)
    setBudgetError(null)
    setBudgetSaving(false)
    setShowHelp(false)
    setHelpQuery('')
    setShowModelSwitcher(false)
    setModelSwitcherQuery('')
    setModelSwitcherError(null)
    setShowSessionsMgr(false)
    setSessionsMgrList(null)
    setSessionsMgrSelected(new Set())
    setSessionsMgrConfirm(false)
    setSessionsMgrError(null)
    setAtMention(null)
    setAtMentionIdx(0)
    setShowImport(false)
    setImportDraft('')
    setImportError(null)
    setHistoryIdx(-1)
    savedDraftRef.current = null
    // Focus the input on session switch so user can type immediately.
    requestAnimationFrame(() => {
      inputRef.current?.focus()
      // Place cursor at end of restored draft
      if (inputRef.current && persistedDraft) {
        inputRef.current.setSelectionRange(persistedDraft.length, persistedDraft.length)
      }
    })
    // persistedDraft intentionally excluded -- we only want to hydrate on session switch,
    // not re-apply on every draft change (which would cause a feedback loop).
    // projectId is included so switching between projects with the same session
    // name (e.g. both "default") still resets modals and draft correctly.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentSessionId, activeProjectId])

  // Debounced cross-session search. Fires 200 ms after the user stops typing
  // so we don't spam the backend on every keystroke. Cancelled-flag guard
  // prevents an in-flight request from overwriting a newer query's results.
  useEffect(() => {
    if (!showGlobalSearch || !activeProjectId) return
    const q = globalQuery.trim()
    if (!q) {
      setGlobalHits(null)
      setGlobalSearchError(null)
      setGlobalSearchLoading(false)
      return
    }
    let cancelled = false
    setGlobalSearchLoading(true)
    setGlobalSearchError(null)
    const timer = window.setTimeout(() => {
      SearchProjectHistory(activeProjectId, q).then((hits: any) => {
        if (cancelled) return
        setGlobalHits(hits || [])
        setGlobalSearchLoading(false)
      }).catch((e: any) => {
        if (cancelled) return
        setGlobalSearchError(String(e?.message || e || 'Search failed'))
        setGlobalHits([])
        setGlobalSearchLoading(false)
      })
    }, 200)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [globalQuery, showGlobalSearch, activeProjectId])

  // Tick elapsed time while THIS session is active; reset when it stops or
  // when the user switches sessions (chatKey changes). Without chatKey as a
  // dep, switching from active session A to active session B keeps counting
  // from A's start time instead of resetting.
  useEffect(() => {
    if (!thisSessionActive) {
      setElapsedMs(0)
      return
    }
    const start = Date.now()
    setElapsedMs(0)
    const id = window.setInterval(() => setElapsedMs(Date.now() - start), 500)
    return () => window.clearInterval(id)
  }, [thisSessionActive, chatKey])

  // Detect project language from indicator files in the project root.
  useEffect(() => {
    if (!activeProjectId) return
    setProjectLang(null) // clear immediately so stale language doesn't show during load
    let cancelled = false
    ListDirectory(activeProjectId, '').then((entries) => {
      if (cancelled || !entries) return
      const names = new Set(entries.map((e: any) => e.name.toLowerCase()))
      let lang: string | null = null
      if (names.has('go.mod')) lang = 'go'
      else if (names.has('cargo.toml')) lang = 'rust'
      else if (names.has('package.json')) lang = names.has('tsconfig.json') ? 'typescript' : 'javascript'
      else if (names.has('pyproject.toml') || names.has('requirements.txt') || names.has('setup.py')) lang = 'python'
      else if (names.has('gemfile')) lang = 'ruby'
      else if (names.has('pom.xml') || names.has('build.gradle') || names.has('build.gradle.kts')) lang = 'java'
      else if (names.has('composer.json')) lang = 'php'
      else if (names.has('mix.exs')) lang = 'elixir'
      else if (names.has('package.swift')) lang = 'swift'
      else if (names.has('pubspec.yaml')) lang = 'dart'
      else if (names.has('build.zig')) lang = 'zig'
      else if ([...names].some(n => n.endsWith('.csproj') || n.endsWith('.sln') || n === 'global.json')) lang = 'csharp'
      else if (names.has('cmakelists.txt') || names.has('configure.ac') || names.has('meson.build')) lang = 'c++'
      setProjectLang(lang)
    }).catch(() => { if (!cancelled) setProjectLang(null) })
    return () => { cancelled = true }
  }, [activeProjectId])

  // Load git context for the smart welcome screen. Best-effort: a missing
  // git, a non-repo, or a slow git all yield empty/zero state and the
  // language-only welcome falls through.
  useEffect(() => {
    if (!activeProjectId) { setGitCtx(null); return }
    setGitCtx(null) // clear stale state immediately on project switch
    let cancelled = false
    GetProjectGitContext(activeProjectId).then((ctx: any) => {
      if (cancelled) return
      setGitCtx(ctx || null)
    }).catch(() => { if (!cancelled) setGitCtx(null) })
    return () => { cancelled = true }
  }, [activeProjectId])

  // iter 1050+: fetch the active model's input rate (USD per million tokens)
  // ONCE per (project, provider, model) change so the live cost preview in
  // the chat input footer can compute estimated cost from `tokens × rate`
  // locally on every keystroke without crossing the Wails bridge. All-zero
  // pricing (Ollama, unknown model) clears inputRate to 0 → frontend hides
  // the cost chip, matching the iter 290+ "0 means don't display" convention.
  const [inputRateUSDPerMTok, setInputRateUSDPerMTok] = useState(0)
  useEffect(() => {
    if (!activeProject) { setInputRateUSDPerMTok(0); return }
    let cancelled = false
    GetModelPricing(activeProject.provider || 'glm', activeProject.model || 'glm-5.1')
      .then((p: any) => {
        if (cancelled) return
        setInputRateUSDPerMTok(p?.InputPerMTok || 0)
      })
      .catch(() => { if (!cancelled) setInputRateUSDPerMTok(0) })
    return () => { cancelled = true }
  }, [activeProject?.provider, activeProject?.model])

  // Load project-wide accumulated cost on project switch and refresh on every
  // chat:complete so the budget chip stays accurate without a manual reopen
  // of the usage modal. ProjectUsageStats walks every session under lock; it's
  // cheap (no LLM call) but we still gate behind activeProjectId to avoid a
  // wasted RPC during the initial mount race.
  useEffect(() => {
    if (!activeProjectId) { setProjectTotalCostUSD(0); return }
    let cancelled = false
    const refresh = () => {
      ProjectUsageStats(activeProjectId).then((stats: any) => {
        if (cancelled) return
        setProjectTotalCostUSD(stats?.totalCostUSD || 0)
      }).catch(() => { /* leave previous value; transient errors shouldn't blank the chip */ })
    }
    refresh()
    // Re-fetch when ANY session in this project finishes a turn so the chip
    // reflects spend across siblings, not just the active session.
    const off = EventsOn('chat:complete', (data: any) => {
      if (data?.projectID === activeProjectId) refresh()
    })
    return () => { cancelled = true; if (typeof off === 'function') off() }
  }, [activeProjectId])

  // Budget threshold alerts (iter 610+). When projectTotalCostUSD crosses
  // 80% or 100% of the configured budget, push a one-time warning toast.
  // Persisted in localStorage so we don't re-fire on every restart.
  // Reset when the user changes the budget (so a new budget gets a fresh
  // alerting cycle).
  useEffect(() => {
    if (!activeProjectId || !activeProject) return
    const budget = activeProject.budgetUSD || 0
    if (budget <= 0) return
    const total = projectTotalCostUSD
    if (total <= 0) return
    // Iter 670+: skip the entire alert flow when the project is muted.
    // Don't persist to the alerted set — that way unmuting later lets the
    // user receive the alert on the next chat:complete (otherwise it'd
    // get silently swallowed forever once the threshold has been crossed).
    if (isProjectMuted(activeProjectId)) return
    const pct = (total / budget) * 100
    const key = `gokin:budget-alerts-${activeProjectId}`
    let stored: { budget: number; alerted: number[] } = { budget, alerted: [] }
    try {
      const raw = localStorage.getItem(key)
      if (raw) {
        const parsed = JSON.parse(raw)
        if (typeof parsed === 'object' && parsed !== null) {
          stored = {
            budget: typeof parsed.budget === 'number' ? parsed.budget : budget,
            alerted: Array.isArray(parsed.alerted) ? parsed.alerted.filter((x: any) => typeof x === 'number') : [],
          }
        }
      }
    } catch { /* corrupt entry — start fresh */ }
    // Reset the alerted set when the budget changes (new ceiling = new alerts).
    if (stored.budget !== budget) {
      stored = { budget, alerted: [] }
    }
    const thresholds = [80, 100]
    let dirty = false
    for (const t of thresholds) {
      if (pct >= t && !stored.alerted.includes(t)) {
        stored.alerted.push(t)
        dirty = true
        // Dispatch a custom alert that ToastStack picks up — same path as
        // chat:complete toasts but with a distinct "warning" kind.
        window.dispatchEvent(new CustomEvent('gokin:budget-alert', {
          detail: {
            projectID: activeProjectId,
            sessionID: currentSessionId,
            projectName: activeProject.name,
            threshold: t,
            total,
            budget,
          },
        }))
      }
    }
    if (dirty) {
      try { localStorage.setItem(key, JSON.stringify(stored)) } catch { /* unavailable */ }
    }
  }, [projectTotalCostUSD, activeProject?.budgetUSD, activeProjectId, activeProject, currentSessionId])

  // Track scroll position; if user scrolls away from bottom, pause auto-scroll.
  //
  // iter 1010+: also persist the scrollTop to chatStore.scrollPositions on
  // every scroll (already rAF-throttled, so this is at most one update per
  // frame). Sentinel -1 marks "at bottom" so the next session-switch back
  // here auto-scrolls to bottom (showing any new content that streamed in).
  // We save live instead of on session-switch because by the time React
  // fires the cleanup of the chatKey effect, the DOM has already been
  // committed to show the NEW chatKey — the OLD scrollTop is gone.
  const scrollRaf = useRef(0)
  const handleScroll = () => {
    cancelAnimationFrame(scrollRaf.current)
    scrollRaf.current = requestAnimationFrame(() => {
      const el = chatContainerRef.current
      if (!el) return
      const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
      const isNearBottom = distanceFromBottom < 80
      userScrolledUpRef.current = !isNearBottom
      setShowScrollBtn(!isNearBottom)
      if (chatKey) {
        setScrollPosition(chatKey, isNearBottom ? -1 : el.scrollTop)
      }
    })
  }

  const scrollToBottom = () => {
    userScrolledUpRef.current = false
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' })
  }

  // activeProject is guaranteed non-null here (the ChatPanel wrapper gates this
  // body on a selected project), so every hook below runs unconditionally and
  // every activeProject access is type-safe.

  // Check if the provider's API key is configured
  const provider = activeProject.provider || 'glm'

  const KEY_FIELDS: Record<string, keyof typeof settings> = {
    glm: 'glmKey',
    minimax: 'minimaxKey',
    kimi: 'kimiKey',
  }

  const missingKey = (() => {
    const field = KEY_FIELDS[provider]
    if (field && !settings[field]) {
      const providerName = provider.charAt(0).toUpperCase() + provider.slice(1)
      return providerName
    }
    return null
  })()

  // Ollama doesn't need an API key
  const isKeyRequired = provider !== 'ollama'
  const dirOK = activeProject.directoryOK !== false
  const canSend = (!missingKey || !isKeyRequired) && dirOK

  const SLASH_COMMANDS_BUILTIN = [
    { cmd: '/clear', desc: 'Clear chat history' },
    { cmd: '/export', desc: 'Export chat as Markdown' },
    { cmd: '/exportall', desc: 'Export all sessions in this project as one Markdown' },
    { cmd: '/summarize', desc: 'Generate a TL;DR of this session via the LLM (costs tokens)' },
    { cmd: '/system', desc: 'Edit system prompt' },
    { cmd: '/search', desc: 'Search messages (optional: /search query)' },
    { cmd: '/memory', desc: 'View what the agent remembers' },
    { cmd: '/budget', desc: 'Set or change the project USD spend cap' },
    { cmd: '/sessions', desc: 'Manage all chat sessions in this project (bulk delete)' },
    { cmd: '/exportjson', desc: 'Export this session as JSON (full backup)' },
    { cmd: '/importsession', desc: 'Import a session from JSON' },
    { cmd: '/help', desc: 'Show all slash commands and keyboard shortcuts (Ctrl+/)' },
  ]
  // Merge user-defined snippets after built-ins. We tag them with `snippet:true`
  // so the slash-handler knows to expand-into-input rather than execute an
  // action. Description shows a body preview so the autocomplete is useful
  // even before picking.
  const SLASH_COMMANDS = useMemo(() => {
    const userCmds = userSnippets.map((s) => ({
      cmd: '/' + s.name,
      desc: '↳ ' + s.body.replace(/\s+/g, ' ').slice(0, 80) + (s.body.length > 80 ? '…' : ''),
      snippet: true as const,
      body: s.body,
    }))
    return [...SLASH_COMMANDS_BUILTIN, ...userCmds]
    // SLASH_COMMANDS_BUILTIN is recreated on each render but its values are
    // stable, so re-deriving on userSnippets change is what we actually care
    // about. The eslint exhaustive-deps rule would force adding it but that
    // creates infinite re-renders.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [userSnippets])

  const slashMatches = input.startsWith('/') && !input.includes(' ')
    ? SLASH_COMMANDS.filter((c) => c.cmd.startsWith(input.toLowerCase()))
    : []

  // Reset keyboard selection whenever the suggestions list content changes
  // (not just length — /c → /clear and /c → /copy have the same length=1).
  const slashMatchKey = slashMatches.map((m) => m.cmd).join(',')
  useEffect(() => { setSlashIdx(-1) }, [slashMatchKey])

  // @path autocomplete: filter the cached project file list against the
  // typed query. Empty query shows the top files (alphabetical). Substring
  // match on full path is good enough for most repos; fuzzy matching is
  // overkill for this surface.
  const atMentionMatches = useMemo(() => {
    if (!atMention) return [] as string[]
    const q = atMention.query.toLowerCase()
    if (q === '') return projectFiles.slice(0, 10)
    const matches: string[] = []
    for (const f of projectFiles) {
      if (f.toLowerCase().includes(q)) {
        matches.push(f)
        if (matches.length >= 10) break
      }
    }
    return matches
  }, [atMention, projectFiles])

  // Reset selection when the matches set changes so we don't point past the
  // end of the new list.
  const atMatchKey = atMentionMatches.join(',')
  useEffect(() => { setAtMentionIdx(0) }, [atMatchKey])

  // Apply a chosen suggestion: replace the @<query> in the input with
  // @<full-path> + trailing space, place cursor after the inserted ref,
  // and dismiss the popup.
  const applyAtMention = (path: string) => {
    if (!atMention) return
    const before = input.slice(0, atMention.start)
    const after = input.slice(atMention.start + 1 + atMention.query.length)
    const inserted = '@' + path + ' '
    const newInput = before + inserted + after
    setInput(newInput)
    setAtMention(null)
    requestAnimationFrame(() => {
      const el = inputRef.current
      if (el) {
        const pos = before.length + inserted.length
        el.setSelectionRange(pos, pos)
        el.focus()
      }
    })
  }

  const openMemoryViewer = async () => {
    setShowMemory(true)
    setMemoryEntries(null) // loading state
    setMemError(null)
    try {
      const entries = await ListProjectMemory(activeProjectId)
      setMemoryEntries(entries || [])
    } catch (e: any) {
      console.error('ListProjectMemory error:', e)
      setMemoryEntries([])
      setMemError(`Failed to load memory: ${String(e?.message || e)}`)
    }
  }

  // Keep a stable ref so the memory event handler always calls the latest
  // openMemoryViewer (which closes over the current activeProjectId) rather
  // than the one captured at the time the effect ran.
  const openMemoryViewerRef = useRef(openMemoryViewer)
  openMemoryViewerRef.current = openMemoryViewer
  useEffect(() => {
    const handler = () => openMemoryViewerRef.current()
    window.addEventListener('gokin:open-memory', handler)
    return () => window.removeEventListener('gokin:open-memory', handler)
  }, [])

  // Open model switcher from external trigger (command palette).
  useEffect(() => {
    const handler = () => {
      setModelSwitcherQuery('')
      setModelSwitcherIdx(0)
      setModelSwitcherError(null)
      setShowModelSwitcher(true)
    }
    window.addEventListener('gokin:open-model-switcher', handler)
    return () => window.removeEventListener('gokin:open-model-switcher', handler)
  }, [])

  // Load user snippets on mount + on a refresh event (fired by Settings
  // page after Save/Delete). Failures are logged but otherwise silent —
  // a missing/corrupt file shouldn't break chat.
  useEffect(() => {
    const refresh = () => {
      ListUserSnippets().then((list: any) => {
        setUserSnippets((list || []).map((s: any) => ({ id: s.id, name: s.name, body: s.body })))
      }).catch((e: any) => console.warn('ListUserSnippets failed:', e))
    }
    refresh()
    window.addEventListener('gokin:snippets-changed', refresh)
    return () => window.removeEventListener('gokin:snippets-changed', refresh)
  }, [])

  // Load project file list once per project switch for the @path
  // autocomplete. The walker is fast (< 100ms even for medium repos) but
  // we still cache to avoid a fresh RPC on every keystroke.
  useEffect(() => {
    if (!activeProjectId) { setProjectFiles([]); return }
    let cancelled = false
    ListProjectFiles(activeProjectId).then((list: any) => {
      if (cancelled) return
      setProjectFiles(list || [])
    }).catch((e: any) => {
      if (cancelled) return
      console.warn('ListProjectFiles failed:', e)
      setProjectFiles([])
    })
    return () => { cancelled = true }
  }, [activeProjectId])

  const executeSlashCommand = (cmd: string, arg?: string) => {
    // Snippet expansion: insert the snippet's body into the input rather
    // than executing an action. Don't blank the input first — instead,
    // replace the slash command with the body so the cursor is at the end
    // of the body and the user can immediately keep typing or press Enter.
    const snippet = SLASH_COMMANDS.find((c) => c.cmd === cmd && (c as any).snippet)
    if (snippet && (snippet as any).body) {
      setInput((snippet as any).body)
      // Place cursor at end after React commits the new value.
      requestAnimationFrame(() => {
        const el = inputRef.current
        if (el) el.setSelectionRange(el.value.length, el.value.length)
      })
      return
    }
    setInput('')
    switch (cmd) {
      case '/clear': setConfirmClear(true); break
      case '/export': handleExport(); break
      case '/exportall': handleExportAll(); break
      case '/summarize': handleSummarize(); break
      case '/system': setSysPromptDraft(activeProject.systemPrompt || ''); setSysPromptError(null); setShowSysPrompt(true); break
      case '/search':
        setShowSearch(true)
        if (arg) setSearchQuery(arg)
        break
      case '/memory': openMemoryViewer(); break
      case '/budget':
        setBudgetDraft(activeProject?.budgetUSD ? String(activeProject.budgetUSD) : '')
        setBudgetEnforceDraft(!!activeProject?.enforceBudget)
        setBudgetError(null)
        setShowBudget(true)
        break
      case '/help':
        setHelpQuery('')
        setShowHelp(true)
        break
      case '/sessions':
        openSessionsManager()
        break
      case '/exportjson':
        handleExportJSON()
        break
      case '/importsession':
        setImportDraft('')
        setImportError(null)
        setShowImport(true)
        break
    }
  }

  // Bulk session manager opener: lazy-loads the session list (cheaper than
  // pre-fetching on every chat panel render) + resets all transient state.
  const openSessionsManager = () => {
    setShowSessionsMgr(true)
    setSessionsMgrList(null)
    setSessionsMgrSelected(new Set())
    setSessionsMgrConfirm(false)
    setSessionsMgrError(null)
    if (!activeProjectId) return
    ListChatSessions(activeProjectId).then((list: any) => {
      setSessionsMgrList(list || [])
    }).catch((e: any) => {
      setSessionsMgrError(`Failed to load sessions: ${String(e?.message || e)}`)
      setSessionsMgrList([])
    })
  }

  const handleSend = async () => {
    const text = input.trim()
    if (!text || thisSessionActive || !canSend || sending) return

    // Reset history recall on send so the next Up arrow starts fresh from
    // the most recent message (which now includes what we just sent).
    setHistoryIdx(-1)
    savedDraftRef.current = null

    // Handle slash commands (case-insensitive, trimmed).
    // Support "/search <query>" with an optional argument after the command.
    const lower = text.toLowerCase()
    const spaceIdx = text.indexOf(' ')
    const cmdPart = spaceIdx >= 0 ? lower.slice(0, spaceIdx) : lower
    const argPart = spaceIdx >= 0 ? text.slice(spaceIdx + 1).trim() : undefined
    const slashCmd = SLASH_COMMANDS.find((c) => c.cmd === cmdPart)
    if (slashCmd) {
      executeSlashCommand(slashCmd.cmd, argPart)
      return
    }

    setSending(true)
    setInput('')
    setDraft(chatKey, '')
    // Starting a new turn truncates the replay buffer server-side, so any
    // pending recovery banner refers to events that no longer exist.
    if (recoveryEvents) {
      DiscardRecoveryEvents(activeProjectId, currentSessionId).catch(() => {})
      setRecoveryEvents(null)
    }
    // Reset textarea height immediately — the useLayoutEffect will also run
    // but this avoids any transient "tall empty textarea" frame.
    if (inputRef.current) {
      inputRef.current.style.height = ''
    }
    addUserMessage(chatKey, text)
    // Force auto-scroll to resume when user sends — they want to see the new exchange.
    userScrolledUpRef.current = false
    setShowScrollBtn(false)
    requestAnimationFrame(() => {
      messagesEndRef.current?.scrollIntoView({ behavior: 'auto' as ScrollBehavior, block: 'end' })
    })
    try {
      const expanded = await expandFileRefs(text, activeProjectId)
      await SendMessage(activeProjectId, expanded, currentSessionId)
    } catch (e: any) {
      console.error('SendMessage error:', e)
      // Restore the draft so the user doesn't lose their text on a transport
      // failure (e.g. binding not ready, agent already running). Surface the
      // error as an assistant error-card so they see what went wrong.
      setInput(text)
      useChatStore.getState().finalizeAssistant(chatKey, 'Error: ' + String(e?.message || e))
    } finally {
      setSending(false)
      // Return focus to input for rapid follow-up messages.
      inputRef.current?.focus()
    }
  }

  const handleStop = async () => {
    try {
      await StopGeneration(activeProjectId, currentSessionId)
    } catch (e: any) {
      console.error('StopGeneration error:', e)
      // Surface the failure so the user knows the agent is still running.
      useChatStore.getState().finalizeAssistant(chatKey, `Error: failed to stop — ${String(e?.message || e)}`)
    }
  }

  const handleClear = () => {
    setConfirmClear(true)
  }

  const doClear = async () => {
    setConfirmClear(false)
    try {
      await StopGeneration(activeProjectId, currentSessionId).catch(() => {})
      await ClearHistory(activeProjectId, currentSessionId)
      clearChat(chatKey)
      setInput('')
    } catch (e: any) {
      console.error('ClearHistory error:', e)
      // History may not have been cleared on the backend — surface the error
      // so the user doesn't think /clear silently succeeded.
      useChatStore.getState().finalizeAssistant(chatKey, `Error: failed to clear history — ${String(e?.message || e)}`)
    }
  }

  const handleExport = async () => {
    try {
      const md = await ExportChat(activeProjectId, currentSessionId)
      const blob = new Blob([md], { type: 'text/markdown' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      // Include session name in export filename so multiple exports from the
      // same project don't collide. Use the human-readable name when available,
      // falling back to the session ID for the 'default' session.
      const nameTag = sessionName && sessionName !== 'Chat 1' ? `-${sessionName.replace(/[^a-zA-Z0-9_\-]/g, '_')}` : currentSessionId === 'default' ? '' : `-${currentSessionId.slice(0, 8)}`
      a.download = `${activeProject.name}${nameTag}-chat.md`
      a.click()
      URL.revokeObjectURL(url)
    } catch (e: any) {
      console.error('ExportChat error:', e)
      useChatStore.getState().finalizeAssistant(chatKey, `Error: failed to export — ${String(e?.message || e)}`)
    }
  }

  // Export the current session as a JSON file for backup or sharing (iter 550+).
  // Uses the same Blob+download flow as the Markdown export so the user gets
  // a real file in their Downloads folder.
  const handleExportJSON = async () => {
    try {
      const json = await ExportSessionJSON(activeProjectId, currentSessionId)
      const blob = new Blob([json], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      const nameTag = sessionName && sessionName !== 'Chat 1' ? `-${sessionName.replace(/[^a-zA-Z0-9_\-]/g, '_')}` : currentSessionId === 'default' ? '' : `-${currentSessionId.slice(0, 8)}`
      a.download = `${activeProject.name}${nameTag}-session.json`
      a.click()
      URL.revokeObjectURL(url)
    } catch (e: any) {
      console.error('ExportSessionJSON error:', e)
      useChatStore.getState().finalizeAssistant(chatKey, `Error: failed to export session — ${String(e?.message || e)}`)
    }
  }

  const handleSummarize = async () => {
    setShowSummary(true)
    if (summaryText) return // already loaded; just reopen the modal
    setSummaryLoading(true)
    setSummaryError(null)
    try {
      const text: any = await SummarizeSession(activeProjectId, currentSessionId)
      setSummaryText(String(text || ''))
    } catch (e: any) {
      setSummaryError(String(e?.message || e || 'summary failed'))
    } finally {
      setSummaryLoading(false)
    }
  }

  const handleExportAll = async () => {
    try {
      const md = await ExportProjectAllSessions(activeProjectId)
      const blob = new Blob([md], { type: 'text/markdown' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      const datestamp = new Date().toISOString().slice(0, 10) // YYYY-MM-DD
      a.download = `${activeProject.name}-all-sessions-${datestamp}.md`
      a.click()
      URL.revokeObjectURL(url)
    } catch (e: any) {
      console.error('ExportProjectAllSessions error:', e)
      useChatStore.getState().finalizeAssistant(chatKey, `Error: failed to export all sessions — ${String(e?.message || e)}`)
    }
  }



  const handleKeyDown = (e: React.KeyboardEvent) => {
    // Don't hijack Enter that terminates an IME composition (Russian dead-
    // keys, CJK commit, etc.) — it must reach the textarea so the composed
    // character is inserted instead of sending a message.
    if ((e as any).isComposing || (e.nativeEvent as any)?.isComposing || e.keyCode === 229) return

    // @path autocomplete popup keyboard nav. Takes precedence over the
    // slash-command popup — they can't be open simultaneously since one
    // triggers on `/` at start of input and the other on `@` mid-input.
    if (atMention && atMentionMatches.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setAtMentionIdx((i) => (i + 1) % atMentionMatches.length)
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setAtMentionIdx((i) => (i - 1 + atMentionMatches.length) % atMentionMatches.length)
        return
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault()
        applyAtMention(atMentionMatches[atMentionIdx])
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setAtMention(null)
        return
      }
    }

    // Slash-command popup keyboard navigation.
    if (slashMatches.length > 0) {
      // ArrowDown cycles forward; ArrowUp cycles back. Tab is intentionally NOT
      // included here — it should keep its native browser focus-movement role.
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setSlashIdx((i) => (i + 1) % slashMatches.length)
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setSlashIdx((i) => (i - 1 + slashMatches.length) % slashMatches.length)
        return
      }
      if (e.key === 'Enter') {
        e.preventDefault()
        if (slashIdx >= 0) {
          // Apply the highlighted suggestion.
          executeSlashCommand(slashMatches[slashIdx].cmd)
        } else if (slashMatches.length === 1) {
          // Only one match — apply it without requiring an explicit selection.
          executeSlashCommand(slashMatches[0].cmd)
        }
        // Multiple matches and nothing selected: block the send so the bare
        // "/"-prefix text doesn't get sent as a message to the agent.
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setInput('')
        return
      }
    }

    // History recall (terminal-style). Up/Down walk through previous user
    // messages. Only fires when we're not in the middle of a multi-line edit
    // (i.e. input has no newlines) AND either the input is empty OR we're
    // already in recall mode. The "no newlines" gate keeps Up/Down free for
    // cursor movement inside a multi-line draft.
    const noNewline = !input.includes('\n')
    const recallActive = historyIdx >= 0
    const canRecall = noNewline && (input === '' || recallActive)
    if (canRecall && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
      // Build the user-only history once per keypress. Filter to text-only
      // user messages — function-response synthetic user turns have no text
      // and shouldn't be recalled. Reverse so index 0 = most recent.
      const userMsgs: string[] = []
      for (let i = messages.length - 1; i >= 0; i--) {
        const m = messages[i]
        if (m.role === 'user' && (m.content || '').trim() !== '') {
          userMsgs.push(m.content)
        }
      }
      if (userMsgs.length === 0) return
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        if (!recallActive) {
          // Save what was being drafted (likely empty) so Down can restore.
          savedDraftRef.current = input
        }
        const next = Math.min(userMsgs.length - 1, historyIdx + 1)
        setHistoryIdx(next)
        setInput(userMsgs[next])
        // Place cursor at end after React commits the new value.
        requestAnimationFrame(() => {
          const el = inputRef.current
          if (el) el.setSelectionRange(el.value.length, el.value.length)
        })
        return
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        if (!recallActive) return // not recalling — let Down do its native job
        const next = historyIdx - 1
        if (next < 0) {
          // Walked past the most recent — restore the saved draft.
          setInput(savedDraftRef.current ?? '')
          setHistoryIdx(-1)
          savedDraftRef.current = null
        } else {
          setHistoryIdx(next)
          setInput(userMsgs[next])
        }
        requestAnimationFrame(() => {
          const el = inputRef.current
          if (el) el.setSelectionRange(el.value.length, el.value.length)
        })
        return
      }
    }
    // Any other key while recalling cancels recall (so the user can edit
    // the recalled message freely without surprise behavior on the next Up).
    if (recallActive && e.key !== 'ArrowUp' && e.key !== 'ArrowDown' && e.key !== 'Shift' && e.key !== 'Control' && e.key !== 'Meta' && e.key !== 'Alt') {
      setHistoryIdx(-1)
      savedDraftRef.current = null
      // Don't return — let the original handler process this keystroke.
    }

    if (e.key === 'Enter') {
      if (e.ctrlKey || e.metaKey || e.shiftKey) {
        // Ctrl+Enter or Shift+Enter = new line (default textarea behavior)
        return
      }
      e.preventDefault()
      handleSend()
    }
  }

  // Show "Thinking..." when project is active but no streaming text and no tool calls pending
  // "Agent is thinking…" placeholder is only useful before any signal of work:
  // if the last message is a tool_call/result or thinking chip, we already have
  // visible activity and the placeholder just crowds the bottom of the chat.
  const lastMsg = messages.length > 0 ? messages[messages.length - 1] : null
  const recentActivity = lastMsg && (lastMsg.role === 'tool' || lastMsg.role === 'thinking')
  const isThinking = thisSessionActive && !streamingText && !recentActivity

  const shortPath = (() => {
    const dir = activeProject.directory
    for (const prefix of ['/Users/', '/home/']) {
      if (dir.startsWith(prefix)) {
        const rest = dir.slice(prefix.length)
        const slash = rest.indexOf('/')
        if (slash !== -1) return '~' + rest.slice(slash)
      }
    }
    return dir
  })()

  // Prefer real token counts from the provider (input_tokens from the most recent
  // LLM round) over the char/4 estimate. This is what the model actually saw in
  // its context window — NOT the per-turn total (that'd double-count history
  // across multiple tool-use rounds).
  const contextWindow = activeProject.contextWindow || 128000
  const providerInputTokens = (liveUsage?.lastInputTokens ?? 0) || (lastTurnUsage?.lastInputTokens ?? 0)
  const estimatedTokens = useMemo(() => {
    if (providerInputTokens > 0) return providerInputTokens
    // Pre-first-response fallback: estimate from visible history chars.
    let chars = streamingText.length
    for (const m of messages) {
      chars += (m.content || '').length
    }
    return Math.round(chars / 4)
  }, [messages, streamingText, providerInputTokens])
  const usingProviderTokens = providerInputTokens > 0
  const contextPct = Math.min(100, (estimatedTokens / contextWindow) * 100)
  const showContextWarning = contextPct > 75

  // Total estimated cost for the session: sum of every completed assistant
  // turn's `estimatedCostUSD`. Cheap recomputation on messages-changed; the
  // header chip updates as new turns finish.
  const sessionCostUSD = useMemo(() => {
    let total = 0
    for (const m of messages) {
      if (m.role !== 'assistant') continue
      const c = m.usage?.estimatedCostUSD
      if (typeof c === 'number') total += c
    }
    return total
  }, [messages])

  // Activity-timeline summary: aggregates every tool message in the current
  // session for the modal. Cheap to recompute; only re-runs when the
  // messages array changes.
  const activityStats = useMemo(() => {
    const items = messages.filter((m) => m.role === 'tool')
    const byTool = new Map<string, { count: number; success: number; fail: number; pending: number }>()
    let oks = 0, fails = 0, pending = 0
    for (const m of items) {
      const tn = m.toolName || 'unknown'
      const cur = byTool.get(tn) || { count: 0, success: 0, fail: 0, pending: 0 }
      cur.count++
      if (m.toolSuccess === true) { cur.success++; oks++ }
      else if (m.toolSuccess === false) { cur.fail++; fails++ }
      else { cur.pending++; pending++ }
      byTool.set(tn, cur)
    }
    // Sort tool types by count desc for the breakdown bar.
    const sortedTools = Array.from(byTool.entries())
      .map(([name, s]) => ({ name, ...s }))
      .sort((a, b) => b.count - a.count)
    // Estimate session start from the first message timestamp; fall back to
    // now if no messages exist (modal would be empty anyway).
    const startTs = items.length > 0 && items[0].timestamp > 0
      ? items[0].timestamp
      : (messages[0]?.timestamp || Date.now())
    return { items, byTool: sortedTools, total: items.length, oks, fails, pending, startTs }
  }, [messages])

  // Pinned messages: load the persisted list whenever the session changes
  // so the context menu can reflect Pin/Unpin state on first render.
  useEffect(() => {
    if (!activeProjectId || !chatKey) {
      setPinnedKeys(new Set())
      return
    }
    let cancelled = false
    ListPinnedMessages(activeProjectId, currentSessionId).then((pins: any) => {
      if (cancelled) return
      const keys = new Set<string>()
      for (const p of pins || []) {
        keys.add(p.role + ':' + p.content)
      }
      setPinnedKeys(keys)
    }).catch(() => { /* fail silently — pinning is a nice-to-have */ })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeProjectId, currentSessionId, chatKey])

  // Derived per-message data that's expensive to compute. Memoised on
  // `messages` so streaming deltas (which only touch `streaming` / thinking
  // buffers) don't trigger the O(N²) recomputation below. The map + array
  // references stay stable across streaming deltas, which feeds into
  // MessageBubble's React.memo shallow compare.
  const { userIndexFromEnd, changedFilesByMsgId } = useMemo(() => {
    const userIdx = new Map<string, number>()
    let seen = 0
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i]
      if (m.role === 'user') {
        userIdx.set(m.id, seen)
        seen++
      }
    }
    // For each assistant message, collect edit/write tool-call paths that
    // happened since the previous user message (or the start). Keyed by the
    // assistant message ID so MessageBubble can look its own up in O(1).
    const changed = new Map<string, string[]>()
    for (let i = 0; i < messages.length; i++) {
      const msg = messages[i]
      if (msg.role !== 'assistant') continue
      const files: string[] = []
      const already = new Set<string>()
      for (let j = i - 1; j >= 0; j--) {
        const m = messages[j]
        if (m.role === 'user') break
        if (m.role === 'tool' && m.toolSuccess && (m.toolName === 'edit' || m.toolName === 'write')) {
          const path = String((m.toolArgs as any)?.file_path || (m.toolArgs as any)?.path || '')
          if (path && !already.has(path)) {
            already.add(path)
            files.push(path)
          }
        }
      }
      files.reverse()
      if (files.length > 0) changed.set(msg.id, files)
    }
    return { userIndexFromEnd: userIdx, changedFilesByMsgId: changed }
  }, [messages])
  const canEditAny = !thisSessionActive && !sending

  return (
    <div className="chat-panel">
      <div className="chat-header">
        <div className="chat-header-info">
          {/* iter 740+: context strip = breadcrumb (project › branch › model) +
              status chips. Separators rendered via CSS ::before so they only
              appear when the next chunk is present. wireframes.html direction 01:
              "context strip is a single truth: project › branch › model › $" */}
          <span className="chat-header-name">{activeProject.name}</span>
          <span className="chat-header-path" title={activeProject.directory}>{shortPath}</span>
          {activeProject.gitBranch && (
            <span className="chat-header-branch">
              <GitBranch size={10} /> {activeProject.gitBranch}
            </span>
          )}
          {activeProject.model && (
            <span className="chat-header-model" title={`Provider: ${activeProject.provider}\nModel: ${activeProject.model}\nCtrl+M to switch`}>
              {activeProject.model}
            </span>
          )}
          {(activeProject.thinkingMode === 'enabled' || ((!activeProject.thinkingMode) && activeProject.provider === 'kimi')) && (
            <span className="chat-header-thinking" title={`Extended thinking ${activeProject.thinkingMode === 'enabled' ? 'enabled' : 'on by default for Kimi'}${activeProject.thinkingBudget ? ` (${activeProject.thinkingBudget} tokens)` : ''}`}>
              <Brain size={10} /> thinking
            </span>
          )}
          {activeProject.pinnedContext && (
            <span className="chat-header-pin" title={`Pinned context:\n${activeProject.pinnedContext}`}>
              <Pin size={10} />
              <span className="chat-header-pin-label">pinned</span>
              <button
                className="chat-header-pin-clear"
                title="Clear pinned context"
                onClick={async (e) => {
                  e.stopPropagation()
                  try {
                    await ClearPinnedContext(activeProjectId)
                    updateProject(activeProjectId, { pinnedContext: '' })
                  } catch (err: any) {
                    console.error('ClearPinnedContext error:', err)
                    useChatStore.getState().finalizeAssistant(chatKey, `Error: failed to clear pinned context — ${String(err?.message || err)}`)
                  }
                }}
              >
                <X size={9} />
              </button>
            </span>
          )}
          {sessionCostUSD > 0 && (
            <span
              className="chat-header-cost"
              title={
                `Approximate session cost across all completed turns.\n` +
                `Sum of per-turn estimates from the published rate table.\n` +
                `Not authoritative billing — provider rounding, credits, and tier discounts are not applied.`
              }
            >
              ≈{formatCostUSD(sessionCostUSD)}
            </span>
          )}
          {(activeProject.budgetUSD || 0) > 0 && (() => {
            // Live total includes every session's persisted spend + any cost
            // from the current session's last turn (sessionCostUSD covers
            // both persisted and freshly-completed turns since
            // projectTotalCostUSD also includes the latest chat:complete
            // refresh — but on the very first turn after a project switch
            // the refresh races with the cost calc, so prefer the larger
            // of the two to avoid an underreporting flicker).
            const total = Math.max(projectTotalCostUSD, sessionCostUSD)
            const budget = activeProject.budgetUSD || 0
            const pct = (total / budget) * 100
            const cls = pct >= 100 ? 'over' : pct >= 80 ? 'warn' : ''
            return (
              <button
                className={`chat-header-budget ${cls}`}
                title={
                  `Budget: ≈${formatCostUSD(total)} of ${formatCostUSD(budget)} (${pct.toFixed(0)}%)\n` +
                  `Click to update or clear the cap.\n` +
                  `Approximate — based on published rates × token counts.`
                }
                onClick={() => {
                  setBudgetDraft(String(budget))
                  setBudgetEnforceDraft(!!activeProject?.enforceBudget)
                  setBudgetError(null)
                  setShowBudget(true)
                }}
              >
                <DollarSign size={11} />
                {formatCostUSD(total)}/{formatCostUSD(budget)}
                <span className="chat-header-budget-pct">{pct.toFixed(0)}%</span>
              </button>
            )
          })()}
        </div>
        <div className="chat-header-actions">
          {pinnedKeys.size > 0 && (
            <button
              className={`icon-btn ${showPins ? 'active-icon' : ''}`}
              onClick={async () => {
                if (!showPins && activeProjectId) {
                  setPinsError(null)
                  try {
                    const list: any = await ListPinnedMessages(activeProjectId, currentSessionId)
                    setPinnedList(list || [])
                  } catch (e: any) {
                    setPinsError(String(e?.message || e || 'Failed to load pins'))
                    setPinnedList([])
                  }
                }
                setShowPins(!showPins)
              }}
              title={`Pinned messages (${pinnedKeys.size})`}
            >
              <Bookmark size={14} />
            </button>
          )}
          <button
            className={`icon-btn ${showActivity ? 'active-icon' : ''}`}
            onClick={() => setShowActivity((s) => !s)}
            title="Agent activity timeline (Ctrl+Shift+A)"
          >
            <Activity size={14} />
          </button>
          <button
            className={`icon-btn ${showUsageStats ? 'active-icon' : ''}`}
            onClick={async () => {
              if (!showUsageStats && activeProjectId) {
                setUsageStats(null)
                setUsageStatsError(null)
                try {
                  const stats: any = await ProjectUsageStats(activeProjectId)
                  setUsageStats(stats || null)
                } catch (e: any) {
                  setUsageStatsError(String(e?.message || e || 'Failed to load usage stats'))
                }
              }
              setShowUsageStats((s) => !s)
            }}
            title="Project usage statistics (cost / tokens / turns across all sessions)"
          >
            <FileText size={14} />
          </button>
          <button
            className={`icon-btn ${showSearch ? 'active-icon' : ''}`}
            onClick={() => { setShowSearch(!showSearch); if (showSearch) setSearchQuery('') }}
            title="Search (Ctrl+F)"
          >
            <Search size={14} />
          </button>
          <button
            className="icon-btn"
            onClick={() => setShowFilePicker(true)}
            title="Browse project files"
          >
            <FolderTree size={14} />
          </button>
          <button
            className={`icon-btn ${quietMode ? 'active-icon' : ''}`}
            onClick={() => {
              const next = !quietMode
              setQuietMode(next)
              if (quietModeKey) {
                try { localStorage.setItem(quietModeKey, next ? '1' : '0') } catch { /* localStorage unavailable */ }
              }
              if (!next) setExpandedMarkers(new Set())
            }}
            title={quietMode ? 'Quiet mode on — tool calls hidden. Click to show all messages.' : 'Quiet mode — hide tool calls and dispatch/thinking messages so the conversation is easier to follow.'}
          >
            {quietMode ? <EyeOff size={14} /> : <Eye size={14} />}
          </button>
          <button
            className={`icon-btn ${showTerminal ? 'active-icon' : ''}`}
            onClick={() => setShowTerminal(!showTerminal)}
            title={showTerminal ? 'Hide terminal (Ctrl+`)' : 'Terminal (Ctrl+`)'}
          >
            <TerminalSquare size={14} />
          </button>
          <div className="menu-wrap">
            <button
              className={`icon-btn ${showMenu ? 'active-icon' : ''}`}
              onClick={() => setShowMenu(!showMenu)}
              title="More"
            >
              <MoreHorizontal size={14} />
            </button>
            {showMenu && (
              <>
                <div className="menu-backdrop" onClick={() => setShowMenu(false)} />
                <div className="header-menu">
                  <button onClick={() => { setSysPromptDraft(activeProject.systemPrompt || ''); setSysPromptError(null); setShowSysPrompt(true); setShowMenu(false) }}>
                    <FileText size={13} />
                    <span>System prompt</span>
                    {activeProject.systemPrompt && <span className="menu-dot" />}
                  </button>
                  <button onClick={() => { openMemoryViewer(); setShowMenu(false) }}>
                    <Database size={13} />
                    <span>Project memory</span>
                  </button>
                  <button onClick={() => { setShowDispatch(true); setShowMenu(false) }}>
                    <ArrowRightLeft size={13} />
                    <span>Dispatch to another project</span>
                  </button>
                  <button onClick={() => { handleExport(); setShowMenu(false) }}>
                    <Download size={13} />
                    <span>Export as Markdown</span>
                  </button>
                  <button onClick={() => { handleExportAll(); setShowMenu(false) }} title="Export every session in this project as one markdown document">
                    <Download size={13} />
                    <span>Export all sessions</span>
                  </button>
                  <button onClick={() => { handleExportJSON(); setShowMenu(false) }} title="Export this session as JSON (importable elsewhere for backup/sharing)">
                    <Download size={13} />
                    <span>Export as JSON</span>
                  </button>
                  <button onClick={() => { setImportDraft(''); setImportError(null); setShowImport(true); setShowMenu(false) }} title="Import a session from a JSON blob (paste it in the next dialog)">
                    <Download size={13} style={{ transform: 'rotate(180deg)' }} />
                    <span>Import session…</span>
                  </button>
                  <button onClick={() => { handleSummarize(); setShowMenu(false) }} title="Generate a TL;DR of this session via the LLM (costs tokens)">
                    <FileText size={13} />
                    <span>Summarize session</span>
                  </button>
                  <button onClick={() => {
                    setBudgetDraft(activeProject.budgetUSD ? String(activeProject.budgetUSD) : '')
                    setBudgetEnforceDraft(!!activeProject.enforceBudget)
                    setBudgetError(null)
                    setShowBudget(true)
                    setShowMenu(false)
                  }} title="Set a USD spend cap that warns when crossed">
                    <DollarSign size={13} />
                    <span>Set budget</span>
                    {(activeProject.budgetUSD || 0) > 0 && <span className="menu-dot" />}
                  </button>
                  <button onClick={() => { setHelpQuery(''); setShowHelp(true); setShowMenu(false) }} title="View all slash commands and keyboard shortcuts">
                    <MessageSquare size={13} />
                    <span>Help & shortcuts</span>
                    <span className="menu-shortcut">Ctrl+/</span>
                  </button>
                  <button onClick={() => {
                    if (activeProjectId) useChatStore.getState().clearAllUnreadForProject(activeProjectId)
                    setShowMenu(false)
                  }} title="Clear unread badges on every session tab in this project">
                    <CheckCircle size={13} />
                    <span>Mark all sessions as read</span>
                  </button>
                  <button onClick={() => { openSessionsManager(); setShowMenu(false) }} title="View, multi-select, and bulk-delete chat sessions in this project">
                    <ListChecks size={13} />
                    <span>Manage sessions</span>
                  </button>
                  <div className="menu-sep" />
                  <button className="menu-danger" onClick={() => { handleClear(); setShowMenu(false) }}>
                    <Trash2 size={13} />
                    <span>Clear chat</span>
                    <span className="menu-shortcut">Ctrl+L</span>
                  </button>
                </div>
              </>
            )}
          </div>
          {estimatedTokens > 200 && (
            <span
              className={`chat-context ${showContextWarning ? 'warn' : ''}`}
              title={
                usingProviderTokens
                  ? `Context: ${estimatedTokens.toLocaleString()} / ${contextWindow.toLocaleString()} tokens (${contextPct.toFixed(0)}%) — reported by provider`
                  : `Context: ~${estimatedTokens.toLocaleString()} / ${contextWindow.toLocaleString()} tokens (${contextPct.toFixed(0)}%) — estimated (chars÷4), real count appears after first response`
              }
            >
              {usingProviderTokens ? '' : '~'}
              {formatTokens(estimatedTokens)}/{formatTokens(contextWindow)}
            </span>
          )}
          {thisSessionActive ? (
            <span className="chat-generating">
              <Loader2 size={12} className="tool-spinner" />
              <span>Generating</span>
              <span className="chat-elapsed">{formatElapsed(elapsedMs)}</span>
              {(() => {
                // Live output-so-far uses per-turn total (accumulated across
                // every LLM round in this turn), falling back to char/4 of the
                // streaming buffer before the first round finishes reporting.
                const realOut = liveUsage?.totalOutputTokens ?? 0
                const streamEst = Math.round(streamingText.length / 4)
                const outTok = realOut > 0 ? realOut : (streamEst > 50 ? streamEst : 0)
                if (outTok === 0) return null
                return (
                  <span
                    className="chat-elapsed chat-live-tokens"
                    title={realOut > 0 ? 'output tokens reported by provider (this turn total)' : 'output tokens estimated (chars÷4)'}
                  >
                    {realOut > 0 ? '' : '~'}{formatTokens(outTok)} out
                  </span>
                )
              })()}
            </span>
          ) : (
            <span className="chat-model">{activeProject.model || 'glm-5.1'}</span>
          )}
        </div>
      </div>

      {activeProject.directoryOK === false && (
        <div className="chat-warning chat-warning-error">
          <AlertTriangle size={14} />
          <span>
            Project directory is missing: <span className="mono">{activeProject.directory}</span>. Move or re-add the project — tool calls targeting this path will fail.
          </span>
        </div>
      )}

      {missingKey && (
        <div className="chat-warning">
          <AlertTriangle size={14} />
          <span>No API key configured for {missingKey}.</span>
          <button
            className="chat-warning-action"
            onClick={() => window.dispatchEvent(new CustomEvent('gokin:open-settings'))}
          >
            Open Settings
          </button>
        </div>
      )}

      {recoveryEvents && recoveryEvents.length > 0 && (
        <div className="chat-recovery">
          <AlertTriangle size={14} />
          <span className="chat-recovery-text">
            Interrupted turn detected: {summarizeRecovery(recoveryEvents)}
          </span>
          <button
            className="btn-primary-sm"
            onClick={() => {
              // Replay events as messages into the chat so the user sees what
              // was in flight when the session was interrupted. History.json
              // already contains the user turn, so we only add downstream
              // events (tool calls / results / assistant text) as read-only
              // recovered markers, then discard the replay file.
              const store = useChatStore.getState()
              const now = Date.now()
              let addCount = 0
              for (const e of recoveryEvents) {
                if (e.type === 'tool_call') {
                  store.addToolCall(chatKey, e.tool, e.args || {})
                  addCount++
                } else if (e.type === 'tool_result') {
                  store.addToolResult(chatKey, e.tool, e.success === true, e.text || '')
                  addCount++
                } else if (e.type === 'assistant_text') {
                  store.finalizeAssistant(chatKey, e.text || '')
                  addCount++
                } else if (e.type === 'thinking') {
                  // Treat recovered thinking as a thinking message so the chip renders.
                  useChatStore.setState((s) => ({
                    messages: {
                      ...s.messages,
                      [chatKey]: [
                        ...(s.messages[chatKey] || []),
                        { id: `recov-${now}-${addCount}`, role: 'thinking' as const, content: e.text || '', timestamp: e.ts || now },
                      ],
                    },
                  }))
                  addCount++
                }
              }
              DiscardRecoveryEvents(activeProjectId, currentSessionId).catch(() => {})
              setRecoveryEvents(null)
            }}
          >Recover into chat</button>
          <button
            className="btn-cancel-sm"
            onClick={() => {
              DiscardRecoveryEvents(activeProjectId, currentSessionId).catch(() => {})
              setRecoveryEvents(null)
            }}
          >Discard</button>
        </div>
      )}

      {showSysPrompt && (
        <div className="sys-prompt-editor">
          <textarea
            className="sys-prompt-input"
            value={sysPromptDraft}
            onChange={(e) => setSysPromptDraft(e.target.value)}
            placeholder="Enter system prompt for this project..."
            rows={3}
            maxLength={20000}
            autoFocus
            onKeyDown={async (e) => {
              if (e.nativeEvent.isComposing || e.keyCode === 229) return
              if (e.key === 'Escape') { e.preventDefault(); setShowSysPrompt(false); setSysPromptError(null); inputRef.current?.focus() }
              if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                e.preventDefault()
                setSysPromptError(null)
                try {
                  await SetProjectSystemPrompt(activeProjectId, sysPromptDraft)
                  updateProject(activeProjectId, { systemPrompt: sysPromptDraft })
                  setShowSysPrompt(false)
                  inputRef.current?.focus()
                } catch (err: any) {
                  console.error('SetProjectSystemPrompt error:', err)
                  setSysPromptError(String(err?.message || err || 'Failed to save'))
                }
              }
            }}
          />
          {sysPromptError && <div className="sysprompt-error">{sysPromptError}</div>}
          <div className="sys-prompt-actions">
            <button
              className="btn-secondary sys-prompt-templates-btn"
              onClick={async () => {
                if (!sysPromptTemplates) {
                  try {
                    // Load BOTH curated + user-defined templates and merge.
                    // The PromptTemplate shape is identical so the picker
                    // can render them in one categorised list.
                    const [curated, user]: any = await Promise.all([
                      ListPromptTemplates(),
                      ListUserPromptTemplates(),
                    ])
                    setSysPromptTemplates([...(user || []), ...(curated || [])])
                  } catch (e: any) {
                    console.error('ListPromptTemplates error:', e)
                    setSysPromptTemplates([])
                  }
                }
                setShowSysPromptTemplates((s) => !s)
              }}
              title="Pick from curated + your saved presets"
            >
              <FileText size={12} /> Templates
            </button>
            <button
              className="btn-secondary sys-prompt-save-template-btn"
              onClick={async () => {
                if (sysPromptDraft.trim() === '') {
                  setSysPromptError('Cannot save an empty prompt as a template')
                  setTimeout(() => setSysPromptError(null), 3000)
                  return
                }
                const name = window.prompt(
                  'Save the current prompt as a template.\n\nName it (e.g. "React + shadcn", "Embedded Rust"):',
                  ''
                )
                if (!name || !name.trim()) return
                try {
                  await SaveUserPromptTemplate(name.trim(), '', sysPromptDraft)
                  // Force a refresh of the templates list on next open.
                  setSysPromptTemplates(null)
                  setSysPromptError(null)
                } catch (e: any) {
                  console.error('SaveUserPromptTemplate error:', e)
                  setSysPromptError(`Save template failed: ${String(e?.message || e)}`)
                  setTimeout(() => setSysPromptError(null), 4000)
                }
              }}
              title="Save the current draft as a reusable template"
              disabled={sysPromptDraft.trim() === ''}
            >
              <BookmarkPlus size={12} /> Save as…
            </button>
            <span className="dispatch-hint">Ctrl+Enter to save · Esc to cancel</span>
            <button className="btn-secondary" onClick={() => { setShowSysPrompt(false); setShowSysPromptTemplates(false); setSysPromptError(null); inputRef.current?.focus() }}>Cancel</button>
            <button className="btn-primary" onClick={async () => {
              setSysPromptError(null)
              try {
                await SetProjectSystemPrompt(activeProjectId, sysPromptDraft)
                updateProject(activeProjectId, { systemPrompt: sysPromptDraft })
                setShowSysPrompt(false)
                setShowSysPromptTemplates(false)
                inputRef.current?.focus()
              } catch (e: any) {
                console.error('SetProjectSystemPrompt error:', e)
                setSysPromptError(String(e?.message || e || 'Failed to save'))
              }
            }}>Save</button>
          </div>
          {showSysPromptTemplates && sysPromptTemplates && sysPromptTemplates.length > 0 && (
            <div className="sys-prompt-templates">
              <div className="sys-prompt-templates-header">
                Choose a starting point — click to load into the editor (existing draft is replaced).
              </div>
              <div className="sys-prompt-templates-list">
                {(() => {
                  // Group templates by Category for a cleaner picker.
                  const byCat: Record<string, any[]> = {}
                  for (const t of sysPromptTemplates) {
                    if (!byCat[t.category]) byCat[t.category] = []
                    byCat[t.category].push(t)
                  }
                  // "Yours" should appear at the top so the user's own
                  // templates are easiest to reach. Stable order for the
                  // curated categories (Coding, Design, Docs, Ops, Reset).
                  const order = ['Yours', 'Coding', 'Design', 'Docs', 'Ops', 'Reset']
                  const sortedCats = Object.keys(byCat).sort((a, b) => {
                    const ai = order.indexOf(a); const bi = order.indexOf(b)
                    if (ai !== -1 && bi !== -1) return ai - bi
                    if (ai !== -1) return -1
                    if (bi !== -1) return 1
                    return a.localeCompare(b)
                  })
                  return sortedCats.map((cat) => (
                    <div key={cat} className="sys-prompt-templates-cat">
                      <div className="sys-prompt-templates-cat-label">{cat}</div>
                      {byCat[cat].map((t) => (
                        <div key={t.id} className="sys-prompt-template-row">
                          <button
                            className="sys-prompt-template-item"
                            onClick={() => {
                              // Replace the draft. The Save action still requires
                              // an explicit click — picking a template doesn't
                              // overwrite the persisted prompt automatically.
                              setSysPromptDraft(t.prompt)
                              setShowSysPromptTemplates(false)
                            }}
                            title={t.description}
                          >
                            <span className="sys-prompt-template-name">{t.name}</span>
                            <span className="sys-prompt-template-desc">{t.description}</span>
                          </button>
                          {cat === 'Yours' && (
                            <button
                              className="icon-btn sys-prompt-template-delete"
                              title="Delete this saved template"
                              onClick={async (e) => {
                                e.stopPropagation()
                                try {
                                  await DeleteUserPromptTemplate(t.id)
                                  setSysPromptTemplates((prev) => (prev || []).filter((x: any) => x.id !== t.id))
                                } catch (err: any) {
                                  console.error('DeleteUserPromptTemplate error:', err)
                                  setSysPromptError(`Delete template failed: ${String(err?.message || err)}`)
                                  setTimeout(() => setSysPromptError(null), 4000)
                                }
                              }}
                            >
                              <Trash2 size={11} />
                            </button>
                          )}
                        </div>
                      ))}
                    </div>
                  ))
                })()}
              </div>
            </div>
          )}
        </div>
      )}

      {showMemory && (
        <>
          <div className="memory-backdrop" onClick={() => setShowMemory(false)} />
          <div className="memory-modal">
            <div className="memory-header">
              <h3>
                <Database size={16} /> Project memory
                {memoryEntries && <span className="memory-count">{memoryEntries.length}</span>}
              </h3>
              <button className="icon-btn" onClick={async () => {
                setMemoryEntries(null)
                setMemError(null)
                try {
                  const entries = await ListProjectMemory(activeProjectId)
                  setMemoryEntries(entries || [])
                } catch (e: any) {
                  setMemoryEntries([])
                  setMemError(`Failed to load memory: ${String(e?.message || e)}`)
                }
              }} title="Refresh">
                <RotateCcw size={14} />
              </button>
              <button className="icon-btn" onClick={() => setShowMemory(false)} title="Close">
                <X size={16} />
              </button>
            </div>
            {memError && <div className="memory-error">{memError}</div>}
            {memoryEntries === null ? (
              <div className="memory-empty">Loading…</div>
            ) : memoryEntries.length === 0 ? (
              <div className="memory-empty">
                Nothing remembered yet. Ask the agent to note something with a phrase like
                &ldquo;remember that the build command is <span className="mono">make test</span>&rdquo; — it uses the{' '}
                <span className="mono">memory</span> / <span className="mono">memorize</span> tools under the hood.
              </div>
            ) : (
              <div className="memory-list">
                {memoryEntries.map((e) => (
                  <div key={e.id} className="memory-entry">
                    <div className="memory-entry-head">
                      <span className={`memory-type memory-type-${e.type}`}>{e.type}</span>
                      {e.key && <span className="memory-entry-key mono">{e.key}</span>}
                      {e.tags && e.tags.length > 0 && (
                        <span className="memory-entry-tags">
                          {e.tags.map((t: string) => <span key={t} className="memory-tag">{t}</span>)}
                        </span>
                      )}
                      <span className="memory-entry-ts" title={new Date(e.timestamp).toLocaleString()}>
                        {new Date(e.timestamp).toLocaleDateString()}
                      </span>
                      <button
                        className="memory-delete"
                        title="Forget this entry"
                        disabled={deletingMemId === e.id}
                        onClick={async () => {
                          if (deletingMemId) return
                          setDeletingMemId(e.id)
                          try {
                            await DeleteMemoryEntry(activeProjectId, e.id)
                            setMemoryEntries((prev) => (prev || []).filter((x: any) => x.id !== e.id))
                          } catch (err) {
                            console.error('DeleteMemoryEntry error:', err)
                            setMemError('Failed to delete entry')
                            setTimeout(() => setMemError(null), 3000)
                          } finally {
                            setDeletingMemId(null)
                          }
                        }}
                      >
                        <Trash2 size={12} />
                      </button>
                    </div>
                    <div className="memory-entry-content">{e.content}</div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </>
      )}

      {showSummary && (
        <>
          <div className="summary-backdrop" onClick={() => setShowSummary(false)} />
          <div className="summary-modal">
            <div className="summary-header">
              <h3>
                <FileText size={16} /> Session summary
              </h3>
              <button className="icon-btn" onClick={() => setShowSummary(false)} title="Close (Esc)">
                <X size={14} />
              </button>
            </div>
            {summaryLoading && (
              <div className="summary-loading">
                <Loader2 size={16} className="tool-spinner" />
                <span>Generating summary… this calls the LLM and consumes tokens.</span>
              </div>
            )}
            {summaryError && (
              <div className="summary-error">
                <AlertTriangle size={14} /> {summaryError}
                <button
                  className="btn-secondary summary-retry"
                  onClick={() => { setSummaryText(null); handleSummarize() }}
                  title="Try again"
                >
                  Retry
                </button>
              </div>
            )}
            {summaryText && !summaryLoading && (
              <>
                <div className="summary-content md-content">
                  <ReactMarkdown rehypePlugins={MD_REHYPE_PLUGINS} components={mdComponents}>{summaryText}</ReactMarkdown>
                </div>
                <div className="summary-actions">
                  <button
                    className="btn-secondary"
                    onClick={() => {
                      copyToClipboard(summaryText).catch(() => {})
                    }}
                    title="Copy summary to clipboard"
                  >
                    <Copy size={12} /> Copy
                  </button>
                  <button
                    className="btn-secondary"
                    onClick={() => {
                      // Re-run with a fresh fetch.
                      setSummaryText(null)
                      handleSummarize()
                    }}
                    title="Re-generate (consumes tokens again)"
                  >
                    <RotateCcw size={12} /> Re-generate
                  </button>
                </div>
                <div className="summary-footer">
                  Approximate · LLM-generated · Not part of session history.
                </div>
              </>
            )}
          </div>
        </>
      )}

      {showBudget && (
        <>
          <div className="budget-backdrop" onClick={() => { setShowBudget(false); setBudgetError(null) }} />
          <div className="budget-modal">
            <div className="budget-header">
              <h3><DollarSign size={16} /> Project budget</h3>
              <button
                className="icon-btn"
                onClick={() => { setShowBudget(false); setBudgetError(null) }}
                title="Close (Esc)"
              >
                <X size={14} />
              </button>
            </div>
            <div className="budget-body">
              <p className="budget-hint">
                Set a USD spend cap for this project. The chat header chip turns amber at 80% and red at 100%.
                Set 0 to remove the cap. Tracked across all sessions in the project.
              </p>
              <div className="budget-input-row">
                <span className="budget-currency">$</span>
                <input
                  type="number"
                  step="0.01"
                  min="0"
                  max="100000"
                  className="budget-input"
                  value={budgetDraft}
                  autoFocus
                  placeholder="e.g. 50.00"
                  onChange={(e) => { setBudgetDraft(e.target.value); setBudgetError(null) }}
                  onKeyDown={(e) => {
                    if (e.nativeEvent.isComposing || e.keyCode === 229) return
                    if (e.key === 'Escape') { e.preventDefault(); setShowBudget(false); setBudgetError(null); return }
                    if (e.key === 'Enter') { e.preventDefault(); document.getElementById('budget-save-btn')?.click() }
                  }}
                />
              </div>
              {/* iter 1040+: strict mode toggle. Disabled when the budget is
                  empty (clearing the cap also implicitly turns enforcement
                  off — saved as false to match). */}
              <label className="budget-enforce-row">
                <input
                  type="checkbox"
                  checked={budgetEnforceDraft}
                  onChange={(e) => { setBudgetEnforceDraft(e.target.checked); setBudgetError(null) }}
                  disabled={!budgetDraft.trim() || parseFloat(budgetDraft) <= 0}
                />
                <span className="budget-enforce-label">
                  <span>Strict mode</span>
                  <span className="budget-enforce-hint">
                    Block new turns once the budget is reached. Without this, exceeding the budget only triggers warning toasts.
                  </span>
                </span>
              </label>
              {budgetError && <div className="budget-error"><AlertTriangle size={12} /> {budgetError}</div>}
            </div>
            <div className="budget-actions">
              <button
                className="btn-secondary"
                onClick={() => { setShowBudget(false); setBudgetError(null) }}
                disabled={budgetSaving}
              >
                Cancel
              </button>
              <button
                id="budget-save-btn"
                className="btn-primary"
                disabled={budgetSaving}
                onClick={async () => {
                  if (!activeProjectId) return
                  const trimmed = budgetDraft.trim()
                  // Empty string == remove the cap (same as 0). The backend
                  // accepts 0 as "no budget" so we don't need a separate path.
                  const value = trimmed === '' ? 0 : parseFloat(trimmed)
                  if (Number.isNaN(value)) {
                    setBudgetError('Enter a number, or leave blank to clear.')
                    return
                  }
                  if (value < 0) { setBudgetError('Budget cannot be negative.'); return }
                  if (value > 100000) { setBudgetError('Budget cannot exceed $100,000.'); return }
                  setBudgetSaving(true)
                  try {
                    await SetProjectBudget(activeProjectId, value)
                    // iter 1040+: persist strict-mode alongside the budget.
                    // Clearing the cap (value=0) also implicitly clears
                    // strict mode — a no-budget project has nothing to
                    // enforce, and leaving the flag on creates confusing
                    // semantics ("enforced but unbounded").
                    const enforce = value > 0 && budgetEnforceDraft
                    await SetProjectEnforceBudget(activeProjectId, enforce)
                    useProjectStore.getState().updateProject(activeProjectId, {
                      budgetUSD: value,
                      enforceBudget: enforce,
                    })
                    setShowBudget(false)
                    setBudgetError(null)
                  } catch (err: any) {
                    setBudgetError(String(err?.message || err || 'Failed to save budget'))
                  } finally {
                    setBudgetSaving(false)
                  }
                }}
              >
                {budgetSaving ? <><Loader2 size={12} className="tool-spinner" /> Saving…</> : 'Save'}
              </button>
            </div>
          </div>
        </>
      )}

      {showImport && (
        <>
          <div className="import-backdrop" onClick={() => { if (!importBusy) { setShowImport(false); setImportError(null) } }} />
          <div className="import-modal">
            <div className="import-header">
              <h3>Import session from JSON</h3>
              <button className="icon-btn" onClick={() => { if (!importBusy) { setShowImport(false); setImportError(null) } }} title="Close (Esc)">
                <X size={14} />
              </button>
            </div>
            <p className="import-hint">
              Paste a session JSON blob produced by "Export as JSON". The imported session lands in the current project with a fresh ID and "(imported)" suffix.
            </p>
            <textarea
              className="import-textarea"
              placeholder='{"version":1,"name":"…","entries":[…]}'
              value={importDraft}
              autoFocus
              maxLength={5_000_000} /* 5 MB cap matches backend ImportPayloadMaxBytes */
              onChange={(e) => { setImportDraft(e.target.value); setImportError(null) }}
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing || e.keyCode === 229) return
                if (e.key === 'Escape' && !importBusy) { e.preventDefault(); setShowImport(false); setImportError(null) }
                if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); document.getElementById('import-go-btn')?.click() }
              }}
              rows={10}
            />
            {importError && <div className="import-error"><AlertTriangle size={12} /> {importError}</div>}
            <div className="import-actions">
              <span className="import-action-hint">Ctrl+Enter to import · Esc to cancel</span>
              <button className="btn-secondary" onClick={() => { setShowImport(false); setImportError(null) }} disabled={importBusy}>
                Cancel
              </button>
              <button
                id="import-go-btn"
                className="btn-primary"
                disabled={importBusy || !importDraft.trim()}
                onClick={async () => {
                  if (!activeProjectId) return
                  setImportBusy(true)
                  setImportError(null)
                  try {
                    const info: any = await ImportSessionJSON(activeProjectId, importDraft)
                    if (info?.id) {
                      // Switch to the imported session immediately.
                      useChatStore.getState().setActiveSession(activeProjectId, info.id)
                      window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: info.id }))
                      // Reload the App.tsx tab list.
                      window.dispatchEvent(new CustomEvent('gokin:sessions-changed'))
                    }
                    setShowImport(false)
                    setImportDraft('')
                  } catch (e: any) {
                    setImportError(String(e?.message || e || 'Import failed'))
                  } finally {
                    setImportBusy(false)
                  }
                }}
              >
                {importBusy ? <><Loader2 size={12} className="tool-spinner" /> Importing…</> : 'Import'}
              </button>
            </div>
          </div>
        </>
      )}

      {showHelp && (() => {
        // Build the help content as data so we can filter all sections with
        // one query. Each section keeps its own ordering; categories help
        // users skim by intent (Navigate vs. Edit vs. Chat history).
        const slashItems = SLASH_COMMANDS.map((c) => ({ left: c.cmd, right: c.desc }))
        const shortcuts: { group: string; rows: { left: string; right: string }[] }[] = [
          { group: 'Navigate', rows: [
            { left: 'Ctrl+1 / Ctrl+2 / Ctrl+3', right: 'Switch to Chat / Files / Settings' },
            { left: 'Alt+1 … Alt+9', right: 'Jump directly to session N in the tab order' },
            { left: 'Ctrl+B', right: 'Toggle sidebar' },
            { left: 'Ctrl+K', right: 'Command palette' },
            { left: 'Ctrl+P', right: 'Browse project files' },
            { left: 'Ctrl+`', right: 'Toggle integrated terminal' },
            { left: 'Ctrl+T', right: 'New chat session' },
            { left: 'Ctrl+PageUp / PageDown', right: 'Cycle through chat sessions' },
            { left: 'Ctrl+Shift+P', right: 'Search projects in sidebar' },
          ] },
          { group: 'Search', rows: [
            { left: 'Ctrl+F', right: 'Search messages in current session' },
            { left: 'Ctrl+Shift+F', right: 'Search across every session in this project' },
            { left: 'Ctrl+Shift+A', right: 'Agent activity timeline (every tool call this session)' },
          ] },
          { group: 'Chat', rows: [
            { left: 'Enter', right: 'Send message' },
            { left: 'Shift+Enter', right: 'Insert newline' },
            { left: 'Ctrl+Enter', right: 'Send (alternate)' },
            { left: 'Up arrow (empty input)', right: 'Recall previous user message (Down to walk forward, Esc to restore)' },
            { left: 'Ctrl+L', right: 'Clear chat history' },
            { left: 'Escape', right: 'Stop running agent / close any open dialog' },
            { left: 'j / k', right: 'Navigate messages up/down (vim-style)' },
            { left: 'Eye icon in header', right: 'Toggle quiet mode — collapses tool calls into "N hidden" markers (per-project preference)' },
          ] },
          { group: 'Help', rows: [
            { left: 'Ctrl+/  or  ?', right: 'Open this help modal' },
            { left: 'Ctrl+M', right: 'Quick model switcher (filter all provider×model combos)' },
            { left: '/sessions', right: 'Manage sessions (multi-select + bulk delete)' },
          ] },
        ]
        const gestures = [
          { left: 'Right-click message', right: 'Context menu (Pin, Branch from here, Edit user msg)' },
          { left: 'Double-click tab name', right: 'Rename chat session' },
          { left: 'Double-click project name', right: 'Rename project' },
          { left: 'Right-click project', right: 'Pin/unpin, mute notifications, rename, export, delete' },
          { left: 'Drag file → chat', right: 'Attach file content as fenced block' },
          { left: 'Type "@" in chat', right: 'Inline file picker — type to filter, ↑↓ Enter to insert' },
          { left: 'Click pinned-context badge ×', right: 'Clear pinned context' },
          { left: 'Click budget chip', right: 'Edit project budget' },
        ]
        const q = helpQuery.trim().toLowerCase()
        const filterRows = (rows: { left: string; right: string }[]) =>
          q === '' ? rows : rows.filter((r) => r.left.toLowerCase().includes(q) || r.right.toLowerCase().includes(q))
        const slashFiltered = filterRows(slashItems)
        const shortcutsFiltered = shortcuts.map((s) => ({ ...s, rows: filterRows(s.rows) })).filter((s) => s.rows.length > 0)
        const gesturesFiltered = filterRows(gestures)
        const totalShown = slashFiltered.length + shortcutsFiltered.reduce((a, s) => a + s.rows.length, 0) + gesturesFiltered.length
        return (
          <>
            <div className="help-backdrop" onClick={() => { setShowHelp(false); setHelpQuery('') }} />
            <div className="help-modal">
              <div className="help-header">
                <h3><MessageSquare size={16} /> Help & shortcuts</h3>
                <button className="icon-btn" onClick={() => { setShowHelp(false); setHelpQuery('') }} title="Close (Esc)">
                  <X size={14} />
                </button>
              </div>
              <div className="help-search-row">
                <Search size={12} className="help-search-icon" />
                <input
                  type="text"
                  className="help-search-input"
                  placeholder="Filter… (e.g. 'fork', 'ctrl+', 'memory')"
                  value={helpQuery}
                  autoFocus
                  maxLength={100}
                  onChange={(e) => setHelpQuery(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.nativeEvent.isComposing || e.keyCode === 229) return
                    if (e.key === 'Escape') { e.preventDefault(); if (helpQuery) { setHelpQuery('') } else { setShowHelp(false) } }
                  }}
                />
                {helpQuery && (
                  <button className="help-search-clear" onClick={() => setHelpQuery('')} title="Clear filter">
                    <X size={11} />
                  </button>
                )}
              </div>
              <div className="help-body">
                {totalShown === 0 && (
                  <div className="help-empty">No matches for "{helpQuery}".</div>
                )}
                {slashFiltered.length > 0 && (
                  <div className="help-section">
                    <h4 className="help-section-title">Slash commands</h4>
                    <div className="help-rows">
                      {slashFiltered.map((r) => (
                        <div className="help-row" key={r.left}>
                          <span className="help-row-left mono">{r.left}</span>
                          <span className="help-row-right">{r.right}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
                {shortcutsFiltered.map((sec) => (
                  <div className="help-section" key={sec.group}>
                    <h4 className="help-section-title">{sec.group}</h4>
                    <div className="help-rows">
                      {sec.rows.map((r) => (
                        <div className="help-row" key={r.left}>
                          <span className="help-row-left mono">{r.left}</span>
                          <span className="help-row-right">{r.right}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                ))}
                {gesturesFiltered.length > 0 && (
                  <div className="help-section">
                    <h4 className="help-section-title">Mouse / gestures</h4>
                    <div className="help-rows">
                      {gesturesFiltered.map((r) => (
                        <div className="help-row" key={r.left}>
                          <span className="help-row-left">{r.left}</span>
                          <span className="help-row-right">{r.right}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
              <div className="help-footer">
                Press <span className="mono">Esc</span> to close · <span className="mono">/help</span> reopens · settings has the official shortcuts table
              </div>
            </div>
          </>
        )
      })()}

      {showModelSwitcher && (() => {
        // Build the flat list of provider×model options once per render.
        // Display label includes provider name + model so users can search
        // by either ("kimi", "5.1", "ollama qwen", etc.). Skip providers
        // with no models registered (e.g. ollama before any local pulls).
        const opts: { provider: string; model: string; label: string; isCurrent: boolean }[] = []
        const currentProv = activeProject?.provider || ''
        const currentMod = activeProject?.model || ''
        for (const p of providers) {
          for (const m of (p.models || [])) {
            opts.push({
              provider: p.id,
              model: m,
              label: `${p.name} · ${m}`,
              isCurrent: p.id === currentProv && m === currentMod,
            })
          }
        }
        const q = modelSwitcherQuery.trim().toLowerCase()
        const filtered = q === '' ? opts : opts.filter((o) => o.label.toLowerCase().includes(q))
        const safeIdx = filtered.length === 0 ? -1 : Math.min(modelSwitcherIdx, filtered.length - 1)
        const apply = async (opt: { provider: string; model: string }) => {
          if (!activeProjectId || modelSwitcherSaving) return
          setModelSwitcherSaving(true)
          setModelSwitcherError(null)
          try {
            await SetProjectProvider(activeProjectId, opt.provider, opt.model)
            useProjectStore.getState().updateProject(activeProjectId, { provider: opt.provider, model: opt.model })
            // Refresh ProjectInfo so contextWindow updates for the new model.
            try {
              const info: any = await GetProject(activeProjectId)
              if (info?.contextWindow) {
                useProjectStore.getState().updateProject(activeProjectId, { contextWindow: info.contextWindow })
              }
            } catch { /* non-fatal — keep stale window */ }
            setShowModelSwitcher(false)
            setModelSwitcherQuery('')
          } catch (err: any) {
            setModelSwitcherError(String(err?.message || err || 'Failed to switch model'))
          } finally {
            setModelSwitcherSaving(false)
          }
        }
        return (
          <>
            <div className="model-switcher-backdrop" onClick={() => { setShowModelSwitcher(false); setModelSwitcherQuery(''); setModelSwitcherError(null) }} />
            <div className="model-switcher-modal" role="dialog" aria-label="Switch model">
              <div className="model-switcher-header">
                <Bot size={14} className="model-switcher-icon" />
                <input
                  type="text"
                  className="model-switcher-input"
                  placeholder={opts.length === 0 ? 'No providers loaded' : 'Switch model… (type to filter)'}
                  value={modelSwitcherQuery}
                  autoFocus
                  maxLength={100}
                  disabled={opts.length === 0}
                  onChange={(e) => { setModelSwitcherQuery(e.target.value); setModelSwitcherIdx(0); setModelSwitcherError(null) }}
                  onKeyDown={(e) => {
                    if (e.nativeEvent.isComposing || e.keyCode === 229) return
                    if (e.key === 'ArrowDown') {
                      e.preventDefault()
                      setModelSwitcherIdx((i) => Math.min(filtered.length - 1, i + 1))
                      return
                    }
                    if (e.key === 'ArrowUp') {
                      e.preventDefault()
                      setModelSwitcherIdx((i) => Math.max(0, i - 1))
                      return
                    }
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      if (safeIdx >= 0) apply(filtered[safeIdx])
                      return
                    }
                    if (e.key === 'Escape') {
                      e.preventDefault()
                      setShowModelSwitcher(false)
                      setModelSwitcherQuery('')
                      setModelSwitcherError(null)
                    }
                  }}
                />
                {modelSwitcherSaving && <Loader2 size={12} className="tool-spinner" />}
                <button className="icon-btn" onClick={() => { setShowModelSwitcher(false); setModelSwitcherQuery(''); setModelSwitcherError(null) }} title="Close (Esc)">
                  <X size={12} />
                </button>
              </div>
              {modelSwitcherError && (
                <div className="model-switcher-error">{modelSwitcherError}</div>
              )}
              <div className="model-switcher-list">
                {filtered.length === 0 ? (
                  <div className="model-switcher-empty">
                    {opts.length === 0
                      ? 'No models registered. Configure providers in Settings.'
                      : `No models match "${modelSwitcherQuery}".`}
                  </div>
                ) : (
                  filtered.map((opt, idx) => (
                    <button
                      key={`${opt.provider}-${opt.model}`}
                      className={`model-switcher-item ${idx === safeIdx ? 'selected' : ''} ${opt.isCurrent ? 'current' : ''}`}
                      onClick={() => apply(opt)}
                      onMouseEnter={() => setModelSwitcherIdx(idx)}
                      disabled={modelSwitcherSaving}
                    >
                      <span className="model-switcher-label">{opt.label}</span>
                      {opt.isCurrent && <span className="model-switcher-current-tag">current</span>}
                    </button>
                  ))
                )}
              </div>
              <div className="model-switcher-footer">
                <span className="model-switcher-hint">↑↓ navigate · Enter apply · Esc close · Ctrl+M reopens</span>
                <span className="model-switcher-count">{filtered.length} of {opts.length}</span>
              </div>
            </div>
          </>
        )
      })()}

      {showSessionsMgr && (() => {
        const list = sessionsMgrList || []
        const selected = sessionsMgrSelected
        const allIds = list.map((s: any) => s.id)
        // Helpers for the bulk-select buttons. "Empty" = no message turns
        // (Messages count is text-only turns from session.Info()).
        const emptyIds = list.filter((s: any) => (s.messages || 0) === 0).map((s: any) => s.id)
        const now = Date.now()
        const day30Ago = now - 30 * 24 * 60 * 60 * 1000
        const day90Ago = now - 90 * 24 * 60 * 60 * 1000
        const olderThan30Ids = list.filter((s: any) => s.lastUsedAt > 0 && s.lastUsedAt < day30Ago).map((s: any) => s.id)
        const olderThan90Ids = list.filter((s: any) => s.lastUsedAt > 0 && s.lastUsedAt < day90Ago).map((s: any) => s.id)
        const setOf = (ids: string[]) => new Set(ids)
        const toggle = (id: string) => {
          const next = new Set(selected)
          if (next.has(id)) next.delete(id); else next.add(id)
          setSessionsMgrSelected(next)
        }
        const close = () => {
          setShowSessionsMgr(false)
          setSessionsMgrConfirm(false)
          setSessionsMgrError(null)
        }
        // Format last-used as a relative age. Keep cheap — recomputed on every
        // render of this modal block, but the modal is rarely open.
        const fmtAge = (ts: number) => {
          if (!ts) return 'never used'
          const diff = now - ts
          if (diff < 60_000) return 'just now'
          if (diff < 3_600_000) return Math.floor(diff / 60_000) + 'm ago'
          if (diff < 86_400_000) return Math.floor(diff / 3_600_000) + 'h ago'
          return Math.floor(diff / 86_400_000) + 'd ago'
        }
        // Refusing to delete the very last session — backend would error
        // anyway, but a UI-side block is more humane than letting the user
        // select-all and then see a partial-success error.
        const wouldDeleteAll = selected.size > 0 && selected.size >= list.length
        const performDelete = async () => {
          if (!activeProjectId || selected.size === 0) return
          setSessionsMgrDeleting(true)
          setSessionsMgrError(null)
          const failed: string[] = []
          for (const id of Array.from(selected)) {
            try {
              await DeleteChatSession(activeProjectId, id)
              useChatStore.getState().dropSession(activeProjectId + '_' + id)
            } catch (e: any) {
              failed.push(`${id}: ${String(e?.message || e)}`)
            }
          }
          // Refresh local list + signal App.tsx to reload its tab list.
          window.dispatchEvent(new CustomEvent('gokin:sessions-changed'))
          ListChatSessions(activeProjectId).then((fresh: any) => {
            setSessionsMgrList(fresh || [])
            setSessionsMgrSelected(new Set())
            setSessionsMgrConfirm(false)
            if (failed.length > 0) {
              setSessionsMgrError(`Could not delete ${failed.length} session${failed.length === 1 ? '' : 's'}: ${failed[0]}`)
            }
          }).catch(() => { /* keep stale list — error already surfaced */ })
          setSessionsMgrDeleting(false)
        }
        return (
          <>
            <div className="sessions-mgr-backdrop" onClick={close} />
            <div className="sessions-mgr-modal">
              <div className="sessions-mgr-header">
                <h3><ListChecks size={16} /> Manage sessions</h3>
                <button className="icon-btn" onClick={close} title="Close (Esc)">
                  <X size={14} />
                </button>
              </div>
              {sessionsMgrError && <div className="sessions-mgr-error">{sessionsMgrError}</div>}
              {sessionsMgrList === null ? (
                <div className="sessions-mgr-empty">Loading…</div>
              ) : list.length === 0 ? (
                <div className="sessions-mgr-empty">No sessions in this project.</div>
              ) : (
                <>
                  <div className="sessions-mgr-toolbar">
                    <button className="sessions-mgr-bulk" onClick={() => setSessionsMgrSelected(setOf(allIds))} disabled={list.length === 0}>
                      Select all ({list.length})
                    </button>
                    <button className="sessions-mgr-bulk" onClick={() => setSessionsMgrSelected(setOf(emptyIds))} disabled={emptyIds.length === 0}>
                      Empty ({emptyIds.length})
                    </button>
                    <button className="sessions-mgr-bulk" onClick={() => setSessionsMgrSelected(setOf(olderThan30Ids))} disabled={olderThan30Ids.length === 0}>
                      &gt; 30 days ({olderThan30Ids.length})
                    </button>
                    <button className="sessions-mgr-bulk" onClick={() => setSessionsMgrSelected(setOf(olderThan90Ids))} disabled={olderThan90Ids.length === 0}>
                      &gt; 90 days ({olderThan90Ids.length})
                    </button>
                    <button className="sessions-mgr-bulk" onClick={() => setSessionsMgrSelected(new Set())} disabled={selected.size === 0}>
                      Clear selection
                    </button>
                  </div>
                  <div className="sessions-mgr-list">
                    {list.map((s: any) => (
                      <label key={s.id} className={`sessions-mgr-row ${selected.has(s.id) ? 'selected' : ''}`}>
                        <input
                          type="checkbox"
                          checked={selected.has(s.id)}
                          onChange={() => toggle(s.id)}
                          disabled={sessionsMgrDeleting}
                        />
                        <div className="sessions-mgr-row-main">
                          <div className="sessions-mgr-row-name">
                            {s.pinned && <Pin size={9} className="sessions-mgr-row-pin" />}
                            {s.name}
                            {s.parentName && (
                              <span className="sessions-mgr-row-parent" title={`Forked from ${s.parentName}`}>↳ {s.parentName}</span>
                            )}
                          </div>
                          <div className="sessions-mgr-row-meta">
                            <span>{s.messages || 0} msg{(s.messages || 0) === 1 ? '' : 's'}</span>
                            <span>·</span>
                            <span>{fmtAge(s.lastUsedAt || 0)}</span>
                          </div>
                        </div>
                      </label>
                    ))}
                  </div>
                </>
              )}
              <div className="sessions-mgr-footer">
                {!sessionsMgrConfirm ? (
                  <>
                    <span className="sessions-mgr-count">{selected.size} selected</span>
                    <button
                      className="btn-secondary"
                      onClick={close}
                    >
                      Close
                    </button>
                    <button
                      className="btn-danger"
                      disabled={selected.size === 0 || sessionsMgrDeleting || wouldDeleteAll}
                      onClick={() => setSessionsMgrConfirm(true)}
                      title={wouldDeleteAll ? 'Cannot delete every session — at least one must remain' : ''}
                    >
                      <Trash2 size={12} /> Delete {selected.size}
                    </button>
                  </>
                ) : (
                  <>
                    <span className="sessions-mgr-confirm-text">
                      Delete {selected.size} session{selected.size === 1 ? '' : 's'}? This is permanent.
                    </span>
                    <button className="btn-secondary" onClick={() => setSessionsMgrConfirm(false)} disabled={sessionsMgrDeleting}>
                      Cancel
                    </button>
                    <button className="btn-danger" onClick={performDelete} disabled={sessionsMgrDeleting}>
                      {sessionsMgrDeleting ? <><Loader2 size={12} className="tool-spinner" /> Deleting…</> : 'Confirm delete'}
                    </button>
                  </>
                )}
              </div>
            </div>
          </>
        )
      })()}

      {showUsageStats && (
        <>
          <div className="usage-stats-backdrop" onClick={() => setShowUsageStats(false)} />
          <div className="usage-stats-modal">
            <div className="usage-stats-header">
              <h3>
                <FileText size={16} /> Project usage
                {usageStats && <span className="usage-stats-count">{usageStats.totalSessions} session{usageStats.totalSessions === 1 ? '' : 's'}</span>}
              </h3>
              <button className="icon-btn" onClick={() => setShowUsageStats(false)} title="Close (Esc)">
                <X size={14} />
              </button>
            </div>
            {usageStatsError && <div className="usage-stats-error">{usageStatsError}</div>}
            {usageStats === null ? (
              <div className="usage-stats-empty">Loading…</div>
            ) : (
              <>
                <div className="usage-stats-totals">
                  <div className="usage-stats-total-cell">
                    <div className="usage-stats-total-label">Total cost (≈)</div>
                    <div className="usage-stats-total-value usage-stats-total-cost">
                      {formatCostUSD(usageStats.totalCostUSD || 0)}
                    </div>
                  </div>
                  <div className="usage-stats-total-cell">
                    <div className="usage-stats-total-label">Input tokens</div>
                    <div className="usage-stats-total-value">{formatTokens(usageStats.totalInputTokens || 0)}</div>
                  </div>
                  <div className="usage-stats-total-cell">
                    <div className="usage-stats-total-label">Output tokens</div>
                    <div className="usage-stats-total-value">{formatTokens(usageStats.totalOutputTokens || 0)}</div>
                  </div>
                  <div className="usage-stats-total-cell">
                    <div className="usage-stats-total-label">Turns</div>
                    <div className="usage-stats-total-value">{usageStats.totalTurns || 0}</div>
                  </div>
                </div>
                {(activeProject.budgetUSD || 0) > 0 && (() => {
                  const total = usageStats.totalCostUSD || 0
                  const budget = activeProject.budgetUSD || 0
                  const pct = (total / budget) * 100
                  const cls = pct >= 100 ? 'over' : pct >= 80 ? 'warn' : ''
                  return (
                    <div className="usage-stats-budget">
                      <div className="usage-stats-budget-row">
                        <span className="usage-stats-budget-label">
                          Budget <span className="usage-stats-budget-value">{formatCostUSD(total)} of {formatCostUSD(budget)}</span>
                        </span>
                        <button
                          className="usage-stats-budget-edit"
                          onClick={() => {
                            setBudgetDraft(String(budget))
                            setBudgetEnforceDraft(!!activeProject?.enforceBudget)
                            setBudgetError(null)
                            setShowBudget(true)
                          }}
                          title="Change or clear the budget"
                        >
                          <Pencil size={11} /> Edit
                        </button>
                      </div>
                      <div className="usage-stats-budget-bar">
                        <div className={`usage-stats-budget-fill ${cls}`} style={{ width: Math.min(100, pct).toFixed(1) + '%' }} />
                      </div>
                      <div className={`usage-stats-budget-pct ${cls}`}>
                        {pct.toFixed(1)}% used
                        {pct >= 100 && ' — over budget'}
                        {pct >= 80 && pct < 100 && ' — approaching cap'}
                      </div>
                    </div>
                  )
                })()}
                {(activeProject.budgetUSD || 0) === 0 && (
                  <div className="usage-stats-budget usage-stats-budget-empty">
                    <span className="usage-stats-budget-hint">No budget set.</span>
                    <button
                      className="usage-stats-budget-edit"
                      onClick={() => {
                        setBudgetDraft('')
                        setBudgetError(null)
                        setShowBudget(true)
                      }}
                      title="Set a USD spend cap"
                    >
                      <DollarSign size={11} /> Set budget
                    </button>
                  </div>
                )}
                {(usageStats.sessions || []).length > 0 ? (
                  <div className="usage-stats-table">
                    <div className="usage-stats-row usage-stats-row-head">
                      <span className="usage-stats-cell-name">Session</span>
                      <span className="usage-stats-cell-num">Cost</span>
                      <span className="usage-stats-cell-num">In</span>
                      <span className="usage-stats-cell-num">Out</span>
                      <span className="usage-stats-cell-num">Turns</span>
                    </div>
                    {usageStats.sessions.map((row: any) => (
                      <div key={row.sessionID} className="usage-stats-row">
                        <span className="usage-stats-cell-name" title={row.sessionName}>{row.sessionName}</span>
                        <span className="usage-stats-cell-num">{row.totalCostUSD > 0 ? '≈' + formatCostUSD(row.totalCostUSD) : '—'}</span>
                        <span className="usage-stats-cell-num">{row.totalInputTokens > 0 ? formatTokens(row.totalInputTokens) : '—'}</span>
                        <span className="usage-stats-cell-num">{row.totalOutputTokens > 0 ? formatTokens(row.totalOutputTokens) : '—'}</span>
                        <span className="usage-stats-cell-num">{row.turnCount > 0 ? row.turnCount : '—'}</span>
                      </div>
                    ))}
                  </div>
                ) : (
                  <div className="usage-stats-empty">No usage recorded yet — run a turn in any session to start tracking.</div>
                )}
                <div className="usage-stats-footer">
                  <span className="usage-stats-hint">Costs are approximate (published rates × token counts). Not authoritative billing.</span>
                  <button
                    className="btn-secondary usage-stats-csv-btn"
                    title="Download per-session usage as CSV"
                    onClick={async () => {
                      if (!activeProjectId || !activeProject) return
                      try {
                        const csv = await ExportProjectUsageCSV(activeProjectId)
                        const blob = new Blob([csv], { type: 'text/csv' })
                        const url = URL.createObjectURL(blob)
                        const a = document.createElement('a')
                        a.href = url
                        const date = new Date().toISOString().slice(0, 10)
                        a.download = `${activeProject.name.replace(/[^a-zA-Z0-9_\-]/g, '_')}-usage-${date}.csv`
                        a.click()
                        URL.revokeObjectURL(url)
                      } catch (e: any) {
                        console.error('ExportProjectUsageCSV failed:', e)
                      }
                    }}
                  >
                    <Download size={12} /> Export CSV
                  </button>
                </div>
              </>
            )}
          </div>
        </>
      )}

      {showActivity && (
        <>
          <div className="activity-backdrop" onClick={() => { setShowActivity(false); setActivityFilter('') }} />
          <div className="activity-modal">
            <div className="activity-header">
              <h3>
                <Activity size={16} /> Agent activity
                {activityStats.total > 0 && <span className="activity-count">{activityStats.total}</span>}
              </h3>
              <button className="icon-btn" onClick={() => { setShowActivity(false); setActivityFilter('') }} title="Close (Esc)">
                <X size={14} />
              </button>
            </div>
            {activityStats.total === 0 ? (
              <div className="activity-empty">
                No tool calls yet in this session. The timeline populates as the agent runs commands, edits files, searches, etc.
              </div>
            ) : (
              <>
                <div className="activity-summary">
                  <div className="activity-summary-row">
                    <span>{activityStats.total} tool call{activityStats.total === 1 ? '' : 's'}</span>
                    {activityStats.oks > 0 && <span className="activity-pill activity-ok">✓ {activityStats.oks}</span>}
                    {activityStats.fails > 0 && <span className="activity-pill activity-fail">✗ {activityStats.fails}</span>}
                    {activityStats.pending > 0 && <span className="activity-pill activity-pending">… {activityStats.pending}</span>}
                  </div>
                  <div className="activity-bytool">
                    {activityStats.byTool.map((b) => (
                      <button
                        key={b.name}
                        className={`activity-tool-pill ${activityFilter === b.name ? 'active' : ''}`}
                        onClick={() => setActivityFilter(activityFilter === b.name ? '' : b.name)}
                        title={`Filter timeline to ${b.name} (${b.count} call${b.count === 1 ? '' : 's'})`}
                      >
                        <span>{b.name}</span>
                        <span className="activity-tool-count">{b.count}</span>
                      </button>
                    ))}
                  </div>
                </div>
                <div className="activity-list">
                  {activityStats.items
                    .filter((m) => !activityFilter || m.toolName === activityFilter)
                    .map((m, idx) => {
                      const elapsedMs = m.timestamp > 0 && activityStats.startTs > 0
                        ? Math.max(0, m.timestamp - activityStats.startTs)
                        : 0
                      const elapsedLabel = elapsedMs > 0 ? `+${formatElapsed(elapsedMs)}` : ''
                      const primary = getToolPrimary(m.toolName || '', m.toolArgs as any)
                      const status = m.toolSuccess === true ? 'ok'
                        : m.toolSuccess === false ? 'fail'
                        : 'pending'
                      return (
                        <button
                          key={`${m.id}-${idx}`}
                          className="activity-item"
                          onClick={() => {
                            const node = document.querySelector(`[data-msg-id="${m.id}"]`) as HTMLElement | null
                            if (node) {
                              node.scrollIntoView({ behavior: 'smooth', block: 'center' })
                              node.classList.add('msg-flash')
                              setTimeout(() => node.classList.remove('msg-flash'), 1500)
                              setShowActivity(false)
                              setActivityFilter('')
                            }
                          }}
                          title="Click to scroll to this tool call in the chat"
                        >
                          <span className={`activity-status activity-status-${status}`}>
                            {status === 'ok' ? <CheckCircle size={11} /> :
                             status === 'fail' ? <XCircle size={11} /> :
                             <Loader2 size={11} className="tool-spinner" />}
                          </span>
                          <span className="activity-tool-name">{m.toolName}</span>
                          {primary && <span className="activity-primary">{shortenForPill(String(primary), 80)}</span>}
                          {elapsedLabel && <span className="activity-elapsed">{elapsedLabel}</span>}
                        </button>
                      )
                    })}
                </div>
                <div className="activity-footer">
                  <span className="activity-hint">Click a row to scroll · Esc to close · Ctrl+Shift+A to toggle</span>
                  {activityFilter && (
                    <button className="btn-cancel-sm" onClick={() => setActivityFilter('')} title="Clear filter">
                      <X size={10} /> {activityFilter}
                    </button>
                  )}
                </div>
              </>
            )}
          </div>
        </>
      )}

      {showPins && (
        <>
          <div className="pins-backdrop" onClick={() => setShowPins(false)} />
          <div className="pins-modal">
            <div className="pins-header">
              <h3><Bookmark size={16} /> Pinned messages {pinnedList && <span className="pins-count">{pinnedList.length}</span>}</h3>
              <button className="icon-btn" onClick={() => setShowPins(false)} title="Close (Esc)">
                <X size={14} />
              </button>
            </div>
            {pinsError && <div className="pins-error">{pinsError}</div>}
            {pinnedList === null ? (
              <div className="pins-empty">Loading…</div>
            ) : pinnedList.length === 0 ? (
              <div className="pins-empty">
                No pinned messages. Right-click a user or assistant message → "Pin message" to bookmark important Q&amp;A you want to find later.
              </div>
            ) : (
              <div className="pins-list">
                {pinnedList.map((p: any) => {
                  const preview = (p.content || '').slice(0, 280)
                  const truncated = (p.content || '').length > 280
                  return (
                    <div key={p.id} className="pin-entry">
                      <div className="pin-entry-head">
                        <span className={`pin-role pin-role-${p.role}`}>{p.role}</span>
                        <span className="pin-ts" title={new Date(p.pinnedAt).toLocaleString()}>
                          {formatMsgTime(p.pinnedAt)}
                        </span>
                        <button
                          className="icon-btn pin-jump"
                          title="Scroll to this message in the chat (best-effort by content match)"
                          onClick={() => {
                            // Find the matching message in the rendered list and scroll to it.
                            // Match by exact (role, content) — same heuristic the pin file uses.
                            const live = useChatStore.getState().messages[chatKey] || []
                            const target = live.find((m) => m.role === p.role && m.content === p.content)
                            if (target) {
                              const node = document.querySelector(`[data-msg-id="${target.id}"]`) as HTMLElement | null
                              if (node) {
                                node.scrollIntoView({ behavior: 'smooth', block: 'center' })
                                node.classList.add('msg-flash')
                                setTimeout(() => node.classList.remove('msg-flash'), 1500)
                                setShowPins(false)
                                return
                              }
                            }
                            // No live match — pin is from a deleted/edited message. Keep modal
                            // open so the user can read the snapshot.
                            setPinsError('Source message no longer in this session — snapshot shown below.')
                            setTimeout(() => setPinsError(null), 4000)
                          }}
                        >
                          <ExternalLink size={12} />
                        </button>
                        <button
                          className="icon-btn pin-remove"
                          title="Remove pin"
                          onClick={async () => {
                            if (!activeProjectId) return
                            try {
                              await UnpinMessage(activeProjectId, currentSessionId, p.id)
                              setPinnedList((prev) => (prev || []).filter((x: any) => x.id !== p.id))
                              setPinnedKeys((prev) => {
                                const n = new Set(prev); n.delete(p.role + ':' + p.content); return n
                              })
                            } catch (e: any) {
                              setPinsError(`Failed to unpin: ${String(e?.message || e)}`)
                              setTimeout(() => setPinsError(null), 3000)
                            }
                          }}
                        >
                          <Trash2 size={12} />
                        </button>
                      </div>
                      <div className="pin-entry-content">
                        {preview}{truncated && '…'}
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </>
      )}

      {showGlobalSearch && (
        <>
          <div className="global-search-backdrop" onClick={() => { setShowGlobalSearch(false); setGlobalQuery(''); setGlobalHits(null); setGlobalSearchError(null) }} />
          <div className="global-search-modal">
            <div className="global-search-header">
              <Search size={14} />
              <input
                className="global-search-input"
                placeholder="Search across all sessions in this project…"
                value={globalQuery}
                autoFocus
                maxLength={200}
                onChange={(e) => setGlobalQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.nativeEvent.isComposing || e.keyCode === 229) return
                  if (e.key === 'Escape') { e.preventDefault(); setShowGlobalSearch(false); setGlobalQuery(''); setGlobalHits(null) }
                }}
              />
              <button className="icon-btn" onClick={() => { setShowGlobalSearch(false); setGlobalQuery(''); setGlobalHits(null); setGlobalSearchError(null) }} title="Close (Esc)">
                <X size={14} />
              </button>
            </div>
            {globalSearchError && <div className="global-search-error">{globalSearchError}</div>}
            {globalSearchLoading && <div className="global-search-empty">Searching…</div>}
            {!globalSearchLoading && globalHits === null && (
              <div className="global-search-empty">Type to search across every session in this project.</div>
            )}
            {!globalSearchLoading && globalHits && globalHits.length === 0 && (
              <div className="global-search-empty">No matches.</div>
            )}
            {!globalSearchLoading && globalHits && globalHits.length > 0 && (
              <div className="global-search-results">
                {globalHits.map((h, i) => {
                  const before = h.snippet.slice(0, h.matchOffset)
                  const matchEnd = Math.min(h.snippet.length, h.matchOffset + globalQuery.length)
                  const matched = h.snippet.slice(h.matchOffset, matchEnd)
                  const after = h.snippet.slice(matchEnd)
                  return (
                    <button
                      key={`${h.sessionID}-${h.messageIdx}-${i}`}
                      className="global-search-result"
                      onClick={() => {
                        if (!activeProjectId) return
                        // Switch to the matched session and remember the
                        // target message so the chat panel can scroll to it
                        // once history is loaded.
                        useChatStore.getState().setActiveSession(activeProjectId, h.sessionID)
                        window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: h.sessionID }))
                        // After a short delay (history load + render), scroll
                        // the matched message into view by data-msg-idx.
                        setTimeout(() => {
                          const nodes = document.querySelectorAll('[data-msg-id]')
                          if (h.messageIdx >= 0 && h.messageIdx < nodes.length) {
                            (nodes[h.messageIdx] as HTMLElement).scrollIntoView({ behavior: 'smooth', block: 'center' });
                            (nodes[h.messageIdx] as HTMLElement).classList.add('msg-flash');
                            setTimeout(() => (nodes[h.messageIdx] as HTMLElement).classList.remove('msg-flash'), 1500)
                          }
                        }, 250)
                        setShowGlobalSearch(false)
                        setGlobalQuery('')
                        setGlobalHits(null)
                      }}
                    >
                      <div className="gs-result-head">
                        <span className={`gs-result-role gs-role-${h.role}`}>{h.role}</span>
                        <span className="gs-result-session">{h.sessionName}</span>
                      </div>
                      <div className="gs-result-snippet">
                        {before}<mark>{matched}</mark>{after}
                      </div>
                    </button>
                  )
                })}
              </div>
            )}
            <div className="global-search-footer">
              <span className="global-search-hint">Esc to close · Click result to jump · Ctrl+Shift+F to toggle</span>
              {globalHits && globalHits.length > 0 && (
                <span className="global-search-count">{globalHits.length} match{globalHits.length === 1 ? '' : 'es'}</span>
              )}
            </div>
          </div>
        </>
      )}

      {showSearch && (
        <div className="chat-search">
          <Search size={14} className="chat-search-icon" />
          <input
            className="chat-search-input"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder="Search messages..."
            autoFocus
            maxLength={500}
          />
          {searchQuery && (
            <span className="chat-search-count">{filteredMessages.length} found</span>
          )}
          <button className="icon-btn" onClick={() => { setShowSearch(false); setSearchQuery('') }}>
            <X size={12} />
          </button>
        </div>
      )}

      <div className="chat-messages" ref={chatContainerRef} onScroll={handleScroll}>
        {filteredMessages.length === 0 && !streamingText && !isThinking && !askUserQ && (
          searchQuery ? (
            <div className="chat-empty">
              <Search size={20} style={{ opacity: 0.3, marginBottom: 8 }} />
              <p>No messages match &ldquo;{searchQuery}&rdquo;</p>
            </div>
          ) : (
            <div className="chat-welcome">
              <div className="chat-welcome-icon">
                <Zap size={28} />
              </div>
              <div className="chat-welcome-title">What can I help with?</div>
              <div className="chat-welcome-hint">
                {projectLang
                  ? <>Detected <span className="welcome-lang">{projectLang}</span> project · try one of these:</>
                  : 'Ask anything about your project. Try one of these:'}
              </div>
              <div className="suggestion-chips">
                {suggestionsForLang(projectLang).map((s) => (
                  <button key={s} className="suggestion-chip" onClick={() => { setInput(s) }}>
                    {s}
                  </button>
                ))}
              </div>
              {gitCtx && gitCtx.isRepo && (
                <div className="welcome-git">
                  <div className="welcome-git-header">
                    <GitBranch size={11} />
                    <span className="welcome-git-branch">{gitCtx.branch || '(no branch)'}</span>
                    {gitCtx.aheadBehind && <span className="welcome-git-ab">{gitCtx.aheadBehind}</span>}
                    {(gitCtx.changedFiles?.length || 0) + (gitCtx.untrackedFiles?.length || 0) === 0 && (
                      <span className="welcome-git-clean">working tree clean</span>
                    )}
                  </div>
                  {((gitCtx.changedFiles?.length || 0) + (gitCtx.untrackedFiles?.length || 0) > 0) && (
                    <>
                      <div className="welcome-git-suggest-row">
                        <button
                          className="suggestion-chip welcome-git-suggest"
                          onClick={() => setInput('Review uncommitted changes and tell me what you see — focus on bugs, edge cases, and missing tests.')}
                        >
                          Review uncommitted changes
                        </button>
                        <button
                          className="suggestion-chip welcome-git-suggest"
                          onClick={() => setInput('Suggest a commit message for the current uncommitted changes (conventional-commits style).')}
                        >
                          Draft a commit message
                        </button>
                      </div>
                      <div className="welcome-git-files">
                        {[...(gitCtx.changedFiles || []), ...(gitCtx.untrackedFiles || [])].slice(0, 8).map((f: any) => (
                          <button
                            key={f.path}
                            className={`welcome-git-file welcome-git-file-${f.status}`}
                            onClick={() => {
                              // Append the file as an @ ref so the user can build a sentence around it.
                              setInput((prev) => (prev ? prev + ' ' : '') + '@' + f.path + ' ')
                              requestAnimationFrame(() => inputRef.current?.focus())
                            }}
                            title={`${f.status} — click to attach as @${f.path}`}
                          >
                            <span className="welcome-git-file-status">{f.status[0].toUpperCase()}</span>
                            <span className="welcome-git-file-path">{f.path}</span>
                          </button>
                        ))}
                        {[...(gitCtx.changedFiles || []), ...(gitCtx.untrackedFiles || [])].length > 8 && (
                          <span className="welcome-git-more">+{[...(gitCtx.changedFiles || []), ...(gitCtx.untrackedFiles || [])].length - 8} more</span>
                        )}
                      </div>
                    </>
                  )}
                  {gitCtx.recentCommits?.length > 0 && (
                    <>
                      <div className="welcome-git-section-label">Recent commits</div>
                      <div className="welcome-git-commits">
                        {gitCtx.recentCommits.slice(0, 5).map((c: any) => (
                          <div key={c.hash} className="welcome-git-commit" title={c.subject}>
                            <span className="mono welcome-git-hash">{c.hash}</span>
                            <span className="welcome-git-subject">{c.subject}</span>
                            {c.age && <span className="welcome-git-age">{c.age}</span>}
                          </div>
                        ))}
                      </div>
                    </>
                  )}
                </div>
              )}
              <div className="chat-welcome-commands">
                <span className="mono">/</span> slash commands · <span className="mono">@path</span> attaches file · <span className="mono">Ctrl+P</span> picker · <span className="mono">Ctrl+K</span> palette · drop a file to attach
              </div>
            </div>
          )
        )}
        {(() => {
          return displayMessages.map((msg: any, i: number) => {
            // Quiet-mode marker: render a one-line collapsed strip with the
            // hidden count + click-to-expand. Bypasses the per-message
            // rendering machinery below, which only applies to real chat
            // messages.
            if (msg.role === 'hidden-marker') {
              return (
                <div className="message-row hidden-marker-row" key={msg.id}>
                  <button
                    className="hidden-marker"
                    onClick={() => {
                      setExpandedMarkers((prev) => {
                        const next = new Set(prev)
                        next.add(msg.id)
                        return next
                      })
                    }}
                    title="Click to show these tool calls"
                  >
                    <Zap size={11} className="hidden-marker-icon" />
                    {msg.count} tool call{msg.count === 1 ? '' : 's'} hidden
                    <span className="hidden-marker-action">show</span>
                  </button>
                </div>
              )
            }
            const changedFiles = changedFilesByMsgId.get(msg.id) ?? EMPTY_ARR
            const idxFromEnd = msg.role === 'user' ? userIndexFromEnd.get(msg.id) ?? -1 : -1
            const canEdit = msg.role === 'user' && idxFromEnd >= 0 && canEditAny
            // Shared submit handler — trims UI from this message onward, calls
            // backend to trim history & re-send. Used by both pencil (edit) and
            // retry (re-run unchanged) buttons.
            const trimAndResend = async (newContent: string) => {
              if (idxFromEnd < 0) return
              // Snapshot the current message list so we can roll back if the
              // backend rejects the edit (agent already running, project gone, etc.)
              // Without this, the UI optimistically shows a trim that the
              // backend never applied, leaving everything inconsistent.
              const beforeMsgs = useChatStore.getState().messages[chatKey] || []
              const cutIdx = beforeMsgs.findIndex((m) => m.id === msg.id)
              if (cutIdx >= 0) {
                useChatStore.getState().setMessages(chatKey, beforeMsgs.slice(0, cutIdx))
              }
              useChatStore.getState().addUserMessage(chatKey, newContent)
              setFocusedMsgId(null)
              userScrolledUpRef.current = false
              setSending(true)
              // Edit/rerun starts a new turn server-side, which truncates the
              // replay buffer; any pending recovery banner becomes stale.
              if (recoveryEvents) {
                DiscardRecoveryEvents(activeProjectId, currentSessionId).catch(() => {})
                setRecoveryEvents(null)
              }
              try {
                await EditUserMessage(activeProjectId, currentSessionId, idxFromEnd, newContent)
              } catch (e: any) {
                console.error('EditUserMessage error:', e)
                // Roll back to the pre-trim state and surface the error so the
                // user knows the edit didn't go through.
                useChatStore.getState().setMessages(chatKey, beforeMsgs)
                useChatStore.getState().finalizeAssistant(chatKey, `Error: ${String(e?.message || e)}`)
              } finally {
                setSending(false)
                inputRef.current?.focus()
              }
            }
            return (
              <MessageBubble
                key={msg.id}
                message={msg}
                changedFiles={changedFiles}
                canEdit={canEdit}
                focused={focusedMsgId === msg.id}
                onEditSubmit={canEdit ? trimAndResend : undefined}
                onRerun={canEdit ? () => trimAndResend(msg.content) : undefined}
                onContextMenu={(e) => {
                  e.preventDefault()
                  e.stopPropagation()
                  setCtxMenu({ msgId: msg.id, x: e.clientX, y: e.clientY })
                  setFocusedMsgId(msg.id)
                }}
              />
            )
          })
        })()}
        {thinkingStreamText && (
          <div className="message thinking live-thinking">
            <div className="thinking-chip live">
              <Brain size={12} className="thinking-icon pulse" />
              <span className="thinking-label">
                Reasoning <span className="thinking-count">({thinkingStreamText.split(/\s+/).filter(Boolean).length} words)</span>
              </span>
            </div>
            <div className="thinking-body visible">
              <div className="thinking-text">{thinkingStreamText}<span className="streaming-cursor" /></div>
            </div>
          </div>
        )}
        {streamingText && (
          <div className="message-row assistant">
            <div className="msg-avatar assistant-avatar">
              <Bot size={16} />
            </div>
            <div className="message assistant">
              <div className="message-content streaming">
                <div className="md-content">
                  <ReactMarkdown rehypePlugins={MD_STREAM_PLUGINS} components={mdComponents}>{streamingText}</ReactMarkdown>
                </div>
                <span className="streaming-cursor" />
              </div>
            </div>
          </div>
        )}
        {isThinking && (
          <div className="message-row assistant">
            <div className="msg-avatar assistant-avatar thinking-pulse">
              <Bot size={16} />
            </div>
            <div className="message assistant">
              <div className="message-content thinking-content">
                <div className="thinking-wave">
                  <span /><span /><span /><span /><span />
                </div>
                <span className="thinking-label-text">Agent is thinking...</span>
              </div>
            </div>
          </div>
        )}
        {askUserQ && (
          <AskUserCard
            question={askUserQ}
            onAnswer={async (answer) => {
              // Let errors propagate to AskUserCard so it can show them.
              // Only dismiss after confirmed success.
              await AnswerQuestion(askUserQ.questionID, answer)
              useChatStore.getState().setAskUser(chatKey, null)
            }}
            onCancel={async () => {
              try {
                await CancelQuestion(askUserQ.questionID)
              } catch (e: any) {
                console.error('CancelQuestion error:', e)
              }
              // Always dismiss on cancel regardless of error.
              useChatStore.getState().setAskUser(chatKey, null)
            }}
          />
        )}
        {retryStatus && (
          <div className="retry-banner">
            <Loader2 size={12} className="tool-spinner" />
            <span>
              Retrying after {retryStatus.reason} (attempt {retryStatus.attempt + 1}/{retryStatus.max}{retryCountdown > 0 ? `, in ${retryCountdown}s` : '…'})
            </span>
          </div>
        )}
        <div ref={messagesEndRef} />
      </div>

      {showScrollBtn && (
        <button className="scroll-to-bottom" onClick={scrollToBottom} title="Scroll to bottom">
          <ChevronRight size={16} style={{ transform: 'rotate(90deg)' }} />
        </button>
      )}

      {confirmClear && (
        <div className="confirm-bar">
          <span>Clear all messages?</span>
          <button className="btn-danger-sm" onClick={doClear}>Clear</button>
          <button className="btn-cancel-sm" onClick={() => setConfirmClear(false)}>Cancel</button>
        </div>
      )}

      {slashMatches.length > 0 && (
        <div className="slash-popup">
          {slashMatches.map((c, i) => (
            <button
              key={c.cmd}
              className={`slash-item${i === slashIdx ? ' slash-item-selected' : ''}`}
              onClick={() => executeSlashCommand(c.cmd)}
            >
              <span className="slash-cmd">{c.cmd}</span>
              <span className="slash-desc">{c.desc}</span>
            </button>
          ))}
        </div>
      )}

      <div
        className={`chat-input-area ${draggingFile ? 'dropzone-active' : ''}`}
        onDragOver={(e) => { e.preventDefault(); setDraggingFile(true) }}
        onDragLeave={() => setDraggingFile(false)}
        onDrop={(e) => {
          e.preventDefault()
          setDraggingFile(false)
          const file = e.dataTransfer.files?.[0]
          if (file && (file.type.startsWith('text/') || file.name.match(/\.(ts|tsx|js|jsx|go|py|rs|css|html|json|yaml|yml|md|txt|sh|sql|toml|cfg|env|mod|sum)$/i))) {
            const MAX_READ = 100_000
            const wasLarge = file.size > MAX_READ
            // Slice before reading so FileReader never buffers more than 100 KB,
            // even for large files (e.g. generated lock files, log dumps).
            const blob = wasLarge ? file.slice(0, MAX_READ) : file
            const reader = new FileReader()
            reader.onload = () => {
              const content = reader.result as string
              const truncated = wasLarge ? content + '\n[truncated]' : content
              setInput((prev) => prev + (prev ? '\n\n' : '') + `File: ${file.name}\n\`\`\`\n${truncated}\n\`\`\``)
            }
            reader.onerror = () => {
              console.error('Failed to read dropped file:', reader.error)
              setInput((prev) => prev + (prev ? '\n\n' : '') + `[Error: could not read ${file.name} — ${reader.error?.message || 'unknown error'}]`)
            }
            reader.readAsText(blob)
          }
        }}
      >
        {draggingFile && <div className="dropzone-overlay">Drop file to attach</div>}
        {atMention && atMentionMatches.length > 0 && (
          <div className="atmention-popup">
            <div className="atmention-header">
              <span className="atmention-title">Files matching "{atMention.query || '*'}"</span>
              <span className="atmention-hint">↑↓ Enter Esc</span>
            </div>
            <div className="atmention-list">
              {atMentionMatches.map((p, idx) => (
                <button
                  key={p}
                  className={`atmention-item ${idx === atMentionIdx ? 'selected' : ''}`}
                  onClick={() => applyAtMention(p)}
                  onMouseEnter={() => setAtMentionIdx(idx)}
                >
                  <FolderTree size={11} className="atmention-icon" />
                  <span className="atmention-path">{p}</span>
                </button>
              ))}
            </div>
          </div>
        )}
        <div className="chat-input-wrapper">
          <textarea
            ref={inputRef}
            className="chat-input"
            value={input}
            onChange={(e) => {
              const newVal = e.target.value
              setInput(newVal)
              // Detect an active @<query> at the cursor position. We scan
              // back from the cursor: if we hit whitespace or start-of-string
              // with no special chars in between (other than path-friendly
              // characters), we have a candidate. Closing happens on space,
              // newline, or when the @ is no longer reachable.
              const cur = e.target.selectionStart ?? newVal.length
              let i = cur - 1
              while (i >= 0) {
                const ch = newVal[i]
                if (ch === '@') break
                // path-friendly chars only — anything else aborts
                if (ch === ' ' || ch === '\n' || ch === '\t' || ch === ',' || ch === ';' || ch === '(' || ch === ')') {
                  i = -2 // sentinel: not in @-context
                  break
                }
                i--
              }
              if (i < 0) {
                if (atMention) setAtMention(null)
                return
              }
              // i points to '@'. Verify that @ is at start-of-input or
              // preceded by whitespace (avoid matching email-style addr@host).
              if (i > 0) {
                const prev = newVal[i - 1]
                if (prev !== ' ' && prev !== '\n' && prev !== '\t') {
                  if (atMention) setAtMention(null)
                  return
                }
              }
              const query = newVal.slice(i + 1, cur)
              setAtMention({ query, start: i })
              setAtMentionIdx(0)
            }}
            onKeyDown={handleKeyDown}
            placeholder={
              !dirOK
                ? 'Project directory is missing — fix or re-add the project before sending'
                : !canSend
                ? `Configure ${missingKey} API key in Settings first`
                : 'Ask anything about your project...'
            }
            rows={1}
            // Cap a runaway paste at 100K chars so the textarea stays responsive
            // and we don't ship a multi-MB blob through the Wails bridge.
            // Normal messages are <1KB; even a long pasted file fits comfortably.
            maxLength={100000}
            disabled={thisSessionActive || !canSend}
          />
          {thisSessionActive ? (
            <button className="chat-input-send stop" onClick={handleStop} title="Stop generation">
              <Square size={14} />
            </button>
          ) : (
            <button
              className="chat-input-send"
              onClick={handleSend}
              disabled={!input.trim() || !canSend || sending}
              title={!canSend ? 'API key required' : 'Send (Enter)'}
            >
              <Send size={14} />
            </button>
          )}
        </div>
        <div className="chat-input-footer">
          {/* iter 1050+: cost preview alongside chars/tokens. tokens × inputRate
              gives an UPPER BOUND on what this user message ALONE will cost
              (ignoring the existing history, which the model also reprocesses
              every turn). It's a "minimum spend for this send"; the real
              billed cost will be higher once history tokens are added. The
              "≈" prefix and "input only" tooltip make this explicit. */}
          {(() => {
            if (input.length === 0) {
              return <span className="chat-char-count"></span>
            }
            const tokens = Math.ceil(input.length / 4)
            const cost = inputRateUSDPerMTok > 0 ? (tokens / 1_000_000) * inputRateUSDPerMTok : 0
            return (
              <span className="chat-char-count">
                {input.length.toLocaleString()} chars · ~{tokens.toLocaleString()} tokens
                {cost > 0 && (
                  <span
                    className="chat-cost-preview"
                    title="Lower-bound cost for this message alone — actual cost adds history tokens reprocessed each turn"
                  >
                    {' · '}≈{formatCostUSD(cost)} input
                  </span>
                )}
              </span>
            )
          })()}
          <span className="chat-send-hint">Enter to send · Shift+Enter for new line</span>
        </div>
      </div>

      {showDispatch && (
        <DispatchModal
          fromProjectId={activeProjectId}
          fromSessionId={currentSessionId}
          onClose={() => { setShowDispatch(false); inputRef.current?.focus() }}
        />
      )}

      {ctxMenu && (() => {
        const msg = messages.find((m) => m.id === ctxMenu.msgId)
        if (!msg) return null
        const isUserMsg = msg.role === 'user'
        const userIdx = userIndexForMessage(messages, msg.id)
        const canRerun = isUserMsg && userIdx >= 0 && !thisSessionActive && !sending
        // Forking is also gated on a known user-turn index. We allow forking
        // even while the agent is running in this session — the fork is a
        // copy of state up to the user turn, not a mutation of the current
        // run. The new session is fresh and idle.
        const canFork = isUserMsg && userIdx >= 0
        // Pinning works for both user and assistant messages — anything with
        // textual content. Tool / dispatch / thinking are not pinnable
        // (their content isn't really a "message"; tool output also rotates).
        const canPin = (msg.role === 'user' || msg.role === 'assistant') && (msg.content || '').trim() !== ''
        const isAlreadyPinned = canPin && pinnedKeys.has(msg.role + ':' + msg.content)
        const close = () => setCtxMenu(null)
        const copy = () => {
          copyToClipboard(msg.content || '').catch(() => {})
          close()
        }
        const quote = () => {
          const quoted = msg.content.split('\n').map((l) => '> ' + l).join('\n')
          setInput((prev) => (prev ? prev + '\n\n' : '') + quoted + '\n\n')
          requestAnimationFrame(() => inputRef.current?.focus())
          close()
        }
        const fork = async () => {
          close()
          if (!canFork || !activeProjectId) return
          try {
            const newSession: any = await ForkChatSession(activeProjectId, currentSessionId, userIdx, '')
            if (!newSession?.id) return
            // Switch to the new session via the same custom event the
            // command palette uses. App.tsx listens and updates `view`.
            useChatStore.getState().setActiveSession(activeProjectId, newSession.id)
            window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: newSession.id }))
            // Tell App.tsx to reload its session-tab list so the new branch
            // appears in the tab bar without waiting for a full reload.
            window.dispatchEvent(new CustomEvent('gokin:sessions-changed'))
          } catch (e: any) {
            console.error('ForkChatSession error:', e)
            useChatStore.getState().finalizeAssistant(chatKey, `Error: branch failed — ${String(e?.message || e)}`)
          }
        }
        const togglePin = async () => {
          close()
          if (!canPin || !activeProjectId) return
          const key = msg.role + ':' + msg.content
          try {
            if (isAlreadyPinned) {
              // Find the existing pin's ID via the loaded list, then call Unpin.
              const list = pinnedList || (await ListPinnedMessages(activeProjectId, currentSessionId))
              const existing = (list || []).find((p: any) => p.role === msg.role && p.content === msg.content)
              if (existing?.id) {
                await UnpinMessage(activeProjectId, currentSessionId, existing.id)
              }
              setPinnedKeys((prev) => {
                const n = new Set(prev); n.delete(key); return n
              })
              setPinnedList((prev) => (prev || []).filter((p: any) => p.role !== msg.role || p.content !== msg.content))
            } else {
              await PinMessage(activeProjectId, currentSessionId, msg.role, msg.content, msg.id)
              setPinnedKeys((prev) => {
                const n = new Set(prev); n.add(key); return n
              })
              // Force the pins modal to re-fetch on next open since we just
              // mutated the on-disk list.
              setPinnedList(null)
            }
          } catch (e: any) {
            console.error('togglePin error:', e)
            useChatStore.getState().finalizeAssistant(chatKey, `Error: ${isAlreadyPinned ? 'unpin' : 'pin'} failed — ${String(e?.message || e)}`)
          }
        }
        const rerun = async () => {
          close()
          if (!canRerun) return
          // Trim UI + backend from this user message and re-run unchanged.
          // Snapshot for rollback so a backend rejection doesn't leave a
          // visibly trimmed UI with no agent activity to follow.
          const beforeMsgs = useChatStore.getState().messages[chatKey] || []
          const cutIdx = beforeMsgs.findIndex((m) => m.id === msg.id)
          if (cutIdx >= 0) {
            useChatStore.getState().setMessages(chatKey, beforeMsgs.slice(0, cutIdx))
          }
          useChatStore.getState().addUserMessage(chatKey, msg.content)
          userScrolledUpRef.current = false
          setSending(true)
          try {
            await EditUserMessage(activeProjectId, currentSessionId, userIdx, msg.content)
          } catch (e: any) {
            console.error('rerun error:', e)
            useChatStore.getState().setMessages(chatKey, beforeMsgs)
            useChatStore.getState().finalizeAssistant(chatKey, `Error: ${String(e?.message || e)}`)
          } finally {
            setSending(false)
            inputRef.current?.focus()
          }
        }
        // Keep menu within viewport: nudge up/left if near edges.
        const MENU_W = 180, MENU_H = 180
        const x = Math.min(ctxMenu.x, window.innerWidth - MENU_W - 8)
        const y = Math.min(ctxMenu.y, window.innerHeight - MENU_H - 8)
        return (
          <div
            className="msg-ctx-menu"
            style={{ top: y, left: x }}
            onMouseDown={(e) => e.stopPropagation()}
            onContextMenu={(e) => { e.preventDefault(); close() }}
          >
            <button className="msg-ctx-item" onClick={copy}>
              <Copy size={12} /> <span>Copy content</span>
            </button>
            <button className="msg-ctx-item" onClick={quote}>
              <MessageSquare size={12} /> <span>Quote in reply</span>
            </button>
            {canPin && (
              <button className="msg-ctx-item" onClick={togglePin} title={isAlreadyPinned ? 'Remove from pinned messages' : 'Bookmark this message for quick access'}>
                {isAlreadyPinned ? <BookmarkMinus size={12} /> : <BookmarkPlus size={12} />}
                <span>{isAlreadyPinned ? 'Unpin message' : 'Pin message'}</span>
              </button>
            )}
            {canFork && (
              <button className="msg-ctx-item" onClick={fork} title="Branch a new session from this message">
                <GitFork size={12} /> <span>Branch from here</span>
              </button>
            )}
            {canRerun && (
              <button className="msg-ctx-item" onClick={rerun}>
                <RotateCcw size={12} /> <span>Re-run (trim &amp; resend)</span>
              </button>
            )}
          </div>
        )
      })()}

      {showFilePicker && (
        <FilePicker
          projectId={activeProjectId}
          onClose={() => { setShowFilePicker(false); inputRef.current?.focus() }}
          onPick={(path) => {
            setShowFilePicker(false)
            // Insert "@<path>" reference at cursor position.
            const el = inputRef.current
            const token = '@' + path
            if (el) {
              const cursor = el.selectionStart ?? input.length
              const before = input.slice(0, cursor)
              const after = input.slice(el.selectionEnd ?? cursor)
              const needsLeadingSpace = before.length > 0 && !before.endsWith(' ') && !before.endsWith('\n')
              const insertion = (needsLeadingSpace ? ' ' : '') + token + ' '
              const next = before + insertion + after
              setInput(next)
              requestAnimationFrame(() => {
                el.focus()
                const pos = (before + insertion).length
                el.setSelectionRange(pos, pos)
              })
            } else {
              setInput((prev) => (prev ? prev + ' ' : '') + token + ' ')
            }
          }}
        />
      )}

      {showTerminal && (
        <div className="chat-terminal-pane">
          <div className="chat-terminal-header">
            <TerminalSquare size={13} />
            <span>Terminal</span>
            <button className="icon-btn" onClick={() => setShowTerminal(false)} style={{ marginLeft: 'auto' }}>
              <X size={12} />
            </button>
          </div>
          <TerminalPanel />
        </div>
      )}
    </div>
  )
}

function CodeBlock({ children, ...props }: any) {
  const [codeCopied, setCodeCopied] = useState(false)
  const ref = useRef<HTMLPreElement>(null)
  const copyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const handleCodeCopy = () => {
    const text = ref.current?.textContent || ''
    copyToClipboard(text).then(() => {
      setCodeCopied(true)
      if (copyTimerRef.current) clearTimeout(copyTimerRef.current)
      copyTimerRef.current = setTimeout(() => setCodeCopied(false), 1500)
    }).catch(() => {})
  }

  return (
    <div className="code-block-wrap">
      <button className="code-copy-btn" onClick={handleCodeCopy}>
        {codeCopied ? <Check size={12} /> : <Copy size={12} />}
        {codeCopied ? 'Copied' : 'Copy'}
      </button>
      <pre ref={ref} {...props}>{children}</pre>
    </div>
  )
}

const mdComponents = { pre: CodeBlock }

// Stable plugin array — inline `[rehypeHighlight]` creates a new array every
// render, causing ReactMarkdown to re-run the full rehype pipeline even when
// the content barely changed (especially bad during streaming).
const MD_REHYPE_PLUGINS = [rehypeHighlight]

// During streaming, skip syntax highlighting (saves 5-15ms/delta for responses
// with code blocks). The finalized message renders with full highlighting.
const MD_STREAM_PLUGINS: never[] = []

// Stable empty-array reference so MessageBubble's React.memo shallow compare
// doesn't see a new array every render for messages with no changed files.
const EMPTY_ARR: string[] = []

// AskUserCard renders a pending ask_user question inline in the chat, letting
// the user click a suggested option or type a free-form answer. The agent
// goroutine on the backend is blocked until onAnswer or onCancel resolves.
function AskUserCard({ question, onAnswer, onCancel }: {
  question: AskUserQuestion
  onAnswer: (answer: string) => Promise<void>
  onCancel: () => void | Promise<void>
}) {
  const [draft, setDraft] = useState(question.default || '')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    setDraft(question.default || '')
    setSubmitError(null)
    inputRef.current?.focus()
    if (question.default) {
      inputRef.current?.setSelectionRange(question.default.length, question.default.length)
    }
  }, [question.questionID, question.default])

  const submit = async (answer?: string) => {
    const v = (answer ?? draft).trim()
    if (!v) return
    setSubmitError(null)
    setSubmitting(true)
    try {
      await onAnswer(v)
      // onAnswer clears the card on success — if we reach here the card is gone
    } catch (e: any) {
      setSubmitError(String(e?.message || e || 'Failed to send answer — try again'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="askuser-card">
      <div className="askuser-header">
        <MessageSquare size={12} />
        <span className="askuser-label">Agent needs input</span>
      </div>
      <div className="askuser-question">{question.question}</div>
      {question.options.length > 0 && (
        <div className="askuser-options">
          {question.options.map((opt) => (
            <button
              key={opt}
              className={`askuser-option ${opt === question.default ? 'default' : ''}`}
              onClick={() => submit(opt)}
              disabled={submitting}
            >
              {opt}
            </button>
          ))}
        </div>
      )}
      <textarea
        ref={inputRef}
        className="askuser-input"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing || e.keyCode === 229) return
          if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
            e.preventDefault()
            submit()
          }
          if (e.key === 'Escape') {
            e.preventDefault()
            onCancel()
          }
        }}
        placeholder={
          question.options.length > 0
            ? 'Or type a custom answer (Ctrl+Enter to send)…'
            : 'Type your answer (Ctrl+Enter to send)…'
        }
        rows={2}
        maxLength={4000}
        disabled={submitting}
      />
      {submitError && <div className="askuser-error">{submitError}</div>}
      <div className="askuser-actions">
        <span className="askuser-hint">Ctrl+Enter to answer · Esc to cancel</span>
        <button className="btn-cancel-sm" onClick={() => onCancel()} disabled={submitting}>Cancel</button>
        <button className="btn-primary-sm" onClick={() => submit()} disabled={submitting || !draft.trim()}>
          {submitting ? 'Sending…' : 'Answer'}
        </button>
      </div>
    </div>
  )
}

// userIndexForMessage returns the 0-based "from end" index of the user text
// message with the given id, matching the backend EditUserMessage contract.
// Returns -1 if the id doesn't belong to a user message (or isn't in the list).
function userIndexForMessage(list: ChatMessage[], id: string): number {
  let seen = 0
  for (let i = list.length - 1; i >= 0; i--) {
    if (list[i].role !== 'user') continue
    if (list[i].id === id) return seen
    seen++
  }
  return -1
}

// Expand `@path/to/file.ext` tokens in a message into markdown code blocks
// appended to the bottom. Paths that fail to read stay as `@path` tokens so
// the agent can still see the reference; successful reads get the content
// inlined with the file name as a header.
const EXPAND_FILE_MAX_BYTES = 50_000 // 50 KB per @-file attachment

async function expandFileRefs(message: string, projectId: string): Promise<string> {
  // Match tokens starting with @ followed by a path-ish character sequence.
  // Accepts slashes, dots, hyphens, digits, letters, underscores.
  const re = /@([A-Za-z0-9._/\-]+\.[A-Za-z0-9]+)(?=\s|$|[,;:!?)])/g
  const uniquePaths = new Set<string>()
  let m: RegExpExecArray | null
  while ((m = re.exec(message)) !== null) {
    uniquePaths.add(m[1])
  }
  if (uniquePaths.size === 0) return message
  const attachments: string[] = []
  for (const path of Array.from(uniquePaths).slice(0, 10)) {
    try {
      const content = await ReadFileContent(projectId, path)
      if (typeof content !== 'string' || content.length === 0) continue
      const truncated = content.length > EXPAND_FILE_MAX_BYTES
        ? content.slice(0, EXPAND_FILE_MAX_BYTES) + `\n... (truncated — ${Math.round(content.length / 1024)} KB total, showing first 50 KB)`
        : content
      const lang = detectLanguage(path)
      attachments.push(`\n\n--- attached: ${path} ---\n\`\`\`${lang}\n${truncated}\n\`\`\``)
    } catch (err) {
      // Non-fatal: leave the @path token in-place for the agent to interpret.
      console.warn(`expandFileRefs: could not read @${path}:`, err)
    }
  }
  return attachments.length > 0 ? message + attachments.join('') : message
}

// Describe what was in flight when the session was interrupted, for the
// recovery banner. "3 tool calls, partial assistant reply" is more actionable
// than a raw event count.
function summarizeRecovery(events: any[]): string {
  let toolCalls = 0
  let toolResults = 0
  let hasAssistantText = false
  let hasThinking = false
  for (const e of events) {
    if (e.type === 'tool_call') toolCalls++
    if (e.type === 'tool_result') toolResults++
    if (e.type === 'assistant_text') hasAssistantText = true
    if (e.type === 'thinking') hasThinking = true
  }
  const parts: string[] = []
  if (toolCalls > 0) {
    parts.push(`${toolCalls} tool call${toolCalls === 1 ? '' : 's'}${toolResults < toolCalls ? ` (${toolCalls - toolResults} unfinished)` : ''}`)
  }
  if (hasThinking) parts.push('reasoning captured')
  if (hasAssistantText) parts.push('partial assistant reply')
  if (parts.length === 0) parts.push(`${events.length} event${events.length === 1 ? '' : 's'} buffered`)
  return parts.join(', ')
}

// Write text to clipboard, falling back to the Wails native bridge when
// navigator.clipboard is blocked (WebKitGTK on Linux in non-secure contexts).
async function copyToClipboard(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    try { await ClipboardSetText(text) } catch { /* silent */ }
  }
}

// Format a token count as "1.2k" / "23k" / "128k".
function formatTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k'
  return Math.round(n / 1000) + 'k'
}

// Format a USD figure with sensible precision: <$0.01 → "<$0.01", under
// $1 → 4 decimals (e.g. "$0.0023") so users can see fractions of a cent
// for cheap models, $1+ → 2 decimals.
function formatCostUSD(usd: number): string {
  if (usd <= 0) return '$0'
  if (usd < 0.01) return '<$0.01'
  if (usd < 1) return '$' + usd.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')
  return '$' + usd.toFixed(2)
}

// Format elapsed milliseconds as "12s", "1m 23s", "1h 5m".
function formatElapsed(ms: number): string {
  if (ms < 1000) return '0s'
  const totalSeconds = Math.floor(ms / 1000)
  if (totalSeconds < 60) return `${totalSeconds}s`
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes < 60) return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const remMin = minutes % 60
  return remMin > 0 ? `${hours}h ${remMin}m` : `${hours}h`
}

// iter 1030+: keep only the last N lines of a streaming output blob so the
// preview area stays a fixed visual size during long builds. Trims from the
// FRONT (oldest lines drop) so the user always sees the most recent activity
// — important for compiler output where "first 1000 unrelated lines, then
// the actual error at the bottom" is the common pattern.
function tailLines(s: string, n: number): string {
  if (!s) return ''
  const lines = s.split('\n')
  if (lines.length <= n) return s
  return lines.slice(-n).join('\n')
}

// iter 1000+: live elapsed-time chip rendered next to the Loader2 spinner
// in a pending tool card. Watches the wall clock so a hung tool surfaces
// "1m 23s..." instead of just an indefinitely-spinning icon. Updates once
// per second; auto-stops + unmounts itself when isPending flips false.
//
// Color escalation thresholds (matches the user's mental model of "this is
// taking longer than I expected"):
//   - 0-30s: muted (normal, no extra attention)
//   - 30s-2m: amber (slow but not pathological)
//   - 2m+: red ("something might be stuck")
//
// Tooltip shows the ISO start timestamp so power users can correlate with
// other logs / system metrics.
function ToolElapsedChip({ startedAt, isPending }: { startedAt: number; isPending: boolean }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!isPending || !startedAt) return
    // Tick once per second. The initial render also shows the current time
    // so we don't need to wait a full second for the first paint.
    setNow(Date.now())
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [isPending, startedAt])
  if (!isPending || !startedAt) return null
  const elapsedMs = Math.max(0, now - startedAt)
  let cls = 'tool-elapsed-chip'
  if (elapsedMs >= 120_000) cls += ' tool-elapsed-red'
  else if (elapsedMs >= 30_000) cls += ' tool-elapsed-amber'
  return (
    <span
      className={cls}
      title={`Running since ${new Date(startedAt).toLocaleTimeString()}`}
    >
      {formatElapsed(elapsedMs)}
    </span>
  )
}

// Build suggestion chips based on detected project language.
function suggestionsForLang(lang: string | null): string[] {
  const base = ['Explain this codebase structure', 'Find potential bugs']
  switch (lang) {
    case 'go':
      return [...base, 'Run go test ./... and fix any failures', 'Review error handling patterns']
    case 'typescript':
      return [...base, 'Add strict type annotations where missing', 'Run tsc --noEmit and fix errors']
    case 'javascript':
      return [...base, 'Convert to TypeScript', 'Find missing error handling']
    case 'python':
      return [...base, 'Run pytest and fix failures', 'Add type hints to public functions']
    case 'rust':
      return [...base, 'Run cargo clippy and fix warnings', 'Review error handling with ?']
    case 'ruby':
      return [...base, 'Run rspec and fix failures', 'Review Ruby style guide violations']
    case 'java':
      return [...base, 'Run tests and fix failures', 'Review thread-safety concerns']
    case 'php':
      return [...base, 'Run phpunit and fix failures', 'Check for SQL injection risks']
    case 'elixir':
      return [...base, 'Run mix test and fix failures', 'Review OTP supervision tree']
    case 'swift':
      return [...base, 'Run swift build and fix errors', 'Add DocC documentation']
    case 'dart':
      return [...base, 'Run dart analyze and fix issues', 'Run dart test']
    case 'zig':
      return [...base, 'Run zig build and fix errors', 'Run zig test']
    case 'csharp':
      return [...base, 'Run dotnet build and fix errors', 'Run dotnet test']
    case 'c++':
      return [...base, 'Run cmake and fix build errors', 'Add memory safety checks']
    default:
      return [...base, 'Write tests for the main module', 'Refactor for better readability']
  }
}

function formatMsgTime(ts: number): string {
  const d = new Date(ts)
  const now = new Date()
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  const yesterday = new Date(now)
  yesterday.setDate(yesterday.getDate() - 1)
  if (d.toDateString() === yesterday.toDateString()) {
    return 'Yesterday ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

function getToolIcon(name: string) {
  if (name.startsWith('bash') || name === 'kill_shell') return <Terminal size={12} />
  if (name === 'edit' || name === 'write') return <Pencil size={12} />
  if (name === 'read' || name === 'list_dir' || name === 'tree') return <Eye size={12} />
  if (name === 'copy' || name === 'move' || name === 'delete' || name === 'mkdir') return <FolderTree size={12} />
  if (name === 'diff') return <FileText size={12} />
  if (name.startsWith('git_')) return <GitBranch size={12} />
  if (name === 'grep' || name === 'glob' || name === 'history_search') return <FolderSearch size={12} />
  if (name === 'web_fetch' || name === 'web_search') return <Search size={12} />
  if (name === 'memory' || name === 'memorize' || name === 'shared_memory' || name === 'pin_context' || name === 'update_scratchpad') return <Database size={12} />
  if (name === 'ask_agent' || name === 'task' || name === 'coordinate') return <Bot size={12} />
  if (name === 'task_output' || name === 'task_stop') return <TerminalSquare size={12} />
  if (name === 'ask_user') return <MessageSquare size={12} />
  if (name === 'todo' || name.startsWith('enter_plan') || name.startsWith('update_plan') || name === 'get_plan_status' || name === 'exit_plan_mode') return <ListChecks size={12} />
  if (name === 'request_tool' || name === 'tools_list') return <Download size={12} />
  if (name === 'run_tests' || name === 'verify_code' || name === 'review_changes') return <FileText size={12} />
  if (name === 'check_impact') return <FolderSearch size={12} />
  return <Zap size={12} />
}

// Extract the primary argument to display inline in the tool pill.
function getToolPrimary(name: string, args: Record<string, unknown> | undefined): string | null {
  if (!args) return null
  const a = args as Record<string, any>
  switch (name) {
    case 'bash': return a.command
    case 'read':
    case 'list_dir':
    case 'tree':
    case 'edit':
    case 'write':
    case 'delete':
    case 'mkdir':
      return a.path || a.file_path
    case 'copy':
    case 'move':
      return `${a.source || a.from} → ${a.destination || a.to}`
    case 'grep':
      return a.pattern
    case 'glob':
      return a.pattern
    case 'git_status': return null
    case 'git_diff':
    case 'git_log':
    case 'git_blame':
    case 'git_branch':
      return a.path || a.branch || null
    case 'git_add':
      return Array.isArray(a.paths) ? a.paths.join(' ') : a.path
    case 'git_commit':
      return a.message
    case 'git_pr':
      return a.title || null
    case 'web_fetch': return a.url
    case 'web_search': return a.query
    case 'diff': return a.path1 || a.file1
    case 'history_search': return a.pattern || a.query
    case 'batch': return a.pattern ? `${a.pattern} → ${a.operation || ''}` : null
    case 'memory': return a.action ? `${a.action}${a.key ? ': ' + a.key : ''}` : null
    case 'memorize': return a.key || null
    case 'coordinate': return Array.isArray(a.tasks) ? `${a.tasks.length} task${a.tasks.length !== 1 ? 's' : ''}` : null
    case 'ask_agent': return a.query ? String(a.query).slice(0, 60) : null
    case 'shared_memory': return a.key || a.action || null
    case 'pin_context':
    case 'update_scratchpad': return a.content ? String(a.content).slice(0, 60) : null
    case 'task': return a.prompt ? String(a.prompt).slice(0, 60) : null
    case 'todo': return a.action || null
    case 'env': return a.name || a.prefix || null
    case 'task_output': return a.id || null
    case 'task_stop': return a.id || null
    case 'kill_shell': return a.shell_id || null
    case 'ask_user': return a.question ? String(a.question).slice(0, 60) : null
    case 'enter_plan_mode': return a.title ? String(a.title).slice(0, 60) : null
    case 'update_plan_progress': return a.step_id != null ? `step ${a.step_id}${a.action ? ': ' + a.action : ''}` : null
    case 'exit_plan_mode': return a.reason ? String(a.reason).slice(0, 60) : null
    case 'get_plan_status': return null
    case 'request_tool': return a.tool_name ? String(a.tool_name) : null
    case 'tools_list': return null
    case 'run_tests': return a.path || a.filter || null
    case 'verify_code': return a.path || null
    case 'review_changes': return a.file ? String(a.file).slice(0, 60) : (a.staged ? 'staged' : null)
    case 'check_impact': return a.symbol ? String(a.symbol) : null
    default: return null
  }
}

// Shorten long paths/commands for inline display.
function shortenForPill(s: string, max = 60): string {
  if (s.length <= max) return s
  return s.slice(0, max - 1) + '…'
}

// Threshold for auto-collapsing long tool outputs. Above this many lines,
// only the first N lines show with a "Show all" button to expand.
const TOOL_OUTPUT_COLLAPSE_LINES = 30

// Diff hunk: a run of consecutive lines with the same kind.
type DiffHunk = { kind: 'equal' | 'add' | 'remove'; lines: string[] }

// Compute a line-level LCS diff of two text snippets. Returns hunks suitable
// for side-by-side rendering. Quadratic in worst case — fine for tool args
// which are typically <200 lines; we cap at 400 lines to stay responsive.
function computeLineDiff(oldText: string, newText: string): DiffHunk[] {
  const MAX_LINES = 400
  const a = (oldText || '').split('\n')
  const b = (newText || '').split('\n')
  if (a.length > MAX_LINES || b.length > MAX_LINES) {
    // Fall back to coarse diff: show all old as remove, all new as add.
    const hunks: DiffHunk[] = []
    if (a.length > 0 && !(a.length === 1 && a[0] === '')) hunks.push({ kind: 'remove', lines: a })
    if (b.length > 0 && !(b.length === 1 && b[0] === '')) hunks.push({ kind: 'add', lines: b })
    return hunks
  }
  // Standard LCS table of lengths
  const n = a.length, m = b.length
  const lcs: Uint16Array[] = new Array(n + 1)
  for (let i = 0; i <= n; i++) lcs[i] = new Uint16Array(m + 1)
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      if (a[i] === b[j]) lcs[i][j] = lcs[i + 1][j + 1] + 1
      else lcs[i][j] = Math.max(lcs[i + 1][j], lcs[i][j + 1])
    }
  }
  // Walk the table to produce hunks
  const hunks: DiffHunk[] = []
  const push = (kind: DiffHunk['kind'], line: string) => {
    const last = hunks[hunks.length - 1]
    if (last && last.kind === kind) last.lines.push(line)
    else hunks.push({ kind, lines: [line] })
  }
  let i = 0, j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) { push('equal', a[i]); i++; j++ }
    else if (lcs[i + 1][j] >= lcs[i][j + 1]) { push('remove', a[i]); i++ }
    else { push('add', b[j]); j++ }
  }
  while (i < n) { push('remove', a[i]); i++ }
  while (j < m) { push('add', b[j]); j++ }
  return hunks
}

// Collapse long equal-context runs to keep the diff readable. Show up to
// `context` lines around each change, replace the middle with a "..." hunk.
function collapseContext(hunks: DiffHunk[], context = 3): DiffHunk[] {
  if (hunks.length === 0) return hunks
  const out: DiffHunk[] = []
  for (let i = 0; i < hunks.length; i++) {
    const h = hunks[i]
    if (h.kind !== 'equal') { out.push(h); continue }
    const isFirst = i === 0
    const isLast = i === hunks.length - 1
    const maxKeep = context
    if (h.lines.length <= (isFirst || isLast ? maxKeep : maxKeep * 2)) { out.push(h); continue }
    if (isFirst) {
      out.push({ kind: 'equal', lines: h.lines.slice(-maxKeep) })
    } else if (isLast) {
      out.push({ kind: 'equal', lines: h.lines.slice(0, maxKeep) })
    } else {
      out.push({ kind: 'equal', lines: h.lines.slice(0, maxKeep) })
      out.push({ kind: 'equal', lines: ['…'] })
      out.push({ kind: 'equal', lines: h.lines.slice(-maxKeep) })
    }
  }
  return out
}

// Map a file path extension to a highlight.js language identifier.
function detectLanguage(filePath: string): string {
  const m = /\.([a-zA-Z0-9]+)$/.exec(filePath)
  if (!m) return ''
  const ext = m[1].toLowerCase()
  const map: Record<string, string> = {
    ts: 'typescript', tsx: 'typescript',
    js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
    go: 'go', rs: 'rust', py: 'python',
    rb: 'ruby', java: 'java', kt: 'kotlin', swift: 'swift',
    c: 'c', h: 'c', cpp: 'cpp', cc: 'cpp', hpp: 'cpp', cxx: 'cpp',
    cs: 'csharp', php: 'php', sh: 'bash', bash: 'bash', zsh: 'bash',
    sql: 'sql', yaml: 'yaml', yml: 'yaml', json: 'json', toml: 'toml',
    md: 'markdown', markdown: 'markdown',
    html: 'html', xml: 'xml', css: 'css', scss: 'scss', less: 'less',
    dockerfile: 'dockerfile', makefile: 'makefile',
    ex: 'elixir', exs: 'elixir', erl: 'erlang',
    proto: 'protobuf', lua: 'lua', dart: 'dart',
    tf: 'hcl', hcl: 'hcl',
  }
  return map[ext] || ''
}

// Render the content of a `read` result with syntax highlighting, optional
// line numbers (if the result starts with a numbered format), and a header
// that identifies the file.
function ReadResultView({ filePath, content }: { filePath: string; content: string }) {
  const language = detectLanguage(filePath)
  const fenced = '```' + language + '\n' + content + '\n```'
  return (
    <div className="read-result">
      <div className="read-result-body md-content">
        <ReactMarkdown rehypePlugins={MD_REHYPE_PLUGINS} components={mdComponents}>
          {fenced}
        </ReactMarkdown>
      </div>
    </div>
  )
}

// Parse and render grep results grouped by file path. Input format lines look
// like "relative/path.go:42: matched line content". Anything not matching the
// pattern is kept as-is (preserves the "Found N match(es)…" summary header).
type GrepGroup = { file: string; matches: { line: number; text: string }[] }

function parseGrepOutput(content: string): { header: string; groups: GrepGroup[] } {
  const lines = content.split('\n')
  const groups = new Map<string, GrepGroup>()
  const header: string[] = []
  let seenMatch = false
  const re = /^([^:\n][^:\n]*):(\d+):\s?(.*)$/
  for (const ln of lines) {
    const m = re.exec(ln)
    if (m) {
      seenMatch = true
      const [, file, lineStr, text] = m
      const lineNum = parseInt(lineStr, 10)
      if (!groups.has(file)) groups.set(file, { file, matches: [] })
      groups.get(file)!.matches.push({ line: lineNum, text })
    } else if (!seenMatch) {
      header.push(ln)
    }
  }
  return {
    header: header.join('\n').trim(),
    groups: Array.from(groups.values()),
  }
}

// Render glob tool output (one file path per line, optional "N file(s) found."
// summary on top) as a clickable list.
function GlobView({ content }: { content: string }) {
  const lines = content.split('\n').map((l) => l.trim()).filter(Boolean)
  // Pull out a summary line if present (e.g. "Found 42 file(s):" or "No matches.")
  let summary: string | null = null
  let paths = lines
  if (lines.length > 0 && /^(found|no)\b|file\(s\)/i.test(lines[0])) {
    summary = lines[0]
    paths = lines.slice(1)
  }
  if (paths.length === 0) {
    return <pre className="tool-result">{content}</pre>
  }
  return (
    <div className="glob-view">
      {summary && <div className="glob-header">{summary}</div>}
      <div className="glob-list">
        {paths.map((p, i) => (
          <button
            key={i}
            className="glob-row"
            title={`Insert @${p} into the chat input`}
            onClick={() => {
              window.dispatchEvent(new CustomEvent('gokin:insert-file-ref', { detail: { path: p } }))
            }}
          >
            <FileText size={11} className="glob-icon" />
            <span className="glob-path">{p}</span>
          </button>
        ))}
      </div>
    </div>
  )
}

function GrepView({ content, pattern }: { content: string; pattern?: string }) {
  const { header, groups } = useMemo(() => parseGrepOutput(content), [content])
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const matcher = useMemo(() => {
    if (!pattern) return null
    try {
      return new RegExp(pattern, 'gi')
    } catch {
      // Non-regex pattern: fall back to case-insensitive literal match.
      const escaped = pattern.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
      return new RegExp(escaped, 'gi')
    }
  }, [pattern])

  const highlight = (line: string): (string | JSX.Element)[] => {
    if (!matcher) return [line]
    const parts: (string | JSX.Element)[] = []
    let lastIdx = 0
    matcher.lastIndex = 0
    let m: RegExpExecArray | null
    let i = 0
    while ((m = matcher.exec(line)) !== null) {
      if (m.index > lastIdx) parts.push(line.slice(lastIdx, m.index))
      parts.push(<mark key={i++} className="grep-hit">{m[0]}</mark>)
      lastIdx = m.index + m[0].length
      if (m[0].length === 0) matcher.lastIndex++ // avoid infinite loop on zero-width
    }
    if (lastIdx < line.length) parts.push(line.slice(lastIdx))
    return parts
  }

  if (groups.length === 0) {
    return <pre className="tool-result">{content}</pre>
  }

  return (
    <div className="grep-view">
      {header && <div className="grep-header">{header}</div>}
      {groups.map((g) => {
        const isCollapsed = !!collapsed[g.file]
        return (
          <div key={g.file} className="grep-group">
            <div
              className="grep-file"
              onClick={() => setCollapsed((s) => ({ ...s, [g.file]: !isCollapsed }))}
            >
              <ChevronRight size={10} className={`tool-chevron ${isCollapsed ? '' : 'expanded'}`} />
              <span className="grep-file-path">{g.file}</span>
              <span className="grep-file-count">{g.matches.length}</span>
            </div>
            {!isCollapsed && (
              <div className="grep-matches">
                {g.matches.map((match, i) => (
                  <div key={i} className="grep-match">
                    <span className="grep-line-num">{match.line}</span>
                    <span className="grep-line-text">{highlight(match.text)}</span>
                  </div>
                ))}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

// Render a unified-diff string (git diff output) with per-line coloring.
function GitDiffView({ text }: { text: string }) {
  const lines = text.split('\n')
  return (
    <pre className="git-diff-body">
      {lines.map((line, i) => {
        let cls = 'git-diff-line'
        if (line.startsWith('+++') || line.startsWith('---')) cls += ' git-diff-meta'
        else if (line.startsWith('@@')) cls += ' git-diff-hunk'
        else if (line.startsWith('diff ') || line.startsWith('index ') || line.startsWith('new file') || line.startsWith('deleted file')) cls += ' git-diff-meta'
        else if (line.startsWith('+')) cls += ' git-diff-add'
        else if (line.startsWith('-')) cls += ' git-diff-remove'
        return <div key={i} className={cls}>{line || '\u00a0'}</div>
      })}
    </pre>
  )
}

function DiffView({ oldText, newText }: { oldText: string; newText: string }) {
  const hunks = useMemo(
    () => collapseContext(computeLineDiff(oldText, newText), 3),
    [oldText, newText]
  )
  let adds = 0, removes = 0
  for (const h of hunks) {
    if (h.kind === 'add') adds += h.lines.length
    if (h.kind === 'remove') removes += h.lines.length
  }
  return (
    <div className="diff-view">
      <div className="diff-summary">
        <span className="diff-added">+{adds}</span>
        <span className="diff-removed">−{removes}</span>
      </div>
      <pre className="diff-body">
        {hunks.map((h, i) => h.lines.map((line, j) => (
          <div key={`${i}-${j}`} className={`diff-line diff-line-${h.kind}`}>
            <span className="diff-line-marker">
              {h.kind === 'add' ? '+' : h.kind === 'remove' ? '−' : ' '}
            </span>
            <span className="diff-line-content">{line || '\u00a0'}</span>
          </div>
        )))}
      </pre>
    </div>
  )
}

// Describe an edit tool call for diff rendering. Returns null if the edit isn't
// representable as a single old→new diff (e.g. regex, line-range insert where
// we don't know the displaced lines).
function editArgsToDiff(args: Record<string, any> | undefined): { oldText: string; newText: string } | null {
  if (!args) return null
  // Multi-edit: concatenate each edit's old→new as pseudo-hunks.
  if (Array.isArray(args.edits)) {
    const oldParts: string[] = []
    const newParts: string[] = []
    for (const e of args.edits) {
      if (e && typeof e.old_string === 'string' && typeof e.new_string === 'string') {
        oldParts.push(String(e.old_string))
        newParts.push(String(e.new_string))
      }
    }
    if (oldParts.length === 0) return null
    return { oldText: oldParts.join('\n\n'), newText: newParts.join('\n\n') }
  }
  // Simple exact-match edit.
  if (typeof args.old_string === 'string' && typeof args.new_string === 'string') {
    return { oldText: String(args.old_string), newText: String(args.new_string) }
  }
  // insert_after_line: no old context, show as pure addition.
  if (typeof args.new_string === 'string' && args.insert_after_line !== undefined) {
    return { oldText: '', newText: String(args.new_string) }
  }
  // line range replace: we don't know the old lines, show new as pure addition.
  if (typeof args.new_string === 'string' && (args.line_start !== undefined || args.line_end !== undefined)) {
    return { oldText: '', newText: String(args.new_string) }
  }
  return null
}

function CollapsibleOutput({ content, className }: { content: string; className: string }) {
  const [expanded, setExpanded] = useState(false)
  // Memoise the split + preview so toggling `expanded` (a state change that
  // normally reruns this component) doesn't re-split multi-kilobyte tool
  // outputs like full `tree` or `bash ls -R` dumps. Only recomputes when the
  // actual content string changes.
  const { lines, needsCollapse, preview, hiddenCount } = useMemo(() => {
    const l = content.split('\n')
    const needs = l.length > TOOL_OUTPUT_COLLAPSE_LINES
    return {
      lines: l,
      needsCollapse: needs,
      preview: needs ? l.slice(0, TOOL_OUTPUT_COLLAPSE_LINES).join('\n') : content,
      hiddenCount: needs ? l.length - TOOL_OUTPUT_COLLAPSE_LINES : 0,
    }
  }, [content])

  if (!needsCollapse) {
    return <pre className={className}>{content}</pre>
  }

  if (expanded) {
    return (
      <>
        <pre className={className}>{content}</pre>
        <button className="tool-output-toggle" onClick={() => setExpanded(false)}>
          Show less
        </button>
      </>
    )
  }
  return (
    <>
      <pre className={`${className} truncated`}>{preview}</pre>
      <button className="tool-output-toggle" onClick={() => setExpanded(true)}>
        Show {hiddenCount} more line{hiddenCount === 1 ? '' : 's'}
      </button>
    </>
  )
}

type MessageBubbleProps = {
  message: ChatMessage
  onRerun?: () => void | Promise<void>
  canEdit?: boolean
  onEditSubmit?: (newContent: string) => void | Promise<void>
  changedFiles?: string[]
  focused?: boolean
  onContextMenu?: (e: React.MouseEvent) => void
}

function MessageBubbleInner({ message, onRerun, canEdit, onEditSubmit, changedFiles, focused, onContextMenu }: MessageBubbleProps) {
  const [expanded, setExpanded] = useState(false)
  const [copied, setCopied] = useState(false)
  const copyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [editing, setEditing] = useState(false)
  const [editDraft, setEditDraft] = useState('')
  const editTextareaRef = useRef<HTMLTextAreaElement>(null)

  // Auto-size the edit textarea.
  useLayoutEffect(() => {
    if (!editing) return
    const el = editTextareaRef.current
    if (!el) return
    el.style.height = '0'
    el.style.height = Math.min(el.scrollHeight, 280) + 'px'
  }, [editing, editDraft])

  // Cache rendered markdown by content so the streaming-delta re-renders of
  // sibling messages don't re-run rehypeHighlight on this message's tree.
  // Must be declared BEFORE any early returns (Rules of Hooks): if we put it
  // after the dispatch/thinking/tool branches, a component instance whose
  // message role changes mid-life would render a different number of hooks
  // and React would throw error #310. Only used by the user/assistant branch
  // below; cheap to compute for other roles.
  const markdownElement = useMemo(
    () => (
      <ReactMarkdown rehypePlugins={MD_REHYPE_PLUGINS} components={mdComponents}>
        {message.content}
      </ReactMarkdown>
    ),
    [message.content],
  )

  if (message.role === 'dispatch') {
    return (
      <div className={`message dispatch ${focused ? 'focused' : ''}`} data-msg-id={message.id} onContextMenu={onContextMenu}>
        <div className={`dispatch-card ${message.dispatchSuccess ? 'success' : 'error'}`}>
          <div className="dispatch-card-header">
            <ExternalLink size={12} />
            <span className="dispatch-card-target">{message.dispatchTarget}</span>
            {message.dispatchSuccess
              ? <CheckCircle size={12} className="tool-ok" />
              : <XCircle size={12} className="tool-fail" />
            }
          </div>
          <div className="dispatch-card-body">
            <div className="md-content">
              <ReactMarkdown rehypePlugins={MD_REHYPE_PLUGINS} components={mdComponents}>{message.content}</ReactMarkdown>
            </div>
          </div>
        </div>
      </div>
    )
  }

  if (message.role === 'thinking') {
    const wordCount = message.content.trim().split(/\s+/).length
    return (
      <div className={`message thinking ${focused ? 'focused' : ''}`} data-msg-id={message.id} onContextMenu={onContextMenu}>
        <div className="thinking-chip" onClick={() => setExpanded(!expanded)}>
          <Brain size={12} className="thinking-icon" />
          <span className="thinking-label">
            {expanded ? 'Thinking' : `Thought · ${wordCount} words`}
          </span>
          <ChevronRight size={10} className={`tool-chevron ${expanded ? 'expanded' : ''}`} />
        </div>
        {expanded && (
          <div className="thinking-body">
            <div className="thinking-text">{message.content}</div>
          </div>
        )}
      </div>
    )
  }

  if (message.role === 'tool') {
    const isPlanTool = message.toolName?.startsWith('enter_plan_mode') ||
      message.toolName?.startsWith('update_plan_progress') ||
      message.toolName?.startsWith('get_plan_status') ||
      message.toolName?.startsWith('exit_plan_mode')

    if (isPlanTool && message.toolSuccess) {
      return <PlanToolCard message={message} focused={focused} onContextMenu={onContextMenu} />
    }

    const isMemoryTool = message.toolName === 'memory' ||
      message.toolName === 'memorize' ||
      message.toolName === 'shared_memory' ||
      message.toolName === 'pin_context' ||
      message.toolName === 'history_search' ||
      message.toolName === 'update_scratchpad'

    if (isMemoryTool && message.toolSuccess) {
      return <MemoryToolCard message={message} focused={focused} onContextMenu={onContextMenu} />
    }

    const toolName = message.toolName || ''
    const toolIcon = getToolIcon(toolName)
    const isPending = message.toolSuccess === undefined
    const primary = getToolPrimary(toolName, message.toolArgs as any)
    const isBash = toolName === 'bash'
    const isTree = toolName === 'tree' || toolName === 'list_dir'
    const isGitDiff = toolName === 'git_diff' && message.toolSuccess === true && !!message.content
    const isEdit = toolName === 'edit' && message.toolSuccess === true
    const isWrite = toolName === 'write' && message.toolSuccess === true
    const isRead = toolName === 'read' && message.toolSuccess === true && !!message.content
    const readPath = isRead ? String((message.toolArgs as any)?.path || (message.toolArgs as any)?.file_path || '') : ''
    const isGrep = toolName === 'grep' && message.toolSuccess === true && !!message.content
    const grepPattern = isGrep ? String((message.toolArgs as any)?.pattern || '') : ''
    const isGlob = toolName === 'glob' && message.toolSuccess === true && !!message.content
    const editDiff = isEdit ? editArgsToDiff(message.toolArgs as any) : null
    const writeContent = isWrite ? String((message.toolArgs as any)?.content ?? '') : ''

    // iter 740+: left accent rail state — pending/success/failure drives a
    // colored left border on .tool-card so status is visible at a glance
    // before the user expands. Replaces the badge soup pattern called out
    // in wireframes.html direction 01.
    const railState = isPending ? 'pending' : (message.toolSuccess ? 'ok' : 'fail')

    return (
      <div className={`message tool ${focused ? 'focused' : ''}`} data-msg-id={message.id} onContextMenu={onContextMenu}>
        <div className={`tool-card tool-rail-${railState} ${expanded ? 'expanded' : ''}`}>
          <div className="tool-header" onClick={() => setExpanded(!expanded)}>
            <span className="tool-icon-wrap">{toolIcon}</span>
            <span className="tool-name">{toolName}</span>
            {primary && (
              <span className="tool-primary">{shortenForPill(String(primary))}</span>
            )}
            {isPending ? (
              <>
                <Loader2 size={12} className="tool-spinner" />
                <ToolElapsedChip startedAt={message.timestamp || 0} isPending={isPending} />
              </>
            ) : message.toolSuccess ? (
              <CheckCircle size={12} className="tool-ok" />
            ) : (
              <XCircle size={12} className="tool-fail" />
            )}
            <ChevronRight size={10} className={`tool-chevron ${expanded ? 'expanded' : ''}`} />
          </div>
          {isPending && message.streamingOutput && (
            // iter 1030+: live preview of the tool's running stdout (currently
            // only bash via the engine's ProgressCallback at 100ms cadence).
            // Always visible while pending — collapses automatically when the
            // tool resolves and streamingOutput is cleared. Capped at 100 KB
            // in the store; we tail the last ~12 lines for UI compactness.
            <div className="tool-streaming" onClick={(e) => e.stopPropagation()}>
              <pre className="tool-streaming-output">
                {tailLines(message.streamingOutput, 12)}
              </pre>
            </div>
          )}
          {expanded && (
            <div className="tool-detail">
              {isBash ? (
                <div className="tool-bash">
                  {(message.toolArgs as any)?.command && (
                    <div className="tool-bash-cmd">
                      <span className="tool-bash-prompt">$</span>
                      <span>{(message.toolArgs as any).command}</span>
                    </div>
                  )}
                  <CollapsibleOutput content={message.content || '(no output)'} className="tool-bash-output" />
                </div>
              ) : isTree ? (
                <CollapsibleOutput content={message.content} className="tool-tree" />
              ) : isGitDiff ? (
                <>
                  {message.toolArgs && Object.keys(message.toolArgs).length > 0 && (
                    <div className="tool-meta">
                      {Object.entries(message.toolArgs as Record<string, unknown>).map(([k, v]) => (
                        <div key={k} className="tool-meta-row">
                          <span className="tool-meta-key">{k}</span>
                          <span className="tool-meta-val">{typeof v === 'string' ? v : JSON.stringify(v)}</span>
                        </div>
                      ))}
                    </div>
                  )}
                  <GitDiffView text={message.content} />
                </>
              ) : isRead ? (
                <>
                  {readPath && (
                    <div className="tool-meta">
                      <div className="tool-meta-row">
                        <span className="tool-meta-key">file</span>
                        <span className="tool-meta-val">{readPath}</span>
                      </div>
                    </div>
                  )}
                  <ReadResultView filePath={readPath} content={message.content} />
                </>
              ) : isGrep ? (
                <>
                  {grepPattern && (
                    <div className="tool-meta">
                      <div className="tool-meta-row">
                        <span className="tool-meta-key">pattern</span>
                        <span className="tool-meta-val">{grepPattern}</span>
                      </div>
                    </div>
                  )}
                  <GrepView content={message.content} pattern={grepPattern} />
                </>
              ) : isGlob ? (
                <>
                  {message.toolArgs && Object.keys(message.toolArgs).length > 0 && (
                    <div className="tool-meta">
                      {Object.entries(message.toolArgs as Record<string, unknown>).map(([k, v]) => (
                        <div key={k} className="tool-meta-row">
                          <span className="tool-meta-key">{k}</span>
                          <span className="tool-meta-val">{typeof v === 'string' ? v : JSON.stringify(v)}</span>
                        </div>
                      ))}
                    </div>
                  )}
                  <GlobView content={message.content} />
                </>
              ) : isEdit && editDiff ? (
                <>
                  {(message.toolArgs as any)?.file_path && (
                    <div className="tool-meta">
                      <div className="tool-meta-row">
                        <span className="tool-meta-key">file</span>
                        <span className="tool-meta-val">{String((message.toolArgs as any).file_path)}</span>
                      </div>
                    </div>
                  )}
                  <DiffView oldText={editDiff.oldText} newText={editDiff.newText} />
                  {message.content && (
                    <div className="tool-result-inline">{message.content}</div>
                  )}
                </>
              ) : isWrite && writeContent ? (
                <>
                  {(message.toolArgs as any)?.file_path && (
                    <div className="tool-meta">
                      <div className="tool-meta-row">
                        <span className="tool-meta-key">file</span>
                        <span className="tool-meta-val">{String((message.toolArgs as any).file_path)}</span>
                      </div>
                    </div>
                  )}
                  <DiffView oldText="" newText={writeContent} />
                  {message.content && (
                    <div className="tool-result-inline">{message.content}</div>
                  )}
                </>
              ) : (
                <>
                  {message.toolArgs && Object.keys(message.toolArgs).length > 0 && (
                    <div className="tool-meta">
                      {Object.entries(message.toolArgs as Record<string, unknown>).map(([k, v]) => (
                        <div key={k} className="tool-meta-row">
                          <span className="tool-meta-key">{k}</span>
                          <span className="tool-meta-val">{typeof v === 'string' ? v : JSON.stringify(v)}</span>
                        </div>
                      ))}
                    </div>
                  )}
                  <CollapsibleOutput content={message.content} className="tool-result" />
                </>
              )}
            </div>
          )}
        </div>
      </div>
    )
  }

  // Error messages get a special card
  const isError = message.role === 'assistant' && message.content.startsWith('Error:')
  if (isError) {
    const errorText = message.content.replace(/^Error:\s*/, '')
    const isMissingKey = errorText.toLowerCase().includes('api key') || errorText.toLowerCase().includes('key required')
    return (
      <div className={`message-row assistant ${focused ? 'focused' : ''}`} data-msg-id={message.id}>
        <div className="msg-avatar" style={{ background: 'rgba(248,113,113,0.15)', color: 'var(--error)' }}>
          <AlertTriangle size={16} />
        </div>
        <div className="message assistant">
          <div className="error-card">
            <div className="error-card-text">{errorText}</div>
            {isMissingKey && (
              <button className="error-card-action" onClick={() => {
                // Switch to settings tab
                window.dispatchEvent(new CustomEvent('gokin:open-settings'))
              }}>Go to Settings</button>
            )}
          </div>
        </div>
      </div>
    )
  }

  // User or assistant message with avatar
  const isUser = message.role === 'user'

  const handleCopy = (e: React.MouseEvent) => {
    e.stopPropagation()
    copyToClipboard(message.content).then(() => {
      setCopied(true)
      if (copyTimerRef.current) clearTimeout(copyTimerRef.current)
      copyTimerRef.current = setTimeout(() => setCopied(false), 1500)
    })
  }

  return (
    <div
      className={`message-row ${isUser ? 'user' : 'assistant'} ${focused ? 'focused' : ''}`}
      data-msg-id={message.id}
      onContextMenu={onContextMenu}
    >
      {!isUser && (
        <div className="msg-avatar assistant-avatar">
          <Bot size={16} />
        </div>
      )}
      <div className={`message ${message.role}`}>
        {!isUser && changedFiles && changedFiles.length > 0 && (
          <div className="changed-files">
            <Pencil size={11} />
            <span className="changed-files-label">
              Changed {changedFiles.length} file{changedFiles.length === 1 ? '' : 's'}:
            </span>
            <span className="changed-files-list">
              {changedFiles.slice(0, 8).map((f, i) => (
                <span key={f} className="changed-file">
                  {i > 0 && <span className="changed-sep">·</span>}
                  <button
                    className="mono changed-file-btn"
                    title={`Insert @${f} into the chat input`}
                    onClick={() => {
                      window.dispatchEvent(new CustomEvent('gokin:insert-file-ref', { detail: { path: f } }))
                    }}
                  >
                    {f}
                  </button>
                </span>
              ))}
              {changedFiles.length > 8 && (
                <span className="changed-sep">· +{changedFiles.length - 8} more</span>
              )}
            </span>
          </div>
        )}
        <div className="message-content">
          {isUser ? (
            editing ? (
              <div className="msg-edit">
                <textarea
                  ref={editTextareaRef}
                  className="msg-edit-input"
                  value={editDraft}
                  autoFocus
                  maxLength={100000}
                  onChange={(e) => setEditDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.nativeEvent.isComposing || e.keyCode === 229) return
                    if (e.key === 'Escape') { e.preventDefault(); setEditing(false) }
                    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                      e.preventDefault()
                      const v = editDraft.trim()
                      if (!v) return
                      setEditing(false)
                      onEditSubmit?.(v)
                    }
                  }}
                />
                <div className="msg-edit-actions">
                  <span className="msg-edit-hint">Ctrl+Enter to save, Esc to cancel</span>
                  <button className="btn-cancel-sm" onClick={() => setEditing(false)}>Cancel</button>
                  <button
                    className="btn-primary-sm"
                    disabled={!editDraft.trim() || editDraft.trim() === message.content.trim()}
                    onClick={() => {
                      const v = editDraft.trim()
                      if (!v) return
                      setEditing(false)
                      onEditSubmit?.(v)
                    }}
                  >Save &amp; re-send</button>
                </div>
              </div>
            ) : (
              <pre className="msg-text">{message.content}</pre>
            )
          ) : (
            <div className="md-content">{markdownElement}</div>
          )}
        </div>
        {!editing && (
          <div className="msg-footer">
            {!isUser && message.model && (
              <span
                className="msg-model"
                title={message.provider ? `Provider: ${message.provider}` : undefined}
              >
                {message.model}
              </span>
            )}
            {!isUser && message.durationMs && (
              <span className="msg-duration">{(message.durationMs / 1000).toFixed(1)}s</span>
            )}
            {!isUser && message.usage && (message.usage.totalInputTokens > 0 || message.usage.totalOutputTokens > 0) ? (
              <>
                <span
                  className="msg-duration"
                  title={
                    `Turn totals (provider reported, billed):\n` +
                    `  input:  ${message.usage.totalInputTokens.toLocaleString()}\n` +
                    `  output: ${message.usage.totalOutputTokens.toLocaleString()}\n` +
                    (message.usage.totalCacheReadTokens ? `  cache read:  ${message.usage.totalCacheReadTokens.toLocaleString()}\n` : '') +
                    (message.usage.totalCacheWriteTokens ? `  cache write: ${message.usage.totalCacheWriteTokens.toLocaleString()}\n` : '') +
                    (message.usage.lastInputTokens ? `Final context: ${message.usage.lastInputTokens.toLocaleString()} tokens` : '')
                  }
                >
                  {formatTokens(message.usage.totalInputTokens)} in / {formatTokens(message.usage.totalOutputTokens)} out
                </span>
                {message.usage.estimatedCostUSD !== undefined && message.usage.estimatedCostUSD > 0 && (
                  <span
                    className="msg-cost"
                    title={
                      `Approximate cost for this turn at published rates.\n` +
                      `Not authoritative billing — provider rounding, credits, and tier discounts are not applied.\n` +
                      `Pricing table is updated periodically; see internal/studio/pricing.go.`
                    }
                  >
                    ≈{formatCostUSD(message.usage.estimatedCostUSD)}
                  </span>
                )}
              </>
            ) : (!isUser && message.content && message.content.length > 200 && (
              <span className="msg-duration" title={`≈${Math.round(message.content.length / 4).toLocaleString()} tokens (estimated from chars÷4; provider didn't report usage)`}>
                ≈{formatTokens(Math.round(message.content.length / 4))} tok
              </span>
            ))}
            {message.timestamp > 0 && (
              <span
                className="msg-ts"
                title={new Date(message.timestamp).toLocaleString()}
              >
                {formatMsgTime(message.timestamp)}
              </span>
            )}
            <div className="msg-actions">
              {isUser && canEdit && onEditSubmit && (
                <button
                  className="msg-action-btn"
                  onClick={(e) => { e.stopPropagation(); setEditDraft(message.content); setEditing(true) }}
                  title="Edit & re-send (trims later history)"
                >
                  <Pencil size={12} />
                </button>
              )}
              {isUser && onRerun && (
                <button
                  className="msg-action-btn"
                  onClick={(e) => { e.stopPropagation(); onRerun() }}
                  title="Re-run from this message (trims later history)"
                >
                  <RotateCcw size={12} />
                </button>
              )}
              <button className="msg-action-btn" onClick={handleCopy} title="Copy">
                {copied ? <Check size={12} /> : <Copy size={12} />}
              </button>
            </div>
          </div>
        )}
      </div>
      {isUser && (
        <div className="msg-avatar user-avatar">
          <User size={16} />
        </div>
      )}
    </div>
  )
}

// Wrap MessageBubble in React.memo so the streaming-delta re-render of
// ChatPanel doesn't re-render 54 bubbles 30 times/sec. Custom equality
// ignores callback identity (the inline closures in the parent map always
// allocate fresh refs but they're functionally equivalent per-message), and
// does a shallow check on changedFiles which also gets re-allocated each
// render. Message, focus and canEdit are the props that actually flip.
const MessageBubble = React.memo(MessageBubbleInner, (prev, next) => {
  if (prev.message !== next.message) return false
  if (prev.focused !== next.focused) return false
  if (prev.canEdit !== next.canEdit) return false
  const a = prev.changedFiles || []
  const b = next.changedFiles || []
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false
  return true
})

function PlanToolCard({ message, focused, onContextMenu }: { message: ChatMessage; focused?: boolean; onContextMenu?: (e: React.MouseEvent) => void }) {
  const [expanded, setExpanded] = useState(true)

  // Try to parse the structured plan data from the tool result content.
  let planData: any = null
  try {
    planData = JSON.parse(message.content)
  } catch {
    // Content is not JSON -- fall back to text display
  }

  const toolLabel =
    message.toolName === 'enter_plan_mode' ? 'Plan Created' :
    message.toolName === 'update_plan_progress' ? 'Step Updated' :
    message.toolName === 'get_plan_status' ? 'Plan Status' :
    message.toolName === 'exit_plan_mode' ? 'Plan Completed' : 'Plan'

  const progress = planData?.progress ?? planData?.['progress%'] ?? null
  const completed = planData?.completed ?? 0
  const total = planData?.total ?? planData?.step_count ?? 0
  const title = planData?.title ?? planData?.step_title ?? ''
  const steps = planData?.steps as { title: string; status: string }[] | undefined

  return (
    <div className={`message tool ${focused ? 'focused' : ''}`} data-msg-id={message.id} onContextMenu={onContextMenu}>
      <div className="plan-card">
        <div className="plan-card-header" onClick={() => setExpanded(!expanded)}>
          <ListChecks size={14} className="plan-icon" />
          <span className="plan-label">{toolLabel}</span>
          {title && <span className="plan-title">{title}</span>}
          {progress !== null && (
            <span className="plan-progress-badge">{Math.round(progress)}%</span>
          )}
          <ChevronRight size={12} className={`tool-chevron ${expanded ? 'expanded' : ''}`} />
        </div>
        {expanded && (
          <div className="plan-card-body">
            {total > 0 && (
              <div className="plan-progress-bar-wrap">
                <div className="plan-progress-bar">
                  <div
                    className="plan-progress-fill"
                    style={{ width: `${total > 0 ? (completed / total) * 100 : 0}%` }}
                  />
                </div>
                <span className="plan-progress-text">{completed}/{total} steps</span>
              </div>
            )}
            {steps && steps.length > 0 && (
              <div className="plan-steps">
                {steps.map((step, i) => (
                  <div key={i} className={`plan-step ${step.status}`}>
                    {step.status === 'completed' ? (
                      <CheckCircle size={11} className="tool-ok" />
                    ) : step.status === 'in_progress' ? (
                      <Circle size={11} className="plan-step-active" />
                    ) : step.status === 'failed' ? (
                      <XCircle size={11} className="tool-fail" />
                    ) : (
                      <Circle size={11} className="plan-step-pending" />
                    )}
                    <span>{step.title}</span>
                  </div>
                ))}
              </div>
            )}
            {!steps && planData && (
              <pre className="tool-result">{JSON.stringify(planData, null, 2)}</pre>
            )}
            {!planData && (
              <pre className="tool-result">{message.content}</pre>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

function MemoryToolCard({ message, focused, onContextMenu }: { message: ChatMessage; focused?: boolean; onContextMenu?: (e: React.MouseEvent) => void }) {
  const [expanded, setExpanded] = useState(false)

  const action = (message.toolArgs as any)?.action || message.toolName || ''
  const key = (message.toolArgs as any)?.key || (message.toolArgs as any)?.query || ''

  const labelMap: Record<string, string> = {
    remember: 'Saved',
    recall: 'Recalled',
    forget: 'Forgotten',
    list: 'Listed',
    feedback: 'Feedback',
    read: 'Read',
    write: 'Written',
  }

  const toolLabel = message.toolName === 'pin_context' ? 'Context Pinned'
    : message.toolName === 'history_search' ? 'History Search'
    : message.toolName === 'shared_memory' ? `Shared Memory: ${labelMap[action] || action}`
    : message.toolName === 'memorize' ? 'Memorized'
    : message.toolName === 'update_scratchpad' ? 'Scratchpad Updated'
    : `Memory: ${labelMap[action] || action}`

  return (
    <div className={`message tool ${focused ? 'focused' : ''}`} data-msg-id={message.id} onContextMenu={onContextMenu}>
      <div className="memory-card">
        <div className="memory-card-header" onClick={() => setExpanded(!expanded)}>
          <Database size={12} className="memory-icon" />
          <span className="memory-label">{toolLabel}</span>
          {key && <span className="memory-key">{key}</span>}
          <ChevronRight size={12} className={`tool-chevron ${expanded ? 'expanded' : ''}`} />
        </div>
        {expanded && (
          <div className="memory-card-body">
            <pre className="tool-result">{message.content}</pre>
          </div>
        )}
      </div>
    </div>
  )
}
