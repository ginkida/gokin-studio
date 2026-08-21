import { useCallback, useEffect, useState } from 'react'
import { CheckCircle, ChevronRight, Sparkles, Key, FolderOpen, Loader2, AlertCircle, Wifi, X, Eye, EyeOff } from 'lucide-react'
import {
  UpdateSettings,
  CheckProviderHealth,
  CheckProviderHealthWithKey,
  AddProject,
  BrowseDirectory,
  GetClipboardText,
} from '../../../wailsjs/go/studio/Studio'
import { BrowserOpenURL } from '../../../wailsjs/runtime/runtime'
import { useSettingsStore } from '../../stores/settingsStore'
import { formatProviderModelLabel, getProviderAccountURL } from '../../lib/providerCatalog'
import { formatContextWindow } from '../../lib/modelCapabilities'
import { useProjectStore, ProjectInfo } from '../../stores/projectStore'
import { useConfirmDialog } from '../common/AppDialog'
import { ONBOARDING_DISMISSED_KEY } from '../../lib/onboarding'

// Provider options shown in step 1. Order matters — first one is the default
// recommended for new users (GLM has the lowest barrier to entry since users
// can sign up directly without a waitlist).
const PROVIDER_CHOICES: Array<{ id: string; name: string; keyFormat: string }> = [
  { id: 'glm',  name: 'GLM (Z.AI)', keyFormat: 'API key from Z.AI' },
  { id: 'kimi', name: 'Kimi Code',   keyFormat: 'Kimi Code membership API key' },
]

const PROVIDER_FALLBACK_MODELS: Record<string, string> = {
  glm: 'glm-5.2',
  kimi: 'k3',
}

const STARTER_PROMPTS = [
  {
    title: 'Understand this project',
    prompt: 'Give me a concise orientation to this project: its purpose, architecture, main entry points, and the first files I should read. Do not modify anything.',
  },
  {
    title: 'Find the entry point',
    prompt: 'Find the main entry points in this project and explain the runtime flow from startup to the first user-visible action. Do not modify files.',
  },
  {
    title: 'Review open work',
    prompt: 'Find TODOs, FIXMEs, and unfinished implementations in this project. Group them by impact and suggest what to tackle first. Do not modify files.',
  },
] as const

type ProviderHealthResult = {
  ok: boolean
  error?: string
  description?: string
  availableModels?: string[]
  recommendedModel?: string
}

type ProviderSetupDraft = {
  apiKey: string
  detectedModel: string
  testResult: { ok: boolean; msg: string } | null
}

interface Props {
  onComplete: (project?: ProjectInfo, starterPrompt?: string) => void
  onSkip: () => void
}

// OnboardingWizard guides a fresh-install user through provider setup and
// adding their first project. Shown when no projects exist and the user
// hasn't explicitly skipped before. Three steps:
//   1. Pick a provider + paste API key + optional Test connection
//   2. Add the first project (name + directory)
//   3. Confirmation + handoff
//
// Persistent skip via localStorage so power users importing config from
// another machine don't see it on every launch.
export function OnboardingWizard({ onComplete, onSkip }: Props) {
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const settings = useSettingsStore((s) => s.settings)
  const updateField = useSettingsStore((s) => s.updateField)
  const setProviderCapability = useSettingsStore((s) => s.setProviderCapability)
  const providerCredentialSources = useSettingsStore((s) => s.providerCredentialSources)
  const providers = useSettingsStore((s) => s.providers)
  const providerCapabilities = useSettingsStore((s) => s.providerCapabilities)
  const [step, setStep] = useState<1 | 2 | 3>(1)
  const [provider, setProvider] = useState<string>('glm')
  const [providerDrafts, setProviderDrafts] = useState<Record<string, ProviderSetupDraft>>(() => ({
    glm: {
      apiKey: settings.glmKey || '',
      detectedModel: settings.defaultProvider === 'glm' ? settings.defaultModel : '',
      testResult: null,
    },
    kimi: {
      apiKey: settings.kimiKey || '',
      detectedModel: settings.defaultProvider === 'kimi' ? settings.defaultModel : '',
      testResult: null,
    },
  }))
  const [showApiKey, setShowApiKey] = useState(false)
  const [testing, setTesting] = useState(false)
  const [pasting, setPasting] = useState(false)
  const [keySaving, setKeySaving] = useState(false)
  const [step1Error, setStep1Error] = useState<string | null>(null)

  const [projectName, setProjectName] = useState('')
  const [projectDir, setProjectDir] = useState('')
  const [projectAdding, setProjectAdding] = useState(false)
  const [step2Error, setStep2Error] = useState<string | null>(null)

  const [createdProject, setCreatedProject] = useState<ProjectInfo | null>(null)

  const activeDraft = providerDrafts[provider] || { apiKey: '', detectedModel: '', testResult: null }
  const apiKey = activeDraft.apiKey
  const detectedModel = activeDraft.detectedModel
  const testResult = activeDraft.testResult
  const usesEnvironmentCredential = !apiKey.trim() && providerCredentialSources[provider] === 'env'
  const hasCredential = !!apiKey.trim() || usesEnvironmentCredential
  const environmentVariable = provider === 'kimi' ? 'KIMI_API_KEY' : 'GLM_API_KEY'

  const providerModelOptions = (providerID: string) => {
    const info = providers.find((item) => item.id === providerID)
    const advertised = providerCapabilities[providerID]?.availableModels || []
    const details = info?.modelDetails || []
    if (advertised.length === 0) return details
    return details.filter((model) => advertised.includes(model.id))
  }

  const providerModel = (providerID: string) => {
    const options = providerModelOptions(providerID)
    const preferredID = providerDrafts[providerID]?.detectedModel
      || providerCapabilities[providerID]?.recommendedModel
    return options.find((model) => model.id === preferredID)
      || options.find((model) => model.latest)
      || options.find((model) => model.recommended)
      || options[0]
  }

  const providerTag = (providerID: string) => {
    const model = providerModel(providerID)
    if (!model) {
      return providerID === 'kimi'
        ? 'Latest Kimi coding model · native vision'
        : 'Latest GLM coding model · deep reasoning'
    }
    const strengths = model.inputModalities.includes('image')
      ? 'native vision'
      : model.reasoningControl
        ? `${model.reasoningControl} reasoning`
        : 'agentic coding'
    return `${formatProviderModelLabel(providerID, model.id)} · ${formatContextWindow(model.contextWindow)} context · ${strengths}`
  }

  const updateProviderDraft = (providerID: string, updates: Partial<ProviderSetupDraft>) => {
    setProviderDrafts((current) => ({
      ...current,
      [providerID]: { ...(current[providerID] || { apiKey: '', detectedModel: '', testResult: null }), ...updates },
    }))
  }

  const savedProviderKey = (providerID: string) => providerID === 'kimi' ? settings.kimiKey : settings.glmKey

  const handleSkip = useCallback(async () => {
    if (testing || pasting || keySaving || projectAdding) return
    const hasUnsavedProviderKey = Object.entries(providerDrafts).some(([providerID, draft]) => (
      draft.apiKey.trim() !== (savedProviderKey(providerID) || '').trim()
    ))
    const chosenModel = providerDrafts[provider]?.detectedModel
    const hasUnsavedProviderChoice = provider !== settings.defaultProvider
      || (!!chosenModel && chosenModel !== settings.defaultModel)
    const hasUnfinishedInput = step === 1
      ? hasUnsavedProviderKey || hasUnsavedProviderChoice
      : step === 2 && (hasUnsavedProviderKey || hasUnsavedProviderChoice || projectName.trim().length > 0 || projectDir.trim().length > 0)
    if (hasUnfinishedInput) {
      const accepted = await requestConfirmation({
        title: 'Leave setup?',
        message: step === 1
          ? 'Unsaved GLM/Kimi provider, model, or API key choices will be lost. You can configure them later in Settings.'
          : hasUnsavedProviderKey
            ? 'The selected provider key is saved, but other API key edits and the unfinished project form will be lost.'
            : 'Your provider key is saved, but the project name and directory on this screen have not been added.',
        confirmLabel: 'Skip for now',
        cancelLabel: 'Keep setting up',
      })
      if (!accepted) return
    }
    try { localStorage.setItem(ONBOARDING_DISMISSED_KEY, '1') } catch { /* unavailable */ }
    onSkip()
  }, [keySaving, onSkip, pasting, projectAdding, provider, projectDir, projectName, providerDrafts, requestConfirmation, settings.defaultModel, settings.defaultProvider, settings.glmKey, settings.kimiKey, step, testing])

  // Esc follows the same guarded skip path as the close buttons. A nested
  // confirmation dialog owns its own Escape press and must not reopen itself.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !((e as any).isComposing || e.keyCode === 229)) {
        if (e.target instanceof Element && e.target.closest('.app-dialog-backdrop')) return
        void handleSkip()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [handleSkip])

  const handlePasteKey = async () => {
    if (pasting || testing || keySaving) return
    const targetProvider = provider
    setPasting(true)
    setStep1Error(null)
    try {
      let text = ''
      try {
        const nativeText = await GetClipboardText()
        if (typeof nativeText === 'string') text = nativeText
      } catch { /* fall through to Web clipboard */ }
      if (!text.trim()) {
        try {
          const browserText = await navigator.clipboard.readText()
          if (typeof browserText === 'string') text = browserText
        } catch { /* surfaced below without logging clipboard contents */ }
      }
      if (!text.trim()) {
        setStep1Error('Clipboard is empty or unavailable. Paste the API key manually with Ctrl/Cmd+V.')
        return
      }
      updateProviderDraft(targetProvider, { apiKey: text.trim(), detectedModel: '', testResult: null })
      setProviderCapability(targetProvider, null)
    } finally {
      setPasting(false)
    }
  }

  const probeConnection = async (): Promise<ProviderHealthResult | null> => {
    setTesting(true)
      updateProviderDraft(provider, { testResult: null })
      setStep1Error(null)
    try {
      // A typed key is probed without saving it. Environment credentials are
      // resolved only in Go and never returned to the WebView.
      const info = (usesEnvironmentCredential
        ? await CheckProviderHealth(provider)
        : await CheckProviderHealthWithKey(provider, apiKey.trim())) as ProviderHealthResult
      const recommended = info?.ok && typeof info?.recommendedModel === 'string' ? info.recommendedModel : ''
      const advertised = info?.ok && Array.isArray(info.availableModels) ? info.availableModels : []
      const requestedModel = activeDraft.detectedModel
      const selectedModel = requestedModel && advertised.includes(requestedModel)
        ? requestedModel
        : recommended
      if (info?.ok && Array.isArray(info.availableModels) && info.availableModels.length > 0) {
        setProviderCapability(provider, {
          availableModels: [...info.availableModels],
          recommendedModel: recommended || undefined,
          checkedAt: Date.now(),
        })
      } else {
        setProviderCapability(provider, null)
      }
      const baseMessage = info?.description || info?.error || (info?.ok ? 'Connected.' : 'Check failed.')
      updateProviderDraft(provider, {
        detectedModel: selectedModel,
        testResult: {
          ok: !!info?.ok,
          msg: selectedModel
            ? `${baseMessage} · Selected: ${formatProviderModelLabel(provider, selectedModel)}`
            : baseMessage,
        },
      })
      return info
    } catch (e: any) {
      setProviderCapability(provider, null)
      updateProviderDraft(provider, {
        detectedModel: '',
        testResult: { ok: false, msg: String(e?.message || e || 'probe failed') },
      })
      return null
    } finally {
      setTesting(false)
    }
  }

  const handleStep1Next = async (allowUnverified = false) => {
    setStep1Error(null)
    if (!hasCredential) {
      setStep1Error(`Paste an API key or provide $${environmentVariable} to continue.`)
      return
    }
    setKeySaving(true)
    try {
      let selectedModel = detectedModel
      if (!allowUnverified && !testResult?.ok) {
        const health = await probeConnection()
        if (!health?.ok) {
          setStep1Error('Connection could not be verified. Check the key and network, or continue without verification.')
          return
        }
        const advertised = Array.isArray(health.availableModels) ? health.availableModels : []
        selectedModel = selectedModel && advertised.includes(selectedModel)
          ? selectedModel
          : health.recommendedModel || ''
      }
      const updates: any = { ...settings, defaultProvider: provider }
      selectedModel = selectedModel
        || providers.find((item) => item.id === provider)?.models[0]
        || PROVIDER_FALLBACK_MODELS[provider]
      if (provider === 'glm') {
        if (apiKey.trim()) updates.glmKey = apiKey.trim()
        updates.defaultModel = selectedModel
      }
      // Keep the product default aligned with the Kimi-first model catalog.
      // If an account does not advertise K3, the verified path above selects
      // its recommended eligible model instead of silently guessing.
      if (provider === 'kimi') {
        if (apiKey.trim()) updates.kimiKey = apiKey.trim()
        updates.defaultModel = selectedModel
      }
      await UpdateSettings({ projects: [], settings: updates } as any)
      // Update the local store so subsequent reads see fresh values.
      for (const k of ['defaultProvider', 'defaultModel', 'glmKey', 'kimiKey']) {
        if (updates[k] !== undefined) updateField(k as any, updates[k])
      }
      setStep(2)
    } catch (e: any) {
      setStep1Error(`Save failed: ${String(e?.message || e || 'unknown')}`)
    } finally {
      setKeySaving(false)
    }
  }

  const handleBrowseDir = async () => {
    try {
      const path: any = await BrowseDirectory()
      if (path && typeof path === 'string') {
        setProjectDir(path)
        setStep2Error(null)
        if (!projectName.trim()) {
          // Suggest the last path segment as the project name.
          const parts = path.split(/[/\\]/).filter(Boolean)
          if (parts.length > 0) setProjectName(parts[parts.length - 1])
        }
      }
    } catch (e: any) {
      setStep2Error(String(e?.message || e || 'directory picker unavailable'))
    }
  }

  const handleStep2Next = async () => {
    setStep2Error(null)
    if (!projectName.trim()) { setStep2Error('Enter a project name.'); return }
    if (!projectDir.trim()) { setStep2Error('Pick or type a project directory.'); return }
    setProjectAdding(true)
    try {
      const info: any = await AddProject(projectName.trim(), projectDir.trim())
      const project = info as ProjectInfo
      // Update the store so the sidebar reflects it immediately.
      useProjectStore.getState().addProject(project)
      useProjectStore.getState().setActiveProject(project.id)
      setCreatedProject(project)
      setStep(3)
    } catch (e: any) {
      setStep2Error(String(e?.message || e || 'failed to add project'))
    } finally {
      setProjectAdding(false)
    }
  }

  const handleStep3Done = () => {
    try { localStorage.setItem(ONBOARDING_DISMISSED_KEY, '1') } catch { /* unavailable */ }
    onComplete(createdProject || undefined)
  }

  const handleStarterPrompt = (prompt: string) => {
    try { localStorage.setItem(ONBOARDING_DISMISSED_KEY, '1') } catch { /* unavailable */ }
    // App.tsx waits for the new project's default session before handing this
    // reviewable draft to ChatPanel, so an old project's active tab can never
    // capture it during the transition.
    onComplete(createdProject || undefined, prompt)
  }

  return (
    <>
    <div className="onboarding-root" onClick={(e) => e.stopPropagation()}>
      <div
        className="onboarding-card"
        role="dialog"
        aria-modal="true"
        aria-labelledby="onboarding-title"
        aria-busy={testing || keySaving || projectAdding}
        onClick={(e) => e.stopPropagation()}
      >
        <button
          className="onboarding-close"
          onClick={() => void handleSkip()}
          disabled={testing || pasting || keySaving || projectAdding}
          title={testing || pasting || keySaving || projectAdding ? 'Finish the current operation first' : 'Skip onboarding (Esc)'}
          aria-label="Skip onboarding"
        >
          <X size={16} />
        </button>

        <div className="onboarding-header">
          <Sparkles size={18} className="onboarding-spark" />
          <h2 id="onboarding-title">Welcome to Gokin Studio</h2>
        </div>

        <div className="onboarding-steps">
          {[1, 2, 3].map((s) => (
            <div
              key={s}
              className={`onboarding-step-dot ${step >= s ? 'active' : ''} ${step === s ? 'current' : ''}`}
              aria-current={step === s ? 'step' : undefined}
            >
              <span className="onboarding-step-num">{step > s ? <CheckCircle size={12} /> : s}</span>
              <span className="onboarding-step-label">
                {s === 1 ? 'Provider' : s === 2 ? 'First project' : 'Done'}
              </span>
            </div>
          ))}
        </div>

        {step === 1 && (
          <div className="onboarding-body">
            <p className="onboarding-text">
              Pick an LLM provider and paste your API key. You can change this anytime in Settings.
            </p>
            <div className="onboarding-providers">
              {PROVIDER_CHOICES.map((p) => (
                <button
                  key={p.id}
                  className={`onboarding-provider ${provider === p.id ? 'selected' : ''}`}
                  disabled={testing || pasting || keySaving}
                  onClick={() => {
                    if (p.id === provider) return
                    setProvider(p.id)
                    setShowApiKey(false)
                    setStep1Error(null)
                  }}
                  aria-pressed={provider === p.id}
                >
                  <div className="onboarding-provider-name">
                    {p.name}
                    {providerDrafts[p.id]?.testResult?.ok ? (
                      <span className="onboarding-provider-state verified"><CheckCircle size={11} /> Verified</span>
                    ) : providerDrafts[p.id]?.apiKey.trim() ? (
                      <span className="onboarding-provider-state">
                        {providerDrafts[p.id].apiKey.trim() === (savedProviderKey(p.id) || '').trim() ? 'Key saved' : 'Key entered'}
                      </span>
                    ) : providerCredentialSources[p.id] === 'env' ? (
                      <span className="onboarding-provider-state environment"><Key size={10} /> Environment</span>
                    ) : null}
                  </div>
                  <div className="onboarding-provider-tag">{providerTag(p.id)}</div>
                </button>
              ))}
            </div>

            <div className="onboarding-field">
                <label htmlFor="ob-key"><Key size={13} /> API key</label>
                <div className="onboarding-key-row">
                  <input
                    id="ob-key"
                    type={showApiKey ? 'text' : 'password'}
                    value={apiKey}
                    onChange={(e) => {
                      updateProviderDraft(provider, { apiKey: e.target.value, detectedModel: '', testResult: null })
                      setProviderCapability(provider, null)
                      setStep1Error(null)
                    }}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && !e.nativeEvent.isComposing && !testing && !keySaving) {
                        e.preventDefault()
                        void handleStep1Next(false)
                      }
                    }}
                    placeholder={usesEnvironmentCredential
                      ? `Using $${environmentVariable}`
                      : `Paste your ${PROVIDER_CHOICES.find((c) => c.id === provider)?.name} key…`}
                    maxLength={500}
                    autoComplete="off"
                    spellCheck={false}
                    disabled={testing || pasting || keySaving}
                  />
                  <button className="btn-secondary" type="button" onClick={handlePasteKey} title="Paste from clipboard" disabled={testing || pasting || keySaving}>
                    {pasting ? <><Loader2 size={12} className="spin" /> Pasting…</> : 'Paste'}
                  </button>
                  <button
                    className="btn-secondary onboarding-key-visibility"
                    type="button"
                    onClick={() => setShowApiKey((visible) => !visible)}
                    disabled={testing || pasting || keySaving}
                    title={showApiKey ? 'Hide API key' : 'Show API key'}
                    aria-label={showApiKey ? 'Hide API key' : 'Show API key'}
                  >
                    {showApiKey ? <EyeOff size={13} /> : <Eye size={13} />}
                  </button>
                </div>
                <div className="onboarding-hint">
                  {usesEnvironmentCredential ? (
                    <span className="onboarding-env-source"><CheckCircle size={11} /> ${environmentVariable} detected. Its value stays hidden.</span>
                  ) : (
                    <>{PROVIDER_CHOICES.find((c) => c.id === provider)?.keyFormat}.{' '}
                    Don't have one?{' '}
                    <button
                      type="button"
                      className="onboarding-inline-link"
                      onClick={() => BrowserOpenURL(getProviderAccountURL(provider))}
                    >
                      Get a {PROVIDER_CHOICES.find((c) => c.id === provider)?.name} key →
                    </button></>
                  )}
                </div>
            </div>

            {providerModelOptions(provider).length > 0 && (() => {
              const options = providerModelOptions(provider)
              const selected = providerModel(provider) || options[0]
              const tested = (providerCapabilities[provider]?.availableModels || []).length > 0
              return (
                <div className="onboarding-field onboarding-model-field">
                  <label htmlFor="ob-model">Default model</label>
                  <select
                    id="ob-model"
                    value={selected?.id || ''}
                    disabled={testing || pasting || keySaving}
                    onChange={(event) => {
                      updateProviderDraft(provider, { detectedModel: event.target.value, testResult: null })
                      setStep1Error(null)
                    }}
                    aria-describedby="ob-model-detail"
                  >
                    {options.map((model) => (
                      <option key={model.id} value={model.id}>
                        {formatProviderModelLabel(provider, model.id)}{model.recommended ? ' · recommended' : model.latest ? ' · latest' : ''}
                      </option>
                    ))}
                  </select>
                  {selected && (
                    <div id="ob-model-detail" className="onboarding-model-detail">
                      <span>{formatContextWindow(selected.contextWindow)} context</span>
                      <span>{selected.inputModalities.includes('image') ? 'Native images' : 'Text input'}</span>
                      <span>{selected.reasoningControl ? `${selected.reasoningControl} reasoning` : 'Agentic coding'}</span>
                      <small>{tested ? 'Models available to this account' : 'Bundled catalog · connection test confirms account access'}</small>
                    </div>
                  )}
                </div>
              )
            })()}

            {testResult && (
              <div className="onboarding-test-row">
                <span className={`onboarding-test-result ${testResult.ok ? 'ok' : 'fail'}`} role="status" aria-live="polite">
                  {testResult.ok ? <CheckCircle size={13} /> : <AlertCircle size={13} />}
                  {testResult.msg}
                </span>
              </div>
            )}

            {step1Error && <div className="onboarding-error" role="alert"><AlertCircle size={13} /> {step1Error}</div>}

            <div className="onboarding-actions">
              <button className="btn-secondary" onClick={() => void handleSkip()} disabled={testing || pasting || keySaving}>Skip setup</button>
              <div className="onboarding-actions-primary">
                {testResult && !testResult.ok && (
                  <button className="btn-secondary" onClick={() => void handleStep1Next(true)} disabled={keySaving || testing || pasting || !hasCredential}>
                    Continue without verification
                  </button>
                )}
                <button className="btn-primary" onClick={() => void handleStep1Next(false)} disabled={keySaving || testing || pasting || !hasCredential}>
                  {keySaving || testing ? <Loader2 size={13} className="spin" /> : <Wifi size={14} />}
                  {keySaving || testing
                    ? `Connecting ${provider === 'kimi' ? 'Kimi' : 'GLM'}…`
                    : testResult && !testResult.ok
                      ? 'Try again'
                      : `Connect ${provider === 'kimi' ? 'Kimi' : 'GLM'}`}
                </button>
              </div>
            </div>
          </div>
        )}

        {step === 2 && (
          <div className="onboarding-body">
            <p className="onboarding-text">
              Point Gokin Studio at a project directory. The agent will read, edit, and run commands inside it.
            </p>
            <div className="onboarding-field">
              <label htmlFor="ob-pname">Project name</label>
              <input
                id="ob-pname"
                type="text"
                value={projectName}
                onChange={(e) => { setProjectName(e.target.value); setStep2Error(null) }}
                placeholder="e.g. my-cli, web-app, scratch…"
                maxLength={60}
                autoFocus
              />
            </div>
            <div className="onboarding-field">
              <label htmlFor="ob-pdir"><FolderOpen size={13} /> Directory</label>
              <div className="onboarding-key-row">
                <input
                  id="ob-pdir"
                  type="text"
                  value={projectDir}
                  onChange={(e) => { setProjectDir(e.target.value); setStep2Error(null) }}
                  placeholder="/Users/you/projects/my-cli"
                />
                <button className="btn-secondary" type="button" onClick={handleBrowseDir} disabled={projectAdding}>
                  Browse…
                </button>
              </div>
            </div>

            <div className="onboarding-project-model" aria-label="Model for the first project">
              <CheckCircle size={13} />
              <span>
                <strong>{formatProviderModelLabel(settings.defaultProvider, settings.defaultModel)}</strong>
                <small>Connected and ready · change per project at any time</small>
              </span>
            </div>

            {step2Error && <div className="onboarding-error" role="alert"><AlertCircle size={13} /> {step2Error}</div>}

            <div className="onboarding-actions">
              <button className="btn-secondary" onClick={() => setStep(1)} disabled={projectAdding}>Back</button>
              <button className="btn-primary" onClick={handleStep2Next} disabled={projectAdding || !projectName.trim() || !projectDir.trim()}>
                {projectAdding ? <Loader2 size={13} className="spin" /> : <ChevronRight size={14} />}
                Add project
              </button>
            </div>
          </div>
        )}

        {step === 3 && (
          <div className="onboarding-body">
            <div className="onboarding-success">
              <CheckCircle size={32} className="onboarding-success-icon" />
              <h3>You're all set</h3>
              <p>
                {createdProject ? <><b>{createdProject.name}</b> is ready.</> : 'Project created.'}{' '}
                Try one of these in the chat:
              </p>
              {createdProject && (
                <div className="onboarding-ready-model" aria-label="Configured model">
                  <CheckCircle size={12} />
                  {formatProviderModelLabel(createdProject.provider, createdProject.model)}
                </div>
              )}
              <div className="onboarding-suggestions" aria-label="Starter prompts">
                {STARTER_PROMPTS.map((starter) => (
                  <button
                    key={starter.title}
                    type="button"
                    onClick={() => handleStarterPrompt(starter.prompt)}
                    aria-label={`${starter.title}. Open as an editable chat draft.`}
                  >
                    <span>
                      <strong>{starter.title}</strong>
                      <small>{starter.prompt}</small>
                    </span>
                    <ChevronRight size={14} aria-hidden />
                  </button>
                ))}
              </div>
              <p className="onboarding-hint">
                Press <kbd>Ctrl+/</kbd> any time for the in-app help, or <kbd>Ctrl+K</kbd> for the command palette.
              </p>
            </div>

            <div className="onboarding-actions onboarding-actions-center">
              <button className="btn-primary onboarding-cta" onClick={handleStep3Done}>
                Start chatting <ChevronRight size={14} />
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
    {confirmationDialog}
    </>
  )
}
