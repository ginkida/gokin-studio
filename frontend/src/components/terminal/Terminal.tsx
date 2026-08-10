import { useEffect, useRef, useState } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useProjectStore } from '../../stores/projectStore'
import { EventsOn } from '../../../wailsjs/runtime/runtime'
import { OpenSessionTerminalAt, WriteTerminal, ResizeTerminal, CloseTerminal } from '../../../wailsjs/go/studio/Studio'
import { AlertTriangle, Loader2, Plus, RotateCcw, TerminalSquare, X } from 'lucide-react'
import { appliedTheme, type ResolvedTheme } from '../../lib/theme'

const DARK_TERM_THEME = {
  background: '#111113',
  foreground: '#e8e8ed',
  cursor: '#a0a0ab',
  selectionBackground: '#32323d',
}

const LIGHT_TERM_THEME = {
  background: '#ffffff',
  foreground: '#1a1a2e',
  cursor: '#5a5a72',
  selectionBackground: '#dcdce4',
}

type TerminalStatus = 'connecting' | 'ready' | 'exited' | 'error'

interface TerminalTab {
  id: string
  path: string
}

interface TerminalTabState {
  status: TerminalStatus
  message?: string
}

let terminalTabSequence = 0

function createTerminalTab(path = ''): TerminalTab {
  terminalTabSequence += 1
  return { id: `terminal-tab-${terminalTabSequence}`, path }
}

function terminalTabLabel(path: string): string {
  const normalized = path.replace(/\\/g, '/').replace(/\/+$/, '')
  if (!normalized || normalized === '.') return 'root'
  return normalized.split('/').pop() || normalized
}

function terminalError(error: unknown): string {
  return String((error as any)?.message || error || 'unknown terminal error')
    .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, '?')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, 300)
}

export function TerminalPanel({
  sessionId,
  worktreePath,
  onClose,
  requestedPath,
  requestKey = 0,
}: {
  sessionId?: string
  worktreePath?: string
  onClose?: () => void
  requestedPath?: string
  requestKey?: number
}) {
  const activeProjectId = useProjectStore((state) => state.activeProjectId)
  const activeProject = useProjectStore((state) => state.projects.find((project) => project.id === state.activeProjectId))
  const initialTabRef = useRef<TerminalTab | null>(null)
  if (!initialTabRef.current) initialTabRef.current = createTerminalTab(requestKey > 0 ? (requestedPath || '') : '')
  const [tabs, setTabs] = useState<TerminalTab[]>([initialTabRef.current])
  const [activeTabID, setActiveTabID] = useState(initialTabRef.current.id)
  const [tabStates, setTabStates] = useState<Record<string, TerminalTabState>>({})
  const [restartKeys, setRestartKeys] = useState<Record<string, number>>({})
  const handledRequestRef = useRef(requestKey)

  const addTab = (path = '') => {
    const tab = createTerminalTab(path)
    setTabs((current) => [...current, tab])
    setActiveTabID(tab.id)
  }

  useEffect(() => {
    if (!requestKey || handledRequestRef.current === requestKey) return
    handledRequestRef.current = requestKey
    addTab(requestedPath || '')
    // requestKey is the identity; requestedPath is payload for that request.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [requestKey])

  const closeTab = (id: string) => {
    setTabs((current) => {
      const index = current.findIndex((tab) => tab.id === id)
      if (index < 0) return current
      const next = current.filter((tab) => tab.id !== id)
      if (next.length === 0) {
        requestAnimationFrame(() => onClose?.())
        return next
      }
      if (activeTabID === id) setActiveTabID(next[Math.min(index, next.length - 1)].id)
      return next
    })
    setTabStates((current) => {
      const next = { ...current }
      delete next[id]
      return next
    })
  }

  if (!activeProjectId) return null
  const activeTab = tabs.find((tab) => tab.id === activeTabID) || tabs[0]
  const activeState = activeTab ? tabStates[activeTab.id] : undefined
  const activeAbsolutePath = activeTab?.path
    ? `${(worktreePath || activeProject?.directory || '').replace(/[\\/]$/, '')}/${activeTab.path}`
    : (worktreePath || activeProject?.directory || '')

  return (
    <div className="terminal-panel">
      <div className="terminal-header">
        <TerminalSquare size={14} />
        <div className="terminal-tabs" role="tablist" aria-label="Terminal tabs">
          {tabs.map((tab, tabIndex) => {
            const state = tabStates[tab.id]?.status || 'connecting'
            return (
              <div className={`terminal-tab ${tab.id === activeTabID ? 'active' : ''}`} key={tab.id}>
                <button
                  id={`terminal-tab-trigger-${tab.id}`}
                  type="button"
                  role="tab"
                  aria-selected={tab.id === activeTabID}
                  aria-controls={`terminal-tab-panel-${tab.id}`}
                  tabIndex={tab.id === activeTabID ? 0 : -1}
                  title={tab.path || 'Session worktree root'}
                  onClick={() => setActiveTabID(tab.id)}
                  onKeyDown={(event) => {
                    if (event.key === 'Delete') {
                      event.preventDefault()
                      closeTab(tab.id)
                      return
                    }
                    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
                    event.preventDefault()
                    const nextIndex = event.key === 'Home' ? 0
                      : event.key === 'End' ? tabs.length - 1
                        : event.key === 'ArrowRight' ? (tabIndex + 1) % tabs.length
                          : (tabIndex - 1 + tabs.length) % tabs.length
                    const next = tabs[nextIndex]
                    setActiveTabID(next.id)
                    requestAnimationFrame(() => document.getElementById(`terminal-tab-trigger-${next.id}`)?.focus())
                  }}
                >
                  <span className={`terminal-tab-dot ${state}`} />
                  <span>{terminalTabLabel(tab.path)}</span>
                </button>
                <button type="button" className="terminal-tab-close" onClick={() => closeTab(tab.id)} title="Close terminal tab" aria-label={`Close ${terminalTabLabel(tab.path)} terminal`}><X size={10} /></button>
              </div>
            )
          })}
        </div>
        <button className="terminal-header-action icon-only" type="button" onClick={() => addTab('')} title="New terminal tab" aria-label="New terminal tab"><Plus size={12} /></button>
        {activeState && (
          <span className={`terminal-status ${activeState.status}`} role="status" title={activeState.message}>
            {activeState.status === 'connecting' ? <><Loader2 size={10} className="spin" /> Connecting</>
              : activeState.status === 'ready' ? 'Ready'
                : activeState.status === 'exited' ? 'Exited'
                  : <><AlertTriangle size={10} /> Error</>}
          </span>
        )}
        {activeTab && (activeState?.status === 'exited' || activeState?.status === 'error') && (
          <button className="terminal-header-action" type="button" onClick={() => setRestartKeys((current) => ({ ...current, [activeTab.id]: (current[activeTab.id] || 0) + 1 }))} title="Restart this terminal"><RotateCcw size={12} /> Restart</button>
        )}
        <span className="terminal-path" title={activeAbsolutePath}>{activeAbsolutePath}</span>
        {onClose && <button className="terminal-header-action icon-only" type="button" onClick={onClose} title="Close terminal pane" aria-label="Close terminal pane"><X size={12} /></button>}
      </div>
      <div className="terminal-tab-panels">
        {tabs.map((tab) => (
          <TerminalInstance
            key={tab.id}
            sessionId={sessionId || 'default'}
            tabID={tab.id}
            path={tab.path}
            active={tab.id === activeTabID}
            restartKey={restartKeys[tab.id] || 0}
            onState={(state) => setTabStates((current) => {
              const previous = current[tab.id]
              if (previous?.status === state.status && previous?.message === state.message) return current
              return { ...current, [tab.id]: state }
            })}
          />
        ))}
      </div>
    </div>
  )
}

function TerminalInstance({
  sessionId,
  tabID,
  path,
  active,
  restartKey,
  onState,
}: {
  sessionId: string
  tabID: string
  path: string
  active: boolean
  restartKey: number
  onState: (state: TerminalTabState) => void
}) {
  const activeProjectId = useProjectStore((state) => state.activeProjectId)
  const [currentTheme, setCurrentTheme] = useState<ResolvedTheme>(appliedTheme)
  const termRef = useRef<HTMLDivElement>(null)
  const xtermRef = useRef<XTerm | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const onStateRef = useRef(onState)
  onStateRef.current = onState

  useEffect(() => {
    const root = document.documentElement
    const observer = new MutationObserver(() => setCurrentTheme(appliedTheme()))
    observer.observe(root, { attributes: true, attributeFilter: ['data-theme'] })
    setCurrentTheme(appliedTheme())
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    const xterm = xtermRef.current
    if (xterm) xterm.options.theme = currentTheme === 'dark' ? DARK_TERM_THEME : LIGHT_TERM_THEME
  }, [currentTheme])

  useEffect(() => {
    if (!active) return
    requestAnimationFrame(() => {
      try { fitAddonRef.current?.fit(); xtermRef.current?.focus() } catch {}
    })
  }, [active])

  useEffect(() => {
    if (!activeProjectId || !termRef.current) return
    onStateRef.current({ status: 'connecting' })
    termRef.current.innerHTML = ''

    const xterm = new XTerm({
      theme: currentTheme === 'dark' ? DARK_TERM_THEME : LIGHT_TERM_THEME,
      fontFamily: '"Cascadia Code", "JetBrains Mono", "Fira Code", "Consolas", monospace',
      fontSize: 14,
      cursorBlink: true,
      convertEol: true,
      disableStdin: true,
    })
    xtermRef.current = xterm
    const fitAddon = new FitAddon()
    fitAddonRef.current = fitAddon
    xterm.loadAddon(fitAddon)
    xterm.open(termRef.current)

    let currentTermId: string | null = null
    let disposed = false
    let opening = true
    let resizeNoticeShown = false
    const preOpenOutput = new Map<string, string>()
    const exitedBeforeReady = new Set<string>()

    const setInputEnabled = (enabled: boolean) => { xterm.options.disableStdin = !enabled }
    const markExited = (id: string) => {
      if (disposed) return
      if (currentTermId === id) currentTermId = null
      setInputEnabled(false)
      onStateRef.current({ status: 'exited' })
      xterm.writeln('\r\n\x1b[90m[Process exited — restart this tab to continue]\x1b[0m')
    }
    const failTransport = (operation: string, id: string, cause: unknown) => {
      if (disposed || currentTermId !== id) return
      const message = `${operation}: ${terminalError(cause)}`
      currentTermId = null
      setInputEnabled(false)
      onStateRef.current({ status: 'error', message })
      xterm.writeln(`\r\n\x1b[31m[${message}]\x1b[0m`)
      CloseTerminal(id).catch(() => {})
    }
    const resize = (id: string) => {
      if (xterm.cols < 1 || xterm.rows < 1) return
      ResizeTerminal(id, xterm.cols, xterm.rows).catch((cause) => {
        if (disposed || currentTermId !== id || resizeNoticeShown) return
        resizeNoticeShown = true
        const message = `Resize warning: ${terminalError(cause)}`
        onStateRef.current({ status: 'ready', message })
        xterm.writeln(`\r\n\x1b[33m[${message}]\x1b[0m`)
      })
    }

    const cancelOutput = EventsOn('terminal:output', (event: any) => {
      if (!event?.id || typeof event.data !== 'string') return
      if (event.id === currentTermId) { xterm.write(event.data); return }
      if (!opening) return
      if (!preOpenOutput.has(event.id) && preOpenOutput.size >= 8) {
        const oldest = preOpenOutput.keys().next().value
        if (oldest) preOpenOutput.delete(oldest)
      }
      preOpenOutput.set(event.id, ((preOpenOutput.get(event.id) || '') + event.data).slice(-64 * 1024))
    })
    const cancelExit = EventsOn('terminal:exit', (event: any) => {
      if (!event?.id) return
      if (event.id === currentTermId) markExited(event.id)
      else if (opening) exitedBeforeReady.add(event.id)
    })

    OpenSessionTerminalAt(activeProjectId, sessionId, path).then((id) => {
      opening = false
      if (disposed) { CloseTerminal(id).catch(() => {}); return }
      const buffered = preOpenOutput.get(id)
      preOpenOutput.clear()
      if (buffered) xterm.write(buffered)
      if (exitedBeforeReady.has(id)) { markExited(id); return }
      currentTermId = id
      setInputEnabled(true)
      onStateRef.current({ status: 'ready' })
      try { fitAddon.fit(); resize(id) } catch {}
    }).catch((cause) => {
      opening = false
      if (disposed) return
      const message = `Open failed: ${terminalError(cause)}`
      setInputEnabled(false)
      onStateRef.current({ status: 'error', message })
      xterm.writeln(`\x1b[31m[${message}]\x1b[0m`)
    })

    const inputDisposable = xterm.onData((data) => {
      if (currentTermId) {
        const id = currentTermId
        WriteTerminal(id, data).catch((cause) => failTransport('Input failed', id, cause))
      }
    })
    const resizeObserver = new ResizeObserver(() => {
      if (!termRef.current || termRef.current.clientWidth === 0 || termRef.current.clientHeight === 0) return
      try { fitAddon.fit(); if (currentTermId) resize(currentTermId) } catch {}
    })
    resizeObserver.observe(termRef.current)

    return () => {
      disposed = true
      xtermRef.current = null
      fitAddonRef.current = null
      inputDisposable.dispose()
      cancelOutput()
      cancelExit()
      resizeObserver.disconnect()
      if (currentTermId) CloseTerminal(currentTermId).catch(() => {})
      xterm.dispose()
    }
    // Theme changes update xterm in place; active only controls visibility.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeProjectId, sessionId, path, restartKey])

  return <div id={`terminal-tab-panel-${tabID}`} className={`terminal-instance ${active ? 'active' : ''}`} role="tabpanel" aria-labelledby={`terminal-tab-trigger-${tabID}`} hidden={!active}><div className="terminal-container" ref={termRef} /></div>
}
