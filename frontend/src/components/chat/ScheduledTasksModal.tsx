import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, CalendarClock, Check, Clock3, History, Loader2, MessageSquare, Pencil, Play, Plus, Trash2, X } from 'lucide-react'
import {
  DeleteScheduledTaskWithData,
  GetScheduledTaskDeletionPreview,
  ListScheduledTaskRuns,
  ListScheduledTasks,
  RunScheduledTaskNow,
  SaveScheduledTask,
} from '../../../wailsjs/go/studio/Studio'
import { useSettingsStore } from '../../stores/settingsStore'
import { formatContextWindow } from '../../lib/modelCapabilities'
import { formatModelLabel, formatProviderModelLabel } from '../../lib/providerCatalog'
import { useConfirmDialog } from '../common/AppDialog'

type ScheduledTask = {
  id: string
  projectID: string
  sessionID: string
  name: string
  prompt: string
  schedule: 'interval' | 'daily' | 'weekdays' | 'weekly' | 'manual'
  intervalMinutes?: number
  timeOfDay?: string
  weekday?: number
  enabled: boolean
  createdAt: number
  nextRunAt?: number
  lastRunAt?: number
  lastRunID?: string
  lastStatus?: string
  lastError?: string
  provider: 'glm' | 'kimi'
  model: string
  approvalMode: 'manual' | 'accept_edits' | 'auto' | 'skip'
}

type ScheduledTaskRun = {
  id: string
  taskID: string
  projectID: string
  sessionID: string
  startedAt: number
  completedAt?: number
  status: 'running' | 'completed' | 'stopped' | 'error'
  error?: string
  provider: 'glm' | 'kimi'
  model: string
  approvalMode: 'manual' | 'accept_edits' | 'auto' | 'skip'
}

type ScheduledTaskDeletionPreview = {
  projectID: string
  taskID: string
  runCount: number
  runChatCount: number
  activeRunCount: number
  missingRunChats: number
  protectedRunChats: number
}

type ScheduledTaskDeleteTarget = {
  task: ScheduledTask
  preview: ScheduledTaskDeletionPreview
  deleteRunData: boolean
}

const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

function emptyForm(provider: string, model: string) {
  return {
    id: '',
    name: '',
    prompt: '',
    schedule: 'daily' as ScheduledTask['schedule'],
    intervalMinutes: 60,
    timeOfDay: '09:00',
    weekday: 1,
    enabled: true,
    provider: (provider || 'glm') as 'glm' | 'kimi',
    model: model || 'glm-5.2',
    approvalMode: 'manual' as 'manual' | 'accept_edits' | 'auto' | 'skip',
  }
}

function formatWhen(value?: number) {
  if (!value) return 'Not scheduled'
  return new Intl.DateTimeFormat(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

function formatSchedule(task: ScheduledTask) {
  switch (task.schedule) {
    case 'interval':
      return `Every ${task.intervalMinutes} min`
    case 'daily':
      return `Daily at ${task.timeOfDay}`
    case 'weekdays':
      return `Weekdays at ${task.timeOfDay}`
    case 'weekly':
      return `${WEEKDAYS[task.weekday || 0]} at ${task.timeOfDay}`
    case 'manual':
      return 'Manual only'
  }
}

function formatApprovalMode(mode: ScheduledTask['approvalMode']) {
  return mode === 'accept_edits' ? 'accept edits' : mode
}

export function ScheduledTasksModal({
  projectID,
  sessionID,
  provider,
  model,
  onClose,
  embedded = false,
}: {
  projectID: string
  sessionID: string
  provider: string
  model: string
  onClose: () => void
  embedded?: boolean
}) {
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const providers = useSettingsStore((state) => state.providers)
  const providerCapabilities = useSettingsStore((state) => state.providerCapabilities)
  const settings = useSettingsStore((state) => state.settings)
  const providerCredentialSources = useSettingsStore((state) => state.providerCredentialSources)
  const [tasks, setTasks] = useState<ScheduledTask[]>([])
  const [form, setForm] = useState(() => emptyForm(provider, model))
  const [historyTaskID, setHistoryTaskID] = useState<string | null>(null)
  const [runs, setRuns] = useState<ScheduledTaskRun[]>([])
  const [runsLoading, setRunsLoading] = useState(false)
  const [runsError, setRunsError] = useState<string | null>(null)
  const [historyRefresh, setHistoryRefresh] = useState(0)
  const [loading, setLoading] = useState(true)
  const [tasksError, setTasksError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<ScheduledTaskDeleteTarget | null>(null)
  const taskLoadRequestRef = useRef(0)
  const historyRequestRef = useRef(0)
  const scopeKey = `${projectID}\u0000${sessionID}`
  const scopeRef = useRef({ key: scopeKey, generation: 0, mounted: false })

  useEffect(() => {
    // A Wails RPC cannot be aborted once sent. Give every response an exact
    // workspace generation so an A → B → A switch cannot revive the first A.
    scopeRef.current.key = scopeKey
    scopeRef.current.generation += 1
    scopeRef.current.mounted = true
    const generation = scopeRef.current.generation
    return () => {
      if (scopeRef.current.generation === generation) {
        scopeRef.current.mounted = false
        scopeRef.current.generation += 1
      }
      taskLoadRequestRef.current += 1
      historyRequestRef.current += 1
    }
  }, [scopeKey])

  const ownsScope = useCallback((generation: number) => (
    scopeRef.current.mounted && scopeRef.current.generation === generation
  ), [])

  const load = useCallback(async () => {
    const scope = scopeRef.current.generation
    const request = ++taskLoadRequestRef.current
    setLoading(true)
    setTasksError(null)
    try {
      const result: any = await ListScheduledTasks(projectID)
      if (!ownsScope(scope) || taskLoadRequestRef.current !== request) return false
      setTasks((result || []) as ScheduledTask[])
      return true
    } catch (e: any) {
      if (ownsScope(scope) && taskLoadRequestRef.current === request) {
        setTasksError(String(e?.message || e || 'Failed to load scheduled tasks'))
      }
      return false
    } finally {
      if (ownsScope(scope) && taskLoadRequestRef.current === request) setLoading(false)
    }
  }, [ownsScope, projectID])

  useEffect(() => {
    void load()
    return () => { taskLoadRequestRef.current += 1 }
  }, [load, sessionID])

  const editing = useMemo(() => tasks.find((task) => task.id === form.id), [tasks, form.id])
  const selectedProvider = providers.find((item) => item.id === form.provider)
  const selectedModel = selectedProvider?.modelDetails?.find((item) => item.id === form.model)
  const selectedCapability = providerCapabilities[form.provider]
  const selectedModelUnavailable = !!selectedCapability?.availableModels.length &&
    !selectedCapability.availableModels.includes(form.model)

  const providerCredentialMissing = (providerID: string) => {
    const savedKey = providerID === 'kimi' ? settings.kimiKey : settings.glmKey
    if (savedKey.trim()) return false
    const source = providerCredentialSources[providerID]
    return source === undefined ? false : source !== 'env'
  }
  const selectedCredentialMissing = providerCredentialMissing(form.provider)
  const enabledScheduleNeedsCredential = form.schedule !== 'manual' && form.enabled && selectedCredentialMissing

  const modelUnavailableForAccount = (taskProvider: string, taskModel: string) => {
    const capability = providerCapabilities[taskProvider]
    return !!capability?.availableModels.length && !capability.availableModels.includes(taskModel)
  }

  const formDirty = useMemo(() => {
    if (editing) {
      return form.name !== editing.name ||
        form.prompt !== editing.prompt ||
        form.schedule !== editing.schedule ||
        form.intervalMinutes !== (editing.intervalMinutes || 60) ||
        form.timeOfDay !== (editing.timeOfDay || '09:00') ||
        form.weekday !== (editing.weekday ?? 1) ||
        form.enabled !== editing.enabled ||
        form.provider !== editing.provider ||
        form.model !== editing.model ||
        form.approvalMode !== (editing.approvalMode === ('ask' as any) ? 'manual' : (editing.approvalMode || 'manual'))
    }
    const initial = emptyForm(provider, model)
    return form.name !== initial.name ||
      form.prompt !== initial.prompt ||
      form.schedule !== initial.schedule ||
      form.intervalMinutes !== initial.intervalMinutes ||
      form.timeOfDay !== initial.timeOfDay ||
      form.weekday !== initial.weekday ||
      form.enabled !== initial.enabled ||
      form.provider !== initial.provider ||
      form.model !== initial.model ||
      form.approvalMode !== initial.approvalMode
  }, [editing, form, provider, model])

  const confirmDiscardDraft = useCallback(async () => {
    if (!formDirty) return true
    return requestConfirmation({
      title: editing ? 'Discard task changes?' : 'Discard new task draft?',
      message: editing
        ? `Unsaved changes to “${editing.name || 'Untitled task'}” will be lost. The saved task and its run history are not changed.`
        : 'The unsaved prompt and schedule will be lost.',
      confirmLabel: 'Discard draft',
      cancelLabel: 'Keep editing',
      danger: true,
    })
  }, [formDirty, editing, requestConfirmation])

  const requestClose = useCallback(async () => {
    if (busy !== null) return
    if (deleteTarget) {
      setDeleteTarget(null)
      return
    }
    const scope = scopeRef.current.generation
    if (await confirmDiscardDraft() && ownsScope(scope)) onClose()
  }, [busy, deleteTarget, confirmDiscardDraft, onClose, ownsScope])

  useEffect(() => {
    const handler = () => { void requestClose() }
    window.addEventListener('gokin:close-scheduled-tasks', handler)
    return () => window.removeEventListener('gokin:close-scheduled-tasks', handler)
  }, [requestClose])

  const resetForm = () => {
    setForm(emptyForm(provider, model))
    setError(null)
  }

  const save = async () => {
    if (!form.prompt.trim()) {
      setError('Enter the prompt that should run.')
      return
    }
    if (selectedModelUnavailable) {
      setError(`${form.model} is not available for the tested ${form.provider.toUpperCase()} key. Choose an available model before saving.`)
      return
    }
    if (enabledScheduleNeedsCredential) {
      setError(`Connect ${form.provider === 'kimi' ? 'Kimi' : 'GLM'} before enabling this task, or turn off “Enabled after save” to keep it as a draft.`)
      return
    }
    const scope = scopeRef.current.generation
    setBusy('save')
    setError(null)
    try {
      const payload: any = {
        id: form.id,
        projectID,
        sessionID: editing?.sessionID || sessionID,
        name: form.name.trim(),
        prompt: form.prompt.trim(),
        schedule: form.schedule,
        intervalMinutes: form.schedule === 'interval' ? form.intervalMinutes : 0,
        timeOfDay: form.schedule === 'interval' || form.schedule === 'manual' ? '' : form.timeOfDay,
        weekday: form.schedule === 'weekly' ? form.weekday : 0,
        enabled: form.schedule === 'manual' ? true : form.enabled,
        createdAt: editing?.createdAt || 0,
        nextRunAt: editing?.nextRunAt || 0,
        lastRunAt: editing?.lastRunAt || 0,
        lastStatus: editing?.lastStatus || '',
        lastError: editing?.lastError || '',
        provider: form.provider,
        model: form.model,
        approvalMode: form.approvalMode,
      }
      await SaveScheduledTask(payload)
      if (!ownsScope(scope)) return
      await load()
      if (!ownsScope(scope)) return
      resetForm()
    } catch (e: any) {
      if (ownsScope(scope)) setError(String(e?.message || e || 'Failed to save scheduled task'))
    } finally {
      if (ownsScope(scope)) setBusy(null)
    }
  }

  const edit = (task: ScheduledTask) => {
    setForm({
      id: task.id,
      name: task.name,
      prompt: task.prompt,
      schedule: task.schedule,
      intervalMinutes: task.intervalMinutes || 60,
      timeOfDay: task.timeOfDay || '09:00',
      weekday: task.weekday ?? 1,
      enabled: task.enabled,
      provider: task.provider,
      model: task.model,
      approvalMode: task.approvalMode === ('ask' as any) ? 'manual' : (task.approvalMode || 'manual'),
    })
    setError(null)
  }

  const toggle = async (task: ScheduledTask) => {
    if (!task.enabled && providerCredentialMissing(task.provider)) {
      setError(`Connect ${task.provider === 'kimi' ? 'Kimi' : 'GLM'} before enabling “${task.name || 'Untitled task'}”.`)
      return
    }
    if (!task.enabled && modelUnavailableForAccount(task.provider, task.model)) {
      setError(`${task.model} is not available for the tested ${task.provider.toUpperCase()} key. Edit the task and choose an available model before enabling it.`)
      return
    }
    const scope = scopeRef.current.generation
    setBusy(task.id)
    setError(null)
    try {
      await SaveScheduledTask({ ...task, enabled: !task.enabled } as any)
      if (!ownsScope(scope)) return
      await load()
      if (ownsScope(scope) && form.id === task.id) {
        setForm((current) => ({ ...current, enabled: !task.enabled }))
      }
    } catch (e: any) {
      if (ownsScope(scope)) setError(String(e?.message || e || 'Failed to update scheduled task'))
    } finally {
      if (ownsScope(scope)) setBusy(null)
    }
  }

  const runNow = async (task: ScheduledTask) => {
    if (providerCredentialMissing(task.provider)) {
      setError(`Connect ${task.provider === 'kimi' ? 'Kimi' : 'GLM'} before running “${task.name || 'Untitled task'}”.`)
      return
    }
    if (modelUnavailableForAccount(task.provider, task.model)) {
      setError(`${task.model} is not available for the tested ${task.provider.toUpperCase()} key. Edit the task and choose an available model before running it.`)
      return
    }
    const scope = scopeRef.current.generation
    setBusy(`run:${task.id}`)
    setError(null)
    try {
      const run: any = await RunScheduledTaskNow(projectID, task.id)
      if (!ownsScope(scope)) return
      await load()
      if (ownsScope(scope) && run?.sessionID) await openRun(run.sessionID, scope)
    } catch (e: any) {
      if (!ownsScope(scope)) return
      const message = String(e?.message || e || 'Failed to run scheduled task')
      await load()
      if (ownsScope(scope)) setError(message)
    } finally {
      if (ownsScope(scope)) setBusy(null)
    }
  }

  const openRunHistory = useCallback((taskID: string) => {
    historyRequestRef.current += 1
    setHistoryTaskID(taskID)
    setHistoryRefresh((value) => value + 1)
    setRuns([])
    setRunsLoading(true)
    setRunsError(null)
  }, [])

  const closeRunHistory = useCallback(() => {
    historyRequestRef.current += 1
    setHistoryTaskID(null)
    setRuns([])
    setRunsLoading(false)
    setRunsError(null)
  }, [])

  useEffect(() => {
    if (!historyTaskID) return
    const taskID = historyTaskID
    const scope = scopeRef.current.generation
    const request = ++historyRequestRef.current
    let stopped = false
    let timer: number | undefined

    const poll = async (initial: boolean) => {
      if (initial) {
        setRunsLoading(true)
        setRunsError(null)
      }
      try {
        const result: any = await ListScheduledTaskRuns(projectID, taskID)
        if (stopped || !ownsScope(scope) || historyRequestRef.current !== request) return
        setRuns((result || []) as ScheduledTaskRun[])
        setRunsError(null)
      } catch (e: any) {
        if (stopped || !ownsScope(scope) || historyRequestRef.current !== request) return
        setRunsError(String(e?.message || e || 'Failed to load run history'))
      } finally {
        if (stopped || !ownsScope(scope) || historyRequestRef.current !== request) return
        setRunsLoading(false)
        // Schedule only after the RPC settles so a slow backend can never
        // accumulate overlapping history reads.
        timer = window.setTimeout(() => { void poll(false) }, 2000)
      }
    }

    void poll(true)
    return () => {
      stopped = true
      if (historyRequestRef.current === request) historyRequestRef.current += 1
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [historyRefresh, historyTaskID, ownsScope, projectID])

  const openRun = async (runSessionID: string, expectedScope = scopeRef.current.generation) => {
    if (!(await confirmDiscardDraft())) return
    if (!ownsScope(expectedScope)) return
    onClose()
    window.dispatchEvent(new CustomEvent('gokin:sessions-changed'))
    window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: runSessionID }))
  }

  const remove = async (task: ScheduledTask) => {
    const scope = scopeRef.current.generation
    setBusy(`preview-delete:${task.id}`)
    setError(null)
    try {
      const result: any = await GetScheduledTaskDeletionPreview(projectID, task.id)
      if (!ownsScope(scope)) return
      setDeleteTarget({
        task,
        preview: result as ScheduledTaskDeletionPreview,
        deleteRunData: false,
      })
    } catch (e: any) {
      if (ownsScope(scope)) setError(String(e?.message || e || 'Failed to inspect scheduled task data'))
    } finally {
      if (ownsScope(scope)) setBusy(null)
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    const { task, deleteRunData } = deleteTarget
    const scope = scopeRef.current.generation
    setBusy(`delete:${task.id}`)
    setError(null)
    try {
      await DeleteScheduledTaskWithData(projectID, task.id, deleteRunData)
      if (!ownsScope(scope)) return
      setTasks((current) => current.filter((item) => item.id !== task.id))
      if (form.id === task.id) resetForm()
      if (historyTaskID === task.id) {
        closeRunHistory()
      }
      setDeleteTarget(null)
      if (deleteRunData) {
        window.dispatchEvent(new CustomEvent('gokin:sessions-changed'))
      }
    } catch (e: any) {
      if (!ownsScope(scope)) return
      const message = String(e?.message || e || 'Failed to delete scheduled task')
      await load()
      if (ownsScope(scope)) setError(message)
    } finally {
      if (ownsScope(scope)) setBusy(null)
    }
  }

  return (
    <>
      {!embedded && <div className="scheduled-backdrop" onClick={() => void requestClose()} />}
      <div className={`scheduled-modal ${embedded ? 'embedded' : ''}`} role={embedded ? 'region' : 'dialog'} aria-modal={embedded ? undefined : true} aria-label="Scheduled tasks">
        <div className="scheduled-header">
          <div>
            <h3><CalendarClock size={17} /> Scheduled tasks</h3>
            <p>Local recurring prompts · runs while Gokin Studio is open</p>
          </div>
          <button className="icon-btn" onClick={() => void requestClose()} title="Close" aria-label="Close scheduled tasks"><X size={14} /></button>
        </div>

        <div className="scheduled-body">
          <section className="scheduled-list" aria-busy={loading}>
            <div className="scheduled-section-title">
              <span>Tasks</span>
              <span>{tasks.length}/64</span>
            </div>
            {tasksError && (
              <div className="scheduled-list-error" role="alert">
                <AlertTriangle size={12} />
                <span>{tasksError}</span>
                <button type="button" onClick={() => void load()} disabled={loading}>Retry</button>
              </div>
            )}
            {loading && <div className="scheduled-empty"><Loader2 size={15} className="spin" /> Loading…</div>}
            {!loading && !tasksError && tasks.length === 0 && (
              <div className="scheduled-empty">
                <CalendarClock size={22} />
                <span>No recurring prompts yet.</span>
              </div>
            )}
            {!loading && tasks.map((task) => {
              const accountUnavailable = modelUnavailableForAccount(task.provider, task.model)
              const credentialMissing = providerCredentialMissing(task.provider)
              return (
              <div className={`scheduled-card ${task.enabled || task.schedule === 'manual' ? '' : 'is-paused'} ${accountUnavailable ? 'model-unavailable' : ''} ${credentialMissing ? 'credential-missing' : ''}`} key={task.id}>
                <button
                  className={`scheduled-toggle ${task.enabled ? 'on' : ''}`}
                  onClick={() => void toggle(task)}
                  disabled={busy !== null || task.schedule === 'manual' || ((accountUnavailable || credentialMissing) && !task.enabled)}
                  title={task.schedule === 'manual'
                    ? 'Manual-only tasks never run automatically'
                    : credentialMissing && !task.enabled
                      ? `Connect ${task.provider === 'kimi' ? 'Kimi' : 'GLM'} before enabling`
                    : accountUnavailable && !task.enabled
                      ? 'Choose a model available for the tested account before enabling'
                      : task.enabled ? 'Pause task' : 'Enable task'}
                  aria-label={task.schedule === 'manual'
                    ? 'Manual-only task'
                    : credentialMissing && !task.enabled
                      ? `Cannot enable: ${task.provider === 'kimi' ? 'Kimi' : 'GLM'} is not connected`
                    : accountUnavailable && !task.enabled
                      ? `Cannot enable: ${task.model} is unavailable for the tested key`
                      : task.enabled ? 'Pause task' : 'Enable task'}
                >
                  {(task.enabled || task.schedule === 'manual') && <Check size={11} />}
                </button>
                <div className="scheduled-card-main">
                  <div className="scheduled-card-title">
                    <strong>{task.name}</strong>
                    <span>{credentialMissing ? 'Connection required' : task.schedule === 'manual' ? 'Run on demand' : task.enabled ? formatWhen(task.nextRunAt) : 'Paused'}</span>
                  </div>
                  <div className="scheduled-card-prompt">{task.prompt}</div>
                  <div className="scheduled-card-meta">
                    <Clock3 size={10} />
                    {formatSchedule(task)}
                    <span>· {task.sessionID === sessionID ? 'this chat' : `chat ${task.sessionID}`}</span>
                    <span>· {formatProviderModelLabel(task.provider, task.model)} · {formatApprovalMode(task.approvalMode)}</span>
                    {credentialMissing && <span className="scheduled-credential-missing">· connection required</span>}
                    {accountUnavailable && <span className="scheduled-model-unavailable">· unavailable for tested key</span>}
                    {task.lastStatus && <span className={`scheduled-status ${task.lastStatus}`}>· {task.lastStatus}</span>}
                    {task.lastError && <span className="scheduled-last-error" title={task.lastError}>· {task.lastError}</span>}
                  </div>
                </div>
                <div className="scheduled-card-actions">
                  <button
                    onClick={() => void runNow(task)}
                    disabled={busy !== null || accountUnavailable || credentialMissing}
                    title={credentialMissing ? `Connect ${task.provider === 'kimi' ? 'Kimi' : 'GLM'} before running` : accountUnavailable ? 'Choose an available model before running' : 'Run now'}
                    aria-label={credentialMissing ? `Cannot run: ${task.provider === 'kimi' ? 'Kimi' : 'GLM'} is not connected` : accountUnavailable ? `Cannot run: ${task.model} is unavailable for the tested key` : 'Run task now'}
                  >
                    {busy === `run:${task.id}` ? <Loader2 size={12} className="spin" /> : <Play size={12} />}
                  </button>
                  <button onClick={() => edit(task)} disabled={busy !== null} title="Edit" aria-label={`Edit ${task.name || 'scheduled task'}`}><Pencil size={12} /></button>
                  <button onClick={() => openRunHistory(task.id)} disabled={busy !== null} title="Run history" aria-label={`View run history for ${task.name || 'scheduled task'}`}><History size={12} /></button>
                  <button className="danger" onClick={() => void remove(task)} disabled={busy !== null} title="Delete" aria-label={`Delete ${task.name || 'scheduled task'}`}>
                    {busy === `delete:${task.id}` || busy === `preview-delete:${task.id}` ? <Loader2 size={12} className="spin" /> : <Trash2 size={12} />}
                  </button>
                </div>
              </div>
              )
            })}
            {historyTaskID && (
              <div className="scheduled-runs" aria-busy={runsLoading}>
                <div className="scheduled-section-title">
                  <span>Run history</span>
                  <button onClick={closeRunHistory}>Close</button>
                </div>
                {runsLoading && <div className="scheduled-run-empty"><Loader2 size={12} className="spin" /> Loading history…</div>}
                {runsError && <div className="scheduled-runs-error" role="alert"><AlertTriangle size={12} /> {runsError} Retrying…</div>}
                {!runsLoading && !runsError && runs.length === 0 && <div className="scheduled-run-empty">No runs yet.</div>}
                {runs.map((run) => (
                  <button className="scheduled-run" key={run.id} onClick={() => void openRun(run.sessionID)}>
                    <span className={`scheduled-run-status ${run.status}`}>
                      {run.status === 'running' ? <Loader2 size={11} className="spin" /> : <MessageSquare size={11} />}
                    </span>
                    <span>
                      <strong>{formatWhen(run.startedAt)}</strong>
                      <small>{formatProviderModelLabel(run.provider, run.model)} · {formatApprovalMode(run.approvalMode)}{run.error ? ` · ${run.error}` : ''}</small>
                    </span>
                  </button>
                ))}
              </div>
            )}
          </section>

          <section className="scheduled-editor">
            <div className="scheduled-section-title">
              <span>{editing ? 'Edit task' : 'New task'}</span>
              {editing && <button onClick={resetForm}>Cancel edit</button>}
            </div>
            <label>
              <span>Name <small>optional</small></span>
              <input
                value={form.name}
                maxLength={120}
                placeholder="e.g. Morning repository review"
                onChange={(e) => setForm((current) => ({ ...current, name: e.target.value }))}
              />
            </label>
            <label>
              <span>Prompt</span>
              <textarea
                value={form.prompt}
                maxLength={100000}
                rows={7}
                placeholder="Review open work, run the relevant checks, and summarize blockers…"
                onChange={(e) => setForm((current) => ({ ...current, prompt: e.target.value }))}
              />
            </label>
            <div className="scheduled-form-row">
              <label>
                <span>Repeat</span>
                <select
                  value={form.schedule}
                  onChange={(e) => setForm((current) => ({
                    ...current,
                    schedule: e.target.value as ScheduledTask['schedule'],
                  }))}
                >
                  <option value="interval">Interval</option>
                  <option value="daily">Daily</option>
                  <option value="weekdays">Weekdays</option>
                  <option value="weekly">Weekly</option>
                  <option value="manual">Manual only</option>
                </select>
              </label>
              {form.schedule === 'interval' ? (
                <label>
                  <span>Minutes</span>
                  <input
                    type="number"
                    min={15}
                    max={10080}
                    value={form.intervalMinutes}
                    onChange={(e) => setForm((current) => ({
                      ...current,
                      intervalMinutes: Number(e.target.value),
                    }))}
                  />
                </label>
              ) : form.schedule !== 'manual' ? (
                <label>
                  <span>Local time</span>
                  <input
                    type="time"
                    value={form.timeOfDay}
                    onChange={(e) => setForm((current) => ({ ...current, timeOfDay: e.target.value }))}
                  />
                </label>
              ) : <div />}
            </div>
            {form.schedule === 'weekly' && (
              <label>
                <span>Weekday</span>
                <select
                  value={form.weekday}
                  onChange={(e) => setForm((current) => ({ ...current, weekday: Number(e.target.value) }))}
                >
                  {WEEKDAYS.map((day, index) => <option value={index} key={day}>{day}</option>)}
                </select>
              </label>
            )}
            <div className="scheduled-form-row">
              <label>
                <span>Provider</span>
                <select
                  value={form.provider}
                  onChange={(e) => {
                    const nextProvider = e.target.value as 'glm' | 'kimi'
                    const nextModels = providers.find((item) => item.id === nextProvider)?.models || []
                    const recommended = providerCapabilities[nextProvider]?.recommendedModel
                    setForm((current) => ({
                      ...current,
                      provider: nextProvider,
                      model: recommended && nextModels.includes(recommended)
                        ? recommended
                        : nextModels[0] || (nextProvider === 'glm' ? 'glm-5.2' : 'k3'),
                    }))
                    setError(null)
                  }}
                >
                  {providers.map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}
                </select>
              </label>
              <label>
                <span>Model</span>
                <select
                  value={form.model}
                  onChange={(e) => {
                    setForm((current) => ({ ...current, model: e.target.value }))
                    setError(null)
                  }}
                >
                  {(selectedProvider?.models || [form.model]).map((item) => {
                    const detail = selectedProvider?.modelDetails?.find((candidate) => candidate.id === item)
                    const unavailable = !!selectedCapability?.availableModels.length && !selectedCapability.availableModels.includes(item)
                    return (
                      <option value={item} key={item} disabled={unavailable}>
                        {formatModelLabel(item)} · {item}{detail?.latest ? ' · latest' : ''}{item === selectedCapability?.recommendedModel ? ' · best for your key' : ''}{unavailable ? ' · unavailable' : ''}
                      </option>
                    )
                  })}
                </select>
              </label>
            </div>
            {selectedModel && (
              <div className={`scheduled-model-detail ${selectedModelUnavailable ? 'is-unavailable' : ''}`}>
                <div>
                  <strong>{selectedModel.description}</strong>
                  {selectedCapability?.availableModels.length ? (
                    <span className={selectedModelUnavailable ? 'unavailable' : 'available'}>
                      {selectedModelUnavailable ? <AlertTriangle size={11} /> : <Check size={11} />}
                      {selectedModelUnavailable ? 'Not available for tested key' : 'Available for tested key'}
                    </span>
                  ) : (
                    <span>Account availability not tested</span>
                  )}
                </div>
                <small>
                  {formatContextWindow(selectedModel.contextWindow)} context · {selectedModel.inputModalities.join(' + ')} input · {selectedModel.reasoningControl} reasoning
                </small>
                {selectedModelUnavailable && selectedCapability?.recommendedModel && (
                  <button
                    type="button"
                    className="btn-secondary"
                    onClick={() => {
                      setForm((current) => ({ ...current, model: selectedCapability.recommendedModel || current.model }))
                      setError(null)
                    }}
                  >
                    Use {selectedCapability.recommendedModel}
                  </button>
                )}
              </div>
            )}
            {selectedCredentialMissing && (
              <div className="scheduled-credential-warning" role="status">
                <AlertTriangle size={12} />
                <span>
                  {form.provider === 'kimi' ? 'Kimi' : 'GLM'} is not connected. {form.schedule === 'manual'
                    ? 'You can save this routine, but Run now stays unavailable until an API key is connected.'
                    : form.enabled
                      ? 'Connect it or turn off Enabled after save to keep this task as a disabled draft.'
                      : 'This disabled draft can be saved; connect the provider before enabling it.'}
                </span>
              </div>
            )}
            <label>
              <span>Approval mode</span>
              <select
                value={form.approvalMode}
                onChange={(e) => setForm((current) => ({
                  ...current,
                  approvalMode: e.target.value as 'manual' | 'accept_edits' | 'auto' | 'skip',
                }))}
              >
                <option value="manual">Manual — ask before mutations</option>
                <option value="accept_edits">Accept edits — allow file changes, ask for shell/Git</option>
                <option value="auto">Auto — approve reviewed safe actions</option>
                <option value="skip">Skip — bypass ordinary approvals</option>
              </select>
            </label>
            <label className="scheduled-enabled">
              <input
                type="checkbox"
                checked={form.schedule === 'manual' ? true : form.enabled}
                disabled={form.schedule === 'manual'}
                onChange={(e) => setForm((current) => ({ ...current, enabled: e.target.checked }))}
              />
              <span>{form.schedule === 'manual' ? 'Runs only with Run now' : 'Enabled after save'}</span>
            </label>
            <div className="scheduled-safety">
              <AlertTriangle size={12} />
              <span>Every run gets its own chat and selected model. A manual-only routine never auto-runs or keeps the computer awake. Manual approval waits before mutations; Auto allows reviewed project-local edits; Skip still hard-gates deletion, computer use, SSH, and MCP.</span>
            </div>
            {error && <div className="scheduled-error" role="alert"><AlertTriangle size={12} /> {error}</div>}
            <button className="btn-primary scheduled-save" onClick={() => void save()} disabled={busy !== null || !form.prompt.trim() || selectedModelUnavailable || enabledScheduleNeedsCredential}>
              {busy === 'save' ? <><Loader2 size={12} className="spin" /> Saving…</> : <><Plus size={13} /> {editing ? 'Save changes' : 'Create task'}</>}
            </button>
          </section>
        </div>
      </div>
      {deleteTarget && (
        <div className="scheduled-delete-backdrop" onMouseDown={() => busy === null && setDeleteTarget(null)}>
          <div
            className="scheduled-delete-dialog"
            role="alertdialog"
            aria-modal="true"
            aria-labelledby="scheduled-delete-title"
            aria-describedby="scheduled-delete-description"
            onMouseDown={(event) => event.stopPropagation()}
          >
            <div className="scheduled-delete-icon"><Trash2 size={16} /></div>
            <div>
              <h4 id="scheduled-delete-title">Delete scheduled task?</h4>
              <p id="scheduled-delete-description">
                <strong>“{deleteTarget.task.name.trim() || deleteTarget.task.prompt.trim().replace(/\s+/g, ' ').slice(0, 72) || 'Untitled task'}”</strong>
                {' '}({formatSchedule(deleteTarget.task)}) will be removed. Any active scheduled run will stop.
              </p>
              {deleteTarget.preview.activeRunCount > 0 && (
                <div className="scheduled-delete-active" role="status">
                  <Loader2 size={12} className="spin" />
                  {deleteTarget.preview.activeRunCount} active run{deleteTarget.preview.activeRunCount === 1 ? '' : 's'} will be stopped before cleanup.
                </div>
              )}
              <label className={`scheduled-delete-data ${deleteTarget.preview.runChatCount === 0 ? 'is-disabled' : ''}`}>
                <input
                  type="checkbox"
                  checked={deleteTarget.deleteRunData}
                  disabled={busy !== null || deleteTarget.preview.runChatCount === 0}
                  onChange={(event) => setDeleteTarget((current) => current ? ({ ...current, deleteRunData: event.target.checked }) : current)}
                />
                <span>
                  <strong>Also delete run data from this device</strong>
                  <small>
                    {deleteTarget.preview.runChatCount === 0
                      ? 'No linked run chats are stored.'
                      : `Delete ${deleteTarget.preview.runChatCount} linked run chat${deleteTarget.preview.runChatCount === 1 ? '' : 's'}, including transcripts, drafts, artifacts, and clean isolated worktrees.`}
                  </small>
                </span>
              </label>
              {deleteTarget.deleteRunData && (
                <div className="scheduled-delete-warning">
                  <AlertTriangle size={13} />
                  <span>This cannot be undone. A run chat with uncommitted worktree changes blocks the entire deletion before anything is removed.</span>
                </div>
              )}
              {(deleteTarget.preview.missingRunChats > 0 || deleteTarget.preview.protectedRunChats > 0) && (
                <div className="scheduled-delete-note">
                  {deleteTarget.preview.missingRunChats + deleteTarget.preview.protectedRunChats} stale or unverified run record{deleteTarget.preview.missingRunChats + deleteTarget.preview.protectedRunChats === 1 ? '' : 's'} will be removed from the index, but cannot authorize deleting a chat.
                </div>
              )}
              <div className="scheduled-delete-actions">
                <button className="btn-secondary" disabled={busy !== null} onClick={() => setDeleteTarget(null)}>Keep task</button>
                <button className="btn-danger" disabled={busy !== null} onClick={() => void confirmDelete()}>
                  {busy === `delete:${deleteTarget.task.id}` ? <><Loader2 size={12} className="spin" /> Deleting…</> : 'Delete task'}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
      {confirmationDialog}
    </>
  )
}
