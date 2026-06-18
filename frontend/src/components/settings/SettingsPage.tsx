import { useState, useEffect, useMemo } from 'react'
import { useSettingsStore, Settings } from '../../stores/settingsStore'
import { useProjectStore, ProjectInfo } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { UpdateSettings, ApplyDefaultToProjects, ListProjects, GetClipboardText, CheckProviderHealth, ListUserSnippets, SaveUserSnippet, DeleteUserSnippet, GetBuildInfo, GetDiagnostics, DiagnosticsReport, GetRecentLogs, ClearLogs, ExportAllDataBase64, ImportAllDataBase64, CleanupOldData, CleanupPreviewDefaults, ListPreImportBackups, DeletePreImportBackup, RestorePreImportBackup, ListAutoBackups, DeleteAutoBackup, RestoreAutoBackup, OpenConfigDir, OpenAutoBackupsDir, ExportLogsCSV } from '../../../wailsjs/go/studio/Studio'
import { Eye, EyeOff, Save, Server, Sun, Moon, CheckCircle, AlertCircle, Keyboard, Info, Clipboard, Brain, Wifi, Loader2, DollarSign, Trash2, Plus, RotateCcw, Activity, Download, AlertTriangle, FileText, Archive, Upload, Sparkle, History, FolderOpen } from 'lucide-react'
import { resetAllPreferences } from '../../lib/mutedProjects'

const KEY_PROVIDER_MAP: Record<string, keyof Settings> = {
  glm: 'glmKey',
  minimax: 'minimaxKey',
  kimi: 'kimiKey',
  deepseek: 'deepseekKey',
}

const PROVIDER_COLORS: Record<string, string> = {
  glm: '#4ade80',
  minimax: '#f472b6',
  kimi: '#fb923c',
  deepseek: '#60a5fa',
  ollama: '#94a3b8',
}

export function SettingsPage() {
  const settings = useSettingsStore((s) => s.settings)
  const providers = useSettingsStore((s) => s.providers)
  const updateField = useSettingsStore((s) => s.updateField)
  const projects = useProjectStore((s) => s.projects)

  const [visibleKeys, setVisibleKeys] = useState<Record<string, boolean>>({})
  const [saving, setSaving] = useState(false)
  const [feedback, setFeedback] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const [pasteErrors, setPasteErrors] = useState<Record<string, string | null>>({})
  // Per-provider health-check state — keyed by provider ID. `pending` means
  // a probe is in flight; `result` is the most recent ProviderHealthInfo.
  // Cleared when the user edits the API key so a stale "OK" doesn't lie
  // after a key change.
  const [healthChecking, setHealthChecking] = useState<Record<string, boolean>>({})
  const [healthResults, setHealthResults] = useState<Record<string, any>>({})
  const [local, setLocal] = useState<Settings>({ ...settings })

  // User snippets (iter 490+). Loaded on mount; refreshed after save/delete.
  const [snippets, setSnippets] = useState<{ id: string; name: string; body: string; updatedAt: number }[]>([])
  const [snippetName, setSnippetName] = useState('')
  const [snippetBody, setSnippetBody] = useState('')
  const [snippetSaving, setSnippetSaving] = useState(false)
  const [snippetError, setSnippetError] = useState<string | null>(null)
  const [deletingSnippetId, setDeletingSnippetId] = useState<string | null>(null)

  const refreshSnippets = () => {
    ListUserSnippets().then((list: any) => {
      setSnippets((list || []).map((s: any) => ({ id: s.id, name: s.name, body: s.body, updatedAt: s.updatedAt })))
    }).catch((e) => console.warn('ListUserSnippets failed:', e))
  }
  useEffect(() => { refreshSnippets() }, [])

  // Toast preference (iter 530+) — frontend-only setting persisted in
  // localStorage. Default ON.
  const [toastsEnabled, setToastsEnabled] = useState<boolean>(() => {
    try { return localStorage.getItem('gokin:toasts-enabled') !== '0' } catch { return true }
  })
  const setToastsPref = (v: boolean) => {
    setToastsEnabled(v)
    try { localStorage.setItem('gokin:toasts-enabled', v ? '1' : '0') } catch { /* unavailable */ }
    // Notify the live ToastStack so it reflects the change without remount.
    window.dispatchEvent(new CustomEvent('gokin:toasts-toggled'))
  }

  // Sound preference (iter 570+). Default OFF — sounds are intrusive.
  const [soundEnabled, setSoundEnabled] = useState<boolean>(() => {
    try { return localStorage.getItem('gokin:sound-on-complete') === '1' } catch { return false }
  })
  const setSoundPref = (v: boolean) => {
    setSoundEnabled(v)
    try { localStorage.setItem('gokin:sound-on-complete', v ? '1' : '0') } catch { /* unavailable */ }
    window.dispatchEvent(new CustomEvent('gokin:sound-toggled'))
  }

  // Reset preferences (iter 630+). Two-stage confirmation so an accidental
  // click can't wipe state. Resets local component state alongside the
  // localStorage clear so the toggles in this section visibly flip back.
  const [confirmReset, setConfirmReset] = useState(false)
  const [resetFeedback, setResetFeedback] = useState<string | null>(null)

  // Build info — version string is shown next to "About" stats; full info is
  // used in the diagnostics modal. Loaded once on mount.
  const [buildInfo, setBuildInfo] = useState<{ version: string; goVersion: string; os: string; arch: string; numCPU: number } | null>(null)
  useEffect(() => {
    GetBuildInfo().then((bi: any) => setBuildInfo(bi)).catch(() => { /* fall back to "—" */ })
  }, [])

  // Diagnostics modal (iter 700+). Opens via the About → Run diagnostics
  // button. Lazy-loads the report so the heavy disk walks only happen on
  // demand. State: showDiag (open), diagInfo (last loaded report),
  // diagLoading (in-flight), diagError (failure message), diagCopied
  // (transient "Copied!" pill on the Copy Report button).
  const [showDiag, setShowDiag] = useState(false)
  const [diagInfo, setDiagInfo] = useState<any>(null)
  const [diagLoading, setDiagLoading] = useState(false)
  const [diagError, setDiagError] = useState<string | null>(null)
  const [diagCopied, setDiagCopied] = useState(false)
  const openDiagnostics = () => {
    setShowDiag(true)
    setDiagError(null)
    setDiagLoading(true)
    // Reset cleanup UI on each open so a stale preview from a previous
    // session doesn't surface across project switches.
    setCleanupPreview(null)
    setCleanupResult(null)
    setCleanupError(null)
    GetDiagnostics().then((info: any) => {
      setDiagInfo(info)
      setDiagLoading(false)
    }).catch((e: any) => {
      setDiagError(String(e?.message || e || 'failed to load'))
      setDiagLoading(false)
    })
  }
  // Auto-backups (iter 850+). Mirror of iter 810+ pre-import panel but for
  // the once-per-24h auto-backups iter 840+ writes to configDir/backups/.
  const [autoList, setAutoList] = useState<any[]>([])
  const [autoLoading, setAutoLoading] = useState(false)
  const [autoError, setAutoError] = useState<string | null>(null)
  const [confirmDeleteAuto, setConfirmDeleteAuto] = useState<string | null>(null)
  const [confirmRestoreAuto, setConfirmRestoreAuto] = useState<any>(null)
  const [autoRestoreBusy, setAutoRestoreBusy] = useState(false)
  const [autoRestoreSuccess, setAutoRestoreSuccess] = useState<string | null>(null)
  const refreshAutoBackupsList = () => {
    setAutoLoading(true)
    setAutoError(null)
    ListAutoBackups().then((list: any) => {
      setAutoList(list || [])
      setAutoLoading(false)
    }).catch((e: any) => {
      setAutoError(String(e?.message || e || 'list failed'))
      setAutoLoading(false)
    })
  }
  useEffect(() => { refreshAutoBackupsList() }, [])
  const handleAutoDelete = async (name: string) => {
    try {
      await DeleteAutoBackup(name)
      setConfirmDeleteAuto(null)
      refreshAutoBackupsList()
    } catch (e: any) {
      setAutoError(String(e?.message || e || 'delete failed'))
    }
  }
  const handleAutoRestore = async (name: string) => {
    setAutoRestoreBusy(true)
    setAutoError(null)
    try {
      const result: any = await RestoreAutoBackup(name)
      setAutoRestoreSuccess(
        `Restored from ${name}. Current data backed up at ${result.preBackupPath}. RESTART REQUIRED.`,
      )
      setConfirmRestoreAuto(null)
      refreshAutoBackupsList()
    } catch (e: any) {
      setAutoError(String(e?.message || e || 'restore failed'))
    } finally {
      setAutoRestoreBusy(false)
    }
  }

  // Pre-import backups (iter 810+). Inline panel that lists rollback
  // snapshots iter 750+ Restore left as siblings of configDir. Lets users
  // delete individual ones (free disk) or restore from a specific one
  // (revert to that point in time, current data → new safety backup).
  const [backupsList, setBackupsList] = useState<any[]>([])
  const [backupsLoading, setBackupsLoading] = useState(false)
  const [backupsError, setBackupsError] = useState<string | null>(null)
  const [confirmDeleteBackup, setConfirmDeleteBackup] = useState<string | null>(null)
  const [confirmRestoreBackup, setConfirmRestoreBackup] = useState<any>(null)
  const [restoreBackupBusy, setRestoreBackupBusy] = useState(false)
  const [restoreBackupSuccess, setRestoreBackupSuccess] = useState<string | null>(null)

  const refreshBackupsList = () => {
    setBackupsLoading(true)
    setBackupsError(null)
    ListPreImportBackups().then((list: any) => {
      setBackupsList(list || [])
      setBackupsLoading(false)
    }).catch((e: any) => {
      setBackupsError(String(e?.message || e || 'list failed'))
      setBackupsLoading(false)
    })
  }
  useEffect(() => { refreshBackupsList() }, [])
  const handleBackupDelete = async (name: string) => {
    try {
      await DeletePreImportBackup(name)
      setConfirmDeleteBackup(null)
      refreshBackupsList()
    } catch (e: any) {
      setBackupsError(String(e?.message || e || 'delete failed'))
    }
  }
  const handleBackupRestore = async (name: string) => {
    setRestoreBackupBusy(true)
    setBackupsError(null)
    try {
      const result: any = await RestorePreImportBackup(name)
      setRestoreBackupSuccess(
        `Restored from ${name}. Current data backed up at ${result.preBackupPath}. RESTART REQUIRED.`,
      )
      setConfirmRestoreBackup(null)
      refreshBackupsList()
    } catch (e: any) {
      setBackupsError(String(e?.message || e || 'restore failed'))
    } finally {
      setRestoreBackupBusy(false)
    }
  }
  const formatBackupTime = (ms: number) => {
    if (!ms) return '—'
    const d = new Date(ms)
    return d.toLocaleString()
  }

  // Cleanup (iter 770+). Two-stage: preview + confirm. The preview runs in
  // dry-run mode so the user sees the exact counts before agreeing to delete.
  const [cleanupBusy, setCleanupBusy] = useState(false)
  const [cleanupError, setCleanupError] = useState<string | null>(null)
  const [cleanupPreview, setCleanupPreview] = useState<any>(null)
  const [cleanupResult, setCleanupResult] = useState<any>(null)

  const handleCleanupPreview = async () => {
    setCleanupBusy(true)
    setCleanupError(null)
    setCleanupResult(null)
    try {
      const preview: any = await CleanupPreviewDefaults()
      setCleanupPreview(preview)
    } catch (e: any) {
      setCleanupError(String(e?.message || e || 'preview failed'))
    } finally {
      setCleanupBusy(false)
    }
  }
  const handleCleanupConfirm = async () => {
    setCleanupBusy(true)
    setCleanupError(null)
    try {
      const result: any = await CleanupOldData({ replayAgeDays: 7, preImportDays: 30, dryRun: false })
      setCleanupResult(result)
      setCleanupPreview(null)
      // Refresh diagnostics so the stale-replays check updates.
      if (showDiag) {
        GetDiagnostics().then((info: any) => setDiagInfo(info)).catch(() => {})
      }
      setTimeout(() => setCleanupResult(null), 8000)
    } catch (e: any) {
      setCleanupError(String(e?.message || e || 'cleanup failed'))
    } finally {
      setCleanupBusy(false)
    }
  }
  const handleCopyDiagnostics = async () => {
    try {
      const text = await DiagnosticsReport()
      try { await navigator.clipboard.writeText(text) } catch { /* clipboard blocked */ }
      setDiagCopied(true)
      setTimeout(() => setDiagCopied(false), 1800)
    } catch (e: any) {
      setDiagError(String(e?.message || e || 'failed to generate report'))
    }
  }
  // Close-on-Esc + skip while typing (IME safety inherited from Escape).
  useEffect(() => {
    if (!showDiag) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if ((e as any).isComposing || e.keyCode === 229) return
        setShowDiag(false)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [showDiag])

  // Logs viewer modal (iter 710+). Companion to diagnostics — surfaces
  // recent backend events (errors, warnings, retries) so users can debug
  // issues without launching with --verbose stderr.
  const [showLogs, setShowLogs] = useState(false)
  const [logs, setLogs] = useState<{ timestampMs: number; level: string; source: string; message: string; count?: number }[]>([])
  const [logFilter, setLogFilter] = useState<'all' | 'info' | 'warn' | 'error'>('all')
  // iter 920+: source filter alongside level. 'all' or a specific source
  // string like "agent" / "settings" / "project" / "config". Dynamic chip
  // list — UI populates from unique sources in the current logs.
  const [logSourceFilter, setLogSourceFilter] = useState<string>('all')
  const [logsLoading, setLogsLoading] = useState(false)
  const refreshLogs = () => {
    setLogsLoading(true)
    GetRecentLogs().then((entries: any) => {
      setLogs((entries || []).map((e: any) => ({
        timestampMs: e.timestampMs, level: e.level, source: e.source, message: e.message, count: e.count,
      })))
      setLogsLoading(false)
    }).catch(() => { setLogsLoading(false) })
  }
  const openLogs = (preset?: { level?: 'all' | 'info' | 'warn' | 'error' }) => {
    setShowLogs(true)
    setLogFilter(preset?.level || 'all')
    setLogSourceFilter('all')
    refreshLogs()
  }
  // iter 990+: status-bar error indicator dispatches `gokin:show-logs` after
  // App.tsx switches to the Settings tab (so this component is mounted by the
  // time the event lands). detail.level pre-filters the modal — typically
  // 'error' since the indicator only counts errors.
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent).detail || {}
      openLogs({ level: detail.level })
    }
    window.addEventListener('gokin:show-logs', handler)
    return () => window.removeEventListener('gokin:show-logs', handler)
  }, [])
  const handleClearLogs = async () => {
    try { await ClearLogs(); refreshLogs() } catch (e: any) {
      console.error('ClearLogs failed:', e)
    }
  }
  useEffect(() => {
    if (!showLogs) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if ((e as any).isComposing || e.keyCode === 229) return
        setShowLogs(false)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [showLogs])
  const filteredLogs = useMemo(() => {
    return logs.filter((l) => {
      if (logFilter !== 'all' && l.level !== logFilter) return false
      if (logSourceFilter !== 'all' && l.source !== logSourceFilter) return false
      return true
    })
  }, [logs, logFilter, logSourceFilter])
  // Unique source strings present in the current log set, alphabetically
  // ordered for stable rendering. Used to build the source-filter chip row.
  const logSources = useMemo(() => {
    const set = new Set<string>()
    for (const l of logs) if (l.source) set.add(l.source)
    return Array.from(set).sort()
  }, [logs])
  const logCounts = useMemo(() => ({
    all: logs.length,
    info: logs.filter((l) => l.level === 'info').length,
    warn: logs.filter((l) => l.level === 'warn').length,
    error: logs.filter((l) => l.level === 'error').length,
  }), [logs])
  const handleResetPreferences = () => {
    const removed = resetAllPreferences()
    setToastsEnabled(true)
    setSoundEnabled(false)
    setConfirmReset(false)
    setResetFeedback(`Cleared ${removed} preference${removed === 1 ? '' : 's'}. Live toggles updated; quiet mode + budget alerts will re-init on next chat panel mount.`)
    setTimeout(() => setResetFeedback(null), 6000)
  }

  // Backup / Restore (iter 750+). One-click snapshot of the entire config
  // directory: config.yaml + history + drafts + pins + snippets + memory.
  // Useful before upgrades, for machine migration, or troubleshooting.
  // Backend returns base64-encoded tar.gz; frontend builds a Blob download
  // so we don't need a native file picker.
  const [backupBusy, setBackupBusy] = useState(false)
  const [backupError, setBackupError] = useState<string | null>(null)
  const [backupSuccess, setBackupSuccess] = useState<string | null>(null)
  const [restoreBusy, setRestoreBusy] = useState(false)
  const [restoreError, setRestoreError] = useState<string | null>(null)
  const [restoreSuccess, setRestoreSuccess] = useState<string | null>(null)
  const [confirmRestore, setConfirmRestore] = useState<{ filename: string; base64: string; size: number } | null>(null)

  const handleBackup = async () => {
    setBackupBusy(true)
    setBackupError(null)
    setBackupSuccess(null)
    try {
      const result: any = await ExportAllDataBase64()
      // Decode base64 → Uint8Array → Blob → download link.
      const binary = atob(result.base64)
      const bytes = new Uint8Array(binary.length)
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
      const blob = new Blob([bytes], { type: 'application/gzip' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = result.filename
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
      const kb = (result.size / 1024).toFixed(1)
      setBackupSuccess(`Saved ${result.filesCount} file${result.filesCount === 1 ? '' : 's'} (${kb} KB).`)
      setTimeout(() => setBackupSuccess(null), 6000)
    } catch (e: any) {
      setBackupError(String(e?.message || e || 'export failed'))
    } finally {
      setBackupBusy(false)
    }
  }

  const handleRestoreFile = (file: File) => {
    setRestoreError(null)
    setRestoreSuccess(null)
    const reader = new FileReader()
    reader.onload = () => {
      const result = reader.result
      if (typeof result !== 'string') {
        setRestoreError('Could not read file as data URL')
        return
      }
      // data URL prefix: "data:application/gzip;base64,XXXX" — strip up to comma.
      const commaIdx = result.indexOf(',')
      const base64 = commaIdx >= 0 ? result.slice(commaIdx + 1) : result
      const sizeKB = (file.size / 1024).toFixed(1)
      setConfirmRestore({ filename: file.name, base64, size: file.size })
      setRestoreSuccess(null)
      setRestoreError(null)
      // 100 MB sanity guard pre-upload too.
      if (file.size > 100 * 1024 * 1024) {
        setRestoreError(`File too large (${sizeKB} KB). Cap is 100 MB.`)
        setConfirmRestore(null)
      }
    }
    reader.onerror = () => {
      setRestoreError('Failed to read file')
    }
    reader.readAsDataURL(file)
  }

  const handleConfirmRestore = async () => {
    if (!confirmRestore) return
    setRestoreBusy(true)
    setRestoreError(null)
    try {
      const result: any = await ImportAllDataBase64(confirmRestore.base64)
      setRestoreSuccess(
        `Imported ${result.filesImported} file${result.filesImported === 1 ? '' : 's'}. ` +
        (result.preBackupPath ? `Previous data backed up at:\n${result.preBackupPath}\n\n` : '') +
        `RESTART REQUIRED — in-memory state was not reloaded. Close the app and reopen to see the restored projects.`
      )
      setConfirmRestore(null)
    } catch (e: any) {
      setRestoreError(String(e?.message || e || 'import failed'))
    } finally {
      setRestoreBusy(false)
    }
  }

  useEffect(() => { setLocal({ ...settings }) }, [settings])

  const allMessages = useChatStore((s) => s.messages)
  const totalMessages = useMemo(() => Object.values(allMessages).reduce((sum, msgs) => sum + msgs.length, 0), [allMessages])

  const handleChange = (key: keyof Settings, value: string) => {
    setLocal((prev) => ({ ...prev, [key]: value }))
    if (key === 'theme') {
      document.documentElement.setAttribute('data-theme', value)
      updateField('theme', value)
    }
    // Clear stale health-check result if the user edits an API key — a
    // green "Connected" badge would otherwise lie about the new (untested)
    // key. The provider ID is derivable from the key's name.
    const keyToProvider: Record<string, string> = {
      glmKey: 'glm', minimaxKey: 'minimax', kimiKey: 'kimi', deepseekKey: 'deepseek', ollamaUrl: 'ollama',
    }
    const provider = keyToProvider[key as string]
    if (provider && healthResults[provider]) {
      setHealthResults((r) => {
        const next = { ...r }; delete next[provider]; return next
      })
    }
  }

  // Probe the provider's API to verify the key + connectivity. Side-effects:
  // sets `healthChecking[provider]=true` while in flight, then stores the
  // ProviderHealthInfo in `healthResults[provider]` (cleared on key edit).
  const runHealthCheck = async (provider: string) => {
    setHealthChecking((s) => ({ ...s, [provider]: true }))
    try {
      const info: any = await CheckProviderHealth(provider)
      setHealthResults((r) => ({ ...r, [provider]: info }))
    } catch (e: any) {
      setHealthResults((r) => ({
        ...r,
        [provider]: { ok: false, error: String(e?.message || e || 'check failed') },
      }))
    } finally {
      setHealthChecking((s) => ({ ...s, [provider]: false }))
    }
  }

  const handleProviderChange = (provider: string) => {
    const p = providers.find((pr) => pr.id === provider)
    const models = p ? p.models : []
    setLocal((prev) => ({ ...prev, defaultProvider: provider, defaultModel: models[0] || '' }))
  }

  const modelsForProvider = (pid: string) => providers.find((p) => p.id === pid)?.models || []

  const handleSave = async () => {
    setSaving(true)
    setFeedback(null)
    try {
      await UpdateSettings({
        projects: [],
        settings: {
          theme: local.theme,
          defaultProvider: local.defaultProvider,
          defaultModel: local.defaultModel,
          glmKey: local.glmKey,
          minimaxKey: local.minimaxKey,
          kimiKey: local.kimiKey,
          deepseekKey: local.deepseekKey,
          ollamaUrl: local.ollamaUrl,
          defaultThinkingMode: local.defaultThinkingMode,
          defaultThinkingBudget: local.defaultThinkingBudget,
          defaultBudgetUSD: local.defaultBudgetUSD,
          autoCleanupDisabled: local.autoCleanupDisabled,
          autoBackupEnabled: local.autoBackupEnabled,
        },
      } as any)
      // Only cascade to existing projects when the DEFAULT provider/model
      // actually changed. Otherwise saving (e.g. to add an API key or tweak
      // the Ollama URL) used to silently overwrite every project's
      // per-project provider choice with the default.
      const defaultChanged =
        local.defaultProvider !== settings.defaultProvider ||
        local.defaultModel !== settings.defaultModel
      if (defaultChanged) {
        await ApplyDefaultToProjects()
      }
      const keys: (keyof Settings)[] = ['theme', 'defaultProvider', 'defaultModel', 'glmKey', 'minimaxKey', 'kimiKey', 'deepseekKey', 'ollamaUrl', 'defaultThinkingMode', 'defaultThinkingBudget', 'defaultBudgetUSD', 'autoCleanupDisabled', 'autoBackupEnabled']
      for (const key of keys) updateField(key, local[key] as string | number | boolean)
      const updatedProjects = await ListProjects()
      if (updatedProjects) useProjectStore.getState().setProjects(updatedProjects as ProjectInfo[])
      setFeedback({ type: 'success', text: 'Settings saved.' })
      setTimeout(() => setFeedback(null), 3000)
    } catch (e: any) {
      setFeedback({ type: 'error', text: `Failed: ${String(e)}` })
    } finally {
      setSaving(false)
    }
  }

  const keyProviders = providers.filter((p) => p.id in KEY_PROVIDER_MAP)

  return (
    <div className="settings-page">
      <div className="settings-header-bar">
        <h2 className="settings-title">Settings</h2>
        <button className="btn-primary settings-save-btn" onClick={handleSave} disabled={saving}>
          <Save size={14} />
          {saving ? 'Saving...' : 'Save'}
        </button>
      </div>

      {feedback && (
        <div className={`settings-toast ${feedback.type}`}>
          {feedback.type === 'success' ? <CheckCircle size={14} /> : <AlertCircle size={14} />}
          {feedback.text}
        </div>
      )}

      {/* Theme */}
      <div className="settings-section">
        <h3 className="settings-section-header">Appearance</h3>
        <div className="settings-section-content">
          <div className="theme-toggle">
            <button className={`theme-option ${local.theme === 'dark' ? 'active' : ''}`} onClick={() => handleChange('theme', 'dark')}>
              <Moon size={16} /> Dark
            </button>
            <button className={`theme-option ${local.theme === 'light' ? 'active' : ''}`} onClick={() => handleChange('theme', 'light')}>
              <Sun size={16} /> Light
            </button>
          </div>
        </div>
      </div>

      {/* Default Provider & Model */}
      <div className="settings-section">
        <h3 className="settings-section-header">Default Provider</h3>
        <div className="settings-section-content">
          <div className="provider-grid">
            {providers.map((p) => (
              <button
                key={p.id}
                className={`provider-card ${local.defaultProvider === p.id ? 'selected' : ''}`}
                onClick={() => handleProviderChange(p.id)}
              >
                <span className="provider-card-dot" style={{ background: PROVIDER_COLORS[p.id] || '#888' }} />
                <span className="provider-card-name">{p.name}</span>
                {local.defaultProvider === p.id && <CheckCircle size={14} className="provider-card-check" />}
              </button>
            ))}
          </div>

          <div className="settings-field" style={{ marginTop: 14 }}>
            <label>Model</label>
            <select value={local.defaultModel} onChange={(e) => handleChange('defaultModel', e.target.value)}>
              {modelsForProvider(local.defaultProvider).map((m) => (
                <option key={m} value={m}>{m}</option>
              ))}
            </select>
          </div>
        </div>
      </div>

      {/* Default Thinking Mode */}
      <div className="settings-section">
        <h3 className="settings-section-header"><Brain size={14} /> Default Thinking</h3>
        <div className="settings-section-content">
          <p className="settings-hint">Applied to new projects. Per-project overrides take precedence.</p>
          <div className="settings-field">
            <label>Mode</label>
            <select value={local.defaultThinkingMode} onChange={(e) => setLocal((prev) => ({ ...prev, defaultThinkingMode: e.target.value }))}>
              <option value="">Auto (provider default)</option>
              <option value="enabled">Enabled</option>
              <option value="disabled">Disabled</option>
            </select>
          </div>
          {local.defaultThinkingMode === 'enabled' && (
            <div className="settings-field">
              <label>Budget tokens (0 = provider default · GLM 8192)</label>
              <input
                type="number"
                min="0"
                max="32768"
                step="1024"
                value={local.defaultThinkingBudget}
                onChange={(e) => setLocal((prev) => ({ ...prev, defaultThinkingBudget: parseInt(e.target.value) || 0 }))}
              />
            </div>
          )}
        </div>
      </div>

      {/* Default Project Budget */}
      <div className="settings-section">
        <h3 className="settings-section-header"><DollarSign size={14} /> Default Budget</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            USD spend cap applied to new projects. The chat header chip turns amber at 80% and red at 100%.
            Set 0 to disable. Per-project overrides take precedence; existing projects are not affected.
          </p>
          <div className="settings-field">
            <label>Default budget (USD)</label>
            <input
              type="number"
              min="0"
              max="100000"
              step="0.01"
              placeholder="0 = no budget"
              value={local.defaultBudgetUSD ?? 0}
              onChange={(e) => {
                const v = parseFloat(e.target.value)
                setLocal((prev) => ({ ...prev, defaultBudgetUSD: Number.isNaN(v) ? 0 : Math.max(0, Math.min(100000, v)) }))
              }}
            />
          </div>
        </div>
      </div>

      {/* Notifications (iter 530+) */}
      <div className="settings-section">
        <h3 className="settings-section-header">Notifications</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Toast popups appear bottom-right when an agent finishes (or errors) in a chat session you're not currently viewing. Click to jump to that session.
            OS notifications already fire when the window is unfocused — toasts cover the focused-but-elsewhere case.
          </p>
          <label className="settings-toggle-row">
            <input
              type="checkbox"
              checked={toastsEnabled}
              onChange={(e) => setToastsPref(e.target.checked)}
            />
            <span>Show completion toasts</span>
          </label>
          <label className="settings-toggle-row">
            <input
              type="checkbox"
              checked={soundEnabled}
              onChange={(e) => setSoundPref(e.target.checked)}
            />
            <span>Play chime on completion</span>
          </label>
        </div>
      </div>

      {/* Slash command snippets (iter 490+) */}
      <div className="settings-section">
        <h3 className="settings-section-header">
          Snippets
          <span className="settings-header-badge">{snippets.length}</span>
        </h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Reusable <code>/name</code> commands that expand into the chat input. Pick from the slash autocomplete to insert; review and edit before sending.
            Names use letters, digits, hyphens, and underscores; built-in command names (clear, export, help, etc.) are reserved.
          </p>
          {snippets.length > 0 && (
            <div className="snippets-list">
              {snippets.map((s) => (
                <div key={s.id} className="snippet-row">
                  <div className="snippet-row-main">
                    <code className="snippet-row-name">/{s.name}</code>
                    <span className="snippet-row-body" title={s.body}>{s.body.replace(/\s+/g, ' ').slice(0, 120)}{s.body.length > 120 ? '…' : ''}</span>
                  </div>
                  <button
                    className="icon-btn snippet-row-delete"
                    title="Delete snippet"
                    disabled={deletingSnippetId === s.id}
                    onClick={async () => {
                      setDeletingSnippetId(s.id)
                      try {
                        await DeleteUserSnippet(s.id)
                        refreshSnippets()
                        window.dispatchEvent(new CustomEvent('gokin:snippets-changed'))
                      } catch (e: any) {
                        setSnippetError(`Delete failed: ${String(e?.message || e)}`)
                      } finally {
                        setDeletingSnippetId(null)
                      }
                    }}
                  >
                    {deletingSnippetId === s.id ? <Loader2 size={12} className="tool-spinner" /> : <Trash2 size={12} />}
                  </button>
                </div>
              ))}
            </div>
          )}
          <div className="snippet-form">
            <div className="snippet-form-row">
              <span className="snippet-form-prefix">/</span>
              <input
                type="text"
                className="snippet-form-name"
                placeholder="name (e.g. lint)"
                value={snippetName}
                maxLength={30}
                onChange={(e) => { setSnippetName(e.target.value); setSnippetError(null) }}
              />
            </div>
            <textarea
              className="snippet-form-body"
              placeholder="Body — text inserted into chat input when you pick this snippet"
              value={snippetBody}
              maxLength={10000}
              rows={3}
              onChange={(e) => { setSnippetBody(e.target.value); setSnippetError(null) }}
            />
            {snippetError && <div className="snippet-form-error"><AlertCircle size={12} /> {snippetError}</div>}
            <div className="snippet-form-actions">
              <button
                className="btn-primary"
                disabled={snippetSaving || !snippetName.trim() || !snippetBody.trim()}
                onClick={async () => {
                  setSnippetSaving(true)
                  setSnippetError(null)
                  try {
                    await SaveUserSnippet(snippetName.trim(), snippetBody)
                    setSnippetName('')
                    setSnippetBody('')
                    refreshSnippets()
                    window.dispatchEvent(new CustomEvent('gokin:snippets-changed'))
                  } catch (e: any) {
                    setSnippetError(String(e?.message || e || 'Save failed'))
                  } finally {
                    setSnippetSaving(false)
                  }
                }}
              >
                {snippetSaving ? <><Loader2 size={12} className="tool-spinner" /> Saving…</> : <><Plus size={12} /> Save snippet</>}
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* API Keys */}
      <div className="settings-section">
        <h3 className="settings-section-header">
          API Keys
          <span className="settings-header-badge">
            {keyProviders.filter((p) => { const f = KEY_PROVIDER_MAP[p.id]; return f && local[f]; }).length}/{keyProviders.length}
          </span>
        </h3>
        <div className="settings-section-content">
          <div className="api-key-grid">
            {keyProviders.map((p) => {
              const fieldName = KEY_PROVIDER_MAP[p.id]
              if (!fieldName) return null
              const isVisible = visibleKeys[p.id] || false
              const isConfigured = !!local[fieldName]
              return (
                <div className={`api-key-card ${isConfigured ? 'configured' : ''}`} key={p.id}>
                  <div className="api-key-card-header">
                    <span className="api-key-dot" style={{ background: PROVIDER_COLORS[p.id] }} />
                    <span className="api-key-name">{p.name}</span>
                    {isConfigured
                      ? <span className="api-key-status ok">Connected</span>
                      : <span className="api-key-status">Not set</span>
                    }
                  </div>
                  <div className="password-field">
                    <input
                      type={isVisible ? 'text' : 'password'}
                      value={local[fieldName] as string}
                      onChange={(e) => handleChange(fieldName, e.target.value)}
                      placeholder="sk-..."
                      autoComplete="off"
                      spellCheck={false}
                      maxLength={500}
                    />
                    <button
                      className="icon-btn password-toggle"
                      title="Paste from clipboard"
                      onClick={async () => {
                        // Prefer the Wails native bridge — WebKitGTK on Linux
                        // blocks navigator.clipboard.readText in many setups.
                        // Fall back to the browser API only if the native
                        // binding somehow fails.
                        setPasteErrors((prev) => ({ ...prev, [p.id]: null }))
                        try {
                          const text = await GetClipboardText()
                          if (text) { handleChange(fieldName, text.trim()); return }
                        } catch (e) {
                          console.error('native clipboard failed:', e)
                        }
                        try {
                          const text = await navigator.clipboard.readText()
                          if (text) { handleChange(fieldName, text.trim()); return }
                        } catch (e) {
                          console.error('browser clipboard failed:', e)
                        }
                        setPasteErrors((prev) => ({ ...prev, [p.id]: 'Clipboard unavailable — paste manually (Ctrl+V)' }))
                        setTimeout(() => setPasteErrors((prev) => ({ ...prev, [p.id]: null })), 4000)
                      }}
                    >
                      <Clipboard size={13} />
                    </button>
                    <button
                      className="icon-btn password-toggle"
                      title={isVisible ? 'Hide' : 'Show'}
                      onClick={() => setVisibleKeys((v) => ({ ...v, [p.id]: !v[p.id] }))}
                    >
                      {isVisible ? <EyeOff size={13} /> : <Eye size={13} />}
                    </button>
                  </div>
                  {pasteErrors[p.id] && (
                    <div className="api-key-paste-error">{pasteErrors[p.id]}</div>
                  )}
                  <div className="api-key-health-row">
                    <button
                      className="btn-secondary api-key-test-btn"
                      onClick={() => runHealthCheck(p.id)}
                      disabled={healthChecking[p.id] || !isConfigured}
                      title={!isConfigured ? 'Set the API key first' : 'Probe the provider to verify the key + connectivity'}
                    >
                      {healthChecking[p.id]
                        ? <><Loader2 size={11} className="tool-spinner" /> Testing…</>
                        : <><Wifi size={11} /> Test connection</>}
                    </button>
                    {healthResults[p.id] && (
                      <span className={`api-key-health-result ${healthResults[p.id].ok ? 'ok' : 'fail'}`} title={healthResults[p.id].endpoint || ''}>
                        {healthResults[p.id].ok ? <CheckCircle size={11} /> : <AlertCircle size={11} />}
                        {healthResults[p.id].description || healthResults[p.id].error || 'Unknown'}
                      </span>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      {/* Ollama */}
      <div className="settings-section">
        <h3 className="settings-section-header"><Server size={14} /> Ollama</h3>
        <div className="settings-section-content">
          <div className="settings-field">
            <label>Base URL</label>
            <input type="text" value={local.ollamaUrl} onChange={(e) => handleChange('ollamaUrl', e.target.value)} placeholder="http://localhost:11434" maxLength={300} />
          </div>
          <div className="api-key-health-row">
            <button
              className="btn-secondary api-key-test-btn"
              onClick={() => runHealthCheck('ollama')}
              disabled={healthChecking['ollama']}
              title="Probe the local Ollama daemon to verify it's running"
            >
              {healthChecking['ollama']
                ? <><Loader2 size={11} className="tool-spinner" /> Testing…</>
                : <><Wifi size={11} /> Test connection</>}
            </button>
            {healthResults['ollama'] && (
              <span className={`api-key-health-result ${healthResults['ollama'].ok ? 'ok' : 'fail'}`} title={healthResults['ollama'].endpoint || ''}>
                {healthResults['ollama'].ok ? <CheckCircle size={11} /> : <AlertCircle size={11} />}
                {healthResults['ollama'].description || healthResults['ollama'].error || 'Unknown'}
              </span>
            )}
          </div>
        </div>
      </div>

      {/* Backup & Restore (iter 750+) */}
      <div className="settings-section">
        <h3 className="settings-section-header"><Archive size={14} /> Backup & Restore</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            One-click snapshot of all your data: config, projects, chat history, drafts, pins, memory, snippets, and templates. Useful before upgrades, for machine migration, or for troubleshooting. Restore puts your current data aside as a pre-import backup before overwriting.
          </p>
          <div className="backup-actions">
            <button className="btn-secondary" onClick={handleBackup} disabled={backupBusy}>
              {backupBusy ? <Loader2 size={13} className="spin" /> : <Download size={13} />}
              Back up all data…
            </button>
            <label className="btn-secondary" style={{ cursor: 'pointer' }}>
              <Upload size={13} />
              Restore from backup…
              <input
                type="file"
                accept=".tar.gz,.gz,application/gzip"
                style={{ display: 'none' }}
                onChange={(e) => {
                  const f = e.target.files?.[0]
                  if (f) handleRestoreFile(f)
                  // Reset so re-selecting the same file works again.
                  e.target.value = ''
                }}
                disabled={restoreBusy}
              />
            </label>
          </div>
          {backupSuccess && <div className="settings-toast success" style={{ marginTop: 10 }}><CheckCircle size={13} /> {backupSuccess}</div>}
          {backupError && <div className="settings-toast error" style={{ marginTop: 10 }}><AlertCircle size={13} /> {backupError}</div>}
          {restoreError && <div className="settings-toast error" style={{ marginTop: 10 }}><AlertCircle size={13} /> {restoreError}</div>}
          {restoreSuccess && (
            <div className="settings-toast success" style={{ marginTop: 10, whiteSpace: 'pre-wrap', alignItems: 'flex-start' }}>
              <CheckCircle size={13} />
              <span style={{ flex: 1 }}>{restoreSuccess}</span>
            </div>
          )}
          {confirmRestore && (
            <div className="reset-prefs-confirm" style={{ marginTop: 10 }}>
              <span className="reset-prefs-confirm-text">
                Restore from <b>{confirmRestore.filename}</b> ({(confirmRestore.size / 1024).toFixed(1)} KB)? This will replace your current data. The existing data will be moved aside as a pre-import backup, but a restart will be required.
              </span>
              <div className="reset-prefs-actions">
                <button className="btn-secondary" onClick={() => setConfirmRestore(null)} disabled={restoreBusy}>Cancel</button>
                <button className="btn-danger" onClick={handleConfirmRestore} disabled={restoreBusy}>
                  {restoreBusy ? <Loader2 size={13} className="spin" /> : null}
                  Restore (replaces current data)
                </button>
              </div>
            </div>
          )}
          {/* iter 810+: pre-import backups list */}
          <div className="backups-list-section">
            <div className="backups-list-header">
              <span className="backups-list-title">
                <History size={12} /> Rollback snapshots
              </span>
              <button
                className="btn-icon-tiny"
                onClick={refreshBackupsList}
                title="Refresh list"
                disabled={backupsLoading}
              >
                {backupsLoading ? <Loader2 size={12} className="spin" /> : <RotateCcw size={12} />}
              </button>
            </div>
            <p className="settings-hint" style={{ marginBottom: 8 }}>
              Snapshots created automatically before each Restore. Useful as undo points; auto-cleanup removes them after 90 days.
            </p>
            {restoreBackupSuccess && (
              <div className="settings-toast success" style={{ marginBottom: 10, whiteSpace: 'pre-wrap', alignItems: 'flex-start' }}>
                <CheckCircle size={13} /> <span style={{ flex: 1 }}>{restoreBackupSuccess}</span>
              </div>
            )}
            {backupsError && (
              <div className="settings-toast error" style={{ marginBottom: 10 }}>
                <AlertCircle size={13} /> {backupsError}
              </div>
            )}
            {!backupsLoading && backupsList.length === 0 && (
              <div className="backups-empty">No rollback snapshots yet.</div>
            )}
            {backupsList.map((b: any) => (
              <div key={b.name} className="backup-row">
                <div className="backup-row-info">
                  <div className="backup-row-name">{b.name.replace('.gokin-studio.pre-import-', '')}</div>
                  <div className="backup-row-meta">
                    {formatBackupTime(b.createdAtMs)} · {formatBytes(b.size)}
                  </div>
                </div>
                <div className="backup-row-actions">
                  {confirmDeleteBackup === b.name ? (
                    <>
                      <button className="btn-secondary" onClick={() => setConfirmDeleteBackup(null)}>Cancel</button>
                      <button className="btn-danger" onClick={() => handleBackupDelete(b.name)}>Confirm</button>
                    </>
                  ) : confirmRestoreBackup?.name === b.name ? (
                    <>
                      <button className="btn-secondary" onClick={() => setConfirmRestoreBackup(null)} disabled={restoreBackupBusy}>Cancel</button>
                      <button className="btn-danger" onClick={() => handleBackupRestore(b.name)} disabled={restoreBackupBusy}>
                        {restoreBackupBusy ? <Loader2 size={12} className="spin" /> : null}
                        Restore
                      </button>
                    </>
                  ) : (
                    <>
                      <button
                        className="btn-icon-tiny"
                        onClick={() => setConfirmRestoreBackup(b)}
                        title="Restore from this snapshot"
                      >
                        <Upload size={11} />
                      </button>
                      <button
                        className="btn-icon-tiny btn-icon-danger"
                        onClick={() => setConfirmDeleteBackup(b.name)}
                        title="Delete this snapshot"
                      >
                        <Trash2 size={11} />
                      </button>
                    </>
                  )}
                </div>
              </div>
            ))}
          </div>

          {/* iter 790+: auto-cleanup opt-out toggle */}
          <div className="auto-cleanup-toggle">
            <label>
              <input
                type="checkbox"
                checked={!local.autoCleanupDisabled}
                onChange={(e) =>
                  setLocal((prev) => ({ ...prev, autoCleanupDisabled: !e.target.checked }))
                }
              />
              <span>Auto-clean stale data on startup</span>
            </label>
            <p className="settings-hint">
              Once per 24h, remove replay logs older than 30 days and pre-import backups older than 90 days. Disable if you want full manual control via the "Clean up" button in Diagnostics. Save to apply.
            </p>
          </div>

          {/* iter 840+: auto-backup opt-in toggle */}
          <div className="auto-cleanup-toggle">
            <label>
              <input
                type="checkbox"
                checked={local.autoBackupEnabled}
                onChange={(e) =>
                  setLocal((prev) => ({ ...prev, autoBackupEnabled: e.target.checked }))
                }
              />
              <span>Auto-backup all data on startup</span>
            </label>
            <p className="settings-hint">
              When enabled, writes a daily tar.gz snapshot of all your data to <code>~/.config/gokin-studio/backups/</code>. Keeps the last 7; older ones are pruned automatically. Disk usage: roughly 7× your config size (typically 10-70 MB total). Save to apply.
            </p>
          </div>

          {/* iter 850+: auto-backups list + restore */}
          <div className="backups-list-section">
            <div className="backups-list-header">
              <span className="backups-list-title">
                <Archive size={12} /> Auto-backups
              </span>
              <div style={{ display: 'flex', gap: 4 }}>
                <button
                  className="btn-icon-tiny"
                  onClick={() => { OpenAutoBackupsDir().catch((e) => setAutoError(String(e?.message || e))) }}
                  title="Reveal backups folder in OS file manager"
                >
                  <FolderOpen size={12} />
                </button>
                <button
                  className="btn-icon-tiny"
                  onClick={refreshAutoBackupsList}
                  title="Refresh list"
                  disabled={autoLoading}
                >
                  {autoLoading ? <Loader2 size={12} className="spin" /> : <RotateCcw size={12} />}
                </button>
              </div>
            </div>
            <p className="settings-hint" style={{ marginBottom: 8 }}>
              Daily snapshots from the auto-backup feature above. Click a snapshot to restore (current data → safety backup).
            </p>
            {autoRestoreSuccess && (
              <div className="settings-toast success" style={{ marginBottom: 10, whiteSpace: 'pre-wrap', alignItems: 'flex-start' }}>
                <CheckCircle size={13} /> <span style={{ flex: 1 }}>{autoRestoreSuccess}</span>
              </div>
            )}
            {autoError && (
              <div className="settings-toast error" style={{ marginBottom: 10 }}>
                <AlertCircle size={13} /> {autoError}
              </div>
            )}
            {!autoLoading && autoList.length === 0 && (
              <div className="backups-empty">No auto-backups yet. {local.autoBackupEnabled ? 'First one will be written on next startup.' : 'Enable auto-backup above first.'}</div>
            )}
            {autoList.map((b: any) => (
              <div key={b.filename} className="backup-row">
                <div className="backup-row-info">
                  <div className="backup-row-name">{b.filename.replace(/^auto-backup-/, '').replace(/\.tar\.gz$/, '')}</div>
                  <div className="backup-row-meta">
                    {formatBackupTime(b.createdAtMs)} · {formatBytes(b.size)}
                  </div>
                </div>
                <div className="backup-row-actions">
                  {confirmDeleteAuto === b.filename ? (
                    <>
                      <button className="btn-secondary" onClick={() => setConfirmDeleteAuto(null)}>Cancel</button>
                      <button className="btn-danger" onClick={() => handleAutoDelete(b.filename)}>Confirm</button>
                    </>
                  ) : confirmRestoreAuto?.filename === b.filename ? (
                    <>
                      <button className="btn-secondary" onClick={() => setConfirmRestoreAuto(null)} disabled={autoRestoreBusy}>Cancel</button>
                      <button className="btn-danger" onClick={() => handleAutoRestore(b.filename)} disabled={autoRestoreBusy}>
                        {autoRestoreBusy ? <Loader2 size={12} className="spin" /> : null}
                        Restore
                      </button>
                    </>
                  ) : (
                    <>
                      <button
                        className="btn-icon-tiny"
                        onClick={() => setConfirmRestoreAuto(b)}
                        title="Restore from this snapshot"
                      >
                        <Upload size={11} />
                      </button>
                      <button
                        className="btn-icon-tiny btn-icon-danger"
                        onClick={() => setConfirmDeleteAuto(b.filename)}
                        title="Delete this snapshot"
                      >
                        <Trash2 size={11} />
                      </button>
                    </>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* Reset preferences (iter 630+) */}
      <div className="settings-section">
        <h3 className="settings-section-header">Reset preferences</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Clear all frontend-only preferences (toasts, sound, mute, quiet mode per project, budget alert thresholds). Backend state — projects, sessions, history, snippets — is untouched. Useful when recovering from bad state after upgrades or when you want a clean baseline.
          </p>
          {resetFeedback && <div className="settings-toast success">{resetFeedback}</div>}
          {!confirmReset ? (
            <button className="btn-secondary" onClick={() => setConfirmReset(true)}>
              <RotateCcw size={12} /> Reset all preferences…
            </button>
          ) : (
            <div className="reset-prefs-confirm">
              <span className="reset-prefs-confirm-text">This will clear toasts, sound, mute, quiet-mode, and budget-alert state across every project. Continue?</span>
              <div className="reset-prefs-actions">
                <button className="btn-secondary" onClick={() => setConfirmReset(false)}>Cancel</button>
                <button className="btn-danger" onClick={handleResetPreferences}>Reset</button>
              </div>
            </div>
          )}
        </div>
      </div>

      {/* About & Shortcuts */}
      <div className="settings-section">
        <h3 className="settings-section-header"><Info size={14} /> About</h3>
        <div className="settings-section-content">
          <div className="about-grid">
            <div className="about-stat">
              <span className="about-stat-value">{projects.length}</span>
              <span className="about-stat-label">Projects</span>
            </div>
            <div className="about-stat">
              <span className="about-stat-value">{totalMessages}</span>
              <span className="about-stat-label">Messages</span>
            </div>
            <div className="about-stat">
              <span className="about-stat-value">{providers.length}</span>
              <span className="about-stat-label">Providers</span>
            </div>
            <div className="about-stat">
              <span className="about-stat-value">v{buildInfo?.version || '1.0.0'}</span>
              <span className="about-stat-label">Version</span>
            </div>
          </div>

          {buildInfo && (
            <div className="about-build-info">
              <span>{buildInfo.os}/{buildInfo.arch} · {buildInfo.goVersion} · {buildInfo.numCPU} CPUs</span>
            </div>
          )}

          <div className="about-actions">
            <button className="btn-secondary about-diag-btn" onClick={openDiagnostics} title="Run health checks">
              <Activity size={14} /> Run diagnostics
            </button>
            <button
              className="btn-secondary about-diag-btn"
              onClick={() => { OpenConfigDir().catch((e) => console.error('OpenConfigDir failed:', e)) }}
              title="Reveal ~/.config/gokin-studio in OS file manager"
            >
              <FolderOpen size={14} /> Show config dir
            </button>
          </div>

          <div className="shortcuts-card">
            <div className="shortcuts-title"><Keyboard size={13} /> Keyboard Shortcuts</div>
            <div className="shortcuts-grid">
              <span className="shortcut-key">Ctrl+1</span><span>Chat</span>
              <span className="shortcut-key">Alt+1..9</span><span>Jump to session N</span>
              <span className="shortcut-key">Ctrl+2</span><span>Files</span>
              <span className="shortcut-key">Ctrl+3</span><span>Settings</span>
              <span className="shortcut-key">Ctrl+B</span><span>Toggle sidebar</span>
              <span className="shortcut-key">Ctrl+J</span><span>Toggle context panel</span>
              <span className="shortcut-key">Enter</span><span>Send message</span>
              <span className="shortcut-key">Shift+Enter</span><span>New line</span>
              <span className="shortcut-key">Ctrl+L</span><span>Clear chat</span>
              <span className="shortcut-key">Ctrl+T</span><span>New chat tab</span>
              <span className="shortcut-key">Ctrl+K</span><span>Command palette</span>
              <span className="shortcut-key">Ctrl+P</span><span>File picker</span>
              <span className="shortcut-key">Ctrl+F</span><span>Search in chat</span>
              <span className="shortcut-key">Ctrl+Shift+F</span><span>Search across all sessions</span>
              <span className="shortcut-key">Ctrl+Shift+A</span><span>Agent activity timeline</span>
              <span className="shortcut-key">Ctrl+Shift+P</span><span>Search projects in sidebar</span>
              <span className="shortcut-key">Ctrl+/  or  ?</span><span>Help & shortcuts (in-app)</span>
              <span className="shortcut-key">Ctrl+M</span><span>Quick model switcher</span>
              <span className="shortcut-key">Ctrl+`</span><span>Toggle terminal</span>
              <span className="shortcut-key">Ctrl+PgUp/Dn</span><span>Cycle sessions</span>
              <span className="shortcut-key">Escape</span><span>Close dialogs / stop agent</span>
              <span className="shortcut-key">j / k</span><span>Navigate messages</span>
            </div>
          </div>
        </div>
      </div>

      {/* Diagnostics modal (iter 700+) */}
      {showDiag && (
        <div className="dispatch-backdrop diag-backdrop" onClick={() => setShowDiag(false)}>
          <div className="dispatch-modal diag-modal" onClick={(e) => e.stopPropagation()}>
            <div className="dispatch-modal-header">
              <Activity size={14} />
              <span>Diagnostics</span>
            </div>
            <div className="dispatch-modal-body">
              {diagLoading && (
                <div className="diag-loading">
                  <Loader2 size={14} className="spin" /> Running health checks…
                </div>
              )}
              {diagError && (
                <div className="diag-error">
                  <AlertCircle size={14} /> {diagError}
                </div>
              )}
              {diagInfo && !diagLoading && (
                <>
                  <div className="diag-stats">
                    <div className="diag-stat"><span className="diag-stat-k">Version</span><span className="diag-stat-v">v{diagInfo.build?.version}</span></div>
                    <div className="diag-stat"><span className="diag-stat-k">Go</span><span className="diag-stat-v">{diagInfo.build?.goVersion}</span></div>
                    <div className="diag-stat"><span className="diag-stat-k">Platform</span><span className="diag-stat-v">{diagInfo.build?.os}/{diagInfo.build?.arch}</span></div>
                    <div className="diag-stat"><span className="diag-stat-k">CPUs</span><span className="diag-stat-v">{diagInfo.build?.numCPU}</span></div>
                    <div className="diag-stat"><span className="diag-stat-k">Projects</span><span className="diag-stat-v">{diagInfo.totalProjects}</span></div>
                    <div className="diag-stat"><span className="diag-stat-k">Sessions</span><span className="diag-stat-v">{diagInfo.totalSessions}</span></div>
                    <div className="diag-stat"><span className="diag-stat-k">Config size</span><span className="diag-stat-v">{formatBytes(diagInfo.configDirSize)}</span></div>
                    <div className="diag-stat"><span className="diag-stat-k">History size</span><span className="diag-stat-v">{formatBytes(diagInfo.historyBytes)}</span></div>
                  </div>
                  <div className="diag-config-path" title={diagInfo.configDir}>
                    <span className="diag-config-label">Config:</span>
                    <span className="diag-config-value">{diagInfo.configDir}</span>
                    <button
                      className="btn-icon-tiny"
                      onClick={() => { OpenConfigDir().catch((e) => setDiagError(String(e?.message || e))) }}
                      title="Reveal in OS file manager"
                      style={{ marginLeft: 'auto' }}
                    >
                      <FolderOpen size={11} />
                    </button>
                  </div>
                  <div className="diag-checks">
                    {(diagInfo.checks || []).map((c: any, i: number) => (
                      <div key={i} className={`diag-check diag-check-${c.status}`}>
                        <div className="diag-check-icon">
                          {c.status === 'ok' && <CheckCircle size={14} />}
                          {c.status === 'warn' && <AlertTriangle size={14} />}
                          {c.status === 'error' && <AlertCircle size={14} />}
                        </div>
                        <div className="diag-check-body">
                          <div className="diag-check-name">{c.name}</div>
                          <div className="diag-check-msg">{c.message}</div>
                          {c.detail && <div className="diag-check-detail">{c.detail}</div>}
                        </div>
                      </div>
                    ))}
                  </div>
                </>
              )}
            </div>
            {/* Cleanup preview + result (iter 770+) — inline above actions */}
            {cleanupPreview && (
              <div className="cleanup-preview">
                <Sparkle size={13} />
                <div className="cleanup-preview-body">
                  <strong>
                    {(cleanupPreview.staleReplaysRemoved || 0) + (cleanupPreview.preImportDirsRemoved || 0) + (cleanupPreview.stagingDirsRemoved || 0) + (cleanupPreview.autoBackupsRemoved || 0)} item(s) to clean up
                  </strong>
                  {' — '}
                  {(() => {
                    const parts: string[] = []
                    if (cleanupPreview.staleReplaysRemoved > 0) parts.push(`${cleanupPreview.staleReplaysRemoved} stale replay log(s)`)
                    if (cleanupPreview.preImportDirsRemoved > 0) parts.push(`${cleanupPreview.preImportDirsRemoved} old pre-import backup(s)`)
                    if (cleanupPreview.stagingDirsRemoved > 0) parts.push(`${cleanupPreview.stagingDirsRemoved} orphaned staging dir(s)`)
                    if (cleanupPreview.autoBackupsRemoved > 0) parts.push(`${cleanupPreview.autoBackupsRemoved} excess auto-backup(s)`)
                    return parts.join(', ')
                  })()}
                  {' '}({formatBytes(cleanupPreview.bytesFreed)})
                </div>
                <div className="cleanup-preview-actions">
                  <button className="btn-secondary" onClick={() => setCleanupPreview(null)}>Cancel</button>
                  <button className="btn-danger" onClick={handleCleanupConfirm} disabled={cleanupBusy}>
                    {cleanupBusy ? <Loader2 size={12} className="spin" /> : null}
                    Delete
                  </button>
                </div>
              </div>
            )}
            {cleanupResult && (
              <div className="settings-toast success" style={{ margin: '0 0 12px' }}>
                <CheckCircle size={13} /> Cleaned up {(cleanupResult.staleReplaysRemoved || 0) + (cleanupResult.preImportDirsRemoved || 0) + (cleanupResult.stagingDirsRemoved || 0) + (cleanupResult.autoBackupsRemoved || 0)} item(s), freed {formatBytes(cleanupResult.bytesFreed)}.
              </div>
            )}
            {cleanupError && (
              <div className="settings-toast error" style={{ margin: '0 0 12px' }}>
                <AlertCircle size={13} /> {cleanupError}
              </div>
            )}
            <div className="dispatch-modal-actions">
              <button className="btn-secondary" onClick={openDiagnostics} disabled={diagLoading}>
                <RotateCcw size={14} /> Refresh
              </button>
              <button className="btn-secondary" onClick={() => { setShowDiag(false); openLogs() }}>
                <FileText size={14} /> View logs
              </button>
              <button
                className="btn-secondary"
                onClick={handleCleanupPreview}
                disabled={cleanupBusy || cleanupPreview !== null}
                title="Find and remove stale replay logs (>7 days) and old pre-import backups (>30 days)"
              >
                {cleanupBusy && !cleanupPreview ? <Loader2 size={14} className="spin" /> : <Sparkle size={14} />}
                Clean up
              </button>
              <button className="btn-secondary" onClick={handleCopyDiagnostics} disabled={diagLoading || !diagInfo}>
                <Clipboard size={14} /> {diagCopied ? 'Copied!' : 'Copy report'}
              </button>
              <button className="btn-secondary" onClick={() => downloadDiagnostics(diagInfo)} disabled={diagLoading || !diagInfo}>
                <Download size={14} /> Save as file
              </button>
              <button className="btn-primary" onClick={() => setShowDiag(false)}>Close</button>
            </div>
          </div>
        </div>
      )}

      {/* Logs viewer modal (iter 710+) */}
      {showLogs && (
        <div className="dispatch-backdrop logs-backdrop" onClick={() => setShowLogs(false)}>
          <div className="dispatch-modal logs-modal" onClick={(e) => e.stopPropagation()}>
            <div className="dispatch-modal-header">
              <FileText size={14} />
              <span>Application Logs</span>
            </div>
            <div className="dispatch-modal-body">
              <div className="logs-filter-row">
                <button className={`logs-filter ${logFilter === 'all' ? 'active' : ''}`} onClick={() => setLogFilter('all')}>
                  All ({logCounts.all})
                </button>
                <button className={`logs-filter logs-filter-info ${logFilter === 'info' ? 'active' : ''}`} onClick={() => setLogFilter('info')}>
                  Info ({logCounts.info})
                </button>
                <button className={`logs-filter logs-filter-warn ${logFilter === 'warn' ? 'active' : ''}`} onClick={() => setLogFilter('warn')}>
                  Warn ({logCounts.warn})
                </button>
                <button className={`logs-filter logs-filter-error ${logFilter === 'error' ? 'active' : ''}`} onClick={() => setLogFilter('error')}>
                  Error ({logCounts.error})
                </button>
              </div>
              {/* iter 920+: source filter — only shown when there's more
                  than one source in the log (single-source = no need to
                  filter, hide the row to reduce visual noise). */}
              {logSources.length > 1 && (
                <div className="logs-filter-row logs-filter-row-source">
                  <button
                    className={`logs-filter ${logSourceFilter === 'all' ? 'active' : ''}`}
                    onClick={() => setLogSourceFilter('all')}
                  >
                    All sources
                  </button>
                  {logSources.map((src) => (
                    <button
                      key={src}
                      className={`logs-filter ${logSourceFilter === src ? 'active' : ''}`}
                      onClick={() => setLogSourceFilter(src)}
                    >
                      {src}
                    </button>
                  ))}
                </div>
              )}
              {logsLoading && (
                <div className="diag-loading"><Loader2 size={14} className="spin" /> Loading…</div>
              )}
              {!logsLoading && filteredLogs.length === 0 && (
                <div className="logs-empty">
                  {logs.length === 0
                    ? 'No events logged yet. Backend errors, retries, and warnings will appear here.'
                    : logSourceFilter !== 'all' && logFilter !== 'all'
                      ? `No ${logFilter} events from ${logSourceFilter}. Adjust filters to see more.`
                      : logSourceFilter !== 'all'
                        ? `No events from ${logSourceFilter}. Adjust source filter to see more.`
                        : `No ${logFilter} events. Switch filter to see other levels.`}
                </div>
              )}
              {!logsLoading && filteredLogs.length > 0 && (
                <div className="logs-list">
                  {filteredLogs.slice().reverse().map((l, i) => (
                    <div key={i} className={`logs-row logs-row-${l.level}`}>
                      <div className="logs-row-meta">
                        <span className={`logs-level logs-level-${l.level}`}>{l.level}</span>
                        <span className="logs-source">{l.source}</span>
                        {l.count && l.count > 1 && (
                          <span className="logs-count" title={`Repeated ${l.count} times within ${(2)}s window`}>×{l.count}</span>
                        )}
                        <span className="logs-time">{new Date(l.timestampMs).toLocaleTimeString()}</span>
                      </div>
                      <div className="logs-message">{l.message}</div>
                    </div>
                  ))}
                </div>
              )}
            </div>
            <div className="dispatch-modal-actions">
              <button className="btn-secondary" onClick={refreshLogs} disabled={logsLoading}>
                <RotateCcw size={14} /> Refresh
              </button>
              <button
                className="btn-secondary"
                onClick={async () => {
                  try {
                    const csv = await ExportLogsCSV()
                    const blob = new Blob([csv], { type: 'text/csv' })
                    const url = URL.createObjectURL(blob)
                    const a = document.createElement('a')
                    a.href = url
                    a.download = `gokin-studio-logs-${new Date().toISOString().slice(0, 10)}.csv`
                    document.body.appendChild(a)
                    a.click()
                    document.body.removeChild(a)
                    URL.revokeObjectURL(url)
                  } catch (e: any) {
                    console.error('ExportLogsCSV failed:', e)
                  }
                }}
                disabled={logsLoading || logs.length === 0}
                title="Download all event log entries as a CSV file"
              >
                <Download size={14} /> Export CSV
              </button>
              <button className="btn-secondary" onClick={handleClearLogs} disabled={logsLoading || logs.length === 0}>
                <Trash2 size={14} /> Clear
              </button>
              <button className="btn-primary" onClick={() => setShowLogs(false)}>Close</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// formatBytes renders a byte count using KB/MB/GB. Matches the backend
// humanBytes formatter so values are consistent between UI and exported report.
function formatBytes(n: number): string {
  if (!n || n < 1024) return `${n || 0} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`
  return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GB`
}

// downloadDiagnostics builds a plain-text report client-side from the loaded
// DiagnosticsInfo. We re-render here (rather than calling DiagnosticsReport)
// because the loaded info is already cached — no extra disk walks.
function downloadDiagnostics(info: any) {
  if (!info) return
  const lines: string[] = []
  lines.push('Gokin Studio Diagnostics', '========================', '')
  lines.push(`Version: ${info.build?.version}`)
  lines.push(`Go:      ${info.build?.goVersion}`)
  lines.push(`OS:      ${info.build?.os}/${info.build?.arch} (${info.build?.numCPU} CPUs)`)
  lines.push(`Time:    ${new Date(info.generatedAtMs).toISOString()}`)
  lines.push('', 'Storage', '-------')
  lines.push(`Config dir: ${info.configDir} (writable: ${info.configDirOK}, ${formatBytes(info.configDirSize)})`)
  lines.push(`History:    ${formatBytes(info.historyBytes)}`)
  lines.push(`Projects:   ${info.totalProjects}`)
  lines.push(`Sessions:   ${info.totalSessions}`)
  lines.push('', 'Health checks', '-------------')
  for (const c of info.checks || []) {
    const marker = c.status === 'ok' ? '[OK]   ' : c.status === 'warn' ? '[WARN] ' : '[ERR]  '
    lines.push(`${marker}${c.name} — ${c.message}`)
    if (c.detail) for (const ln of String(c.detail).split('\n')) lines.push('        ' + ln)
  }
  const blob = new Blob([lines.join('\n')], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `gokin-diagnostics-${new Date().toISOString().slice(0, 10)}.txt`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
