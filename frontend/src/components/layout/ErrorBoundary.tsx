import React from 'react'
import { AlertOctagon, RefreshCw, FileText } from 'lucide-react'
import { LogEvent } from '../../../wailsjs/go/studio/Studio'

interface State {
  error: Error | null
  componentStack: string
  showStack: boolean
}

interface Props {
  children: React.ReactNode
}

// ErrorBoundary catches React render errors anywhere in the tree below it
// and shows a recovery UI instead of the white-screen-of-death default.
// Also tee's the error into the backend event log so users can include it
// in their diagnostics report (Settings → Run diagnostics → View logs).
//
// Class component because functional components can't be error boundaries
// in React 18 — getDerivedStateFromError + componentDidCatch are required.
export class ErrorBoundary extends React.Component<Props, State> {
  state: State = { error: null, componentStack: '', showStack: false }

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    const stack = info.componentStack || ''
    this.setState({ componentStack: stack })
    // Best-effort log to backend so it appears in the diagnostics viewer.
    // Wrap in try/catch + .catch() — a log failure must NOT re-throw out of
    // componentDidCatch or we'd recurse.
    try {
      const msg = `${error.name}: ${error.message}\n${(error.stack || '').split('\n').slice(0, 5).join('\n')}`
      LogEvent('error', 'frontend', msg).catch(() => { /* backend unreachable */ })
    } catch { /* defensive */ }
  }

  handleReload = () => {
    // Force a full app reload — the only safe recovery for an unknown render
    // error since component state is potentially corrupt.
    window.location.reload()
  }

  handleReset = () => {
    // Try a soft recovery first: clear the error state and let React
    // re-render the children. If the error is deterministic this will
    // immediately re-trigger, but for transient/race-related errors
    // (e.g. a stale event handler) it gives the user a path back.
    this.setState({ error: null, componentStack: '', showStack: false })
  }

  render() {
    if (!this.state.error) return this.props.children

    const err = this.state.error
    const errSummary = `${err.name || 'Error'}: ${err.message || 'unknown'}`

    return (
      <div className="error-boundary-root">
        <div className="error-boundary-card">
          <div className="error-boundary-icon"><AlertOctagon size={28} /></div>
          <h2 className="error-boundary-title">Something went wrong</h2>
          <p className="error-boundary-msg">{errSummary}</p>
          <p className="error-boundary-hint">
            This error has been logged. Open <strong>Settings → About → Run diagnostics → View logs</strong>{' '}
            after recovery to inspect or copy the details.
          </p>
          <div className="error-boundary-actions">
            <button className="btn-primary" onClick={this.handleReload}>
              <RefreshCw size={14} /> Reload app
            </button>
            <button className="btn-secondary" onClick={this.handleReset}>
              Try to recover
            </button>
            <button
              className="btn-secondary"
              onClick={() => this.setState({ showStack: !this.state.showStack })}
            >
              <FileText size={14} />
              {this.state.showStack ? 'Hide details' : 'Show details'}
            </button>
          </div>
          {this.state.showStack && (
            <pre className="error-boundary-stack">
              {err.stack || '(no stack available)'}
              {this.state.componentStack && '\n\n--- Component stack ---' + this.state.componentStack}
            </pre>
          )}
        </div>
      </div>
    )
  }
}

// installGlobalErrorHandlers attaches window-level listeners for uncaught
// errors and unhandled promise rejections, teeing them into the backend
// event log so async failures land in the diagnostics viewer too.
//
// Call ONCE near app bootstrap. Idempotent via a module-level flag — if the
// app is hot-reloaded the handlers don't stack.
let globalHandlersInstalled = false
export function installGlobalErrorHandlers() {
  if (globalHandlersInstalled) return
  globalHandlersInstalled = true

  window.addEventListener('error', (e) => {
    const msg = e.error
      ? `${e.error.name || 'Error'}: ${e.error.message || e.message || 'unknown'}`
      : (e.message || 'unknown error')
    const where = e.filename ? ` at ${e.filename}:${e.lineno}:${e.colno}` : ''
    try { LogEvent('error', 'frontend', msg + where).catch(() => {}) } catch { /* defensive */ }
  })

  window.addEventListener('unhandledrejection', (e) => {
    const reason = e.reason
    let msg = 'Unhandled promise rejection: '
    if (reason instanceof Error) {
      msg += `${reason.name}: ${reason.message}`
    } else if (typeof reason === 'string') {
      msg += reason
    } else {
      try { msg += JSON.stringify(reason) } catch { msg += String(reason) }
    }
    try { LogEvent('error', 'frontend', msg).catch(() => {}) } catch { /* defensive */ }
  })
}
