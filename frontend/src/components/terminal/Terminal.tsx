import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useProjectStore } from '../../stores/projectStore'
import { useSettingsStore } from '../../stores/settingsStore'
import { EventsOn } from '../../../wailsjs/runtime/runtime'
import { OpenTerminal, WriteTerminal, ResizeTerminal, CloseTerminal } from '../../../wailsjs/go/studio/Studio'
import { TerminalSquare } from 'lucide-react'

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

export function TerminalPanel() {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const activeProject = useProjectStore((s) => s.projects.find((p) => p.id === s.activeProjectId))
  const currentTheme = useSettingsStore((s) => s.settings.theme)
  const termRef = useRef<HTMLDivElement>(null)
  const xtermRef = useRef<XTerm | null>(null)

  // Apply theme changes to an existing terminal without recreating the PTY.
  useEffect(() => {
    const xterm = xtermRef.current
    if (!xterm) return
    xterm.options.theme = currentTheme !== 'light' ? DARK_TERM_THEME : LIGHT_TERM_THEME
  }, [currentTheme])

  useEffect(() => {
    if (!activeProjectId || !termRef.current) return

    // Clear any previous terminal content
    termRef.current.innerHTML = ''

    const isDark = currentTheme !== 'light'
    const xterm = new XTerm({
      theme: isDark ? DARK_TERM_THEME : LIGHT_TERM_THEME,
      fontFamily: '"Cascadia Code", "JetBrains Mono", "Fira Code", "Consolas", monospace',
      fontSize: 14,
      cursorBlink: true,
      convertEol: true,
    })
    xtermRef.current = xterm
    const fitAddon = new FitAddon()
    xterm.loadAddon(fitAddon)
    xterm.open(termRef.current)

    // Delay fit to ensure container has dimensions
    requestAnimationFrame(() => {
      try { fitAddon.fit() } catch {}
    })

    let currentTermId: string | null = null
    let disposed = false

    // Open PTY
    OpenTerminal(activeProjectId).then((id) => {
      if (disposed) {
        CloseTerminal(id).catch(() => {})
        return
      }
      currentTermId = id
      try {
        fitAddon.fit()
        const { cols, rows } = xterm
        ResizeTerminal(id, cols, rows).catch(() => {})
      } catch {}
    }).catch((err) => {
      xterm.writeln(`\x1b[31mFailed to open terminal: ${err}\x1b[0m`)
    })

    // User input -> Go backend
    const inputDisposable = xterm.onData((data) => {
      if (currentTermId) {
        WriteTerminal(currentTermId, data).catch(() => {})
      }
    })

    // PTY output -> xterm
    const cancelOutput = EventsOn('terminal:output', (ev: any) => {
      if (ev.id === currentTermId) {
        xterm.write(ev.data)
      }
    })

    const cancelExit = EventsOn('terminal:exit', (ev: any) => {
      if (ev.id === currentTermId) {
        xterm.writeln('\r\n\x1b[90m[Process exited]\x1b[0m')
      }
    })

    // Handle container resize
    const resizeObserver = new ResizeObserver(() => {
      try {
        fitAddon.fit()
        if (currentTermId) {
          const { cols, rows } = xterm
          ResizeTerminal(currentTermId, cols, rows).catch(() => {})
        }
      } catch {}
    })
    resizeObserver.observe(termRef.current)

    return () => {
      disposed = true
      xtermRef.current = null
      inputDisposable.dispose()
      cancelOutput()
      cancelExit()
      resizeObserver.disconnect()
      if (currentTermId) {
        CloseTerminal(currentTermId).catch(() => {})
      }
      xterm.dispose()
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeProjectId])

  if (!activeProjectId) {
    return null
  }

  return (
    <div className="terminal-panel">
      <div className="terminal-header">
        <TerminalSquare size={14} />
        <span>Terminal</span>
        {activeProject && (
          <span className="terminal-path">{activeProject.directory}</span>
        )}
      </div>
      <div className="terminal-container" ref={termRef} />
    </div>
  )
}
