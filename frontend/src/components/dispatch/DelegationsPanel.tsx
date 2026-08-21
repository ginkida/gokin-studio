import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { X, Square, ExternalLink, ChevronDown, ChevronRight, Loader2 } from 'lucide-react'
import {
  ListDelegationRuns,
  CancelDelegationRun,
  FetchDelegationAnswer,
} from '../../../wailsjs/go/studio/Studio'
import {
  useDelegationStore,
  isTerminalDelegation,
  type DelegationRun,
} from '../../stores/delegationStore'
import { useProjectStore } from '../../stores/projectStore'

interface Props {
  onClose: () => void
  onOpenTarget?: (projectID: string, sessionID: string) => void
}

function elapsedLabel(run: DelegationRun): string {
  const end = run.completedAt || Date.now()
  const seconds = Math.max(0, Math.round((end - (run.startedAt || end)) / 1000))
  if (seconds < 60) return `${seconds}s`
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

function statusClass(status: string): string {
  if (status === 'completed') return 'delegation-status delegation-status-ok'
  if (status === 'running') return 'delegation-status delegation-status-live'
  return 'delegation-status delegation-status-bad'
}

export default function DelegationsPanel({ onClose, onOpenTarget }: Props) {
  const runs = useDelegationStore((state) => state.runs)
  const tails = useDelegationStore((state) => state.tails)
  const names = useDelegationStore((state) => state.names)
  const hydrate = useDelegationStore((state) => state.hydrate)
  const projects = useProjectStore((state) => state.projects)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState<Record<string, boolean>>({})
  const [loadError, setLoadError] = useState('')
  const [initialLoading, setInitialLoading] = useState(true)
  const answerRequests = useRef(new Set<string>())

  useEffect(() => {
    let current = true
    // The store only holds what this process saw; the durable record survives
    // a restart, so a reopened panel must read from disk.
    ListDelegationRuns('', '')
      .then((stored) => {
        if (current) hydrate((stored || []) as unknown as DelegationRun[])
      })
      .catch((err) => {
        if (current) setLoadError(String(err))
      })
      .finally(() => {
        if (current) setInitialLoading(false)
      })
    return () => { current = false }
  }, [hydrate])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      event.stopPropagation()
      onClose()
    }
    document.addEventListener('keydown', onKeyDown, true)
    return () => document.removeEventListener('keydown', onKeyDown, true)
  }, [onClose])

  const projectName = (id: string) =>
    projects.find((project) => project.id === id)?.name || names[id] || id

  const ordered = useMemo(
    () =>
      Object.values(runs).sort((a, b) => {
        const aLive = isTerminalDelegation(a.status) ? 0 : 1
        const bLive = isTerminalDelegation(b.status) ? 0 : 1
        if (aLive !== bLive) return bLive - aLive
        return (b.startedAt || 0) - (a.startedAt || 0)
      }),
    [runs],
  )

  const batches = useMemo(() => {
    const groups = new Map<string, DelegationRun[]>()
    for (const run of ordered) {
      const key = run.batchID || `single:${run.id}`
      const list = groups.get(key)
      if (list) list.push(run)
      else groups.set(key, [run])
    }
    return [...groups.entries()]
  }, [ordered])

  const cancel = async (runID: string) => {
    setBusy((state) => ({ ...state, [runID]: true }))
    try {
      await CancelDelegationRun(runID)
    } catch (err) {
      setLoadError(String(err))
    } finally {
      setBusy((state) => ({ ...state, [runID]: false }))
    }
  }

  const loadAnswer = useCallback(async (runID: string) => {
    if (answerRequests.current.has(runID)) return
    answerRequests.current.add(runID)
    try {
      const page = await FetchDelegationAnswer(runID, 0, 8192)
      setAnswers((state) => ({
        ...state,
        [runID]: page.truncated ? `${page.text}\n\n… (${page.fullSize} bytes total)` : page.text,
      }))
    } catch (err) {
      setAnswers((state) => ({ ...state, [runID]: `Could not read the answer: ${err}` }))
    } finally {
      answerRequests.current.delete(runID)
    }
  }, [])

  const toggle = (runID: string) => {
    const next = !expanded[runID]
    setExpanded((state) => ({ ...state, [runID]: next }))
  }

  // If a row was expanded while still running, fetch its answer as soon as
  // the terminal event arrives. This avoids pinning the empty pre-completion
  // response until the user manually collapses and reopens the row.
  useEffect(() => {
    for (const run of ordered) {
      if (expanded[run.id] && isTerminalDelegation(run.status) && answers[run.id] === undefined) {
        void loadAnswer(run.id)
      }
    }
  }, [answers, expanded, loadAnswer, ordered])

  // A running row shows elapsed time, which only advances if something
  // re-renders. Tick while anything is live, and stop when nothing is.
  const anyLive = ordered.some((run) => !isTerminalDelegation(run.status))
  const [, forceTick] = useState(0)
  useEffect(() => {
    if (!anyLive) return
    const timer = window.setInterval(() => forceTick((value) => value + 1), 1000)
    return () => window.clearInterval(timer)
  }, [anyLive])

  return (
    <div className="app-dialog-backdrop" onClick={onClose}>
      <div
        className="app-dialog delegations-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="delegations-title"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="app-dialog-heading delegations-header">
          <h3 id="delegations-title">Delegations</h3>
          <button className="app-dialog-close" onClick={onClose} aria-label="Close delegations">
            <X size={16} />
          </button>
        </div>

        {loadError && <div className="delegations-load-error" role="alert">{loadError}</div>}

        {initialLoading && ordered.length === 0 ? (
          <p className="modal-empty delegation-loading" role="status" aria-live="polite">
            <Loader2 size={14} className="spin" /> Loading delegation history…
          </p>
        ) : ordered.length === 0 && !loadError && (
          <p className="modal-empty">
            Nothing delegated yet. Send a task to another project from a chat, or let the agent
            call <code>delegate</code>.
          </p>
        )}

        <div className="delegations-list">
          {batches.map(([key, group]) => {
            const isBatch = group.length > 1
            const live = group.filter((run) => !isTerminalDelegation(run.status))
            return (
              <div key={key} className={isBatch ? 'delegation-batch' : ''}>
                {isBatch && (
                  <div className="delegation-batch-header">
                    <span>
                      Fan-out · {group.length} projects · {live.length} still running
                    </span>
                    {live.length > 0 && (
                      <button
                        className="btn-secondary btn-small"
                        onClick={() => live.forEach((run) => void cancel(run.id))}
                      >
                        Cancel all
                      </button>
                    )}
                  </div>
                )}
                {group.map((run) => {
                  const tail = tails[run.id] || run.progressTail || []
                  const open = !!expanded[run.id]
                  return (
                    <div key={run.id} className="delegation-row">
                      <div className="delegation-row-main">
                        <button
                          className="icon-btn delegation-expand"
                          onClick={() => toggle(run.id)}
                          aria-label={open ? 'Hide delegation answer' : 'Show delegation answer'}
                          aria-expanded={open}
                          aria-controls={`delegation-answer-${run.id}`}
                        >
                          {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                        </button>
                        <span className="delegation-target">{projectName(run.toProjectID)}</span>
                        <span className={statusClass(run.status)}>{run.status}</span>
                        {run.kind === 'ask' && <span className="delegation-kind">question</span>}
                        <span className="delegation-meta">{elapsedLabel(run)}</span>
                        {!!run.estimatedCostUSD && (
                          <span className="delegation-meta">
                            ${run.estimatedCostUSD.toFixed(3)}
                          </span>
                        )}
                        <span className="delegation-row-actions">
                          {!isTerminalDelegation(run.status) && (
                            <button
                              className="btn-secondary btn-small"
                              disabled={busy[run.id]}
                              onClick={() => void cancel(run.id)}
                            >
                              <Square size={12} /> Stop
                            </button>
                          )}
                          {/* Bounded questions are archived automatically. An
                              "Open chat" action would silently land on the
                              target project's unrelated active tab. */}
                          {run.kind !== 'ask' && run.toSessionID && onOpenTarget && (
                            <button
                              className="btn-secondary btn-small"
                              onClick={() => onOpenTarget(run.toProjectID, run.toSessionID!)}
                            >
                              <ExternalLink size={12} /> Open chat
                            </button>
                          )}
                        </span>
                      </div>

                      {run.goal && <div className="delegation-goal">{run.goal}</div>}

                      {tail.length > 0 && (
                        <pre className="delegation-tail">{tail.join('\n')}</pre>
                      )}

                      {run.errorType && (
                        <div className="delegation-error">
                          {run.errorType}: {run.error}
                        </div>
                      )}

                      {/* A cancelled delegation is not a rolled-back delegation. */}
                      {run.mutatedBeforeStop && (
                        <div className="delegation-warning">
                          This delegation had already written changes in {projectName(run.toProjectID)}{' '}
                          when it was stopped. Cancelling did not undo them.
                        </div>
                      )}

                      {!!run.deniedTools?.length && (
                        <div className="delegation-warning">
                          Finished, but {run.deniedTools.length} tool call(s) were blocked, so the
                          answer may be incomplete: {run.deniedTools.join(', ')}.
                        </div>
                      )}

                      {open && (
                        <pre className="delegation-answer" id={`delegation-answer-${run.id}`}>
                          {!isTerminalDelegation(run.status)
                            ? 'The answer will be available when this delegation finishes.'
                            : answers[run.id] ?? 'Loading…'}
                        </pre>
                      )}
                    </div>
                  )
                })}
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
