import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { FolderOpen, MessageSquare, Settings, FolderTree, Plus, Trash2, Search, Database, PanelLeftClose, CheckCircle, Bot, Sparkles, PanelsTopLeft, Monitor, TerminalSquare, LayoutDashboard, FileDiff, ListChecks, CalendarClock, Crosshair } from 'lucide-react'
import { useProjectStore } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'

type Action = {
  id: string
  label: string
  hint?: string
  icon: JSX.Element
  group: string
  onSelect: () => void
}

type Props = {
  onClose: () => void
  onSwitchProject: (id: string) => void
  onOpenSettings: () => void
  onOpenFiles: () => void
  onOpenArtifacts: () => void
  onNewChat: () => void
  onClearChat: () => void
  onOpenMemory: () => void
  onToggleSidebar: () => void
}

export function CommandPalette({ onClose, onSwitchProject, onOpenSettings, onOpenFiles, onOpenArtifacts, onNewChat, onClearChat, onOpenMemory, onToggleSidebar }: Props) {
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(0)
  const listRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const paletteRef = useRef<HTMLDivElement>(null)
  const returnFocusRef = useRef<HTMLElement | null>(
    document.activeElement instanceof HTMLElement ? document.activeElement : null,
  )

  const actions = useMemo<Action[]>(() => {
    const projectActions: Action[] = projects.map((p) => ({
      id: `project:${p.id}`,
      label: p.name,
      hint: p.directory,
      icon: <FolderOpen size={14} />,
      group: p.id === activeProjectId ? 'Active project' : 'Projects',
      onSelect: () => { onSwitchProject(p.id); onClose() },
    }))

    const miscActions: Action[] = [
      {
        id: 'new-chat',
        label: 'New chat',
        hint: 'Ctrl+N',
        icon: <Plus size={14} />,
        group: 'Actions',
        onSelect: () => { onNewChat(); onClose() },
      },
      {
        id: 'clear-chat',
        label: 'Clear current chat',
        hint: 'Ctrl+L',
        icon: <Trash2 size={14} />,
        group: 'Actions',
        onSelect: () => { onClearChat(); onClose() },
      },
      {
        id: 'open-memory',
        label: 'View project memory',
        hint: '/memory',
        icon: <Database size={14} />,
        group: 'Actions',
        onSelect: () => { onOpenMemory(); onClose() },
      },
      {
        id: 'global-search',
        label: 'Search across all sessions',
        hint: 'Ctrl+Shift+F',
        icon: <Search size={14} />,
        group: 'Actions',
        onSelect: () => {
          // Make sure we're on the chat tab so the modal renders, then open
          // the global search via a custom event the ChatPanel listens for.
          window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: 'chat' }))
          window.dispatchEvent(new CustomEvent('gokin:open-global-search'))
          onClose()
        },
      },
      {
        id: 'open-files',
        label: 'Open file browser',
        hint: 'Ctrl+2',
        icon: <FolderTree size={14} />,
        group: 'Navigate',
        onSelect: () => { onOpenFiles(); onClose() },
      },
      {
        id: 'open-settings',
        label: 'Open settings',
        hint: 'Ctrl/Cmd+, · Ctrl+3',
        icon: <Settings size={14} />,
        group: 'Navigate',
        onSelect: () => { onOpenSettings(); onClose() },
      },
      {
        id: 'open-artifacts',
        label: 'Open artifacts library',
        hint: 'Ctrl+4',
        icon: <PanelsTopLeft size={14} />,
        group: 'Navigate',
        onSelect: () => { onOpenArtifacts(); onClose() },
      },
      {
        id: 'toggle-sidebar',
        label: 'Toggle sidebar',
        hint: 'Ctrl+B',
        icon: <PanelLeftClose size={14} />,
        group: 'Navigate',
        onSelect: () => { onToggleSidebar(); onClose() },
      },
      {
        id: 'toggle-live-preview',
        label: 'Toggle live app preview',
        hint: 'Ctrl+Shift+B',
        icon: <Monitor size={14} />,
        group: 'Navigate',
        onSelect: () => {
          onClose()
          requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:toggle-live-preview')))
        },
      },
      {
        id: 'select-preview-element',
        label: 'Select element in app preview',
        hint: 'Ctrl+Shift+S',
        icon: <Crosshair size={14} />,
        group: 'Actions',
        onSelect: () => {
          onClose()
          requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:select-preview-element')))
        },
      },
      {
        id: 'open-workspace-terminal',
        label: 'Toggle terminal pane',
        hint: 'Ctrl+`',
        icon: <TerminalSquare size={14} />,
        group: 'Workspace panes',
        onSelect: () => {
          onClose()
          requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:toggle-workspace-terminal')))
        },
      },
      {
        id: 'open-workspace-diff',
        label: 'Toggle diff pane',
        hint: 'Ctrl+Shift+D',
        icon: <FileDiff size={14} />,
        group: 'Workspace panes',
        onSelect: () => {
          onClose()
          requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:toggle-diff-pane')))
        },
      },
      {
        id: 'open-workspace-plan',
        label: 'Open plan pane',
        icon: <ListChecks size={14} />,
        group: 'Workspace panes',
        onSelect: () => {
          onClose()
          requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:open-workspace-pane', { detail: 'plan' })))
        },
      },
      {
        id: 'open-workspace-tasks',
        label: 'Open scheduled tasks pane',
        icon: <CalendarClock size={14} />,
        group: 'Workspace panes',
        onSelect: () => {
          onClose()
          requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:open-workspace-pane', { detail: 'tasks' })))
        },
      },
      {
        id: 'open-workspace-files',
        label: 'Open files beside chat',
        icon: <FolderTree size={14} />,
        group: 'Workspace panes',
        onSelect: () => {
          onClose()
          requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:open-workspace-pane', { detail: 'files' })))
        },
      },
      {
        id: 'open-workspace-artifacts',
        label: 'Open artifacts beside chat',
        icon: <PanelsTopLeft size={14} />,
        group: 'Workspace panes',
        onSelect: () => {
          onClose()
          requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:open-workspace-pane', { detail: 'artifacts' })))
        },
      },
      {
        id: 'open-workspace-context',
        label: 'Open context pane',
        hint: 'Ctrl+J',
        icon: <LayoutDashboard size={14} />,
        group: 'Workspace panes',
        onSelect: () => {
          onClose()
          requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:open-workspace-pane', { detail: 'context' })))
        },
      },
      {
        id: 'focus-sidebar-search',
        label: 'Search projects in sidebar',
        hint: 'Ctrl+Shift+G',
        icon: <PanelLeftClose size={14} />,
        group: 'Navigate',
        onSelect: () => {
          onClose()
          // Let the sidebar reveal the search even when it is collapsed or
          // normally hidden for a short project list, then own the focus.
          requestAnimationFrame(() => {
            window.dispatchEvent(new CustomEvent('gokin:focus-project-search'))
          })
        },
      },
      {
        id: 'mark-all-read',
        label: 'Mark all sessions as read',
        icon: <CheckCircle size={14} />,
        group: 'Actions',
        onSelect: () => {
          if (activeProjectId) useChatStore.getState().clearAllUnreadForProject(activeProjectId)
          onClose()
        },
      },
      {
        id: 'switch-model',
        label: 'Switch model',
        hint: 'Ctrl+M',
        icon: <Bot size={14} />,
        group: 'Actions',
        onSelect: () => {
          onClose()
          // Defer one tick so the palette unmount finishes before dispatch.
          requestAnimationFrame(() => {
            // The chat panel listens for window keydown; synthesize Ctrl+M
            // would be brittle. Instead dispatch a custom event the panel
            // can listen for.
            window.dispatchEvent(new CustomEvent('gokin:open-model-switcher'))
          })
        },
      },
      {
        id: 'show-onboarding',
        label: 'Show first-run setup wizard',
        icon: <Sparkles size={14} />,
        group: 'Actions',
        onSelect: () => {
          onClose()
          try { localStorage.removeItem('gokin:onboarding-dismissed') } catch { /* unavailable */ }
          // Dispatch event so App.tsx can flip the showOnboarding state
          // without requiring a full reload.
          window.dispatchEvent(new CustomEvent('gokin:show-onboarding'))
        },
      },
    ]

    return [...projectActions, ...miscActions]
  }, [projects, activeProjectId, onSwitchProject, onOpenSettings, onOpenFiles, onOpenArtifacts, onNewChat, onClearChat, onOpenMemory, onToggleSidebar, onClose])

  const q = query.trim().toLowerCase()
  const filtered = useMemo(() => {
    if (!q) return actions
    return actions.filter((a) =>
      a.label.toLowerCase().includes(q) ||
      (a.hint ? a.hint.toLowerCase().includes(q) : false)
    )
  }, [actions, q])

  // Reset selection when query changes or results change size
  useLayoutEffect(() => {
    setSelected(0)
  }, [q])

  useLayoutEffect(() => {
    if (selected >= filtered.length) setSelected(Math.max(0, filtered.length - 1))
  }, [filtered.length, selected])

  useEffect(() => {
    inputRef.current?.focus()
    return () => {
      const target = returnFocusRef.current
      requestAnimationFrame(() => { if (target?.isConnected) target.focus() })
    }
  }, [])

  // Keep selected item in view
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-idx="${selected}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [selected])

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      if (filtered.length > 0) setSelected((s) => Math.min(s + 1, filtered.length - 1))
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      if (filtered.length > 0) setSelected((s) => Math.max(s - 1, 0))
      return
    }
    if (e.key === 'Home') {
      e.preventDefault()
      if (filtered.length > 0) setSelected(0)
      return
    }
    if (e.key === 'End') {
      e.preventDefault()
      if (filtered.length > 0) setSelected(filtered.length - 1)
      return
    }
    if (e.key === 'Enter') {
      e.preventDefault()
      const a = filtered[selected]
      if (a) a.onSelect()
      return
    }
  }

  // Group items for display
  let lastGroup = ''

  return (
    <div className="palette-backdrop" onMouseDown={onClose}>
      <div
        ref={paletteRef}
        className="palette"
        onMouseDown={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.preventDefault()
            event.stopPropagation()
            onClose()
            return
          }
          if (event.key !== 'Tab') return
          const focusable = Array.from(paletteRef.current?.querySelectorAll<HTMLElement>(
            'button:not([disabled]):not([tabindex="-1"]), input:not([disabled]):not([tabindex="-1"]), [tabindex]:not([tabindex="-1"])',
          ) || [])
          if (focusable.length === 0) return
          const first = focusable[0]
          const last = focusable[focusable.length - 1]
          if (event.shiftKey && document.activeElement === first) {
            event.preventDefault()
            last.focus()
          } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault()
            first.focus()
          }
        }}
      >
        <div className="palette-input-wrap">
          <Search size={14} className="palette-search-icon" />
          <input
            ref={inputRef}
            className="palette-input"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Switch project, run action…"
            maxLength={200}
            role="combobox"
            aria-label="Search projects and actions"
            aria-autocomplete="list"
            aria-expanded="true"
            aria-controls="command-palette-results"
            aria-activedescendant={filtered.length > 0 ? `command-palette-option-${selected}` : undefined}
          />
          <span className="palette-esc">Esc</span>
        </div>
        <div
          id="command-palette-results"
          className="palette-list"
          ref={listRef}
          role="listbox"
          aria-label="Projects and actions"
        >
          {filtered.length === 0 ? (
            <div className="palette-empty">No results</div>
          ) : (
            filtered.map((a, idx) => {
              const showGroup = a.group !== lastGroup
              lastGroup = a.group
              return (
                <div key={a.id} role="presentation">
                  {showGroup && <div className="palette-group" role="presentation">{a.group}</div>}
                  <button
                    id={`command-palette-option-${idx}`}
                    data-idx={idx}
                    className={`palette-item ${selected === idx ? 'selected' : ''}`}
                    onMouseEnter={() => setSelected(idx)}
                    onClick={() => a.onSelect()}
                    role="option"
                    aria-selected={selected === idx}
                    tabIndex={-1}
                  >
                    <span className="palette-item-icon">{a.icon}</span>
                    <span className="palette-item-label">{a.label}</span>
                    {a.hint && <span className="palette-item-hint">{a.hint}</span>}
                  </button>
                </div>
              )
            })
          )}
        </div>
        <div className="palette-footer">
          <span><kbd>↑↓</kbd> <kbd>Home End</kbd> navigate</span>
          <span><kbd>↵</kbd> select</span>
          <span><kbd>Esc</kbd> close</span>
        </div>
      </div>
    </div>
  )
}
