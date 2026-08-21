import { useState, useEffect, useMemo, useRef, useCallback } from 'react'
import { useSettingsStore, Settings, SETTINGS_FIELD_KEYS, settingsEqual } from '../../stores/settingsStore'
import { useProjectStore } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { UpdateSettings, GetClipboardText, CheckProviderHealth, CheckProviderHealthWithKey, GetProviderCredentialSources, ListUserSnippets, SaveUserSnippet, DeleteUserSnippet, GetBuildInfo, GetUpdateStatus, CheckForUpdates, GetQuickEntryStatus, GetSpeechDictationStatus, RequestSpeechDictationPermissions, GetWakeStatus, GetWorkspaceIsolationStatus, ListLocalEnvironment, SaveLocalEnvironment, ListExternalBrowserPermissions, RevokeExternalBrowserPermission, GetDiagnostics, DiagnosticsReport, GetRecentLogs, ClearLogs, ExportAllDataBase64, ExportAllDataToFile, ImportAllDataBase64, SelectRestoreArchiveFile, ConfirmSelectedRestoreArchive, DiscardSelectedRestoreArchive, CleanupOldData, CleanupPreviewDefaults, ListPreImportBackups, DeletePreImportBackup, RestorePreImportBackup, ListAutoBackups, DeleteAutoBackup, RestoreAutoBackup, OpenConfigDir, OpenAutoBackupsDir, ExportLogsCSV, ListMCPServers, SaveMCPServer, RemoveMCPServer, TestMCPServer, AuthorizeMCPServer, DisconnectMCPServerOAuth, SelectMCPBundle, InstallMCPBundle, BrowseMCPBundleConfigPath, ListInstalledPlugins, SelectPluginBundle, InstallPluginBundle, SetPluginEnabled, RemovePlugin, InspectPluginMCPConnectors, ImportPluginMCPConnector, InspectPluginHooks, SetPluginHooksEnabled } from '../../../wailsjs/go/studio/Studio'
import { Eye, EyeOff, Save, Sun, Moon, Monitor, CheckCircle, AlertCircle, Keyboard, Info, Clipboard, Brain, Wifi, Loader2, DollarSign, Trash2, Plus, RotateCcw, Activity, Download, AlertTriangle, FileText, Archive, Upload, Sparkle, History, FolderOpen, LogIn, LogOut, ShieldCheck, Bell, BellOff, X, Mic, GitPullRequest } from 'lucide-react'
import { resetAllPreferences } from '../../lib/mutedProjects'
import { formatContextWindow } from '../../lib/modelCapabilities'
import { formatModelLabel, formatProviderModelLabel } from '../../lib/providerCatalog'
import { studioReasoningControlKind } from '../../lib/studioModelIds'
import { applyThemePreference } from '../../lib/theme'
import { scrollIntoViewWithMotion, scrollToWithMotion } from '../../lib/motion'
import { decodeBackupBase64Chunks, validateBackupImportFile } from '../../lib/backupImport'
import { useConfirmDialog } from '../common/AppDialog'
import { hasOpenModal } from '../../hooks/useModalFocusManagement'
import { BrowserOpenURL, ClipboardSetText } from '../../../wailsjs/runtime/runtime'
import { downloadBlob } from '../../lib/download'

const KEY_PROVIDER_MAP: Record<string, keyof Settings> = {
  glm: 'glmKey',
  kimi: 'kimiKey',
}

const PROVIDER_COLORS: Record<string, string> = {
  glm: '#4ade80',
  kimi: '#fb923c',
}

const SETTINGS_NAV_ITEMS = [
  { id: 'settings-general', label: 'General' },
  { id: 'settings-models', label: 'Models' },
  { id: 'settings-personalization', label: 'Instructions' },
  { id: 'settings-extensions', label: 'Plugins' },
  { id: 'settings-connections', label: 'Connections' },
  { id: 'settings-data', label: 'Data' },
  { id: 'settings-about', label: 'About' },
] as const

type ProviderHealthResult = {
  provider: string
  ok: boolean
  latencyMs?: number
  statusCode?: number
  error?: string
  endpoint?: string
  description?: string
  availableModels?: string[]
  recommendedModel?: string
}

type DesktopUpdateStatus = {
  currentVersion: string
  latestVersion?: string
  available: boolean
  checkedAt?: number
  publishedAt?: number
  releaseURL?: string
}

type LocalEnvironmentRow = {
  id: number
  name: string
  value: string
  existing: boolean
  keepExisting: boolean
}

type LocalEnvironmentStatus = {
  variables?: Array<{ name: string }>
  error?: string
}

type BrowserPermission = { origin: string }

type PendingRestore =
  | { source: 'native'; filename: string; size: number; token: string }
  | { source: 'bridge'; filename: string; size: number; base64: string }

type MCPServerStatus = {
  name: string
  transport: 'stdio' | 'http'
  command: string
  args?: string[]
  env?: Record<string, string>
  url?: string
  headers?: Record<string, string>
  authType?: 'headers' | 'oauth'
  oauthClientID?: string
  enabled: boolean
  toolCount?: number
  error?: string
  authorized?: boolean
  authorizationExpiresAt?: number
  authorizationError?: string
}

type MCPBundleConfigField = {
  key: string
  type: 'string' | 'number' | 'boolean' | 'directory' | 'file'
  title: string
  description?: string
  required: boolean
  multiple?: boolean
  sensitive?: boolean
  default?: any
  min?: number
  max?: number
}

type MCPBundlePreview = {
  path: string
  digest: string
  name: string
  displayName: string
  version: string
  description: string
  author: string
  serverType: string
  tools?: string[]
  configFields?: MCPBundleConfigField[]
  warnings?: string[]
  existingServer?: boolean
}

type PluginComponent = {
  name: string
  description?: string
  path: string
}

type PluginCommand = PluginComponent & {
  slashName: string
  body?: string
  plugin?: string
}

type PluginMCPSummary = {
  name: string
  transport: string
  importable: boolean
  warnings?: string[]
}

type PluginBundle = {
  path?: string
  digest: string
  name: string
  version?: string
  description?: string
  author?: string
  enabled?: boolean
  hooksEnabled?: boolean
  hooksDigest?: string
  installedAt?: number
  skills?: PluginComponent[]
  commands?: PluginCommand[]
  agents?: PluginComponent[]
  mcpServers?: PluginMCPSummary[]
  hasMCP: boolean
  hasHooks: boolean
  hasScripts: boolean
  warnings?: string[]
  existing?: boolean
}

type PluginMCPServerReview = {
  sourceName: string
  suggestedName: string
  transport: string
  command?: string
  args?: string[]
  env?: Record<string, string>
  url?: string
  headers?: Record<string, string>
  authType?: string
  oauthClientID?: string
  importable: boolean
  existingServer?: boolean
  warnings?: string[]
}

type PluginMCPReview = {
  plugin: string
  digest: string
  servers: PluginMCPServerReview[]
  warnings?: string[]
}

type PluginHookHandlerReview = {
  event: string
  matcher?: string
  type: string
  command?: string
  args?: string[]
  timeout: number
  supported: boolean
  warnings?: string[]
}

type PluginHookReview = {
  plugin: string
  digest: string
  path: string
  armed: boolean
  handlers: PluginHookHandlerReview[]
  warnings?: string[]
}

const parseCommandArgs = (input: string): string[] => {
  const out: string[] = []
  let current = ''
  let quote: '"' | "'" | null = null
  let escaped = false
  for (const ch of input) {
    if (escaped) {
      current += ch
      escaped = false
      continue
    }
    if (ch === '\\' && quote !== "'") {
      escaped = true
      continue
    }
    if ((ch === '"' || ch === "'") && !quote) {
      quote = ch
      continue
    }
    if (quote === ch) {
      quote = null
      continue
    }
    if (!quote && /\s/.test(ch)) {
      if (current) {
        out.push(current)
        current = ''
      }
      continue
    }
    current += ch
  }
  if (escaped) current += '\\'
  if (quote) throw new Error('Unclosed quote in args')
  if (current) out.push(current)
  return out
}

const formatCommandArgs = (args?: string[]) => {
  return (args || []).map((arg) => {
    if (!arg) return '""'
    if (!/[\s"'\\]/.test(arg)) return arg
    return `"${arg.replace(/(["\\])/g, '\\$1')}"`
  }).join(' ')
}

const parseEnvLines = (input: string): Record<string, string> => {
  const env: Record<string, string> = {}
  for (const rawLine of input.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#')) continue
    const eq = line.indexOf('=')
    if (eq <= 0) throw new Error(`Invalid env line: ${rawLine}`)
    const key = line.slice(0, eq).trim()
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) throw new Error(`Invalid env key: ${key}`)
    env[key] = line.slice(eq + 1)
  }
  return env
}

const formatEnvLines = (env?: Record<string, string>) => {
  return Object.entries(env || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => `${key}=${value}`)
    .join('\n')
}

const parseHeaderLines = (input: string): Record<string, string> => {
  const headers: Record<string, string> = {}
  for (const rawLine of input.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#')) continue
    const eq = line.indexOf('=')
    if (eq <= 0) throw new Error(`Invalid header line: ${rawLine}`)
    const key = line.slice(0, eq).trim()
    if (!/^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(key)) throw new Error(`Invalid header name: ${key}`)
    headers[key] = line.slice(eq + 1)
  }
  return headers
}

const formatHeaderLines = (headers?: Record<string, string>) => {
  return Object.entries(headers || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => `${key}=${value}`)
    .join('\n')
}

export function SettingsPage({ isActive = true }: { isActive?: boolean }) {
	const isMacPlatform = typeof navigator !== 'undefined' && navigator.platform.toLowerCase().includes('mac')
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const settings = useSettingsStore((s) => s.settings)
  const providers = useSettingsStore((s) => s.providers)
  const updateField = useSettingsStore((s) => s.updateField)
  const setSettingsDraft = useSettingsStore((s) => s.setSettingsDraft)
  const setProviderCapability = useSettingsStore((s) => s.setProviderCapability)
  const providerCapabilities = useSettingsStore((s) => s.providerCapabilities)
  const providerCredentialSources = useSettingsStore((s) => s.providerCredentialSources)
  const setProviderCredentialSources = useSettingsStore((s) => s.setProviderCredentialSources)
  const projects = useProjectStore((s) => s.projects)
  const activeProjectID = useProjectStore((s) => s.activeProjectId)

  const restoredDraft = useRef(useSettingsStore.getState().settingsDraft).current
  const settingsPageRef = useRef<HTMLDivElement>(null)
  const settingsNavRef = useRef<HTMLElement>(null)
  const scrollFrameRef = useRef<number | null>(null)
  const [activeSettingsSection, setActiveSettingsSection] = useState<string>('settings-general')

  const [visibleKeys, setVisibleKeys] = useState<Record<string, boolean>>({})
  const [saving, setSaving] = useState(false)
  const [feedback, setFeedback] = useState<{ type: 'success' | 'warning' | 'error'; text: string } | null>(
    restoredDraft ? { type: 'success', text: 'Unsaved settings restored from this app session.' } : null,
  )
  const [pasteErrors, setPasteErrors] = useState<Record<string, string | null>>({})
  // Per-provider health-check state — keyed by provider ID. `pending` means
  // a probe is in flight; `result` is the most recent ProviderHealthInfo.
  // Cleared when the user edits the API key so a stale "OK" doesn't lie
  // after a key change.
  const [healthChecking, setHealthChecking] = useState<Record<string, boolean>>({})
  const [healthResults, setHealthResults] = useState<Record<string, ProviderHealthResult>>({})
  const [local, setLocal] = useState<Settings>({ ...(restoredDraft || settings) })
  const localRef = useRef(local)
  const healthRequestRef = useRef<Record<string, number>>({})
  const saveFeedbackTimerRef = useRef<number | null>(null)

  useEffect(() => { localRef.current = local }, [local])
  useEffect(() => () => {
    if (saveFeedbackTimerRef.current !== null) window.clearTimeout(saveFeedbackTimerRef.current)
  }, [])

  // Settings stays mounted across workspace navigation. Never keep a secret
  // visually revealed after leaving the page or switching away from the app.
  useEffect(() => {
    if (!isActive) setVisibleKeys({})
  }, [isActive])
  useEffect(() => {
    const hideKeys = () => setVisibleKeys({})
    const onVisibility = () => { if (document.visibilityState !== 'visible') hideKeys() }
    window.addEventListener('blur', hideKeys)
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      window.removeEventListener('blur', hideKeys)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [])

  // MCP stdio + Streamable HTTP connectors. Saving one immediately invalidates
  // project clients so the next GLM/Kimi turn rediscovers tool declarations.
  const [mcpServers, setMcpServers] = useState<MCPServerStatus[]>([])
  const [mcpLoading, setMcpLoading] = useState(false)
  const [mcpBusy, setMcpBusy] = useState<string | null>(null)
  const [mcpTesting, setMcpTesting] = useState<string | null>(null)
  const [mcpAuthorizing, setMcpAuthorizing] = useState<string | null>(null)
  const [mcpError, setMcpError] = useState<string | null>(null)
  const [mcpResults, setMcpResults] = useState<Record<string, MCPServerStatus>>({})
  const [mcpForm, setMcpForm] = useState({
    name: '',
    transport: 'stdio' as 'stdio' | 'http',
    command: '',
    args: '',
    env: '',
    url: '',
    headers: '',
    authType: 'headers' as 'headers' | 'oauth',
    oauthClientID: '',
  })
  const [mcpBundle, setMcpBundle] = useState<MCPBundlePreview | null>(null)
  const [mcpBundleValues, setMcpBundleValues] = useState<Record<string, string | boolean>>({})
  const [mcpBundleEnable, setMcpBundleEnable] = useState(false)
  const [mcpBundleBusy, setMcpBundleBusy] = useState(false)

  const [plugins, setPlugins] = useState<PluginBundle[]>([])
  const [pluginsLoading, setPluginsLoading] = useState(false)
  const [pluginBusy, setPluginBusy] = useState<string | null>(null)
  const [pluginError, setPluginError] = useState<string | null>(null)
  const [pluginPreview, setPluginPreview] = useState<PluginBundle | null>(null)
  const [pluginMCPReview, setPluginMCPReview] = useState<PluginMCPReview | null>(null)
  const [pluginHookReview, setPluginHookReview] = useState<PluginHookReview | null>(null)

  const refreshMCPServers = () => {
    setMcpLoading(true)
    setMcpError(null)
    ListMCPServers().then((list: any) => {
      setMcpServers((list || []).map((s: any) => ({
        name: s.name,
        transport: s.transport === 'http' ? 'http' : 'stdio',
        command: s.command || '',
        args: s.args || [],
        env: s.env || {},
        url: s.url || '',
        headers: s.headers || {},
        authType: s.authType === 'oauth' ? 'oauth' : 'headers',
        oauthClientID: s.oauthClientID || '',
        enabled: !!s.enabled,
        toolCount: s.toolCount || 0,
        error: s.error || '',
        authorized: !!s.authorized,
        authorizationExpiresAt: s.authorizationExpiresAt || 0,
        authorizationError: s.authorizationError || '',
      })))
      setMcpLoading(false)
    }).catch((e: any) => {
      setMcpError(String(e?.message || e || 'failed to load MCP servers'))
      setMcpLoading(false)
    })
  }
  useEffect(() => { refreshMCPServers() }, [])

  const refreshPlugins = () => {
    setPluginsLoading(true)
    setPluginError(null)
    ListInstalledPlugins().then((list: any) => {
      setPlugins((list || []) as PluginBundle[])
      setPluginsLoading(false)
    }).catch((e: any) => {
      setPluginError(String(e?.message || e || 'failed to load plugins'))
      setPluginsLoading(false)
    })
  }
  useEffect(() => { refreshPlugins() }, [])

  // User snippets (iter 490+). Loaded on mount; refreshed after save/delete.
  const [snippets, setSnippets] = useState<{ id: string; name: string; body: string; updatedAt: number }[]>([])
  const [snippetName, setSnippetName] = useState('')
  const [snippetBody, setSnippetBody] = useState('')
  const [snippetSaving, setSnippetSaving] = useState(false)
  const [snippetError, setSnippetError] = useState<string | null>(null)
  const [snippetNotice, setSnippetNotice] = useState<string | null>(null)
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

  // OS notification permission must be requested from an explicit user
  // gesture. Asking at startup (or after a background completion) produces a
  // contextless system prompt and is rejected by several desktop WebViews.
  const readNotificationPermission = (): NotificationPermission | 'unsupported' => (
    typeof window !== 'undefined' && 'Notification' in window
      ? Notification.permission
      : 'unsupported'
  )
  const [notificationPermission, setNotificationPermission] = useState<NotificationPermission | 'unsupported'>(readNotificationPermission)
  useEffect(() => {
    const refresh = () => setNotificationPermission(readNotificationPermission())
    window.addEventListener('focus', refresh)
    return () => window.removeEventListener('focus', refresh)
  }, [])
  const enableOSNotifications = async () => {
    if (!('Notification' in window)) return
    try {
      const permission = await Notification.requestPermission()
      setNotificationPermission(permission)
      if (permission === 'denied') {
        setFeedback({ type: 'error', text: 'Notifications were blocked. Enable them in the operating-system notification settings.' })
      }
    } catch (e: any) {
      setFeedback({ type: 'error', text: `Could not request notification access: ${String(e?.message || e)}` })
    }
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
  const [updateStatus, setUpdateStatus] = useState<DesktopUpdateStatus | null>(null)
  const [updateChecking, setUpdateChecking] = useState(false)
  const [updateError, setUpdateError] = useState<string | null>(null)
  useEffect(() => {
    GetUpdateStatus()
      .then((status) => setUpdateStatus(status as DesktopUpdateStatus))
      .catch(() => { /* a missing disposable cache is equivalent to not checked */ })
  }, [])
  const checkForDesktopUpdate = useCallback(async () => {
    setUpdateChecking(true)
    setUpdateError(null)
    try {
      const status = await CheckForUpdates() as DesktopUpdateStatus
      setUpdateStatus(status)
    } catch (error: any) {
      setUpdateError(String(error?.message || error || 'Could not reach the release service.'))
    } finally {
      setUpdateChecking(false)
    }
  }, [])
  useEffect(() => {
    const check = () => { void checkForDesktopUpdate() }
    window.addEventListener('gokin:check-updates', check)
    return () => window.removeEventListener('gokin:check-updates', check)
  }, [checkForDesktopUpdate])
  const [quickEntryStatus, setQuickEntryStatus] = useState<{
    supported: boolean
    enabled: boolean
    active: boolean
    shortcut: string
    error?: string
    voiceEnabled: boolean
    voiceActive: boolean
    voiceShortcut: string
    voiceError?: string
  } | null>(null)
  const refreshQuickEntryStatus = async () => {
    try {
      const status: any = await GetQuickEntryStatus()
      setQuickEntryStatus(status || null)
    } catch {
      setQuickEntryStatus(null)
    }
  }
  useEffect(() => { void refreshQuickEntryStatus() }, [])

  const [speechStatus, setSpeechStatus] = useState<{
    supported: boolean
    native: boolean
    available: boolean
    listening: boolean
    speechAuthorization: string
    microphoneAuthorization: string
    error?: string
  } | null>(null)
  const [requestingSpeechPermissions, setRequestingSpeechPermissions] = useState(false)
  const refreshSpeechStatus = async () => {
    try {
      const status: any = await GetSpeechDictationStatus()
      setSpeechStatus(status || null)
    } catch {
      setSpeechStatus(null)
    }
  }
  useEffect(() => {
    void refreshSpeechStatus()
    const refresh = () => { void refreshSpeechStatus() }
    window.addEventListener('focus', refresh)
    return () => window.removeEventListener('focus', refresh)
  }, [])
  const requestSpeechPermissions = async () => {
    setRequestingSpeechPermissions(true)
    setFeedback(null)
    try {
      const status: any = await RequestSpeechDictationPermissions()
      setSpeechStatus(status || null)
      if (status?.speechAuthorization === 'authorized' && status?.microphoneAuthorization === 'authorized') {
        setFeedback({ type: 'success', text: 'Native dictation is ready. Audio stays inside Apple Speech Recognition; only transcript text enters your draft.' })
      } else {
        setFeedback({ type: 'warning', text: status?.error || 'Both Speech Recognition and Microphone access are required. Review them in macOS Privacy & Security.' })
      }
    } catch (reason: any) {
      setFeedback({ type: 'error', text: `Could not request dictation access: ${String(reason?.message || reason)}` })
      await refreshSpeechStatus()
    } finally {
      setRequestingSpeechPermissions(false)
    }
  }

  const [wakeStatus, setWakeStatus] = useState<{
    supported: boolean
    enabled: boolean
    active: boolean
    activeRuns: number
    scheduledTasks: boolean
    error?: string
  } | null>(null)
  const refreshWakeStatus = async () => {
    try {
      const status: any = await GetWakeStatus()
      setWakeStatus(status || null)
    } catch {
      setWakeStatus(null)
    }
  }
  useEffect(() => { void refreshWakeStatus() }, [])

  const [workspaceIsolation, setWorkspaceIsolation] = useState<{
    available: boolean
    enforced: boolean
    mode: string
    detail: string
  } | null>(null)
  useEffect(() => {
    GetWorkspaceIsolationStatus()
      .then((status: any) => setWorkspaceIsolation(status || null))
      .catch(() => setWorkspaceIsolation(null))
  }, [])

  const localEnvironmentRowID = useRef(0)
  const [localEnvironmentRows, setLocalEnvironmentRows] = useState<LocalEnvironmentRow[]>([])
  const [localEnvironmentSavedNames, setLocalEnvironmentSavedNames] = useState<string[]>([])
  const [localEnvironmentLoading, setLocalEnvironmentLoading] = useState(true)
  const [localEnvironmentSaving, setLocalEnvironmentSaving] = useState(false)
  const [localEnvironmentError, setLocalEnvironmentError] = useState<string | null>(null)
  const applyLocalEnvironmentStatus = useCallback((status: LocalEnvironmentStatus | null) => {
    const names = (status?.variables || []).map((variable) => variable.name).sort((a, b) => a.localeCompare(b))
    setLocalEnvironmentSavedNames(names)
    setLocalEnvironmentRows(names.map((name) => ({
      id: ++localEnvironmentRowID.current,
      name,
      value: '',
      existing: true,
      keepExisting: true,
    })))
    setLocalEnvironmentError(status?.error || null)
  }, [])
  const refreshLocalEnvironment = useCallback(async () => {
    setLocalEnvironmentLoading(true)
    try {
      applyLocalEnvironmentStatus(await ListLocalEnvironment() as LocalEnvironmentStatus)
    } catch (error: any) {
      setLocalEnvironmentError(String(error?.message || error || 'Could not load the local environment.'))
    } finally {
      setLocalEnvironmentLoading(false)
    }
  }, [applyLocalEnvironmentStatus])
  useEffect(() => {
    if (isActive) void refreshLocalEnvironment()
  }, [isActive, refreshLocalEnvironment])

  const saveLocalEnvironment = async () => {
    const names = localEnvironmentRows.map((row) => row.name)
    const invalid = names.find((name) => !/^[A-Za-z_][A-Za-z0-9_]*$/.test(name))
    if (invalid !== undefined) {
      setLocalEnvironmentError(invalid ? `Invalid variable name: ${invalid}` : 'Variable names cannot be empty.')
      return
    }
    const normalized = new Set<string>()
    for (const name of names) {
      const key = name.toUpperCase()
      if (normalized.has(key)) {
        setLocalEnvironmentError(`Duplicate variable name: ${name}`)
        return
      }
      normalized.add(key)
    }
    const removed = localEnvironmentSavedNames.filter((name) => !names.includes(name))
    if (removed.length > 0 && !await requestConfirmation({
      title: `Remove ${removed.length} environment variable${removed.length === 1 ? '' : 's'}?`,
      message: `${removed.join(', ')} will be deleted from secure storage. Existing terminals and running tasks keep their current environment until restarted.`,
      confirmLabel: 'Remove and save',
      danger: true,
    })) return
    setLocalEnvironmentSaving(true)
    setLocalEnvironmentError(null)
    try {
      const status = await SaveLocalEnvironment(localEnvironmentRows.map((row) => ({
        name: row.name,
        value: row.value,
        keepExisting: row.existing && row.keepExisting,
      }))) as LocalEnvironmentStatus
      applyLocalEnvironmentStatus(status)
      setFeedback({ type: 'success', text: 'Local environment saved. New sessions, terminals, tasks, and previews will use it.' })
    } catch (error: any) {
      setLocalEnvironmentError(String(error?.message || error || 'Could not save the local environment.'))
    } finally {
      setLocalEnvironmentSaving(false)
    }
  }

  const [browserPermissions, setBrowserPermissions] = useState<BrowserPermission[]>([])
  const [browserPermissionsLoading, setBrowserPermissionsLoading] = useState(true)
  const [browserPermissionsError, setBrowserPermissionsError] = useState<string | null>(null)
  const refreshBrowserPermissions = useCallback(async () => {
    setBrowserPermissionsLoading(true)
    try {
      setBrowserPermissions((await ListExternalBrowserPermissions() || []) as BrowserPermission[])
      setBrowserPermissionsError(null)
    } catch (error: any) {
      setBrowserPermissionsError(String(error?.message || error || 'Could not load browser permissions.'))
    } finally {
      setBrowserPermissionsLoading(false)
    }
  }, [])
  useEffect(() => {
    if (isActive) void refreshBrowserPermissions()
  }, [isActive, refreshBrowserPermissions])
  const revokeBrowserPermission = async (origin: string) => {
    if (!await requestConfirmation({
      title: 'Revoke browser domain permission?',
      message: `${origin} will require explicit approval the next time a Browser tab opens it. Existing open tabs are not closed.`,
      confirmLabel: 'Revoke permission',
      danger: true,
    })) return
    try {
      await RevokeExternalBrowserPermission(origin)
      setBrowserPermissions((current) => current.filter((permission) => permission.origin !== origin))
      setFeedback({ type: 'success', text: `Browser permission revoked for ${origin}.` })
    } catch (error: any) {
      setBrowserPermissionsError(String(error?.message || error || 'Could not revoke browser permission.'))
    }
  }

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
  const [autoDeleteBusy, setAutoDeleteBusy] = useState(false)
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
    setAutoDeleteBusy(true)
    setAutoError(null)
    setAutoRestoreSuccess(null)
    try {
      await DeleteAutoBackup(name)
      setConfirmDeleteAuto(null)
      refreshAutoBackupsList()
    } catch (e: any) {
      setAutoError(String(e?.message || e || 'delete failed'))
    } finally {
      setAutoDeleteBusy(false)
    }
  }
  const handleAutoRestore = async (name: string) => {
    setAutoRestoreBusy(true)
    setAutoError(null)
    setAutoRestoreSuccess(null)
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
  const [backupDeleteBusy, setBackupDeleteBusy] = useState(false)
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
    setBackupDeleteBusy(true)
    setBackupsError(null)
    setRestoreBackupSuccess(null)
    try {
      await DeletePreImportBackup(name)
      setConfirmDeleteBackup(null)
      refreshBackupsList()
    } catch (e: any) {
      setBackupsError(String(e?.message || e || 'delete failed'))
    } finally {
      setBackupDeleteBusy(false)
    }
  }
  const handleBackupRestore = async (name: string) => {
    setRestoreBackupBusy(true)
    setBackupsError(null)
    setRestoreBackupSuccess(null)
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
  const cleanupRemovedCount = (value: any) =>
    (value?.staleReplaysRemoved || 0) +
    (value?.preImportDirsRemoved || 0) +
    (value?.stagingDirsRemoved || 0) +
    (value?.autoBackupsRemoved || 0) +
    (value?.delegationRunsRemoved || 0)

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
      const result: any = await CleanupOldData({ replayAgeDays: 7, preImportDays: 30, delegationAgeDays: 30, dryRun: false })
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
    setDiagError(null)
    try {
      const text = await DiagnosticsReport()
      try {
        await navigator.clipboard.writeText(text)
      } catch {
        await ClipboardSetText(text)
      }
      setDiagCopied(true)
      setTimeout(() => setDiagCopied(false), 1800)
    } catch (e: any) {
      setDiagCopied(false)
      setDiagError(`Could not copy diagnostics: ${String(e?.message || e || 'clipboard unavailable')}`)
    }
  }
  // Close-on-Esc + skip while typing (IME safety inherited from Escape).
  useEffect(() => {
    if (!showDiag) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if ((e as any).isComposing || e.keyCode === 229) return
        if (cleanupBusy) return
        setShowDiag(false)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [showDiag, cleanupBusy])

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
  const [logsActionBusy, setLogsActionBusy] = useState<'export' | 'clear' | null>(null)
  const [logsActionFeedback, setLogsActionFeedback] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const refreshLogs = () => {
    setLogsLoading(true)
    setLogsActionFeedback(null)
    GetRecentLogs().then((entries: any) => {
      setLogs((entries || []).map((e: any) => ({
        timestampMs: e.timestampMs, level: e.level, source: e.source, message: e.message, count: e.count,
      })))
      setLogsLoading(false)
    }).catch((e: any) => {
      setLogsLoading(false)
      setLogsActionFeedback({ type: 'error', text: `Could not refresh logs: ${String(e?.message || e || 'unknown error')}` })
    })
  }
  const openLogs = (preset?: { level?: 'all' | 'info' | 'warn' | 'error' }) => {
    setShowLogs(true)
    setLogFilter(preset?.level || 'all')
    setLogSourceFilter('all')
    setLogsActionFeedback(null)
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
    if (!(await requestConfirmation({
      title: 'Clear application logs?',
      message: `${logs.length} local log entr${logs.length === 1 ? 'y' : 'ies'} will be permanently removed. This does not delete chats or project files.`,
      confirmLabel: 'Clear logs',
      cancelLabel: 'Keep logs',
      danger: true,
    }))) return
    setLogsActionBusy('clear')
    setLogsActionFeedback(null)
    try {
      await ClearLogs()
      setLogs([])
      setLogsActionFeedback({ type: 'success', text: 'Application logs cleared.' })
    } catch (e: any) {
      console.error('ClearLogs failed:', e)
      setLogsActionFeedback({ type: 'error', text: `Could not clear logs: ${String(e?.message || e || 'unknown error')}` })
    } finally {
      setLogsActionBusy(null)
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
    setResetFeedback(`Cleared ${removed} preference${removed === 1 ? '' : 's'}. Notifications and panel layout reset now; quiet mode + budget alerts will re-init on next chat panel mount.`)
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
  const [restoreSelectionBusy, setRestoreSelectionBusy] = useState(false)
  const [restoreError, setRestoreError] = useState<string | null>(null)
  const [restoreSuccess, setRestoreSuccess] = useState<string | null>(null)
  const [restoreFileReading, setRestoreFileReading] = useState<string | null>(null)
  const [confirmRestore, setConfirmRestore] = useState<PendingRestore | null>(null)
  const restoreFileInputRef = useRef<HTMLInputElement | null>(null)
  const restoreFileRequestRef = useRef(0)
  const restoreNativeRequestRef = useRef(0)
  const restoreNativeTokenRef = useRef<string | null>(null)
  const restoreFileReaderRef = useRef<FileReader | null>(null)
  // These operations all read or replace the same config tree. The backend
  // serializes them for correctness; mirror that ownership in the UI so a
  // second request cannot sit behind a destructive restore with stale intent.
  const dataOperationBusy = backupBusy || restoreSelectionBusy || restoreBusy || backupDeleteBusy || restoreBackupBusy || autoDeleteBusy || autoRestoreBusy || cleanupBusy
  const dataOperationLabel = (() => {
    if (backupBusy) return 'Creating and saving a backup…'
    if (restoreSelectionBusy) return 'Staging the selected backup for review…'
    if (restoreBusy) return 'Restoring the selected backup…'
    if (restoreBackupBusy) return 'Restoring the selected rollback snapshot…'
    if (backupDeleteBusy) return 'Deleting the selected rollback snapshot…'
    if (autoRestoreBusy) return 'Restoring the selected automatic backup…'
    if (autoDeleteBusy) return 'Deleting the selected automatic backup…'
    if (cleanupBusy) return 'Checking and cleaning old data…'
    return ''
  })()

  useEffect(() => () => {
    restoreFileRequestRef.current++
    restoreNativeRequestRef.current++
    const activeReader = restoreFileReaderRef.current
    restoreFileReaderRef.current = null
    if (activeReader?.readyState === FileReader.LOADING) activeReader.abort()
    const nativeToken = restoreNativeTokenRef.current
    restoreNativeTokenRef.current = null
    const nativeBridge = (window as any)?.go?.studio?.Studio
    if (nativeToken && typeof nativeBridge?.DiscardSelectedRestoreArchive === 'function') {
      void DiscardSelectedRestoreArchive(nativeToken).catch(() => {})
    }
  }, [])

  const handleBackup = async () => {
    setBackupBusy(true)
    setBackupError(null)
    setBackupSuccess(null)
    try {
      const nativeBridge = (window as any)?.go?.studio?.Studio
      if (typeof nativeBridge?.ExportAllDataToFile === 'function') {
        const result: any = await ExportAllDataToFile()
        if (result.canceled) return
        const kb = (result.size / 1024).toFixed(1)
        setBackupSuccess(
          `Saved ${result.filesCount} file${result.filesCount === 1 ? '' : 's'} (${kb} KB) to:\n${result.path}`,
        )
        setTimeout(() => setBackupSuccess(null), 8000)
        return
      }

      // Compatibility path for a frontend temporarily paired with an older
      // backend binding (for example during browser-only UI development).
      const result: any = await ExportAllDataBase64()
      // Decode in bounded chunks: a near-limit archive must not allocate an
      // additional archive-sized binary string in the WebView.
      const decoded = decodeBackupBase64Chunks(result.base64)
      if (!Number.isSafeInteger(result.size) || result.size !== decoded.byteLength) {
        throw new Error('Backup export size did not match the downloaded archive.')
      }
      const blob = new Blob(decoded.chunks, { type: 'application/gzip' })
      downloadBlob(blob, result.filename)
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
    const requestID = ++restoreFileRequestRef.current
    const previousReader = restoreFileReaderRef.current
    restoreFileReaderRef.current = null
    if (previousReader?.readyState === FileReader.LOADING) previousReader.abort()
    setRestoreFileReading(null)
    setRestoreError(null)
    setRestoreSuccess(null)
    setConfirmRestore(null)
    const validationError = validateBackupImportFile(file)
    if (validationError) {
      setRestoreError(validationError)
      return
    }
    const reader = new FileReader()
    restoreFileReaderRef.current = reader
    setRestoreFileReading(file.name)
    reader.onload = () => {
      if (restoreFileRequestRef.current !== requestID || restoreFileReaderRef.current !== reader) return
      restoreFileReaderRef.current = null
      setRestoreFileReading(null)
      const result = reader.result
      if (typeof result !== 'string') {
        setRestoreError('Could not read file as data URL')
        return
      }
      // data URL prefix: "data:application/gzip;base64,XXXX" — strip up to comma.
      const commaIdx = result.indexOf(',')
      const base64 = commaIdx >= 0 ? result.slice(commaIdx + 1) : result
      setConfirmRestore({ source: 'bridge', filename: file.name, base64, size: file.size })
      setRestoreSuccess(null)
      setRestoreError(null)
    }
    reader.onerror = () => {
      if (restoreFileRequestRef.current !== requestID || restoreFileReaderRef.current !== reader) return
      restoreFileReaderRef.current = null
      setRestoreFileReading(null)
      setRestoreError('Failed to read file')
    }
    reader.readAsDataURL(file)
  }

  const handleChooseRestore = async () => {
    const nativeBridge = (window as any)?.go?.studio?.Studio
    if (typeof nativeBridge?.SelectRestoreArchiveFile !== 'function') {
      restoreFileInputRef.current?.click()
      return
    }
    const requestID = ++restoreNativeRequestRef.current
    const previousToken = restoreNativeTokenRef.current
    restoreNativeTokenRef.current = null
    setConfirmRestore(null)
    setRestoreSelectionBusy(true)
    setRestoreError(null)
    setRestoreSuccess(null)
    try {
      if (previousToken) await DiscardSelectedRestoreArchive(previousToken).catch(() => {})
      const review: any = await SelectRestoreArchiveFile()
      if (review.canceled) return
      if (restoreNativeRequestRef.current !== requestID) {
        await DiscardSelectedRestoreArchive(review.token).catch(() => {})
        return
      }
      restoreNativeTokenRef.current = review.token
      setConfirmRestore({ source: 'native', filename: review.filename, size: review.size, token: review.token })
    } catch (e: any) {
      if (restoreNativeRequestRef.current === requestID) {
        setRestoreError(String(e?.message || e || 'could not select restore archive'))
      }
    } finally {
      if (restoreNativeRequestRef.current === requestID) setRestoreSelectionBusy(false)
    }
  }

  const handleCancelRestore = () => {
    const pending = confirmRestore
    setConfirmRestore(null)
    if (pending?.source !== 'native') return
    restoreNativeTokenRef.current = null
    // A concurrent new selection may already have invalidated this token;
    // either outcome means there is no longer anything for this UI to retain.
    void DiscardSelectedRestoreArchive(pending.token).catch(() => {})
  }

  const handleConfirmRestore = async () => {
    if (!confirmRestore) return
    const pending = confirmRestore
    setRestoreBusy(true)
    setRestoreError(null)
    try {
      if (pending.source === 'native') {
        // Native review tokens are single-use, including failed validation.
        restoreNativeTokenRef.current = null
        setConfirmRestore(null)
      }
      const result: any = pending.source === 'native'
        ? await ConfirmSelectedRestoreArchive(pending.token)
        : await ImportAllDataBase64(pending.base64)
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

  // Keep a process-local draft while fields differ from the persisted store.
  // Do not mirror this to web storage: it can contain GLM/Kimi API keys.
  // In particular, never replace the whole form when `settings` changes — the
  // theme toggle intentionally updates immediately and used to erase every
  // other unsaved field through that old synchronization effect.
  useEffect(() => {
    setSettingsDraft(settingsEqual(local, settings) ? null : { ...local })
  }, [local, settings, setSettingsDraft])

  // Appearance is previewed immediately, but the persisted settings store
  // remains the save/discard baseline. Including settings.theme ensures a
  // newer unsaved preview wins again if an older in-flight save settles.
  useEffect(() => {
    applyThemePreference(local.theme)
  }, [local.theme, settings.theme])

  const allMessages = useChatStore((s) => s.messages)
  const totalMessages = useMemo(() => Object.values(allMessages).reduce((sum, msgs) => sum + msgs.length, 0), [allMessages])

  const handleChange = (key: keyof Settings, value: string) => {
    setLocal((prev) => ({ ...prev, [key]: value }))
    if (key === 'theme') {
      applyThemePreference(value)
    }
    // Clear stale health-check result if the user edits an API key — a
    // green "Connected" badge would otherwise lie about the new (untested)
    // key. The provider ID is derivable from the key's name.
    const keyToProvider: Record<string, string> = {
      glmKey: 'glm', kimiKey: 'kimi',
    }
    const provider = keyToProvider[key as string]
    if (provider) {
      healthRequestRef.current[provider] = (healthRequestRef.current[provider] || 0) + 1
      setProviderCapability(provider, null)
      setHealthChecking((state) => ({ ...state, [provider]: false }))
      setHealthResults((results) => {
        if (!(provider in results)) return results
        const next = { ...results }
        delete next[provider]
        return next
      })
    }
  }

  // Probe the provider's API to verify the key + connectivity. Side-effects:
  // sets `healthChecking[provider]=true` while in flight, then stores the
  // ProviderHealthInfo in `healthResults[provider]` (cleared on key edit).
  const runHealthCheck = async (provider: string, apiKey: string, useResolvedCredential = false) => {
    const request = (healthRequestRef.current[provider] || 0) + 1
    healthRequestRef.current[provider] = request
    setHealthChecking((s) => ({ ...s, [provider]: true }))
    setHealthResults((results) => {
      if (!(provider in results)) return results
      const next = { ...results }
      delete next[provider]
      return next
    })
    try {
      const info = (useResolvedCredential
        ? await CheckProviderHealth(provider)
        : await CheckProviderHealthWithKey(provider, apiKey.trim())) as ProviderHealthResult
      if (healthRequestRef.current[provider] !== request) return
      setHealthResults((r) => ({ ...r, [provider]: info }))
      if (info.ok && info.availableModels && info.availableModels.length > 0) {
        setProviderCapability(provider, {
          availableModels: [...info.availableModels],
          recommendedModel: info.recommendedModel,
          checkedAt: Date.now(),
        })
      } else {
        setProviderCapability(provider, null)
      }
    } catch (e: any) {
      if (healthRequestRef.current[provider] !== request) return
      setProviderCapability(provider, null)
      setHealthResults((r) => ({
        ...r,
        [provider]: { provider, ok: false, error: String(e?.message || e || 'check failed') },
      }))
    } finally {
      if (healthRequestRef.current[provider] !== request) return
      setHealthChecking((s) => ({ ...s, [provider]: false }))
    }
  }

  const mcpConfigFromForm = () => ({
    name: mcpForm.name.trim(),
    transport: mcpForm.transport,
    command: mcpForm.transport === 'stdio' ? mcpForm.command.trim() : '',
    args: mcpForm.transport === 'stdio' ? parseCommandArgs(mcpForm.args) : [],
    env: mcpForm.transport === 'stdio' ? parseEnvLines(mcpForm.env) : {},
    url: mcpForm.transport === 'http' ? mcpForm.url.trim() : '',
    headers: mcpForm.transport === 'http' ? parseHeaderLines(mcpForm.headers) : {},
    authType: mcpForm.transport === 'http' ? mcpForm.authType : '',
    oauthClientID: mcpForm.transport === 'http' && mcpForm.authType === 'oauth' ? mcpForm.oauthClientID.trim() : '',
    enabled: true,
  })

  const saveMCPFromForm = async () => {
    setMcpBusy('form')
    setMcpError(null)
    try {
      await SaveMCPServer(mcpConfigFromForm())
      setMcpForm({ name: '', transport: 'stdio', command: '', args: '', env: '', url: '', headers: '', authType: 'headers', oauthClientID: '' })
      refreshMCPServers()
    } catch (e: any) {
      setMcpError(String(e?.message || e || 'save failed'))
    } finally {
      setMcpBusy(null)
    }
  }

  const testMCPConfig = async (cfg: MCPServerStatus | ReturnType<typeof mcpConfigFromForm>, key: string) => {
    setMcpTesting(key)
    setMcpError(null)
    try {
      const result: any = await TestMCPServer({
        name: cfg.name,
        transport: cfg.transport,
        command: cfg.command,
        args: cfg.args || [],
        env: cfg.env || {},
        url: cfg.url || '',
        headers: cfg.headers || {},
        authType: cfg.authType || 'headers',
        oauthClientID: cfg.oauthClientID || '',
        enabled: cfg.enabled,
      })
      setMcpResults((r) => ({ ...r, [key]: result }))
    } catch (e: any) {
      setMcpResults((r) => ({
        ...r,
        [key]: { ...(cfg as MCPServerStatus), toolCount: 0, error: String(e?.message || e || 'test failed') },
      }))
    } finally {
      setMcpTesting(null)
    }
  }

  const testMCPFromForm = async () => {
    setMcpError(null)
    try {
      await testMCPConfig(mcpConfigFromForm(), 'form')
    } catch (e: any) {
      setMcpError(String(e?.message || e || 'test failed'))
    }
  }

  const toggleMCPServer = async (server: MCPServerStatus) => {
    setMcpBusy(server.name)
    setMcpError(null)
    try {
      await SaveMCPServer({ ...server, enabled: !server.enabled })
      refreshMCPServers()
    } catch (e: any) {
      setMcpError(String(e?.message || e || 'update failed'))
    } finally {
      setMcpBusy(null)
    }
  }

  const deleteMCPServer = async (name: string) => {
    setMcpBusy(name)
    setMcpError(null)
    try {
      await RemoveMCPServer(name)
      setMcpResults((r) => {
        const next = { ...r }; delete next[name]; return next
      })
      refreshMCPServers()
    } catch (e: any) {
      setMcpError(String(e?.message || e || 'remove failed'))
    } finally {
      setMcpBusy(null)
    }
  }

  const authorizeMCPServer = async (name: string) => {
    setMcpAuthorizing(name)
    setMcpError(null)
    try {
      await AuthorizeMCPServer(name)
      refreshMCPServers()
    } catch (e: any) {
      setMcpError(String(e?.message || e || 'OAuth authorization failed'))
    } finally {
      setMcpAuthorizing(null)
    }
  }

  const disconnectMCPServerOAuth = async (name: string) => {
    if (!await requestConfirmation({
      title: `Disconnect ${name}?`,
      message: 'The protected OAuth token will be removed. You can connect the account again later.',
      confirmLabel: 'Disconnect',
      danger: true,
    })) return
    setMcpAuthorizing(name)
    setMcpError(null)
    try {
      await DisconnectMCPServerOAuth(name)
      refreshMCPServers()
    } catch (e: any) {
      setMcpError(String(e?.message || e || 'OAuth disconnect failed'))
    } finally {
      setMcpAuthorizing(null)
    }
  }

  const selectMCPBundle = async () => {
    setMcpBundleBusy(true)
    setMcpError(null)
    try {
      const preview: any = await SelectMCPBundle()
      if (!preview) return
      const typed = preview as MCPBundlePreview
      const values: Record<string, string | boolean> = {}
      for (const field of typed.configFields || []) {
        if (field.multiple) {
          values[field.key] = Array.isArray(field.default)
            ? field.default.map(String).join('\n')
            : (field.default == null ? '' : String(field.default))
        } else if (field.type === 'boolean') {
          values[field.key] = Boolean(field.default)
        } else {
          values[field.key] = field.default == null ? '' : String(field.default)
        }
      }
      setMcpBundle(typed)
      setMcpBundleValues(values)
      setMcpBundleEnable(false)
    } catch (e: any) {
      setMcpError(String(e?.message || e || 'failed to inspect MCP Bundle'))
    } finally {
      setMcpBundleBusy(false)
    }
  }

  const browseMCPBundleField = async (field: MCPBundleConfigField) => {
    setMcpError(null)
    try {
      const selected = await BrowseMCPBundleConfigPath(field.type)
      if (!selected) return
      setMcpBundleValues((values) => {
        if (!field.multiple) return { ...values, [field.key]: selected }
        const current = String(values[field.key] || '').trim()
        const paths = current ? current.split(/\r?\n/).map((v) => v.trim()).filter(Boolean) : []
        if (!paths.includes(selected)) paths.push(selected)
        return { ...values, [field.key]: paths.join('\n') }
      })
    } catch (e: any) {
      setMcpError(String(e?.message || e || 'path picker failed'))
    }
  }

  const installSelectedMCPBundle = async () => {
    if (!mcpBundle) return
    setMcpBundleBusy(true)
    setMcpError(null)
    try {
      const values: Record<string, any> = {}
      for (const field of mcpBundle.configFields || []) {
        const raw = mcpBundleValues[field.key]
        if (field.multiple) {
          const list = String(raw || '').split(/\r?\n/).map((value) => value.trim()).filter(Boolean)
          if (list.length > 0 || field.required) values[field.key] = list
        } else if (field.type === 'number') {
          if (String(raw ?? '').trim() !== '') values[field.key] = Number(raw)
        } else if (field.type === 'boolean') {
          values[field.key] = Boolean(raw)
        } else if (String(raw ?? '') !== '' || field.required) {
          values[field.key] = String(raw ?? '')
        }
      }
      await InstallMCPBundle(mcpBundle.path, mcpBundle.digest, values, mcpBundleEnable)
      setMcpBundle(null)
      setMcpBundleValues({})
      setMcpBundleEnable(false)
      refreshMCPServers()
    } catch (e: any) {
      setMcpError(String(e?.message || e || 'MCP Bundle installation failed'))
    } finally {
      setMcpBundleBusy(false)
    }
  }

  const notifyPluginsChanged = () => {
    window.dispatchEvent(new CustomEvent('gokin:plugins-changed'))
  }

  const selectPluginBundle = async () => {
    setPluginBusy('select')
    setPluginError(null)
    try {
      const preview: any = await SelectPluginBundle()
      if (preview) setPluginPreview(preview as PluginBundle)
    } catch (e: any) {
      setPluginError(String(e?.message || e || 'failed to inspect plugin ZIP'))
    } finally {
      setPluginBusy(null)
    }
  }

  const installSelectedPlugin = async () => {
    if (!pluginPreview?.path) return
    setPluginBusy('install')
    setPluginError(null)
    try {
      await InstallPluginBundle(pluginPreview.path, pluginPreview.digest)
      setPluginPreview(null)
      setPluginHookReview(null)
      refreshPlugins()
      notifyPluginsChanged()
    } catch (e: any) {
      setPluginError(String(e?.message || e || 'plugin installation failed'))
    } finally {
      setPluginBusy(null)
    }
  }

  const togglePlugin = async (plugin: PluginBundle) => {
    setPluginBusy(plugin.name)
    setPluginError(null)
    try {
      await SetPluginEnabled(plugin.name, !plugin.enabled)
      if (pluginHookReview?.plugin === plugin.name) setPluginHookReview(null)
      refreshPlugins()
      notifyPluginsChanged()
    } catch (e: any) {
      setPluginError(String(e?.message || e || 'plugin update failed'))
    } finally {
      setPluginBusy(null)
    }
  }

  const removeInstalledPlugin = async (plugin: PluginBundle) => {
    if (!await requestConfirmation({
      title: `Remove ${plugin.name}?`,
      message: 'The plugin and its locally installed files will be removed.',
      confirmLabel: 'Remove plugin',
      danger: true,
    })) return
    setPluginBusy(plugin.name)
    setPluginError(null)
    try {
      await RemovePlugin(plugin.name)
      if (pluginMCPReview?.plugin === plugin.name) setPluginMCPReview(null)
      if (pluginHookReview?.plugin === plugin.name) setPluginHookReview(null)
      refreshPlugins()
      notifyPluginsChanged()
    } catch (e: any) {
      setPluginError(String(e?.message || e || 'plugin removal failed'))
    } finally {
      setPluginBusy(null)
    }
  }

  const inspectPluginMCP = async (plugin: PluginBundle) => {
    const busyKey = `mcp-review:${plugin.name}`
    setPluginBusy(busyKey)
    setPluginError(null)
    try {
      const review: any = await InspectPluginMCPConnectors(plugin.name)
      setPluginMCPReview(review as PluginMCPReview)
    } catch (e: any) {
      setPluginError(String(e?.message || e || 'failed to inspect plugin connectors'))
    } finally {
      setPluginBusy(null)
    }
  }

  const inspectPluginHooks = async (plugin: PluginBundle) => {
    const busyKey = `hook-review:${plugin.name}`
    setPluginBusy(busyKey)
    setPluginError(null)
    try {
      const review: any = await InspectPluginHooks(plugin.name)
      setPluginHookReview(review as PluginHookReview)
    } catch (e: any) {
      setPluginError(String(e?.message || e || 'failed to inspect plugin hooks'))
    } finally {
      setPluginBusy(null)
    }
  }

  const togglePluginHooks = async () => {
    if (!pluginHookReview) return
    const enabling = !pluginHookReview.armed
    if (enabling && !await requestConfirmation({
      title: `Arm ${pluginHookReview.plugin} hooks?`,
      message: 'The supported commands shown in this review will run automatically as your user before or after matching tool calls.',
      confirmLabel: 'Arm reviewed hooks',
      danger: true,
    })) return
    const busyKey = `hook-arm:${pluginHookReview.plugin}`
    setPluginBusy(busyKey)
    setPluginError(null)
    try {
      await SetPluginHooksEnabled(pluginHookReview.plugin, enabling ? pluginHookReview.digest : '', enabling)
      const review: any = await InspectPluginHooks(pluginHookReview.plugin)
      setPluginHookReview(review as PluginHookReview)
      refreshPlugins()
    } catch (e: any) {
      setPluginError(String(e?.message || e || 'failed to update plugin hooks'))
    } finally {
      setPluginBusy(null)
    }
  }

  const importPluginMCP = async (server: PluginMCPServerReview) => {
    if (!pluginMCPReview) return
    if (server.existingServer && !await requestConfirmation({
      title: `Replace ${server.suggestedName}?`,
      message: 'The existing connector will be replaced with the reviewed plugin definition and left disabled for inspection.',
      confirmLabel: 'Replace connector',
      danger: true,
    })) return
    const busyKey = `mcp-import:${server.sourceName}`
    setPluginBusy(busyKey)
    setPluginError(null)
    try {
      await ImportPluginMCPConnector(pluginMCPReview.plugin, server.sourceName, pluginMCPReview.digest)
      setFeedback({
        type: 'success',
        text: `Imported ${server.suggestedName} as a disabled connector. Review, test, and enable it in MCP connectors.`,
      })
      refreshMCPServers()
      const refreshed: any = await InspectPluginMCPConnectors(pluginMCPReview.plugin)
      setPluginMCPReview(refreshed as PluginMCPReview)
    } catch (e: any) {
      setPluginError(String(e?.message || e || 'plugin connector import failed'))
    } finally {
      setPluginBusy(null)
    }
  }

  const handleProviderChange = (provider: string) => {
    const p = providers.find((pr) => pr.id === provider)
    const models = p ? p.models : []
    const checked = providerCapabilities[provider]
    const recommended = checked?.recommendedModel
    const nextModel = recommended && models.includes(recommended) ? recommended : models[0] || ''
    setLocal((prev) => ({ ...prev, defaultProvider: provider, defaultModel: nextModel }))
  }

  const modelsForProvider = (pid: string) => providers.find((p) => p.id === pid)?.models || []

  const handleSave = async () => {
    if (new TextEncoder().encode(local.globalInstructions || '').length > 65536) {
      setFeedback({ type: 'error', text: 'Global instructions exceed the 64 KiB limit.' })
      return
    }
    if (defaultModelUnavailable) {
      setFeedback({
        type: 'error',
        text: `${formatProviderModelLabel(local.defaultProvider, local.defaultModel)} is not currently available for this account. Re-test the connection or select an advertised model before saving.`,
      })
      jumpToSettings('settings-models')
      return
    }
    const submitted = { ...local }
    const snapshot = {
      ...submitted,
      glmKey: submitted.glmKey.trim(),
      kimiKey: submitted.kimiKey.trim(),
    }
    if (saveFeedbackTimerRef.current !== null) {
      window.clearTimeout(saveFeedbackTimerRef.current)
      saveFeedbackTimerRef.current = null
    }
    setSaving(true)
    setFeedback(null)
    try {
      await UpdateSettings({
        projects: [],
        settings: {
          theme: snapshot.theme,
          defaultProvider: snapshot.defaultProvider,
          defaultModel: snapshot.defaultModel,
          glmKey: snapshot.glmKey,
          kimiKey: snapshot.kimiKey,
          globalInstructions: snapshot.globalInstructions,
          defaultThinkingMode: snapshot.defaultThinkingMode,
          defaultThinkingBudget: snapshot.defaultThinkingBudget,
          defaultBudgetUSD: snapshot.defaultBudgetUSD,
          autoCleanupDisabled: snapshot.autoCleanupDisabled,
          autoBackupEnabled: snapshot.autoBackupEnabled,
          quickEntryEnabled: snapshot.quickEntryEnabled,
          quickEntryShortcut: snapshot.quickEntryShortcut,
          voiceShortcutEnabled: snapshot.voiceShortcutEnabled,
          voiceShortcut: snapshot.voiceShortcut,
          keepAwakeEnabled: snapshot.keepAwakeEnabled,
          autoUpdateCheckDisabled: snapshot.autoUpdateCheckDisabled,
          autoArchivePRAfterClose: snapshot.autoArchivePRAfterClose,
        },
      } as any)
      // A default is a creation-time preference. Existing projects keep their
      // explicitly selected provider/model; silently rewriting every workspace
      // here made a routine Settings save a surprising bulk mutation.
      const keys: (keyof Settings)[] = ['theme', 'defaultProvider', 'defaultModel', 'glmKey', 'kimiKey', 'globalInstructions', 'defaultThinkingMode', 'defaultThinkingBudget', 'defaultBudgetUSD', 'autoCleanupDisabled', 'autoBackupEnabled', 'quickEntryEnabled', 'quickEntryShortcut', 'voiceShortcutEnabled', 'voiceShortcut', 'keepAwakeEnabled', 'autoUpdateCheckDisabled', 'autoArchivePRAfterClose']
      for (const key of keys) updateField(key, snapshot[key] as string | number | boolean)
      const currentDraft = localRef.current
      const newerEditsRemain = !settingsEqual(currentDraft, submitted)
      if (!newerEditsRemain) setLocal({ ...snapshot })
      setSettingsDraft(newerEditsRemain ? { ...currentDraft } : null)
      setVisibleKeys({})

      // Update the obvious credential sources immediately. A saved explicit
      // key always wins; a cleared setting is conservatively shown as absent
      // until the backend confirms whether an environment fallback exists.
      const sourceFallback = { ...providerCredentialSources }
      for (const [provider, field] of Object.entries(KEY_PROVIDER_MAP)) {
        sourceFallback[provider] = String(snapshot[field] || '').trim()
          ? 'setting'
          : snapshot[field] === settings[field] && providerCredentialSources[provider] === 'env'
            ? 'env'
            : ''
      }
      setProviderCredentialSources(sourceFallback as Record<string, string>)

      let sourceRefreshError = ''
      try {
        const sources = await GetProviderCredentialSources()
        if (!sources || typeof sources !== 'object') throw new Error('invalid credential-source response')
        setProviderCredentialSources(sources as Record<string, string>)
      } catch (error: any) {
        sourceRefreshError = String(error?.message || error || 'unknown error')
      }
      await Promise.all([refreshQuickEntryStatus(), refreshSpeechStatus(), refreshWakeStatus()])

      if (saveFeedbackTimerRef.current !== null) window.clearTimeout(saveFeedbackTimerRef.current)
      const suffix = newerEditsRemain ? ' Newer edits remain unsaved.' : ''
      if (sourceRefreshError) {
        setFeedback({ type: 'warning', text: `Settings saved, but connection metadata could not refresh.${suffix}` })
      } else {
        setFeedback({ type: 'success', text: `Settings saved.${suffix}` })
      }
      saveFeedbackTimerRef.current = window.setTimeout(() => {
        setFeedback(null)
        saveFeedbackTimerRef.current = null
      }, sourceRefreshError ? 6500 : 3000)
    } catch (e: any) {
      setFeedback({ type: 'error', text: `Could not save settings: ${String(e?.message || e || 'unknown error')}` })
      await refreshQuickEntryStatus()
      await refreshSpeechStatus()
      await refreshWakeStatus()
    } finally {
      setSaving(false)
    }
  }

  const keyProviders = providers.filter((p) => p.id in KEY_PROVIDER_MAP)
  const configuredProviderCount = keyProviders.filter((provider) => {
    const field = KEY_PROVIDER_MAP[provider.id]
    if (!field) return false
    if (String(local[field] || '').trim()) return true
    return local[field] === settings[field] && providerCredentialSources[provider.id] === 'env'
  }).length
  const globalInstructionsBytes = new TextEncoder().encode(local.globalInstructions || '').length
  const hasUnsavedChanges = SETTINGS_FIELD_KEYS.some((key) => local[key] !== settings[key])
  const savedProviderCapability = providerCapabilities[local.defaultProvider]
  const discoveredDefaultModels = savedProviderCapability?.availableModels || []
  const recommendedDefaultModel = savedProviderCapability?.recommendedModel
  const selectedProvider = providers.find((item) => item.id === local.defaultProvider)
  const defaultModelMissingFromCatalog = !!local.defaultModel && !selectedProvider?.models.includes(local.defaultModel)
  const defaultModelUnavailable = defaultModelMissingFromCatalog
    || (discoveredDefaultModels.length > 0 && !discoveredDefaultModels.includes(local.defaultModel))
  const fallbackDefaultModel = recommendedDefaultModel && selectedProvider?.models.includes(recommendedDefaultModel)
    ? recommendedDefaultModel
    : selectedProvider?.models[0] || ''
  const selectedDefaultModel = selectedProvider?.modelDetails?.find((item) => item.id === local.defaultModel)
  const defaultReasoningControl = studioReasoningControlKind(
    local.defaultProvider,
    local.defaultModel,
    selectedDefaultModel?.reasoningControl,
  )
  const defaultUsesKimiEffort = defaultReasoningControl === 'kimi-effort'
  const defaultUsesGLMEffort = defaultReasoningControl === 'glm-effort'
  const jumpToSettings = (id: string) => {
    setActiveSettingsSection(id)
    scrollIntoViewWithMotion(document.getElementById(id), { block: 'start' })
  }

  // The category strip overflows horizontally on compact windows. Keep the
  // active category visible as page scrolling updates it, without disturbing
  // the Settings page's vertical scroll position.
  useEffect(() => {
    const nav = settingsNavRef.current
    const button = nav?.querySelector<HTMLElement>(`[data-settings-section="${activeSettingsSection}"]`)
    if (!nav || !button) return
    const left = button.offsetLeft
    const right = left + button.offsetWidth
    const visibleLeft = nav.scrollLeft + 6
    const visibleRight = nav.scrollLeft + nav.clientWidth - 6
    if (left < visibleLeft) scrollToWithMotion(nav, { left: Math.max(0, left - 6) })
    else if (right > visibleRight) scrollToWithMotion(nav, { left: right - nav.clientWidth + 6 })
  }, [activeSettingsSection])

  // Keep the compact category bar oriented while the user scans this long
  // page. The page itself is the scroll container, so window-based observers
  // would report the wrong active section.
  useEffect(() => {
    const root = settingsPageRef.current
    if (!root) return
    const updateActiveSection = () => {
      scrollFrameRef.current = null
      if (root.scrollTop + root.clientHeight >= root.scrollHeight - 12) {
        setActiveSettingsSection(SETTINGS_NAV_ITEMS[SETTINGS_NAV_ITEMS.length - 1].id)
        return
      }
      const threshold = root.getBoundingClientRect().top + 124
      let active: string = SETTINGS_NAV_ITEMS[0].id
      for (const item of SETTINGS_NAV_ITEMS) {
        const section = document.getElementById(item.id)
        if (section && section.getBoundingClientRect().top <= threshold) active = item.id
        else break
      }
      setActiveSettingsSection(active)
    }
    const onScroll = () => {
      if (scrollFrameRef.current !== null) return
      scrollFrameRef.current = window.requestAnimationFrame(updateActiveSection)
    }
    root.addEventListener('scroll', onScroll, { passive: true })
    updateActiveSection()
    return () => {
      root.removeEventListener('scroll', onScroll)
      if (scrollFrameRef.current !== null) window.cancelAnimationFrame(scrollFrameRef.current)
    }
  }, [])

  // Deep links from chat/auth errors land directly on the actionable card,
  // after App has switched to the persistently mounted Settings view.
  useEffect(() => {
    const handler = (event: Event) => {
      const id = (event as CustomEvent).detail?.section
      if (!SETTINGS_NAV_ITEMS.some((item) => item.id === id)) return
      setActiveSettingsSection(id)
      requestAnimationFrame(() => scrollIntoViewWithMotion(document.getElementById(id), { block: 'start' }))
    }
    window.addEventListener('gokin:show-settings-section', handler)
    return () => window.removeEventListener('gokin:show-settings-section', handler)
  }, [])

  const discardUnsavedChanges = async () => {
    if (!await requestConfirmation({
      title: 'Discard unsaved settings?',
      message: 'Every unsaved field on this page, including API key edits, will return to its last saved value.',
      confirmLabel: 'Discard changes',
      danger: true,
    })) return
    setLocal({ ...settings })
    applyThemePreference(settings.theme)
    setVisibleKeys({})
    setHealthResults({})
    setSettingsDraft(null)
    setFeedback({ type: 'success', text: 'Unsaved changes discarded.' })
  }

  useEffect(() => {
    const onSaveShortcut = (event: KeyboardEvent) => {
      if ((event as any).isComposing || event.keyCode === 229) return
      if (!(event.ctrlKey || event.metaKey) || event.altKey || event.key.toLowerCase() !== 's') return
      event.preventDefault()
      if (hasOpenModal()) return
      if (hasUnsavedChanges && !saving) void handleSave()
    }
    window.addEventListener('keydown', onSaveShortcut)
    return () => window.removeEventListener('keydown', onSaveShortcut)
  }, [hasUnsavedChanges, saving, handleSave])

  return (
    <div className="settings-page" ref={settingsPageRef}>
      <div className="settings-header-bar">
        <div>
          <h2 className="settings-title">Settings</h2>
          <p className="settings-subtitle">GLM, Kimi, tools, and local data</p>
        </div>
        <div className="settings-header-actions">
          {hasUnsavedChanges && (
            <button className="btn-secondary settings-discard-btn" onClick={() => void discardUnsavedChanges()} disabled={saving}>
              Discard
            </button>
          )}
          <button
            className={`btn-primary settings-save-btn ${!hasUnsavedChanges ? 'is-saved' : ''}`}
            onClick={handleSave}
            disabled={saving || globalInstructionsBytes > 65536 || defaultModelUnavailable || !hasUnsavedChanges}
            title={defaultModelUnavailable ? 'Select a model advertised for the tested account before saving' : 'Save settings (Ctrl/Cmd+S)'}
          >
            {!hasUnsavedChanges && !saving ? <CheckCircle size={14} /> : <Save size={14} />}
            {saving ? 'Saving…' : hasUnsavedChanges ? 'Save changes' : 'Saved'}
          </button>
        </div>
      </div>

      <nav
        ref={settingsNavRef}
        className="settings-jump-nav"
        aria-label="Settings sections"
        aria-keyshortcuts="ArrowLeft ArrowRight Home End"
        onKeyDown={(event) => {
          if (event.nativeEvent.isComposing || event.keyCode === 229) return
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
          const buttons = Array.from(settingsNavRef.current?.querySelectorAll<HTMLButtonElement>('button') || [])
          if (buttons.length === 0) return
          event.preventDefault()
          const current = buttons.indexOf(document.activeElement as HTMLButtonElement)
          const next = event.key === 'Home' ? 0
            : event.key === 'End' ? buttons.length - 1
              : event.key === 'ArrowRight' ? (current + 1 + buttons.length) % buttons.length
                : (current - 1 + buttons.length) % buttons.length
          const target = buttons[next]
          target?.focus()
          const id = target?.dataset.settingsSection
          if (id) jumpToSettings(id)
        }}
      >
        {SETTINGS_NAV_ITEMS.map((item) => (
          <button
            key={item.id}
            type="button"
            data-settings-section={item.id}
            className={activeSettingsSection === item.id ? 'active' : ''}
            aria-current={activeSettingsSection === item.id ? 'location' : undefined}
            onClick={() => jumpToSettings(item.id)}
          >
            {item.label}
          </button>
        ))}
      </nav>

      {feedback && (
        <div className={`settings-toast ${feedback.type}`} role={feedback.type === 'error' ? 'alert' : 'status'}>
          {feedback.type === 'success'
            ? <CheckCircle size={14} />
            : feedback.type === 'warning'
              ? <AlertTriangle size={14} />
              : <AlertCircle size={14} />}
          {feedback.text}
        </div>
      )}

      {/* Theme */}
      <div className="settings-section" id="settings-general">
        <h3 className="settings-section-header">Appearance</h3>
        <div className="settings-section-content">
          <div className="theme-toggle">
            <button className={`theme-option ${local.theme === 'system' ? 'active' : ''}`} onClick={() => handleChange('theme', 'system')} aria-pressed={local.theme === 'system'} title="Follow the operating-system appearance">
              <Monitor size={16} /> System
            </button>
            <button className={`theme-option ${local.theme === 'dark' ? 'active' : ''}`} onClick={() => handleChange('theme', 'dark')} aria-pressed={local.theme === 'dark'}>
              <Moon size={16} /> Dark
            </button>
            <button className={`theme-option ${local.theme === 'light' ? 'active' : ''}`} onClick={() => handleChange('theme', 'light')} aria-pressed={local.theme === 'light'}>
              <Sun size={16} /> Light
            </button>
          </div>
          <p className="settings-field-hint theme-hint">
            System follows macOS or Windows automatically, including changes made while Gokin Studio is open.
          </p>
        </div>
      </div>

      <div className="settings-section">
        <h3 className="settings-section-header"><Keyboard size={14} /> Quick Entry</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Open a compact, always-on-top GLM/Kimi draft with your five most recent chats, direct dictation, and reviewed
            desktop/window capture. Text access and voice are independent, opt-in global shortcuts registered only while Gokin
            is running. A text shortcut never starts listening; only the separate voice shortcut does, and neither path sends a
            message automatically.
          </p>
          <label className="settings-toggle-row">
            <input
              type="checkbox"
              checked={local.quickEntryEnabled}
              disabled={quickEntryStatus !== null && !quickEntryStatus.supported}
              onChange={(event) => setLocal((previous) => ({ ...previous, quickEntryEnabled: event.target.checked }))}
            />
            <span>Enable global Quick Entry</span>
          </label>
          <label className="settings-field quick-entry-shortcut-field">
            <span className="settings-field-label">Global shortcut</span>
            <input
              type="text"
              value={local.quickEntryShortcut}
              disabled={quickEntryStatus !== null && !quickEntryStatus.supported}
              onChange={(event) => setLocal((previous) => ({ ...previous, quickEntryShortcut: event.target.value }))}
              placeholder={quickEntryStatus?.shortcut || (isMacPlatform ? 'Double-tap Option' : 'Ctrl+Alt+Space')}
              spellCheck={false}
              autoComplete="off"
              aria-describedby="quick-entry-shortcut-help"
              list="quick-entry-shortcut-presets"
            />
            <datalist id="quick-entry-shortcut-presets">
              {isMacPlatform && <option value="Double-tap Option" />}
              {isMacPlatform && <option value="Option+Space" />}
              <option value={isMacPlatform ? 'Command+Shift+K' : 'Ctrl+Alt+Space'} />
            </datalist>
          </label>
          <p id="quick-entry-shortcut-help" className="settings-field-hint">
            {isMacPlatform
              ? 'Use Double-tap Option (Claude-style default), Option+Space, or one key with modifiers such as Command+Shift+K. Double-tap Option requires macOS Accessibility access.'
              : 'Use one key with at least one modifier, for example Ctrl+Alt+Space or Ctrl+Shift+G.'}
            {' '}If registration fails, the previous shortcut stays active.
          </p>
          <div className={`quick-entry-status ${quickEntryStatus?.active ? 'active' : quickEntryStatus?.error ? 'error' : ''}`}>
            <Keyboard size={12} />
            <span>
              {quickEntryStatus === null
                ? 'Checking operating-system support…'
                : !quickEntryStatus.supported
                  ? 'Available on macOS and Windows.'
                  : quickEntryStatus.active
                    ? local.quickEntryShortcut !== settings.quickEntryShortcut
                      ? `${quickEntryStatus.shortcut} is active · save to change it`
                      : `${quickEntryStatus.shortcut} is active`
                    : local.quickEntryEnabled !== settings.quickEntryEnabled || local.quickEntryShortcut !== settings.quickEntryShortcut
                      ? `Save to ${local.quickEntryEnabled ? 'register' : 'remove'} ${local.quickEntryShortcut || quickEntryStatus.shortcut}`
                      : `${quickEntryStatus.shortcut} is inactive`}
            </span>
          </div>
          {quickEntryStatus?.error && <p className="settings-hint settings-hint-error">{quickEntryStatus.error}</p>}

          <div className="quick-entry-voice-divider" />
          <label className="settings-toggle-row">
            <input
              type="checkbox"
              checked={local.voiceShortcutEnabled}
              disabled={quickEntryStatus !== null && !quickEntryStatus.supported}
              onChange={(event) => setLocal((previous) => ({ ...previous, voiceShortcutEnabled: event.target.checked }))}
            />
            <span>Enable global voice dictation</span>
          </label>
          <label className="settings-field quick-entry-shortcut-field">
            <span className="settings-field-label">Voice shortcut</span>
            <input
              type="text"
              value={local.voiceShortcut}
              disabled={quickEntryStatus !== null && !quickEntryStatus.supported}
              onChange={(event) => setLocal((previous) => ({ ...previous, voiceShortcut: event.target.value }))}
              placeholder={quickEntryStatus?.voiceShortcut || (isMacPlatform ? 'Caps Lock' : 'Ctrl+Alt+D')}
              spellCheck={false}
              autoComplete="off"
              aria-describedby="voice-shortcut-help"
              list="voice-shortcut-presets"
            />
            <datalist id="voice-shortcut-presets">
              {isMacPlatform && <option value="Caps Lock" />}
              <option value={isMacPlatform ? 'Control+Option+D' : 'Ctrl+Alt+D'} />
            </datalist>
          </label>
          <p id="voice-shortcut-help" className="settings-field-hint">
            {isMacPlatform
              ? 'Caps Lock opens Quick Entry and starts dictation; press it again to stop and review. While enabled, it replaces normal Caps Lock and requires Accessibility plus Speech Recognition permission.'
              : 'The voice shortcut opens Quick Entry and starts dictation; press it again to stop and review. Speech recognition permission is still required.'}
          </p>
          <div className={`quick-entry-status ${quickEntryStatus?.voiceActive ? 'active' : quickEntryStatus?.voiceError ? 'error' : ''}`}>
            <Mic size={12} />
            <span>
              {quickEntryStatus === null
                ? 'Checking voice shortcut support…'
                : !quickEntryStatus.supported
                  ? 'Available on macOS and Windows.'
                  : quickEntryStatus.voiceActive
                    ? local.voiceShortcut !== settings.voiceShortcut
                      ? `${quickEntryStatus.voiceShortcut} is active · save to change it`
                      : `${quickEntryStatus.voiceShortcut} is active`
                    : local.voiceShortcutEnabled !== settings.voiceShortcutEnabled || local.voiceShortcut !== settings.voiceShortcut
                      ? `Save to ${local.voiceShortcutEnabled ? 'register' : 'remove'} ${local.voiceShortcut || quickEntryStatus.voiceShortcut}`
                      : `${quickEntryStatus.voiceShortcut} is inactive`}
            </span>
          </div>
          {quickEntryStatus?.voiceError && <p className="settings-hint settings-hint-error">{quickEntryStatus.voiceError}</p>}

          <div className="native-dictation-permissions" aria-live="polite">
            <div className={`quick-entry-status ${speechStatus?.supported && speechStatus.speechAuthorization === 'authorized' && speechStatus.microphoneAuthorization === 'authorized' && speechStatus.available ? 'active' : speechStatus?.error || speechStatus?.speechAuthorization === 'denied' || speechStatus?.microphoneAuthorization === 'denied' ? 'error' : ''}`}>
              <Mic size={12} />
              <span>
                {speechStatus === null
                  ? 'Checking native dictation…'
                  : !speechStatus.supported
                    ? 'Native dictation needs macOS 14+; WebView speech remains the fallback when available.'
                    : speechStatus.speechAuthorization === 'authorized' && speechStatus.microphoneAuthorization === 'authorized'
                      ? speechStatus.available ? 'Apple native dictation is ready' : 'Permissions granted · Apple Speech is temporarily unavailable'
                      : `Speech: ${speechStatus.speechAuthorization} · Microphone: ${speechStatus.microphoneAuthorization}`}
              </span>
            </div>
            {speechStatus?.supported && (speechStatus.speechAuthorization !== 'authorized' || speechStatus.microphoneAuthorization !== 'authorized') && (
              <button
                type="button"
                className="btn-secondary native-dictation-permission-btn"
                onClick={() => void requestSpeechPermissions()}
                disabled={requestingSpeechPermissions}
              >
                {requestingSpeechPermissions ? <Loader2 size={12} className="spin" /> : <Mic size={12} />}
                {requestingSpeechPermissions ? 'Waiting for macOS…' : 'Enable native dictation'}
              </button>
            )}
          </div>
          <p className="settings-field-hint">
            Permission checks never activate the microphone. Capture begins only after Dictate or the enabled voice shortcut,
            and raw audio stays inside Apple Speech Recognition; Gokin receives transcript text only.
          </p>
          {speechStatus?.error && <p className="settings-hint settings-hint-error">{speechStatus.error}</p>}

          <div className="quick-entry-voice-divider" />
          <div className="desktop-deep-link" aria-labelledby="desktop-deep-link-title">
            <div>
              <strong id="desktop-deep-link-title">Desktop links</strong>
              <p className="settings-field-hint">
                Open Gokin from a browser, script, or another app. Add <code>q=</code> to prefill an editable draft;
                links never send a message or approve an action automatically.
              </p>
            </div>
            <div className="desktop-deep-link-example">
              <code>{`gokin://studio/new${activeProjectID ? `?project=${encodeURIComponent(activeProjectID)}` : ''}`}</code>
              <button
                type="button"
                className="btn-secondary"
                onClick={() => {
                  const value = `gokin://studio/new${activeProjectID ? `?project=${encodeURIComponent(activeProjectID)}` : ''}`
                  ClipboardSetText(value)
                  setFeedback({ type: 'success', text: 'New-chat desktop link copied.' })
                }}
              >
                <Clipboard size={12} /> Copy link
              </button>
            </div>
          </div>
        </div>
      </div>

      <div className="settings-section">
        <h3 className="settings-section-header"><Activity size={14} /> Background readiness</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Keep this computer from entering system sleep while a GLM/Kimi agent is running. If any scheduled task is enabled,
            the inhibitor stays active so the local scheduler can reach its next run time. The display may still turn off.
            This setting works only while Gokin Studio is open and can increase laptop battery use.
          </p>
          <label className="settings-toggle-row">
            <input
              type="checkbox"
              checked={local.keepAwakeEnabled}
              disabled={wakeStatus !== null && !wakeStatus.supported}
              onChange={(event) => setLocal((previous) => ({ ...previous, keepAwakeEnabled: event.target.checked }))}
            />
            <span>Keep computer awake for active and scheduled tasks</span>
          </label>
          <div className={`quick-entry-status ${wakeStatus?.active ? 'active' : wakeStatus?.error ? 'error' : ''}`}>
            <Activity size={12} />
            <span>
              {wakeStatus === null
                ? 'Checking operating-system support…'
                : !wakeStatus.supported
                  ? 'Sleep inhibition is unavailable on this system.'
                  : wakeStatus.active
                    ? wakeStatus.scheduledTasks
                      ? `Active · scheduled tasks armed${wakeStatus.activeRuns ? ` · ${wakeStatus.activeRuns} running` : ''}`
                      : `Active · ${wakeStatus.activeRuns} agent run${wakeStatus.activeRuns === 1 ? '' : 's'}`
                    : local.keepAwakeEnabled !== settings.keepAwakeEnabled
                      ? `Save to ${local.keepAwakeEnabled ? 'enable' : 'disable'}`
                      : local.keepAwakeEnabled
                        ? 'Ready · activates when work starts'
                        : 'Disabled'}
            </span>
          </div>
          {wakeStatus?.error && <p className="settings-hint settings-hint-error">{wakeStatus.error}</p>}
        </div>
      </div>

      <div className="settings-section">
        <h3 className="settings-section-header"><GitPullRequest size={14} /> Pull request lifecycle</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Optionally archive a local chat after its GitHub pull request is merged or closed. Studio checks only while it is open.
            Running chats, the last active chat, dirty worktrees, unavailable worktrees, and unattended task runs always remain visible.
          </p>
          <label className="settings-toggle-row">
            <input
              type="checkbox"
              checked={local.autoArchivePRAfterClose}
              onChange={(event) => setLocal((previous) => ({ ...previous, autoArchivePRAfterClose: event.target.checked }))}
            />
            <span>Auto-archive finished chats after PR merge or close</span>
          </label>
          <div className={`quick-entry-status ${local.autoArchivePRAfterClose ? 'active' : ''}`}>
            <Archive size={12} />
            <span>
              {local.autoArchivePRAfterClose !== settings.autoArchivePRAfterClose
                ? `Save to ${local.autoArchivePRAfterClose ? 'enable' : 'disable'}`
                : local.autoArchivePRAfterClose
                  ? 'Enabled · clean idle sessions only'
                  : 'Disabled · chats remain user-managed'}
            </span>
          </div>
        </div>
      </div>

      <div className="settings-section">
        <h3 className="settings-section-header"><ShieldCheck size={14} /> Local environment</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Variables apply to new agent commands, background tasks, integrated terminals, and preview servers in every local chat.
            Values are stored in macOS Keychain or Windows DPAPI and are never loaded back into this screen, logs, exports, or backups.
            Project code can read them, so use only credentials you intend to expose to local tools.
          </p>
          {localEnvironmentLoading ? (
            <div className="local-environment-empty"><Loader2 size={13} className="spin" /> Loading secure environment…</div>
          ) : (
            <div className="local-environment-list">
              {localEnvironmentRows.length === 0 && (
                <div className="local-environment-empty">No local environment variables configured.</div>
              )}
              {localEnvironmentRows.map((row) => (
                <div className="local-environment-row" key={row.id}>
                  <input
                    aria-label="Environment variable name"
                    spellCheck={false}
                    value={row.name}
                    disabled={row.existing || localEnvironmentSaving}
                    placeholder="VARIABLE_NAME"
                    onChange={(event) => setLocalEnvironmentRows((rows) => rows.map((candidate) => (
                      candidate.id === row.id ? { ...candidate, name: event.target.value } : candidate
                    )))}
                  />
                  <input
                    aria-label={`Value for ${row.name || 'new environment variable'}`}
                    type="password"
                    autoComplete="new-password"
                    spellCheck={false}
                    value={row.value}
                    disabled={localEnvironmentSaving}
                    placeholder={row.existing && row.keepExisting ? 'Stored securely — enter to replace' : 'Value (empty is allowed)'}
                    onChange={(event) => setLocalEnvironmentRows((rows) => rows.map((candidate) => (
                      candidate.id === row.id
                        ? { ...candidate, value: event.target.value, keepExisting: false }
                        : candidate
                    )))}
                  />
                  <button
                    className="btn-danger-sm local-environment-remove"
                    aria-label={`Remove ${row.name || 'environment variable'}`}
                    title="Remove variable"
                    disabled={localEnvironmentSaving}
                    onClick={() => setLocalEnvironmentRows((rows) => rows.filter((candidate) => candidate.id !== row.id))}
                  >
                    <Trash2 size={12} />
                  </button>
                </div>
              ))}
              <div className="local-environment-actions">
                <button
                  className="btn-secondary"
                  disabled={localEnvironmentSaving || localEnvironmentRows.length >= 64}
                  onClick={() => setLocalEnvironmentRows((rows) => [...rows, {
                    id: ++localEnvironmentRowID.current,
                    name: '',
                    value: '',
                    existing: false,
                    keepExisting: false,
                  }])}
                >
                  <Plus size={12} /> Add variable
                </button>
                <button className="btn-primary" disabled={localEnvironmentLoading || localEnvironmentSaving} onClick={() => void saveLocalEnvironment()}>
                  {localEnvironmentSaving ? <Loader2 size={12} className="spin" /> : <Save size={12} />}
                  {localEnvironmentSaving ? 'Saving…' : 'Save environment'}
                </button>
              </div>
            </div>
          )}
          {localEnvironmentError && <p className="settings-hint settings-hint-error local-environment-error">{localEnvironmentError}</p>}
          <p className="settings-field-hint">
            Changes affect newly started processes. Preview-specific <code>.claude/launch.json</code> values override matching names;
            Studio still controls HOME, PATH, host, port, and other isolation variables.
          </p>
        </div>
      </div>

      <div className="settings-section">
        <h3 className="settings-section-header"><ShieldCheck size={14} /> Browser domain permissions</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            “Always allow” permissions from Browser tabs are stored per exact origin. A different scheme, port, or subdomain always requires its own review. Private, loopback, link-local, and reserved network addresses remain blocked even when an origin appears here.
          </p>
          <div className="browser-permission-list">
            {browserPermissionsLoading ? (
              <div className="browser-permission-empty"><Loader2 size={13} className="spin" /> Loading browser permissions…</div>
            ) : browserPermissions.length === 0 ? (
              <div className="browser-permission-empty">No browser domains are permanently allowed.</div>
            ) : browserPermissions.map((permission) => (
              <div className="browser-permission-row" key={permission.origin}>
                <ShieldCheck size={12} />
                <code>{permission.origin}</code>
                <button className="btn-danger-sm" onClick={() => void revokeBrowserPermission(permission.origin)}><Trash2 size={11} /> Revoke</button>
              </div>
            ))}
          </div>
          {browserPermissionsError && <p className="settings-hint settings-hint-error">{browserPermissionsError}</p>}
          <p className="settings-field-hint">Open tabs keep an ephemeral, tab-isolated cookie profile until the tab, chat, project, or application closes.</p>
        </div>
      </div>

      <div className="settings-section">
        <h3 className="settings-section-header"><ShieldCheck size={14} /> Code execution isolation</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Commands requested by GLM 5.2 or Kimi K3 receive an isolated HOME, temporary directory, and language caches.
            When the platform sandbox is active, project code can write only inside the connected workspace and its private
            runtime; the real user home and external volumes are hidden. IP networking is blocked by default. A command,
            test, or verification run can request full host networking, but every such request shows a fresh exact-action
            approval warning that includes LAN/private-service access.
          </p>
          <div className={`quick-entry-status ${workspaceIsolation?.enforced ? 'active' : workspaceIsolation ? 'error' : ''}`}>
            {workspaceIsolation?.enforced ? <ShieldCheck size={12} /> : <AlertTriangle size={12} />}
            <span>
              {workspaceIsolation === null
                ? 'Checking workspace isolation…'
                : workspaceIsolation.enforced
                  ? `Active · ${workspaceIsolation.mode}`
                  : 'Host execution · exact approval required for every command'}
            </span>
          </div>
          {workspaceIsolation?.detail && <p className="settings-hint">{workspaceIsolation.detail}</p>}
        </div>
      </div>

      {/* Default Provider & Model */}
      <div className="settings-section" id="settings-models">
        <h3 className="settings-section-header">Default Provider</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Used for new projects only. Existing projects keep their current GLM or Kimi model.
          </p>
          <div className="provider-grid">
            {providers.map((p) => (
              <button
                key={p.id}
                className={`provider-card ${local.defaultProvider === p.id ? 'selected' : ''}`}
                onClick={() => handleProviderChange(p.id)}
                aria-pressed={local.defaultProvider === p.id}
              >
                <span className="provider-card-dot" style={{ background: PROVIDER_COLORS[p.id] || '#888' }} />
                <span className="provider-card-name">{p.name}</span>
                {local.defaultProvider === p.id && <CheckCircle size={14} className="provider-card-check" />}
              </button>
            ))}
          </div>

          <div className="settings-field default-model-field">
            <div className="default-model-label-row">
              <label id="default-model-label">Model for new projects</label>
              {discoveredDefaultModels.length > 0 ? (
                <span className="default-model-account-state"><CheckCircle size={11} /> Account checked</span>
              ) : (
                <button className="default-model-test-link" onClick={() => jumpToSettings('settings-connections')}>
                  Test account availability
                </button>
              )}
            </div>
            <div className="default-model-grid" role="group" aria-labelledby="default-model-label">
              {(selectedProvider?.modelDetails || []).map((model) => {
                const selected = local.defaultModel === model.id
                const knownUnavailable = discoveredDefaultModels.length > 0 && !discoveredDefaultModels.includes(model.id)
                const accountRecommended = recommendedDefaultModel === model.id
                return (
                  <button
                    key={model.id}
                    type="button"
                    aria-pressed={selected}
                    disabled={knownUnavailable}
                    className={`default-model-card ${selected ? 'selected' : ''} ${knownUnavailable ? 'unavailable' : ''}`}
                    onClick={() => handleChange('defaultModel', model.id)}
                    title={knownUnavailable ? `${model.id} was not advertised for the tested key` : `Use ${model.id} for new projects`}
                  >
                    <span className="default-model-card-head">
                      <span className="default-model-identity">
                        <strong>{formatProviderModelLabel(local.defaultProvider, model.id)}</strong>
                        <code>{model.id}</code>
                      </span>
                      <span className="default-model-badges">
                        {model.latest && <span className="default-model-badge latest">Latest</span>}
                        {accountRecommended && <span className="default-model-badge best">Best for your key</span>}
                        {knownUnavailable && <span className="default-model-badge unavailable">Unavailable</span>}
                        {selected && <CheckCircle size={14} aria-hidden />}
                      </span>
                    </span>
                    <span className="default-model-description">{model.description}</span>
                    <span className="default-model-meta">
                      <span>{formatContextWindow(model.contextWindow)} context</span>
                      <span>{model.inputModalities.join(' + ')}</span>
                      <span>{model.reasoningControl}</span>
                    </span>
                  </button>
                )
              })}
            </div>
            {defaultModelUnavailable && (
              <div className="model-availability-warning" role="status">
                <AlertTriangle size={13} />
                <span>
                  {defaultModelMissingFromCatalog
                    ? `${formatProviderModelLabel(local.defaultProvider, local.defaultModel)} is no longer in the current account catalog. Re-test the connection if account access changed.`
                    : `${formatProviderModelLabel(local.defaultProvider, local.defaultModel)} was not advertised for the tested ${local.defaultProvider.toUpperCase()} key.`}
                  {fallbackDefaultModel && <> Available choice: <strong>{formatProviderModelLabel(local.defaultProvider, fallbackDefaultModel)}</strong>.</>}
                </span>
                {fallbackDefaultModel && (
                  <button
                    className="btn-secondary"
                    onClick={() => handleChange('defaultModel', fallbackDefaultModel)}
                  >
                    Use {formatModelLabel(fallbackDefaultModel)}
                  </button>
                )}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Default Thinking Mode */}
      <div className="settings-section">
        <h3 className="settings-section-header"><Brain size={14} /> Default Thinking</h3>
        <div className="settings-section-content">
          <p className="settings-hint">Applied to new projects. Per-project overrides take precedence.</p>
          <div className="settings-field">
            <label>{
              defaultUsesKimiEffort || defaultUsesGLMEffort
                ? 'Reasoning effort'
                : 'Mode'
            }</label>
            {defaultUsesKimiEffort ? (
              <select
                value={local.defaultThinkingMode === '' ? 'auto' : local.defaultThinkingMode === 'disabled' ? 'low' : local.defaultThinkingBudget > 16384 ? 'max' : local.defaultThinkingBudget > 4096 ? 'high' : 'low'}
                onChange={(e) => {
                  const value = e.target.value
                  setLocal((prev) => value === 'auto'
                    ? { ...prev, defaultThinkingMode: '', defaultThinkingBudget: 0 }
                    : {
                        ...prev,
                        defaultThinkingMode: 'enabled',
                        defaultThinkingBudget: value === 'max' ? 32768 : value === 'high' ? 8192 : 4096,
                      })
                }}
              >
                <option value="auto">Auto · High (recommended)</option>
                <option value="low">Low</option>
                <option value="high">High</option>
                <option value="max">Max</option>
              </select>
            ) : defaultUsesGLMEffort ? (
              <select
                value={local.defaultThinkingMode === '' ? 'auto' : local.defaultThinkingMode === 'disabled' ? 'disabled' : local.defaultThinkingBudget > 16384 ? 'max' : 'high'}
                onChange={(e) => {
                  const value = e.target.value
                  setLocal((prev) => value === 'auto'
                    ? { ...prev, defaultThinkingMode: '', defaultThinkingBudget: 0 }
                    : value === 'disabled'
                      ? { ...prev, defaultThinkingMode: 'disabled', defaultThinkingBudget: 0 }
                      : {
                          ...prev,
                          defaultThinkingMode: 'enabled',
                          defaultThinkingBudget: value === 'max' ? 32768 : 8192,
                        })
                }}
              >
                <option value="auto">Auto · Max (recommended)</option>
                <option value="high">High</option>
                <option value="max">Max</option>
                <option value="disabled">Disabled</option>
              </select>
            ) : (
              <select value={local.defaultThinkingMode} onChange={(e) => setLocal((prev) => ({ ...prev, defaultThinkingMode: e.target.value }))}>
                <option value="">Auto (provider default)</option>
                <option value="enabled">Enabled</option>
                <option value="disabled">Disabled</option>
              </select>
            )}
          </div>
          {local.defaultThinkingMode === 'enabled' &&
            !defaultUsesKimiEffort &&
            !defaultUsesGLMEffort && (
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

      {/* Global user preferences */}
      <div className="settings-section" id="settings-personalization">
        <h3 className="settings-section-header"><Brain size={14} /> Global Instructions</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Preferences applied to every GLM 5.2 and Kimi K3 chat, such as language, tone, formatting, or review conventions. Project system prompts remain more specific and take precedence. Runtime permission and safety rules cannot be overridden here.
          </p>
          <div className="settings-field global-instructions-field">
            <label htmlFor="global-instructions">Your instructions</label>
            <textarea
              id="global-instructions"
              value={local.globalInstructions}
              onChange={(e) => setLocal((prev) => ({ ...prev, globalInstructions: e.target.value }))}
              placeholder={"Examples:\n- Answer in Russian unless asked otherwise.\n- Keep summaries concise and cite file paths.\n- Run focused tests after code changes."}
              rows={6}
              maxLength={65536}
              spellCheck
            />
            <span className={`settings-field-hint ${globalInstructionsBytes > 65536 ? 'limit' : ''}`}>
              {globalInstructionsBytes.toLocaleString()} / 65,536 bytes · stored locally; content is never written to diagnostics logs
            </span>
          </div>
        </div>
      </div>

      {/* Notifications (iter 530+) */}
      <div className="settings-section">
        <h3 className="settings-section-header"><Bell size={14} /> Notifications</h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Toast popups appear bottom-right when an agent finishes (or errors) in a chat session you're not currently viewing. Click to jump to that session.
            OS notifications can appear while the window is unfocused after you explicitly allow them; Gokin never opens the system permission prompt on startup.
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
          <div className="notification-permission-row">
            <div className={`quick-entry-status ${notificationPermission === 'granted' ? 'active' : notificationPermission === 'denied' ? 'error' : ''}`} role="status">
              {notificationPermission === 'denied' ? <BellOff size={12} /> : <Bell size={12} />}
              <span>
                {notificationPermission === 'granted'
                  ? 'OS notifications enabled'
                  : notificationPermission === 'denied'
                    ? 'Blocked by system settings'
                    : notificationPermission === 'unsupported'
                      ? 'OS notifications unavailable in this WebView'
                      : 'OS notifications not enabled'}
              </span>
            </div>
            {notificationPermission === 'default' && (
              <button className="btn-secondary notification-enable-btn" onClick={() => void enableOSNotifications()}>
                <Bell size={12} /> Enable OS notifications
              </button>
            )}
          </div>
          {notificationPermission === 'denied' && (
            <p className="settings-hint settings-hint-error">Permission can only be restored from the operating-system or WebView notification settings.</p>
          )}
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
                    aria-label={`Delete /${s.name} snippet`}
                    disabled={deletingSnippetId !== null || snippetSaving}
                    onClick={async () => {
                      const accepted = await requestConfirmation({
                        title: `Delete /${s.name}?`,
                        message: `The saved /${s.name} snippet will be permanently removed. Text already inserted into a chat draft will not be changed.`,
                        confirmLabel: 'Delete snippet',
                        cancelLabel: 'Keep snippet',
                        danger: true,
                      })
                      if (!accepted) return
                      setDeletingSnippetId(s.id)
                      setSnippetError(null)
                      setSnippetNotice(null)
                      try {
                        await DeleteUserSnippet(s.id)
                        setSnippets((previous) => previous.filter((snippet) => snippet.id !== s.id))
                        setSnippetNotice(`Deleted /${s.name}`)
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
                onChange={(e) => { setSnippetName(e.target.value); setSnippetError(null); setSnippetNotice(null) }}
              />
            </div>
            <textarea
              className="snippet-form-body"
              placeholder="Body — text inserted into chat input when you pick this snippet"
              value={snippetBody}
              maxLength={10000}
              rows={3}
              onChange={(e) => { setSnippetBody(e.target.value); setSnippetError(null); setSnippetNotice(null) }}
            />
            {snippetError && <div className="snippet-form-error"><AlertCircle size={12} /> {snippetError}</div>}
            {snippetNotice && <div className="snippet-form-success" role="status"><CheckCircle size={12} /> {snippetNotice}</div>}
            <div className="snippet-form-actions">
              <button
                className="btn-primary"
                disabled={snippetSaving || deletingSnippetId !== null || !snippetName.trim() || !snippetBody.trim()}
                onClick={async () => {
                  setSnippetSaving(true)
                  setSnippetError(null)
                  setSnippetNotice(null)
                  try {
                    const savedName = snippetName.trim()
                    await SaveUserSnippet(savedName, snippetBody)
                    setSnippetName('')
                    setSnippetBody('')
                    refreshSnippets()
                    setSnippetNotice(`Saved /${savedName}`)
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

      {/* Claude-compatible plugin bundles */}
      <div className="settings-section" id="settings-extensions">
        <h3 className="settings-section-header">
          <Archive size={14} /> Plugins
          <span className="settings-header-badge">{plugins.filter((plugin) => plugin.enabled).length}/{plugins.length}</span>
        </h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Install Claude-compatible ZIP plugins with Skills and namespaced commands. Compatibility refers only to the plugin bundle format: every agent run still uses the project's GLM or Kimi model. Files are reviewed and copied into private app storage with scripts made non-executable. New plugins stay disabled until you enable them.
          </p>
          <p className="settings-hint">
            Enabled agent definitions become permission-gated GLM/Kimi specialists in separate inspectable child chats. Their tool allowlists are enforced, recursive delegation is blocked, and plugin model/permission hints cannot override your project. Bundled MCP definitions import one at a time, always disabled; hooks, header helpers, and scripts never run automatically.
          </p>

          {pluginError && <div className="snippet-form-error"><AlertCircle size={12} /> {pluginError}</div>}

          <div className="mcp-bundle-launch">
            <button className="btn-secondary" disabled={pluginBusy !== null} onClick={() => void selectPluginBundle()}>
              {pluginBusy === 'select' ? <Loader2 size={12} className="tool-spinner" /> : <Archive size={12} />}
              Install plugin ZIP…
            </button>
            <span>Preview manifest, components, and security warnings before installation.</span>
          </div>

          {pluginPreview && (
            <div className="mcp-bundle-review plugin-review">
              <div className="mcp-bundle-heading">
                <div className="mcp-bundle-icon"><Archive size={18} /></div>
                <div>
                  <strong>{pluginPreview.name}</strong>
                  <span>
                    {pluginPreview.version ? `v${pluginPreview.version}` : 'unversioned'}
                    {pluginPreview.author ? ` · by ${pluginPreview.author}` : ''}
                    {pluginPreview.existing ? ' · replaces installed copy' : ''}
                  </span>
                </div>
                <button className="icon-btn" title="Cancel installation" aria-label="Cancel plugin installation" disabled={pluginBusy !== null} onClick={() => setPluginPreview(null)}>
                  <Trash2 size={12} />
                </button>
              </div>
              {pluginPreview.description && <p className="mcp-bundle-description">{pluginPreview.description}</p>}
              <div className="plugin-component-summary">
                <span><strong>{pluginPreview.skills?.length || 0}</strong> Skills</span>
                <span><strong>{pluginPreview.commands?.length || 0}</strong> commands</span>
                <span><strong>{pluginPreview.agents?.length || 0}</strong> specialist agents</span>
                {pluginPreview.hasMCP && <code>{pluginPreview.mcpServers?.length || 0} MCP definitions</code>}
                {pluginPreview.hasHooks && <code>hooks install disarmed</code>}
                {pluginPreview.hasScripts && <code>scripts installed non-executable</code>}
              </div>
              {(pluginPreview.mcpServers?.length || 0) > 0 && (
                <div className="mcp-bundle-tools">
                  <span>Bundled connectors</span>
                  {(pluginPreview.mcpServers || []).map((server) => (
                    <code key={server.name}>
                      {server.name} · {server.transport || 'unknown'}{server.importable ? '' : ' · manual setup'}
                    </code>
                  ))}
                </div>
              )}
              {(pluginPreview.commands?.length || 0) > 0 && (
                <div className="mcp-bundle-tools">
                  <span>Slash commands</span>
                  {(pluginPreview.commands || []).map((command) => (
                    <code key={command.slashName}>/{command.slashName}</code>
                  ))}
                </div>
              )}
              {(pluginPreview.agents?.length || 0) > 0 && (
                <div className="mcp-bundle-tools">
                  <span>Permission-gated specialists after enable</span>
                  {(pluginPreview.agents || []).map((agent) => (
                    <code key={agent.name} title={agent.description || agent.name}>
                      {pluginPreview.name}:{agent.name}
                    </code>
                  ))}
                </div>
              )}
              {(pluginPreview.warnings || []).map((warning, index) => (
                <div key={`${warning}-${index}`} className="mcp-bundle-warning">
                  <AlertTriangle size={12} />
                  <span>{warning}</span>
                </div>
              ))}
              <span className="settings-field-hint">Installation does not enable the plugin. You can inspect this summary again before activating it below.</span>
              <div className="mcp-bundle-actions">
                <button className="btn-secondary" disabled={pluginBusy !== null} onClick={() => setPluginPreview(null)}>Cancel</button>
                <button className="btn-primary" disabled={pluginBusy !== null} onClick={() => void installSelectedPlugin()}>
                  {pluginBusy === 'install' ? <Loader2 size={12} className="tool-spinner" /> : <Archive size={12} />}
                  {pluginPreview.existing ? 'Replace plugin' : 'Install disabled'}
                </button>
              </div>
            </div>
          )}

          {plugins.length > 0 && (
            <div className="plugin-list">
              {plugins.map((plugin) => (
                <div key={plugin.name} className={`plugin-row ${plugin.enabled ? 'enabled' : ''}`}>
                  <div className="plugin-row-main">
                    <div className="plugin-row-title">
                      <span className={`mcp-dot ${plugin.enabled ? 'on' : ''}`} />
                      <strong>{plugin.name}</strong>
                      {plugin.version && <span className="mcp-status">v{plugin.version}</span>}
                      <span className={`mcp-status ${plugin.enabled ? 'ok' : ''}`}>{plugin.enabled ? 'enabled' : 'disabled'}</span>
                    </div>
                    {plugin.description && <span className="plugin-row-description">{plugin.description}</span>}
                    <div className="plugin-component-summary compact">
                      <span>{plugin.skills?.length || 0} Skills</span>
                      <span>{plugin.commands?.length || 0} commands</span>
                      <span>{plugin.agents?.length || 0} agents</span>
                      {plugin.hasMCP && <code>MCP</code>}
                      {plugin.hasHooks && <code>{plugin.hooksEnabled ? 'hooks armed' : 'hooks disarmed'}</code>}
                      {plugin.hasScripts && <code>scripts installed</code>}
                    </div>
                    {plugin.enabled && (plugin.commands?.length || 0) > 0 && (
                      <div className="plugin-command-list">
                        {(plugin.commands || []).map((command) => <code key={command.slashName}>/{command.slashName}</code>)}
                      </div>
                    )}
                    {plugin.enabled && (plugin.agents?.length || 0) > 0 && (
                      <div className="plugin-command-list">
                        {(plugin.agents || []).map((agent) => (
                          <code key={agent.name} title={agent.description || agent.name}>
                            agent · {plugin.name}:{agent.name}
                          </code>
                        ))}
                      </div>
                    )}
                  </div>
                  <div className="plugin-row-actions">
                    {plugin.hasHooks && (
                      <button
                        className="btn-secondary"
                        disabled={pluginBusy !== null}
                        onClick={() => void inspectPluginHooks(plugin)}
                      >
                        {pluginBusy === `hook-review:${plugin.name}`
                          ? <Loader2 size={11} className="tool-spinner" />
                          : <AlertTriangle size={11} />}
                        Review hooks
                      </button>
                    )}
                    {plugin.hasMCP && (
                      <button
                        className="btn-secondary"
                        disabled={pluginBusy !== null}
                        onClick={() => void inspectPluginMCP(plugin)}
                      >
                        {pluginBusy === `mcp-review:${plugin.name}`
                          ? <Loader2 size={11} className="tool-spinner" />
                          : <Wifi size={11} />}
                        Review connectors
                      </button>
                    )}
                    <button className="btn-secondary" disabled={pluginBusy === plugin.name} onClick={() => void togglePlugin(plugin)}>
                      {pluginBusy === plugin.name ? <Loader2 size={11} className="tool-spinner" /> : null}
                      {plugin.enabled ? 'Disable' : 'Enable'}
                    </button>
                    <button className="icon-btn" title="Remove plugin" aria-label={`Remove ${plugin.name}`} disabled={pluginBusy === plugin.name} onClick={() => void removeInstalledPlugin(plugin)}>
                      <Trash2 size={12} />
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
          {pluginMCPReview && (
            <div className="mcp-bundle-review plugin-mcp-review">
              <div className="mcp-bundle-heading">
                <div className="mcp-bundle-icon"><Wifi size={18} /></div>
                <div>
                  <strong>Connector review · {pluginMCPReview.plugin}</strong>
                  <span>{pluginMCPReview.servers.length} definition{pluginMCPReview.servers.length === 1 ? '' : 's'} · nothing is running</span>
                </div>
                <button
                  className="icon-btn"
                  title="Close connector review"
                  disabled={pluginBusy !== null}
                  onClick={() => setPluginMCPReview(null)}
                >
                  <Trash2 size={12} />
                </button>
              </div>
              {(pluginMCPReview.warnings || []).map((warning, index) => (
                <div key={`${warning}-${index}`} className="mcp-bundle-warning">
                  <AlertTriangle size={12} /><span>{warning}</span>
                </div>
              ))}
              {pluginMCPReview.servers.length === 0 && (
                <p className="settings-empty">The plugin declares no connector entries.</p>
              )}
              <div className="plugin-mcp-server-list">
                {pluginMCPReview.servers.map((server) => {
                  const envKeys = Object.keys(server.env || {}).sort()
                  const headerKeys = Object.keys(server.headers || {}).sort()
                  const busyKey = `mcp-import:${server.sourceName}`
                  return (
                    <div key={server.sourceName} className={`plugin-mcp-server ${server.importable ? '' : 'unsupported'}`}>
                      <div className="plugin-row-title">
                        <strong>{server.sourceName}</strong>
                        <span className="mcp-status">{server.transport}</span>
                        <span className="mcp-status">imports as {server.suggestedName}</span>
                        {server.existingServer && <span className="mcp-status fail">replaces existing</span>}
                      </div>
                      <code className="mcp-command">
                        {server.transport === 'http'
                          ? server.url
                          : `${server.command || ''}${server.args?.length ? ` ${formatCommandArgs(server.args)}` : ''}`}
                      </code>
                      <div className="plugin-component-summary compact">
                        {server.authType && <span>auth: {server.authType}</span>}
                        {server.oauthClientID && <span>public client ID declared</span>}
                        {envKeys.length > 0 && <span>env keys: {envKeys.join(', ')}</span>}
                        {headerKeys.length > 0 && <span>header names: {headerKeys.join(', ')}</span>}
                        {!server.importable && <code>manual setup required</code>}
                      </div>
                      {(server.warnings || []).map((warning, index) => (
                        <div key={`${warning}-${index}`} className="mcp-bundle-warning compact">
                          <AlertTriangle size={11} /><span>{warning}</span>
                        </div>
                      ))}
                      <div className="mcp-bundle-actions">
                        <button
                          className="btn-primary"
                          disabled={!server.importable || pluginBusy !== null}
                          onClick={() => void importPluginMCP(server)}
                        >
                          {pluginBusy === busyKey
                            ? <Loader2 size={12} className="tool-spinner" />
                            : <Download size={12} />}
                          {server.existingServer ? 'Replace as disabled' : 'Import disabled'}
                        </button>
                      </div>
                    </div>
                  )
                })}
              </div>
              <span className="settings-field-hint">
                Environment placeholders stay unresolved on disk and are read only when you test or enable the connector. Import never starts a process or contacts a server.
              </span>
            </div>
          )}
          {pluginHookReview && (
            <div className="mcp-bundle-review plugin-hook-review">
              <div className="mcp-bundle-heading">
                <div className="mcp-bundle-icon"><AlertTriangle size={18} /></div>
                <div>
                  <strong>Hook review · {pluginHookReview.plugin}</strong>
                  <span>
                    {pluginHookReview.handlers.length} handler{pluginHookReview.handlers.length === 1 ? '' : 's'}
                    {' · '}{pluginHookReview.path}
                    {' · '}{pluginHookReview.armed ? 'armed' : 'disarmed'}
                  </span>
                </div>
                <button
                  className="icon-btn"
                  title="Close hook review"
                  disabled={pluginBusy !== null}
                  onClick={() => setPluginHookReview(null)}
                >
                  <Trash2 size={12} />
                </button>
              </div>
              <div className="mcp-bundle-warning">
                <AlertTriangle size={12} />
                <span>
                  Armed command hooks run automatically with your OS account and project as their working directory.
                  Hook “allow” decisions never bypass Studio permissions; changed inputs are validated and permission-checked again.
                </span>
              </div>
              {(pluginHookReview.warnings || []).map((warning, index) => (
                <div key={`${warning}-${index}`} className="mcp-bundle-warning">
                  <AlertTriangle size={12} /><span>{warning}</span>
                </div>
              ))}
              {pluginHookReview.handlers.length === 0 && (
                <p className="settings-empty">The hook file declares no handlers.</p>
              )}
              <div className="plugin-mcp-server-list">
                {pluginHookReview.handlers.map((handler, index) => (
                  <div
                    key={`${handler.event}-${handler.matcher || '*'}-${index}`}
                    className={`plugin-mcp-server ${handler.supported ? '' : 'unsupported'}`}
                  >
                    <div className="plugin-row-title">
                      <strong>{handler.event}</strong>
                      <span className="mcp-status">{handler.matcher || '*'}</span>
                      <span className={`mcp-status ${handler.supported ? 'ok' : 'fail'}`}>
                        {handler.supported ? 'supported' : 'not executed'}
                      </span>
                      <span className="mcp-status">{handler.timeout}s</span>
                    </div>
                    <code className="mcp-command">
                      {handler.command || `[${handler.type}]`}
                      {handler.args?.length ? ` ${formatCommandArgs(handler.args)}` : ''}
                    </code>
                    {(handler.warnings || []).map((warning, warningIndex) => (
                      <div key={`${warning}-${warningIndex}`} className="mcp-bundle-warning compact">
                        <AlertTriangle size={11} /><span>{warning}</span>
                      </div>
                    ))}
                  </div>
                ))}
              </div>
              <div className="mcp-bundle-actions">
                <button
                  className={pluginHookReview.armed ? 'btn-secondary' : 'btn-primary'}
                  disabled={
                    pluginBusy !== null ||
                    (!pluginHookReview.armed && !pluginHookReview.handlers.some((handler) => handler.supported)) ||
                    (!pluginHookReview.armed && !plugins.find((plugin) => plugin.name === pluginHookReview.plugin)?.enabled)
                  }
                  onClick={() => void togglePluginHooks()}
                >
                  {pluginBusy === `hook-arm:${pluginHookReview.plugin}`
                    ? <Loader2 size={12} className="tool-spinner" />
                    : <AlertTriangle size={12} />}
                  {pluginHookReview.armed ? 'Disarm hooks' : 'Arm reviewed hooks'}
                </button>
              </div>
              {!pluginHookReview.armed && !plugins.find((plugin) => plugin.name === pluginHookReview.plugin)?.enabled && (
                <span className="settings-field-hint">Enable the plugin before arming its hooks.</span>
              )}
            </div>
          )}
          {pluginsLoading && <p className="settings-hint">Loading plugins…</p>}
          {!pluginsLoading && plugins.length === 0 && !pluginPreview && (
            <p className="settings-empty">No plugins installed.</p>
          )}
        </div>
      </div>

      {/* API Keys */}
      <div className="settings-section" id="settings-connections">
        <h3 className="settings-section-header">
          API Keys
          <span className="settings-header-badge">
            {configuredProviderCount}/{keyProviders.length}
          </span>
        </h3>
        <div className="settings-section-content">
          <div className="api-key-grid">
            {keyProviders.map((p) => {
              const fieldName = KEY_PROVIDER_MAP[p.id]
              if (!fieldName) return null
              const isVisible = visibleKeys[p.id] || false
              const apiKeyValue = local[fieldName] as string
              const persistedCredentialSource = String(settings[fieldName] || '').trim()
                ? 'setting'
                : providerCredentialSources[p.id] || ''
              const isKeyDirty = local[fieldName] !== settings[fieldName]
              const effectiveCredentialSource = isKeyDirty
                ? apiKeyValue.trim() ? 'setting' : ''
                : persistedCredentialSource
              const isConfigured = effectiveCredentialSource !== ''
              const usesEnvironmentKey = effectiveCredentialSource === 'env'
              const health = healthResults[p.id]
              const currentCapability = providerCapabilities[p.id]
              const healthExpired = !!health?.ok && !!health.availableModels?.length && !currentCapability
              const healthVerified = !!health?.ok && !healthExpired
              const keyStatus = healthChecking[p.id]
                ? 'Checking…'
                : healthExpired
                  ? 'Check expired'
                  : healthVerified
                  ? isKeyDirty ? 'Verified · save required' : 'Verified'
                  : health
                    ? 'Check failed'
                    : isKeyDirty
                      ? 'Unsaved'
                        : usesEnvironmentKey
                          ? 'Environment key'
                          : isConfigured
                        ? 'Key saved'
                        : 'Not set'
              return (
                <div className={`api-key-card ${isConfigured ? 'configured' : ''}`} key={p.id}>
                  <div className="api-key-card-header">
                    <span className="api-key-dot" style={{ background: PROVIDER_COLORS[p.id] }} />
                    <span className="api-key-name">{p.name}</span>
                    <span className={`api-key-status ${healthVerified && !isKeyDirty ? 'ok' : health && !health.ok ? 'fail' : isKeyDirty || healthExpired ? 'dirty' : ''}`}>
                      {keyStatus}
                    </span>
                  </div>
                  <div className="password-field">
                    <input
                      type={isVisible ? 'text' : 'password'}
                      value={local[fieldName] as string}
                      onChange={(e) => handleChange(fieldName, e.target.value)}
                      onKeyDown={(event) => {
                        if (event.nativeEvent.isComposing || event.keyCode === 229 || event.key !== 'Enter') return
                        if (healthChecking[p.id] || !isConfigured) return
                        event.preventDefault()
                        void runHealthCheck(p.id, apiKeyValue, usesEnvironmentKey)
                      }}
                      placeholder={usesEnvironmentKey ? `Using $${p.id === 'glm' ? 'GLM_API_KEY' : 'KIMI_API_KEY'}` : 'sk-...'}
                      aria-label={`${p.name} API key`}
                      autoComplete="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      maxLength={500}
                    />
                    <button
                      className="icon-btn password-action password-paste"
                      title="Paste from clipboard"
                      aria-label={`Paste ${p.name} API key from clipboard`}
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
                      className="icon-btn password-action password-toggle"
                      title={isVisible ? 'Hide' : 'Show'}
                      aria-label={`${isVisible ? 'Hide' : 'Show'} ${p.name} API key`}
                      onClick={() => setVisibleKeys((v) => ({ ...v, [p.id]: !v[p.id] }))}
                    >
                      {isVisible ? <EyeOff size={13} /> : <Eye size={13} />}
                    </button>
                  </div>
                  {pasteErrors[p.id] && (
                    <div className="api-key-paste-error">{pasteErrors[p.id]}</div>
                  )}
                  {usesEnvironmentKey && (
                    <div className="api-key-env-source" role="status">
                      Using ${p.id === 'glm' ? 'GLM_API_KEY' : 'KIMI_API_KEY'} from the process environment. The value is never exposed to the interface.
                    </div>
                  )}
                  <div className="api-key-health-row">
                    <button
                      className="btn-secondary api-key-test-btn"
                      onClick={() => runHealthCheck(p.id, apiKeyValue, usesEnvironmentKey)}
                      disabled={healthChecking[p.id] || !isConfigured}
                      title={!isConfigured ? 'Set the API key first' : usesEnvironmentKey ? 'Probe the environment credential + connectivity' : 'Probe the provider to verify the key + connectivity'}
                    >
                      {healthChecking[p.id]
                        ? <><Loader2 size={11} className="tool-spinner" /> Testing…</>
                        : <><Wifi size={11} /> Test connection</>}
                    </button>
                    {health && (
                      <span className={`api-key-health-result ${healthVerified ? 'ok' : 'fail'}`} title={health.endpoint || ''} role="status">
                        {healthVerified ? <CheckCircle size={11} /> : healthExpired ? <AlertTriangle size={11} /> : <AlertCircle size={11} />}
                        {healthExpired ? 'Availability check expired — test again' : health.description || health.error || 'Unknown'}
                      </span>
                    )}
                  </div>
                  {healthVerified && currentCapability && currentCapability.availableModels.length > 0 && (
                    <div className="api-key-models" aria-label={`${p.name} available models`}>
                      <span className="api-key-models-label">Available</span>
                      {currentCapability.availableModels.map((model) => (
                        <code className={model === currentCapability.recommendedModel ? 'recommended' : ''} key={model} title={model}>
                          {formatModelLabel(model)}{model === currentCapability.recommendedModel ? ' · best' : ''}
                        </code>
                      ))}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      </div>

      {/* MCP connectors */}
      <div className="settings-section" id="mcp-connectors">
        <h3 className="settings-section-header">
          <Sparkle size={14} /> MCP Connectors
          <span className="settings-header-badge">{mcpServers.filter((s) => s.enabled).length}/{mcpServers.length}</span>
        </h3>
        <div className="settings-section-content">
          <p className="settings-hint">
            Local stdio, one-click MCP Bundles, and remote Streamable HTTP servers. Enabled connectors expose tools to GLM and Kimi on the next chat turn. Remote connectors negotiate MCP 2026 with automatic legacy fallback and support manual headers or OAuth 2.1/PKCE account connection.
          </p>

          {mcpError && <div className="snippet-form-error"><AlertCircle size={12} /> {mcpError}</div>}

          <div className="mcp-bundle-launch">
            <button className="btn-secondary" disabled={mcpBundleBusy} onClick={() => void selectMCPBundle()}>
              {mcpBundleBusy && !mcpBundle ? <Loader2 size={12} className="tool-spinner" /> : <Archive size={12} />}
              Install .mcpb…
            </button>
            <span>Review manifest permissions and configuration before local code is installed.</span>
          </div>

          {mcpBundle && (
            <div className="mcp-bundle-review">
              <div className="mcp-bundle-heading">
                <div className="mcp-bundle-icon"><Archive size={18} /></div>
                <div>
                  <strong>{mcpBundle.displayName}</strong>
                  <span>{mcpBundle.name} · v{mcpBundle.version} · {mcpBundle.serverType} · by {mcpBundle.author}</span>
                </div>
                <button className="icon-btn" title="Cancel installation" aria-label="Cancel connector installation" disabled={mcpBundleBusy} onClick={() => setMcpBundle(null)}>
                  <Trash2 size={12} />
                </button>
              </div>
              <p className="mcp-bundle-description">{mcpBundle.description}</p>
              {(mcpBundle.tools?.length || 0) > 0 && (
                <div className="mcp-bundle-tools">
                  <span>Declared tools</span>
                  {(mcpBundle.tools || []).map((tool) => <code key={tool}>{tool}</code>)}
                </div>
              )}
              {(mcpBundle.warnings || []).map((warning, index) => (
                <div key={`${warning}-${index}`} className={`mcp-bundle-warning ${mcpBundle.existingServer && index === (mcpBundle.warnings || []).length - 1 ? 'replace' : ''}`}>
                  <AlertTriangle size={12} />
                  <span>{warning}</span>
                </div>
              ))}
              {(mcpBundle.configFields || []).length > 0 && (
                <div className="mcp-bundle-fields">
                  {(mcpBundle.configFields || []).map((field) => (
                    <div key={field.key} className={`settings-field ${field.type === 'boolean' ? 'mcp-bundle-bool' : ''}`}>
                      {field.type === 'boolean' ? (
                        <label className="settings-toggle-row">
                          <input
                            type="checkbox"
                            checked={Boolean(mcpBundleValues[field.key])}
                            onChange={(e) => setMcpBundleValues((values) => ({ ...values, [field.key]: e.target.checked }))}
                          />
                          <span>{field.title}{field.required ? ' *' : ''}</span>
                        </label>
                      ) : (
                        <>
                          <label>{field.title}{field.required ? ' *' : ''}</label>
                          <div className="mcp-bundle-input-row">
                            {field.multiple ? (
                              <textarea
                                value={String(mcpBundleValues[field.key] || '')}
                                onChange={(e) => setMcpBundleValues((values) => ({ ...values, [field.key]: e.target.value }))}
                                placeholder="One absolute path per line"
                                rows={3}
                                spellCheck={false}
                              />
                            ) : (
                              <input
                                type={field.sensitive ? 'password' : field.type === 'number' ? 'number' : 'text'}
                                value={String(mcpBundleValues[field.key] ?? '')}
                                min={field.min}
                                max={field.max}
                                onChange={(e) => setMcpBundleValues((values) => ({ ...values, [field.key]: e.target.value }))}
                                spellCheck={false}
                                autoComplete={field.sensitive ? 'off' : undefined}
                              />
                            )}
                            {(field.type === 'directory' || field.type === 'file') && (
                              <button className="btn-secondary" type="button" onClick={() => void browseMCPBundleField(field)}>
                                <FolderOpen size={11} /> Browse
                              </button>
                            )}
                          </div>
                        </>
                      )}
                      {field.description && <span className="settings-field-hint">{field.description}</span>}
                    </div>
                  ))}
                </div>
              )}
              <label className="settings-toggle-row mcp-bundle-enable">
                <input type="checkbox" checked={mcpBundleEnable} onChange={(e) => setMcpBundleEnable(e.target.checked)} />
                <span>Enable immediately for GLM/Kimi</span>
              </label>
              <span className="settings-field-hint">Recommended: leave disabled, install, then use Test before enabling.</span>
              <div className="mcp-bundle-actions">
                <button className="btn-secondary" disabled={mcpBundleBusy} onClick={() => setMcpBundle(null)}>Cancel</button>
                <button className="btn-primary" disabled={mcpBundleBusy} onClick={() => void installSelectedMCPBundle()}>
                  {mcpBundleBusy ? <Loader2 size={12} className="tool-spinner" /> : <Archive size={12} />}
                  {mcpBundle.existingServer ? 'Replace connector' : 'Install extension'}
                </button>
              </div>
            </div>
          )}

          {mcpServers.length > 0 && (
            <div className="mcp-server-list">
              {mcpServers.map((server, index) => {
                const result = mcpResults[server.name]
                const statusError = result?.error || server.error
                const oauth = server.transport === 'http' && server.authType === 'oauth'
                return (
                  <div key={`${server.name}-${index}`} className={`mcp-server-row ${server.enabled ? 'enabled' : ''}`}>
                    <div className="mcp-server-main">
                      <div className="mcp-server-title">
                        <span className={`mcp-dot ${server.enabled ? 'on' : ''}`} />
                        <strong>{server.name || '(unnamed connector)'}</strong>
                        <span className={`mcp-transport-badge ${server.transport}`}>{server.transport === 'http' ? 'REMOTE' : 'LOCAL'}</span>
                        {oauth && (
                          <span
                            className={`mcp-status ${server.authorized ? 'ok' : 'fail'}`}
                            title={server.authorizationError || (server.authorizationExpiresAt
                              ? `Access token expiry: ${new Date(server.authorizationExpiresAt * 1000).toLocaleString()}`
                              : 'OAuth tokens are held in OS-protected storage')}
                          >
                            {server.authorized ? 'OAuth connected' : server.authorizationError || 'OAuth required'}
                          </span>
                        )}
                        <span className="mcp-status">{server.enabled ? 'enabled' : 'disabled'}</span>
                        {(result || statusError) && (
                          <span className={`mcp-status ${statusError ? 'fail' : 'ok'}`}>
                            {statusError || `${result?.toolCount || 0} tools`}
                          </span>
                        )}
                      </div>
                      <code className="mcp-command">
                        {server.transport === 'http'
                          ? server.url
                          : `${server.command}${server.args?.length ? ` ${formatCommandArgs(server.args)}` : ''}`}
                      </code>
                    </div>
                    <div className="mcp-server-actions">
                      {oauth && (
                        <button
                          className="btn-secondary"
                          disabled={mcpAuthorizing === server.name || !!server.error}
                          onClick={() => server.authorized
                            ? void disconnectMCPServerOAuth(server.name)
                            : void authorizeMCPServer(server.name)}
                        >
                          {mcpAuthorizing === server.name
                            ? <><Loader2 size={11} className="tool-spinner" /> Waiting…</>
                            : server.authorized
                              ? <><LogOut size={11} /> Disconnect</>
                              : <><LogIn size={11} /> Connect account</>}
                        </button>
                      )}
                      <button className="btn-secondary" disabled={!!server.error || mcpBusy === server.name} onClick={() => toggleMCPServer(server)}>
                        {server.enabled ? 'Disable' : 'Enable'}
                      </button>
                      <button className="btn-secondary" disabled={!!server.error || (oauth && !server.authorized) || mcpTesting === server.name} onClick={() => testMCPConfig(server, server.name)}>
                        {mcpTesting === server.name ? <><Loader2 size={11} className="tool-spinner" /> Testing…</> : <><Wifi size={11} /> Test</>}
                      </button>
                      <button
                        className="icon-btn"
                        title="Edit in form"
                        onClick={() => setMcpForm({
                          name: server.name,
                          transport: server.transport,
                          command: server.command,
                          args: formatCommandArgs(server.args),
                          env: formatEnvLines(server.env),
                          url: server.url || '',
                          headers: formatHeaderLines(server.headers),
                          authType: server.authType === 'oauth' ? 'oauth' : 'headers',
                          oauthClientID: server.oauthClientID || '',
                        })}
                      >
                        <FileText size={12} />
                      </button>
                      <button className="icon-btn" title="Remove connector" aria-label={`Remove ${server.name}`} disabled={mcpBusy === server.name} onClick={() => deleteMCPServer(server.name)}>
                        <Trash2 size={12} />
                      </button>
                    </div>
                  </div>
                )
              })}
            </div>
          )}

          {mcpLoading && <p className="settings-hint">Loading MCP connectors…</p>}

          <div className="mcp-form">
            <div className="mcp-transport-switch" role="group" aria-label="MCP transport">
              <button
                type="button"
                className={mcpForm.transport === 'stdio' ? 'active' : ''}
                onClick={() => setMcpForm((f) => ({ ...f, transport: 'stdio' }))}
              >
                Local stdio
              </button>
              <button
                type="button"
                className={mcpForm.transport === 'http' ? 'active' : ''}
                onClick={() => setMcpForm((f) => ({ ...f, transport: 'http' }))}
              >
                Remote HTTP
              </button>
            </div>
            <div className="settings-field">
              <label>Name</label>
              <input
                type="text"
                value={mcpForm.name}
                onChange={(e) => setMcpForm((f) => ({ ...f, name: e.target.value }))}
                placeholder="filesystem"
                maxLength={50}
              />
            </div>
            {mcpForm.transport === 'stdio' ? (
              <>
                <div className="settings-field">
                  <label>Command</label>
                  <input
                    type="text"
                    value={mcpForm.command}
                    onChange={(e) => setMcpForm((f) => ({ ...f, command: e.target.value }))}
                    placeholder="/path/to/server or npx"
                    spellCheck={false}
                    maxLength={500}
                  />
                </div>
                <div className="settings-field">
                  <label>Args</label>
                  <input
                    type="text"
                    value={mcpForm.args}
                    onChange={(e) => setMcpForm((f) => ({ ...f, args: e.target.value }))}
                    placeholder='-y @modelcontextprotocol/server-filesystem "/Users/me/project"'
                    spellCheck={false}
                    maxLength={2000}
                  />
                </div>
                <div className="settings-field mcp-env-field">
                  <label>Environment</label>
                  <textarea
                    value={mcpForm.env}
                    onChange={(e) => setMcpForm((f) => ({ ...f, env: e.target.value }))}
                    placeholder={"KEY=value\n# one variable per line"}
                    spellCheck={false}
                    rows={3}
                    maxLength={4000}
                  />
                </div>
              </>
            ) : (
              <>
                <div className="settings-field">
                  <label>Streamable HTTP endpoint</label>
                  <input
                    type="url"
                    value={mcpForm.url}
                    onChange={(e) => setMcpForm((f) => ({ ...f, url: e.target.value }))}
                    placeholder="https://mcp.example.com/mcp"
                    spellCheck={false}
                    maxLength={4096}
                  />
                  <span className="settings-field-hint">HTTPS is required except for localhost. Redirects are blocked to protect credentials.</span>
                </div>
                <div className="settings-field">
                  <label>Authentication</label>
                  <select
                    value={mcpForm.authType}
                    onChange={(e) => setMcpForm((f) => ({ ...f, authType: e.target.value === 'oauth' ? 'oauth' : 'headers' }))}
                  >
                    <option value="headers">Manual headers / API token</option>
                    <option value="oauth">OAuth 2.1 + PKCE</option>
                  </select>
                  <span className="settings-field-hint">
                    {mcpForm.authType === 'oauth'
                      ? 'Save first, then use Connect account. Discovery follows MCP protected-resource metadata and opens the system browser.'
                      : 'Use this for a provider-issued API key or bearer token.'}
                  </span>
                </div>
                {mcpForm.authType === 'oauth' && (
                  <div className="settings-field">
                    <label>OAuth public client ID <span className="optional-label">optional</span></label>
                    <input
                      type="text"
                      value={mcpForm.oauthClientID}
                      onChange={(e) => setMcpForm((f) => ({ ...f, oauthClientID: e.target.value }))}
                      placeholder="Leave empty to use dynamic client registration"
                      spellCheck={false}
                      autoComplete="off"
                      maxLength={2048}
                    />
                    <span className="settings-field-hint">No client secret is accepted. Gokin is a public native client and requires S256 PKCE.</span>
                    <span className="settings-field-hint">OAuth sessions are machine-bound and are not transferred by Gokin backups; reconnect after migration.</span>
                  </div>
                )}
                <div className="settings-field mcp-env-field">
                  <label>{mcpForm.authType === 'oauth' ? 'Additional HTTP headers' : 'HTTP headers'}</label>
                  <textarea
                    value={mcpForm.headers}
                    onChange={(e) => setMcpForm((f) => ({ ...f, headers: e.target.value }))}
                    placeholder={mcpForm.authType === 'oauth'
                      ? "X-Tenant=example\n# Authorization is managed securely"
                      : "Authorization=Bearer <token>\n# one header per line"}
                    spellCheck={false}
                    rows={3}
                    maxLength={20000}
                  />
                  <span className="settings-field-hint">
                    {mcpForm.authType === 'oauth'
                      ? 'Authorization headers are rejected here. OAuth tokens stay in macOS Keychain or Windows DPAPI storage.'
                      : 'Stored locally in the protected connector file. Tokens are never placed in URLs or logs.'}
                  </span>
                </div>
              </>
            )}
            {mcpResults.form && (
              <div className={`mcp-form-result ${mcpResults.form.error ? 'fail' : 'ok'}`}>
                {mcpResults.form.error ? <AlertCircle size={12} /> : <CheckCircle size={12} />}
                {mcpResults.form.error || `${mcpResults.form.toolCount || 0} tools discovered`}
              </div>
            )}
            <div className="mcp-form-actions">
              <button
                className="btn-secondary"
                disabled={!mcpForm.name.trim() || !(mcpForm.transport === 'http' ? mcpForm.url.trim() : mcpForm.command.trim()) ||
                  (mcpForm.transport === 'http' && mcpForm.authType === 'oauth') || mcpTesting === 'form'}
                title={mcpForm.transport === 'http' && mcpForm.authType === 'oauth' ? 'Save and connect the OAuth account before testing' : undefined}
                onClick={testMCPFromForm}
              >
                {mcpTesting === 'form' ? <><Loader2 size={11} className="tool-spinner" /> Testing…</> : <><Wifi size={11} /> Test</>}
              </button>
              <button
                className="btn-primary"
                disabled={!mcpForm.name.trim() || !(mcpForm.transport === 'http' ? mcpForm.url.trim() : mcpForm.command.trim()) || mcpBusy === 'form'}
                onClick={saveMCPFromForm}
              >
                {mcpBusy === 'form' ? <><Loader2 size={12} className="tool-spinner" /> Saving…</> : <><Plus size={12} /> Add / update connector</>}
              </button>
            </div>
          </div>
        </div>
      </div>

      {/* Backup & Restore (iter 750+) */}
      <div className="settings-section" id="settings-data">
        <h3 className="settings-section-header"><Archive size={14} /> Backup & Restore</h3>
        <div className="settings-section-content" aria-busy={dataOperationBusy || restoreFileReading !== null}>
          <p className="settings-hint">
            One-click snapshot of all your data: config, projects, chat history, drafts, pins, memory, snippets, and templates. Useful before upgrades, for machine migration, or for troubleshooting. Restore puts your current data aside as a pre-import backup before overwriting. Manual archives and file imports are limited to 100 MB so every export remains restorable without unbounded WebView memory use.
          </p>
          <div className="backup-actions">
            <button className="btn-secondary" onClick={handleBackup} disabled={dataOperationBusy}>
              {backupBusy ? <Loader2 size={13} className="spin" /> : <Download size={13} />}
              {backupBusy ? 'Backing up…' : 'Back up all data…'}
            </button>
            <button
              type="button"
              className="btn-secondary"
              onClick={handleChooseRestore}
              disabled={dataOperationBusy}
              aria-controls="settings-restore-file"
            >
              {restoreSelectionBusy || restoreFileReading ? <Loader2 size={13} className="spin" /> : <Upload size={13} />}
              {restoreSelectionBusy ? 'Selecting backup…' : restoreFileReading ? 'Reading backup…' : 'Restore from backup…'}
            </button>
            <input
              ref={restoreFileInputRef}
              id="settings-restore-file"
              type="file"
              accept=".tar.gz,.gz,application/gzip"
              hidden
              onChange={(e) => {
                const f = e.target.files?.[0]
                if (f) handleRestoreFile(f)
                // Reset so re-selecting the same file works again.
                e.target.value = ''
              }}
              disabled={dataOperationBusy}
            />
          </div>
          {(dataOperationLabel || restoreFileReading) && (
            <div className="settings-hint" role="status" aria-live="polite" aria-atomic="true" style={{ marginTop: 10 }}>
              {dataOperationLabel || `Reading ${restoreFileReading} before confirmation…`}
            </div>
          )}
          {backupSuccess && (
            <div className="settings-toast success" role="status" aria-live="polite" aria-atomic="true" style={{ marginTop: 10, whiteSpace: 'pre-wrap', alignItems: 'flex-start' }}>
              <CheckCircle size={13} /> <span style={{ flex: 1 }}>{backupSuccess}</span>
            </div>
          )}
          {backupError && <div className="settings-toast error" role="alert" style={{ marginTop: 10 }}><AlertCircle size={13} /> {backupError}</div>}
          {restoreError && <div className="settings-toast error" role="alert" style={{ marginTop: 10 }}><AlertCircle size={13} /> {restoreError}</div>}
          {restoreSuccess && (
            <div className="settings-toast success" role="status" aria-live="polite" aria-atomic="true" style={{ marginTop: 10, whiteSpace: 'pre-wrap', alignItems: 'flex-start' }}>
              <CheckCircle size={13} />
              <span style={{ flex: 1 }}>{restoreSuccess}</span>
            </div>
          )}
          {confirmRestore && (
            <div className="reset-prefs-confirm" role="group" aria-label={`Confirm restore from ${confirmRestore.filename}`} style={{ marginTop: 10 }}>
              <span className="reset-prefs-confirm-text" role="status" aria-live="polite">
                Restore from <b>{confirmRestore.filename}</b> ({(confirmRestore.size / 1024).toFixed(1)} KB)? This will replace your current data. The existing data will be moved aside as a pre-import backup, but a restart will be required.
              </span>
              <div className="reset-prefs-actions">
                <button className="btn-secondary" onClick={handleCancelRestore} disabled={dataOperationBusy}>Cancel</button>
                <button className="btn-danger" onClick={handleConfirmRestore} disabled={dataOperationBusy}>
                  {restoreBusy ? <Loader2 size={13} className="spin" /> : null}
                  Restore (replaces current data)
                </button>
              </div>
            </div>
          )}
          {/* iter 810+: pre-import backups list */}
          <div className="backups-list-section" aria-busy={backupsLoading || backupDeleteBusy || restoreBackupBusy}>
            <div className="backups-list-header">
              <span className="backups-list-title">
                <History size={12} /> Rollback snapshots
              </span>
              <button
                className="btn-icon-tiny"
                onClick={refreshBackupsList}
                title="Refresh list"
                aria-label="Refresh rollback snapshots"
                disabled={backupsLoading || dataOperationBusy}
              >
                {backupsLoading ? <Loader2 size={12} className="spin" /> : <RotateCcw size={12} />}
              </button>
            </div>
            <p className="settings-hint" style={{ marginBottom: 8 }}>
              Snapshots created automatically before each Restore. Useful as undo points; auto-cleanup removes them after 90 days.
            </p>
            {restoreBackupSuccess && (
              <div className="settings-toast success" role="status" aria-live="polite" aria-atomic="true" style={{ marginBottom: 10, whiteSpace: 'pre-wrap', alignItems: 'flex-start' }}>
                <CheckCircle size={13} /> <span style={{ flex: 1 }}>{restoreBackupSuccess}</span>
              </div>
            )}
            {backupsError && (
              <div className="settings-toast error" role="alert" style={{ marginBottom: 10 }}>
                <AlertCircle size={13} /> {backupsError}
              </div>
            )}
            {backupsLoading && <div className="backups-empty" role="status" aria-live="polite">Loading rollback snapshots…</div>}
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
                      <button className="btn-secondary" onClick={() => setConfirmDeleteBackup(null)} disabled={dataOperationBusy}>Cancel</button>
                      <button className="btn-danger" onClick={() => handleBackupDelete(b.name)} disabled={dataOperationBusy}>
                        {backupDeleteBusy ? <Loader2 size={12} className="spin" /> : null}
                        {backupDeleteBusy ? 'Deleting…' : 'Confirm'}
                      </button>
                    </>
                  ) : confirmRestoreBackup?.name === b.name ? (
                    <>
                      <button className="btn-secondary" onClick={() => setConfirmRestoreBackup(null)} disabled={dataOperationBusy}>Cancel</button>
                      <button className="btn-danger" onClick={() => handleBackupRestore(b.name)} disabled={dataOperationBusy}>
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
                        aria-label={`Restore rollback snapshot ${b.name}`}
                        disabled={dataOperationBusy}
                      >
                        <Upload size={11} />
                      </button>
                      <button
                        className="btn-icon-tiny btn-icon-danger"
                        onClick={() => setConfirmDeleteBackup(b.name)}
                        title="Delete this snapshot"
                        aria-label={`Delete rollback snapshot ${b.name}`}
                        disabled={dataOperationBusy}
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
              Once per 24h, remove replay logs older than 30 days, rollback backups and safely disposable completed delegation chats older than 90 days. Active, changed, pinned, drafted, subsequently used, or archived-project delegation chats are always retained. Disable if you want full manual control via the "Clean up" button in Diagnostics. Save to apply.
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
          <div className="backups-list-section" aria-busy={autoLoading || autoDeleteBusy || autoRestoreBusy}>
            <div className="backups-list-header">
              <span className="backups-list-title">
                <Archive size={12} /> Auto-backups
              </span>
              <div style={{ display: 'flex', gap: 4 }}>
                <button
                  className="btn-icon-tiny"
                  onClick={() => { OpenAutoBackupsDir().catch((e) => setAutoError(String(e?.message || e))) }}
                  title="Reveal backups folder in OS file manager"
                  aria-label="Reveal automatic backups folder"
                  disabled={dataOperationBusy}
                >
                  <FolderOpen size={12} />
                </button>
                <button
                  className="btn-icon-tiny"
                  onClick={refreshAutoBackupsList}
                  title="Refresh list"
                  aria-label="Refresh automatic backups"
                  disabled={autoLoading || dataOperationBusy}
                >
                  {autoLoading ? <Loader2 size={12} className="spin" /> : <RotateCcw size={12} />}
                </button>
              </div>
            </div>
            <p className="settings-hint" style={{ marginBottom: 8 }}>
              Daily snapshots from the auto-backup feature above. Click a snapshot to restore (current data → safety backup).
            </p>
            {autoRestoreSuccess && (
              <div className="settings-toast success" role="status" aria-live="polite" aria-atomic="true" style={{ marginBottom: 10, whiteSpace: 'pre-wrap', alignItems: 'flex-start' }}>
                <CheckCircle size={13} /> <span style={{ flex: 1 }}>{autoRestoreSuccess}</span>
              </div>
            )}
            {autoError && (
              <div className="settings-toast error" role="alert" style={{ marginBottom: 10 }}>
                <AlertCircle size={13} /> {autoError}
              </div>
            )}
            {autoLoading && <div className="backups-empty" role="status" aria-live="polite">Loading automatic backups…</div>}
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
                      <button className="btn-secondary" onClick={() => setConfirmDeleteAuto(null)} disabled={dataOperationBusy}>Cancel</button>
                      <button className="btn-danger" onClick={() => handleAutoDelete(b.filename)} disabled={dataOperationBusy}>
                        {autoDeleteBusy ? <Loader2 size={12} className="spin" /> : null}
                        {autoDeleteBusy ? 'Deleting…' : 'Confirm'}
                      </button>
                    </>
                  ) : confirmRestoreAuto?.filename === b.filename ? (
                    <>
                      <button className="btn-secondary" onClick={() => setConfirmRestoreAuto(null)} disabled={dataOperationBusy}>Cancel</button>
                      <button className="btn-danger" onClick={() => handleAutoRestore(b.filename)} disabled={dataOperationBusy}>
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
                        aria-label={`Restore automatic backup ${b.filename}`}
                        disabled={dataOperationBusy}
                      >
                        <Upload size={11} />
                      </button>
                      <button
                        className="btn-icon-tiny btn-icon-danger"
                        onClick={() => setConfirmDeleteAuto(b.filename)}
                        title="Delete this snapshot"
                        aria-label={`Delete automatic backup ${b.filename}`}
                        disabled={dataOperationBusy}
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
            Clear all frontend-only preferences (toasts, sound, mute, quiet mode, panel sizes, budget alert thresholds, and last workspace location). Backend state — projects, sessions, history, snippets — is untouched. Useful when recovering from bad state after upgrades or when you want a clean baseline.
          </p>
          {resetFeedback && <div className="settings-toast success">{resetFeedback}</div>}
          {!confirmReset ? (
            <button className="btn-secondary" onClick={() => setConfirmReset(true)}>
              <RotateCcw size={12} /> Reset all preferences…
            </button>
          ) : (
            <div className="reset-prefs-confirm">
              <span className="reset-prefs-confirm-text">This will reset notifications, panel sizes, collapsed panels, last workspace location, mute, quiet-mode, and budget-alert state across every project. Continue?</span>
              <div className="reset-prefs-actions">
                <button className="btn-secondary" onClick={() => setConfirmReset(false)}>Cancel</button>
                <button className="btn-danger" onClick={handleResetPreferences}>Reset</button>
              </div>
            </div>
          )}
        </div>
      </div>

      {/* About & Shortcuts */}
      <div className="settings-section" id="settings-about">
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
              <span className="about-stat-value">v{buildInfo?.version || '2.0.0'}</span>
              <span className="about-stat-label">Version</span>
            </div>
          </div>

          {buildInfo && (
            <div className="about-build-info">
              <span>{buildInfo.os}/{buildInfo.arch} · {buildInfo.goVersion} · {buildInfo.numCPU} CPUs</span>
            </div>
          )}

          <div className={`about-update-card ${updateStatus?.available ? 'available' : ''}`}>
            <div className="about-update-main">
              <span className="about-update-icon">
                {updateChecking
                  ? <Loader2 size={15} className="spin" />
                  : updateStatus?.available
                    ? <Download size={15} />
                    : <CheckCircle size={15} />}
              </span>
              <span className="about-update-copy">
                <strong>
                  {updateChecking
                    ? 'Checking for updates…'
                    : updateStatus?.available
                      ? `Gokin Studio ${updateStatus.latestVersion} is available`
                      : updateStatus?.checkedAt
                        ? updateStatus.latestVersion
                          ? 'Gokin Studio is up to date'
                          : 'No published releases yet'
                        : 'Desktop updates'}
                </strong>
                <small>
                  {updateStatus?.checkedAt
                    ? `Last checked ${new Date(updateStatus.checkedAt).toLocaleString()}`
                    : 'Compare this build with the latest stable GitHub release.'}
                </small>
              </span>
            </div>
            <div className="about-update-actions">
              {updateStatus?.available && updateStatus.releaseURL && (
                <button type="button" className="btn-primary" onClick={() => BrowserOpenURL(updateStatus.releaseURL!)}>
                  <Download size={12} /> Release page
                </button>
              )}
              <button type="button" className="btn-secondary" onClick={() => void checkForDesktopUpdate()} disabled={updateChecking}>
                {updateChecking ? <Loader2 size={12} className="spin" /> : <RotateCcw size={12} />}
                Check now
              </button>
            </div>
            {updateError && <div className="about-update-error" role="alert"><AlertTriangle size={12} /> {updateError}</div>}
            <label className="settings-toggle-row about-update-toggle">
              <input
                type="checkbox"
                checked={!local.autoUpdateCheckDisabled}
                onChange={(event) => setLocal((previous) => ({ ...previous, autoUpdateCheckDisabled: !event.target.checked }))}
              />
              <span>Check once every 24 hours</span>
            </label>
            <p className="about-update-note">Checks GitHub only after Studio opens. Updates are never downloaded or installed automatically.</p>
          </div>

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
              <span className="shortcut-key">Ctrl/Cmd+,</span><span>Settings</span>
              <span className="shortcut-key">Ctrl+4</span><span>Artifacts</span>
              <span className="shortcut-key">Ctrl/Cmd+[ / ]</span><span>Back / forward</span>
              <span className="shortcut-key">Ctrl+B</span><span>Toggle sidebar</span>
              <span className="shortcut-key">Ctrl+J</span><span>Toggle context panel</span>
              <span className="shortcut-key">Enter</span><span>Send message</span>
              <span className="shortcut-key">Shift+Enter</span><span>New line</span>
              <span className="shortcut-key">Cmd/Ctrl+Shift+D</span><span>Toggle diff pane</span>
              <span className="shortcut-key">Double Option</span><span>Global Quick Entry (macOS, when enabled)</span>
              <span className="shortcut-key">Caps Lock</span><span>Global voice dictation (macOS, when enabled)</span>
              <span className="shortcut-key">Ctrl+L</span><span>Clear chat</span>
              <span className="shortcut-key">Cmd/Ctrl+N</span><span>New chat session</span>
              <span className="shortcut-key">Ctrl+T</span><span>New chat session (compatibility)</span>
              <span className="shortcut-key">Ctrl/Cmd+W</span><span>Close (archive) current chat</span>
              <span className="shortcut-key">← / → on tab</span><span>Switch adjacent chat</span>
              <span className="shortcut-key">Ctrl+K</span><span>Command palette</span>
              <span className="shortcut-key">Ctrl+P</span><span>File picker</span>
              <span className="shortcut-key">Arrows in Files</span><span>Navigate project tree</span>
              <span className="shortcut-key">Ctrl+F</span><span>Search current Chat / Artifacts</span>
              <span className="shortcut-key">Ctrl+Shift+F</span><span>Search across all sessions</span>
              <span className="shortcut-key">Ctrl+Shift+A</span><span>Agent activity timeline</span>
              <span className="shortcut-key">Cmd/Ctrl+Shift+B</span><span>Toggle Browser / Preview pane</span>
              <span className="shortcut-key">Cmd/Ctrl+Shift+P</span><span>Toggle Browser / Preview (compatibility)</span>
              <span className="shortcut-key">Ctrl+Shift+G</span><span>Search projects in sidebar</span>
              <span className="shortcut-key">Ctrl+/  or  ?</span><span>Help & shortcuts (in-app)</span>
              <span className="shortcut-key">Ctrl+M</span><span>Quick model switcher</span>
              <span className="shortcut-key">Ctrl+`</span><span>Toggle terminal</span>
              <span className="shortcut-key">Ctrl+Tab / Ctrl+Shift+Tab</span><span>Next / previous chat session</span>
              <span className="shortcut-key">Cmd/Ctrl+Shift+] / [</span><span>Next / previous chat session</span>
              <span className="shortcut-key">Ctrl+PgUp/Dn</span><span>Cycle sessions (compatibility)</span>
              <span className="shortcut-key">Escape</span><span>Close dialogs / stop agent</span>
              <span className="shortcut-key">j / k</span><span>Navigate messages</span>
            </div>
          </div>
        </div>
      </div>

      {/* Diagnostics modal (iter 700+) */}
      {showDiag && (
        <div className="dispatch-backdrop diag-backdrop" onClick={() => { if (!cleanupBusy) setShowDiag(false) }}>
          <div className="dispatch-modal diag-modal" role="dialog" aria-modal="true" aria-label="Diagnostics" aria-busy={diagLoading || cleanupBusy} onClick={(e) => e.stopPropagation()}>
            <div className="dispatch-modal-header">
              <Activity size={14} />
              <span>Diagnostics</span>
            </div>
            <div className="dispatch-modal-body">
              {diagLoading && (
                <div className="diag-loading" role="status" aria-live="polite">
                  <Loader2 size={14} className="spin" /> Running health checks…
                </div>
              )}
              {diagError && (
                <div className="diag-error" role="alert">
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
                      aria-label="Reveal config folder"
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
            {dataOperationBusy && (
              <div className="settings-hint" role="status" aria-live="polite" aria-atomic="true" style={{ margin: '0 0 12px' }}>
                {cleanupBusy
                  ? (cleanupPreview ? 'Deleting the reviewed stale data…' : 'Checking for stale data…')
                  : dataOperationLabel}
              </div>
            )}
            {cleanupPreview && (
              <div className="cleanup-preview" role="group" aria-label="Confirm cleanup">
                <Sparkle size={13} />
                <div className="cleanup-preview-body" role="status" aria-live="polite">
                  <strong>
                    {cleanupRemovedCount(cleanupPreview)} item(s) to clean up
                  </strong>
                  {' — '}
                  {(() => {
                    const parts: string[] = []
                    if (cleanupPreview.staleReplaysRemoved > 0) parts.push(`${cleanupPreview.staleReplaysRemoved} stale replay log(s)`)
                    if (cleanupPreview.preImportDirsRemoved > 0) parts.push(`${cleanupPreview.preImportDirsRemoved} old pre-import backup(s)`)
                    if (cleanupPreview.stagingDirsRemoved > 0) parts.push(`${cleanupPreview.stagingDirsRemoved} orphaned staging dir(s)`)
                    if (cleanupPreview.autoBackupsRemoved > 0) parts.push(`${cleanupPreview.autoBackupsRemoved} excess auto-backup(s)`)
                    if (cleanupPreview.delegationRunsRemoved > 0) parts.push(`${cleanupPreview.delegationRunsRemoved} old delegation chat(s)`)
                    if (cleanupPreview.delegationRunsSkipped > 0) parts.push(`${cleanupPreview.delegationRunsSkipped} protected delegation chat(s) kept`)
                    return parts.join(', ')
                  })()}
                  {' '}({formatBytes(cleanupPreview.bytesFreed)})
                  {cleanupPreview.errors?.length > 0 && (
                    <div className="settings-hint">Preview is incomplete: {cleanupPreview.errors[0]}</div>
                  )}
                </div>
                <div className="cleanup-preview-actions">
                  <button className="btn-secondary" onClick={() => setCleanupPreview(null)} disabled={dataOperationBusy}>Cancel</button>
                  <button className="btn-danger" onClick={handleCleanupConfirm} disabled={dataOperationBusy}>
                    {cleanupBusy ? <Loader2 size={12} className="spin" /> : null}
                    Delete
                  </button>
                </div>
              </div>
            )}
            {cleanupResult && !cleanupResult.errors?.length && (
              <div className="settings-toast success" role="status" aria-live="polite" aria-atomic="true" style={{ margin: '0 0 12px' }}>
                <CheckCircle size={13} /> Cleaned up {cleanupRemovedCount(cleanupResult)} item(s), freed {formatBytes(cleanupResult.bytesFreed)}.{cleanupResult.delegationRunsSkipped > 0 ? ` Kept ${cleanupResult.delegationRunsSkipped} protected delegation chat(s).` : ''}
              </div>
            )}
            {cleanupResult?.errors?.length > 0 && (
              <div className="settings-toast error" role="alert" style={{ margin: '0 0 12px' }}>
                <AlertCircle size={13} /> Cleanup removed {cleanupRemovedCount(cleanupResult)} item(s) and freed {formatBytes(cleanupResult.bytesFreed)}, but {cleanupResult.errors.length} operation(s) failed: {cleanupResult.errors[0]}
              </div>
            )}
            {cleanupError && (
              <div className="settings-toast error" role="alert" style={{ margin: '0 0 12px' }}>
                <AlertCircle size={13} /> {cleanupError}
              </div>
            )}
            <div className="dispatch-modal-actions">
              <button className="btn-secondary" onClick={openDiagnostics} disabled={diagLoading || cleanupBusy}>
                <RotateCcw size={14} /> Refresh
              </button>
              <button className="btn-secondary" onClick={() => { setShowDiag(false); openLogs() }} disabled={cleanupBusy}>
                <FileText size={14} /> View logs
              </button>
              <button
                className="btn-secondary"
                onClick={handleCleanupPreview}
                disabled={dataOperationBusy || cleanupPreview !== null}
                title="Find stale replay logs (>7 days), rollback backups and safely disposable delegation chats (>30 days)"
              >
                {cleanupBusy && !cleanupPreview ? <Loader2 size={14} className="spin" /> : <Sparkle size={14} />}
                Clean up
              </button>
              <button className="btn-secondary" onClick={handleCopyDiagnostics} disabled={diagLoading || cleanupBusy || !diagInfo}>
                <Clipboard size={14} /> {diagCopied ? 'Copied!' : 'Copy report'}
              </button>
              <button className="btn-secondary" onClick={() => downloadDiagnostics(diagInfo)} disabled={diagLoading || cleanupBusy || !diagInfo}>
                <Download size={14} /> Save as file
              </button>
              <button className="btn-primary" onClick={() => setShowDiag(false)} disabled={cleanupBusy}>Close</button>
            </div>
          </div>
        </div>
      )}

      {/* Logs viewer modal (iter 710+) */}
      {showLogs && (
        <div className="dispatch-backdrop logs-backdrop" onClick={() => setShowLogs(false)}>
          <div className="dispatch-modal logs-modal" role="dialog" aria-modal="true" aria-label="Application logs" onClick={(e) => e.stopPropagation()}>
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
              {logsActionFeedback && (
                <div
                  className={`logs-action-feedback ${logsActionFeedback.type}`}
                  role={logsActionFeedback.type === 'error' ? 'alert' : 'status'}
                >
                  {logsActionFeedback.type === 'success' ? <CheckCircle size={12} /> : <AlertCircle size={12} />}
                  <span>{logsActionFeedback.text}</span>
                  <button onClick={() => setLogsActionFeedback(null)} aria-label="Dismiss log action status"><X size={11} /></button>
                </div>
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
                  setLogsActionBusy('export')
                  setLogsActionFeedback(null)
                  try {
                    const csv = await ExportLogsCSV()
                    const blob = new Blob([csv], { type: 'text/csv' })
                    downloadBlob(blob, `gokin-studio-logs-${new Date().toISOString().slice(0, 10)}.csv`)
                    setLogsActionFeedback({ type: 'success', text: `Exported ${logs.length} log entr${logs.length === 1 ? 'y' : 'ies'} as CSV.` })
                  } catch (e: any) {
                    console.error('ExportLogsCSV failed:', e)
                    setLogsActionFeedback({ type: 'error', text: `Could not export logs: ${String(e?.message || e || 'unknown error')}` })
                  } finally {
                    setLogsActionBusy(null)
                  }
                }}
                disabled={logsLoading || logs.length === 0 || logsActionBusy !== null}
                title="Download all event log entries as a CSV file"
              >
                {logsActionBusy === 'export' ? <Loader2 size={14} className="spin" /> : <Download size={14} />} Export CSV
              </button>
              <button className="btn-secondary" onClick={() => void handleClearLogs()} disabled={logsLoading || logs.length === 0 || logsActionBusy !== null}>
                {logsActionBusy === 'clear' ? <Loader2 size={14} className="spin" /> : <Trash2 size={14} />} Clear
              </button>
              <button className="btn-primary" onClick={() => setShowLogs(false)}>Close</button>
            </div>
          </div>
        </div>
      )}
      {confirmationDialog}
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
  downloadBlob(blob, `gokin-diagnostics-${new Date().toISOString().slice(0, 10)}.txt`)
}
