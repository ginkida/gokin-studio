import { create } from 'zustand'

// Live view of cross-project delegations.
//
// The backend owns the durable record; this store is a bounded cache so the
// chat card and the panel can render without an RPC per repaint. Everything
// here is capped: a delegation writes a progress tail roughly once a second
// and a long-lived session must not accumulate them.

export interface DelegationRun {
  id: string
  batchID?: string
  kind: string
  fromProjectID: string
  fromSessionID: string
  toProjectID: string
  toSessionID?: string
  goal?: string
  task?: string
  status: string
  answer?: string
  answerBytes?: number
  truncated?: boolean
  progressTail?: string[]
  deniedTools?: string[]
  mutatedBeforeStop?: boolean
  errorType?: string
  error?: string
  estimatedCostUSD?: number
  startedAt: number
  completedAt?: number
}

export interface DelegationEvent {
  runID: string
  batchID?: string
  fromProjectID: string
  fromSessionID: string
  toProjectID: string
  toProjectName?: string
  toSessionID?: string
  kind: string
  goal?: string
  status: string
  errorType?: string
  error?: string
  summary?: string
  tail?: string[]
  lastTool?: string
  deniedTools?: string[]
  mutatedBeforeStop?: boolean
  elapsedMs?: number
  estimatedCostUSD?: number
}

/** Matches the backend cap so the two cannot drift. */
const MAX_TAIL_LINES = 20
/** Bounds the store itself; the durable history lives on disk. */
const MAX_TRACKED_RUNS = 60

export const chatKeyFor = (projectID: string, sessionID: string) => `${projectID}_${sessionID}`

interface DelegationState {
  runs: Record<string, DelegationRun>
  /** Live progress, evicted the moment a run reaches a terminal state. */
  tails: Record<string, string[]>
  names: Record<string, string>
  applyEvent: (event: DelegationEvent) => void
  hydrate: (runs: DelegationRun[]) => void
  removeRun: (runID: string) => void
  runsForChat: (projectID: string, sessionID: string) => DelegationRun[]
  activeRuns: () => DelegationRun[]
}

const terminal = (status: string) =>
  status === 'completed' || status === 'stopped' || status === 'error'

/** Keeps the newest MAX_TRACKED_RUNS, always retaining anything still live. */
function prune(runs: Record<string, DelegationRun>): Record<string, DelegationRun> {
  const entries = Object.values(runs)
  if (entries.length <= MAX_TRACKED_RUNS) return runs
  const sorted = entries.sort((a, b) => {
    const aLive = terminal(a.status) ? 0 : 1
    const bLive = terminal(b.status) ? 0 : 1
    if (aLive !== bLive) return bLive - aLive
    return (b.startedAt || 0) - (a.startedAt || 0)
  })
  const kept: Record<string, DelegationRun> = {}
  for (const run of sorted.slice(0, MAX_TRACKED_RUNS)) kept[run.id] = run
  return kept
}

// Merges a durable snapshot that may have been read concurrently with live
// events. A terminal state is irreversible in the backend, so it must always
// beat a running row regardless of which side arrived last. For two live rows,
// the event-fed copy keeps the freshest status while the disk row enriches it
// with fields that are not present in progress events (task and true start).
export function mergeHydratedDelegationRuns(
  current: Record<string, DelegationRun>,
  incoming: DelegationRun[],
): Record<string, DelegationRun> {
  const merged = { ...current }
  for (const durable of incoming) {
    if (!durable?.id) continue
    const live = merged[durable.id]
    if (!live) {
      merged[durable.id] = durable
      continue
    }

    const liveTerminal = terminal(live.status)
    const durableTerminal = terminal(durable.status)
    if (liveTerminal && !durableTerminal) {
      // The RPC snapshot was captured before the completion event.
      continue
    }
    if (!liveTerminal && !durableTerminal) {
      const definedLive = Object.fromEntries(
        Object.entries(live).filter(([, value]) => value !== undefined),
      ) as Partial<DelegationRun>
      merged[durable.id] = {
        ...durable,
        ...definedLive,
        startedAt: durable.startedAt || live.startedAt,
      }
      continue
    }
    // Disk is authoritative for a terminal row: it contains the full bounded
    // answer, exact timestamps, usage and the final progress tail.
    merged[durable.id] = { ...live, ...durable }
  }
  return prune(merged)
}

export const useDelegationStore = create<DelegationState>((set, get) => ({
  runs: {},
  tails: {},
  names: {},

  applyEvent: (event) =>
    set((state) => {
      const previous = state.runs[event.runID]
      // Bridge events and a concurrent hydration RPC are not a single ordered
      // stream. Once either source has observed a durable terminal transition,
      // a late started/progress event must enrich the card at most — never
      // resurrect it as running.
      const previousTerminal = previous ? terminal(previous.status) : false
      const incomingTerminal = terminal(event.status)
      const nextStatus = previousTerminal && !incomingTerminal
        ? previous.status
        : event.status || previous?.status || 'running'
      const run: DelegationRun = {
        ...previous,
        id: event.runID,
        batchID: event.batchID ?? previous?.batchID,
        kind: event.kind || previous?.kind || 'run',
        fromProjectID: event.fromProjectID || previous?.fromProjectID || '',
        fromSessionID: event.fromSessionID || previous?.fromSessionID || '',
        toProjectID: event.toProjectID || previous?.toProjectID || '',
        toSessionID: event.toSessionID ?? previous?.toSessionID,
        goal: event.goal ?? previous?.goal,
        status: nextStatus,
        errorType: event.errorType ?? previous?.errorType,
        error: event.error ?? previous?.error,
        answer: event.summary ?? previous?.answer,
        progressTail: event.tail?.slice(-MAX_TAIL_LINES) ?? previous?.progressTail,
        deniedTools: event.deniedTools ?? previous?.deniedTools,
        mutatedBeforeStop: event.mutatedBeforeStop ?? previous?.mutatedBeforeStop,
        estimatedCostUSD: event.estimatedCostUSD ?? previous?.estimatedCostUSD,
        startedAt: previous?.startedAt || Date.now(),
		completedAt: incomingTerminal ? Date.now() : previous?.completedAt,
      }

      const tails = { ...state.tails }
	  if (terminal(nextStatus)) {
        // A finished run's live tail is dead weight; the durable record keeps
        // a post-mortem copy for anyone who wants it.
        delete tails[event.runID]
      } else if (event.tail?.length) {
        tails[event.runID] = event.tail.slice(-MAX_TAIL_LINES)
      }

      const names = { ...state.names }
      if (event.toProjectName) names[event.toProjectID] = event.toProjectName

      return { runs: prune({ ...state.runs, [event.runID]: run }), tails, names }
    }),

  hydrate: (incoming) =>
    set((state) => ({ runs: mergeHydratedDelegationRuns(state.runs, incoming) })),

  removeRun: (runID) =>
    set((state) => {
      const runs = { ...state.runs }
      const tails = { ...state.tails }
      delete runs[runID]
      delete tails[runID]
      return { runs, tails }
    }),

  runsForChat: (projectID, sessionID) =>
    Object.values(get().runs)
      .filter((run) => run.fromProjectID === projectID && run.fromSessionID === sessionID)
      .sort((a, b) => (b.startedAt || 0) - (a.startedAt || 0)),

  activeRuns: () => Object.values(get().runs).filter((run) => !terminal(run.status)),
}))

export const isTerminalDelegation = terminal
