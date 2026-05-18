import { useEffect } from 'react'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { useChatStore } from '../stores/chatStore'
import { useProjectStore } from '../stores/projectStore'
import { isProjectMuted } from '../lib/mutedProjects'

function notifyIfBlurred(title: string, body: string) {
  if (document.hasFocus()) return
  if ('Notification' in window && Notification.permission === 'granted') {
    new Notification(title, { body, icon: '/wails.png' })
  } else if ('Notification' in window && Notification.permission === 'default') {
    Notification.requestPermission()
  }
}

// Composite key for chat store: projectID_sessionID. Returns null when the
// event payload is missing projectID so callers can drop malformed events
// instead of silently corrupting the wrong chat session.
function chatKey(data: any): string | null {
  if (!data?.projectID) return null
  return data.projectID + '_' + (data.sessionID || 'default')
}

export function useWailsEvents() {
  useEffect(() => {
    if ('Notification' in window && Notification.permission === 'default') {
      Notification.requestPermission()
    }
  }, [])

  useEffect(() => {
    const cleanups = [
      EventsOn('chat:delta', (data: any) => {
        const key = chatKey(data); if (!key) return
        useChatStore.getState().appendStreamText(key, data.text)
      }),
      EventsOn('chat:thinking_delta', (data: any) => {
        const key = chatKey(data); if (!key) return
        useChatStore.getState().appendThinkingStream(key, data.text)
      }),
      EventsOn('chat:thinking', (data: any) => {
        const key = chatKey(data); if (!key) return
        useChatStore.getState().addThinking(key, data.text)
      }),
      EventsOn('chat:text', (data: any) => {
        const key = chatKey(data); if (!key) return
        const store = useChatStore.getState()
        const streamed = store.streaming[key] || ''
        // Always finalize: if we have streamed text use it, otherwise use
        // data.text (e.g. non-streaming provider or text-only event).
        store.finalizeAssistant(key, streamed || data.text)
      }),
      EventsOn('chat:tool_call', (data: any) => {
        const key = chatKey(data); if (!key) return
        useChatStore.getState().addToolCall(key, data.tool, data.args)
      }),
      EventsOn('chat:tool_result', (data: any) => {
        const key = chatKey(data); if (!key) return
        useChatStore.getState().addToolResult(key, data.tool, data.success, data.content)
      }),
      EventsOn('chat:usage', (data: any) => {
        const key = chatKey(data); if (!key) return
        useChatStore.getState().setCurrentUsage(key, {
          totalInputTokens: data.totalInputTokens || 0,
          totalOutputTokens: data.totalOutputTokens || 0,
          totalCacheReadTokens: data.totalCacheReadTokens || 0,
          totalCacheWriteTokens: data.totalCacheWriteTokens || 0,
          lastInputTokens: data.lastInputTokens || 0,
          lastOutputTokens: data.lastOutputTokens || 0,
          lastCacheReadTokens: data.lastCacheReadTokens || 0,
          lastCacheWriteTokens: data.lastCacheWriteTokens || 0,
        })
      }),
      EventsOn('chat:complete', (data: any) => {
        const key = chatKey(data); if (!key) return
        const store = useChatStore.getState()
        // Clear any lingering retry banner: even a successful retry can leave
        // the status set if the next stream chunk arrives as a tool call
        // (which doesn't flow through appendStreamText).
        store.setRetrying(key, null)
        // Clear any stale ask_user card. If the agent goroutine's ctx was
        // cancelled while the user was deliberating, the handler returned
        // without resolving the question — the card would otherwise linger.
        store.setAskUser(key, null)
        // Flush any thinking buffer that never got a final chat:thinking
        // close signal (e.g. provider returned text but no trailing thinking
        // close event) so it doesn't leak into the next turn's live chip.
        const pendingThink = store.thinkingStream[key] || ''
        if (pendingThink) store.addThinking(key, pendingThink)
        const streamed = store.streaming[key] || ''
        if (streamed) {
          store.finalizeAssistant(key, streamed)
        }

        const finalUsage = (data.inputTokens || data.outputTokens) ? {
          totalInputTokens: data.inputTokens || 0,
          totalOutputTokens: data.outputTokens || 0,
          totalCacheReadTokens: data.cacheReadTokens || 0,
          totalCacheWriteTokens: data.cacheWriteTokens || 0,
          lastInputTokens: data.lastInputTokens || 0,
          lastOutputTokens: data.lastOutputTokens || 0,
          lastCacheReadTokens: data.lastCacheReadTokens || 0,
          lastCacheWriteTokens: data.lastCacheWriteTokens || 0,
          estimatedCostUSD: data.estimatedCostUSD || 0,
        } : null
        store.finalizeUsage(key, finalUsage)

        // Attach duration + usage + model info to the last assistant message
        // so it can show real numbers and which backend answered in its footer
        // instead of the char/4 estimate and nothing else.
        if (data.durationMs || finalUsage || data.model || data.provider) {
          const msgs = store.messages[key] || []
          for (let i = msgs.length - 1; i >= 0; i--) {
            if (msgs[i].role === 'assistant') {
              const updated = [...msgs]
              updated[i] = {
                ...updated[i],
                durationMs: data.durationMs || updated[i].durationMs,
                usage: finalUsage || updated[i].usage,
                model: data.model || updated[i].model,
                provider: data.provider || updated[i].provider,
              }
              store.setMessages(key, updated)
              break
            }
          }
        }

        store.setSessionActive(key, false)
        // Per-project active flag is derived from sessionActive in Sidebar
        // and StatusBar, so we don't need to (and shouldn't) clear the project
        // flag here — another session might still be running.

        // Bump the per-tab unread count if the user isn't currently viewing
        // this exact session. The badge clears when they switch to it. Two
        // signals say "they're viewing it": (1) the session is the active
        // tab in its project, AND (2) the project is the active project,
        // AND (3) the window is focused. Any miss → bump.
        const activeProj = useProjectStore.getState().activeProjectId
        const activeSid = store.activeSession[data.projectID || '']
        const sessionMatches = activeProj === data.projectID && activeSid === (data.sessionID || 'default')
        if ((!sessionMatches || !document.hasFocus()) && !isProjectMuted(data.projectID)) {
          store.bumpUnread(key)
        }

        // Sync pinned context from the completed turn so the header pin badge
        // reflects what the agent pinned without a separate GetProject call.
        if (data.projectID) {
          useProjectStore.getState().updateProject(data.projectID, {
            pinnedContext: data.pinnedContext || '',
          })
        }

        const project = useProjectStore.getState().projects.find((p) => p.id === data.projectID)
        const name = project?.name || 'Project'
        const preview = (data.text || '').slice(0, 80)
        // Iter 680+: respect per-project mute (iter 580+) — silencing a
        // project means silencing OS notifications too, not just toasts +
        // unread badges + chime. The OS notification fires from a different
        // gate (window-blurred), but the same mute should apply.
        if (!isProjectMuted(data.projectID)) {
          notifyIfBlurred(`${name} -- done`, preview || 'Agent finished.')
        }
      }),
      EventsOn('chat:error', (data: any) => {
        const key = chatKey(data); if (!key) return
        const store = useChatStore.getState()
        store.setRetrying(key, null)
        // Same stale-card protection as chat:complete — a fatal error aborts
        // the agent before it can resolve a pending ask_user question.
        store.setAskUser(key, null)
        // Flush the reasoning buffer into a thinking message so the user
        // still sees what the model was thinking when it blew up. Without
        // this, a crash mid-reasoning left a ghost "Reasoning..." chip
        // that never resolved and polluted the next turn.
        const pendingThink = store.thinkingStream[key] || ''
        if (pendingThink) store.addThinking(key, pendingThink)
        // Always render errors with a single "Error: " prefix so the error-card
        // branch in MessageBubble picks them up. Strip any existing "Error: "
        // the backend may already have added via humanizeAPIError before
        // re-adding our own, so we don't end up with doubled prefixes.
        let text = String(data.text || '')
        text = text.replace(/^Error:\s*/i, '')
        store.finalizeAssistant(key, `Error: ${text}`)
        store.setSessionActive(key, false)
        // Don't touch project.active — it's derived from sessionActive elsewhere.
        // Bump unread for background sessions so the user sees that something
        // failed elsewhere and can investigate. Same gate as chat:complete.
        const activeProj = useProjectStore.getState().activeProjectId
        const activeSid = store.activeSession[data.projectID || '']
        const sessionMatches = activeProj === data.projectID && activeSid === (data.sessionID || 'default')
        if ((!sessionMatches || !document.hasFocus()) && !isProjectMuted(data.projectID)) {
          store.bumpUnread(key)
        }
      }),
      EventsOn('chat:ask_user', (data: any) => {
        const key = chatKey(data); if (!key) return
        const store = useChatStore.getState()
        store.setAskUser(key, {
          questionID: data.questionID,
          question: data.question,
          options: data.options || [],
          default: data.default || '',
          askedAt: Date.now(),
        })
        // Bump unread when the agent asks for input in a session the user
        // isn't currently viewing — otherwise the agent silently blocks
        // forever and the user has no idea something needs their attention.
        // Same active-tab gate as chat:complete / chat:error.
        const activeProj = useProjectStore.getState().activeProjectId
        const activeSid = store.activeSession[data.projectID || '']
        const sessionMatches = activeProj === data.projectID && activeSid === (data.sessionID || 'default')
        if ((!sessionMatches || !document.hasFocus()) && !isProjectMuted(data.projectID)) {
          store.bumpUnread(key)
        }
      }),
      EventsOn('chat:retry', (data: any) => {
        const key = chatKey(data); if (!key) return
        useChatStore.getState().setRetrying(key, {
          attempt: data.attempt,
          max: data.max,
          delayMs: data.delayMs,
          reason: data.reason,
          startedAt: Date.now(),
        })
      }),
      EventsOn('session:renamed', (data: any) => {
        // App.tsx owns the sessions list; emit a DOM event it can listen to.
        window.dispatchEvent(new CustomEvent('gokin:session-renamed', { detail: data }))
      }),
      EventsOn('project:status', (data: any) => {
        const active = data.status === 'active'
        if (data.sessionID) {
          const key = data.id + '_' + data.sessionID
          useChatStore.getState().setSessionActive(key, active)
        }
        // Bump lastUsedAt when a turn starts so the sidebar re-sorts without
        // a full reload; the backend also persists the bump for next startup.
        if (active) {
          useProjectStore.getState().bumpLastUsed(data.id)
        }
        // Refresh git branch whenever an agent turn ends (branch may have
        // changed if the agent ran git checkout / git switch).
        if (!active && data.gitBranch !== undefined) {
          useProjectStore.getState().updateProject(data.id, { gitBranch: data.gitBranch })
        }
      }),
      EventsOn('dispatch:complete', (data: any) => {
        if (!data?.from) return
        const fromKey = data.from + '_' + (data.sessionID || 'default')
        const toProjectName = data.toName || data.to || 'unknown'
        const success = data.success !== false
        const content = success
          ? (data.result || 'Dispatch completed.')
          : (data.error || 'Dispatch failed.')
        useChatStore.getState().addDispatchResult(fromKey, toProjectName, success, content)
        // Desktop notification so the user doesn't miss the result if they
        // moved away from the source chat while the dispatch was in flight.
        // Mute applies to the SOURCE project (the one the user originated
        // the dispatch from) — that's the project they said "don't ping me".
        const title = success
          ? `Dispatch to ${toProjectName} finished`
          : `Dispatch to ${toProjectName} failed`
        if (!isProjectMuted(data.from)) {
          notifyIfBlurred(title, content.slice(0, 120))
        }
      }),
    ]

    return () => {
      cleanups.forEach((cancel) => cancel())
    }
  }, [])
}
