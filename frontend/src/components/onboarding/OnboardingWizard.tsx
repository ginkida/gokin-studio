import { useEffect, useState } from 'react'
import { CheckCircle, ChevronRight, Sparkles, Key, FolderOpen, Loader2, AlertCircle, Wifi, Server, X } from 'lucide-react'
import {
  UpdateSettings,
  CheckProviderHealth,
  AddProject,
  BrowseDirectory,
  GetClipboardText,
} from '../../../wailsjs/go/studio/Studio'
import { useSettingsStore } from '../../stores/settingsStore'
import { useProjectStore, ProjectInfo } from '../../stores/projectStore'

// Provider options shown in step 1. Order matters — first one is the default
// recommended for new users (GLM has the lowest barrier to entry since users
// can sign up directly without a waitlist).
const PROVIDER_CHOICES: Array<{ id: string; name: string; tag: string; signupUrl: string; keyFormat: string }> = [
  { id: 'glm',      name: 'GLM (Zhipu)',  tag: 'Recommended · cloud',    signupUrl: 'https://open.bigmodel.cn/usercenter/apikeys',     keyFormat: 'starts with a long alphanumeric token' },
  { id: 'deepseek', name: 'DeepSeek V4',  tag: 'cloud · 128K ctx',       signupUrl: 'https://platform.deepseek.com/api_keys',          keyFormat: 'API key from platform.deepseek.com' },
  { id: 'kimi',     name: 'Kimi',         tag: 'cloud · 262K ctx',       signupUrl: 'https://platform.moonshot.ai/console/api-keys',   keyFormat: 'starts with sk-kimi-' },
  { id: 'minimax',  name: 'MiniMax',      tag: 'cloud · 245K ctx',       signupUrl: 'https://www.minimaxi.com/user-center/basic-information/interface-key', keyFormat: 'JWT-style token' },
  { id: 'ollama',   name: 'Ollama',       tag: 'local · no key needed',  signupUrl: 'https://ollama.com/download',                     keyFormat: '' },
]

const ONBOARDING_DISMISSED_KEY = 'gokin:onboarding-dismissed'

interface Props {
  onComplete: (project?: ProjectInfo) => void
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
  const [step, setStep] = useState<1 | 2 | 3>(1)
  const [provider, setProvider] = useState<string>('glm')
  const [apiKey, setApiKey] = useState('')
  const [ollamaUrl, setOllamaUrl] = useState('http://localhost:11434')
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [keySaving, setKeySaving] = useState(false)
  const [step1Error, setStep1Error] = useState<string | null>(null)

  const [projectName, setProjectName] = useState('')
  const [projectDir, setProjectDir] = useState('')
  const [projectAdding, setProjectAdding] = useState(false)
  const [step2Error, setStep2Error] = useState<string | null>(null)

  const [createdProject, setCreatedProject] = useState<ProjectInfo | null>(null)

  const settings = useSettingsStore((s) => s.settings)
  const updateField = useSettingsStore((s) => s.updateField)

  // Esc dismisses with the skip path so persistence happens.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !((e as any).isComposing || e.keyCode === 229)) {
        handleSkip()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const handleSkip = () => {
    try { localStorage.setItem(ONBOARDING_DISMISSED_KEY, '1') } catch { /* unavailable */ }
    onSkip()
  }

  const handlePasteKey = async () => {
    try {
      const text = await GetClipboardText()
      if (text && typeof text === 'string') {
        setApiKey(text.trim())
      }
    } catch { /* clipboard blocked — user can paste manually */ }
  }

  const handleTestConnection = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      // Persist key/url before probing so the backend reads it.
      const updates: any = { ...settings }
      if (provider === 'glm') updates.glmKey = apiKey.trim()
      if (provider === 'minimax') updates.minimaxKey = apiKey.trim()
      if (provider === 'kimi') updates.kimiKey = apiKey.trim()
      if (provider === 'deepseek') updates.deepseekKey = apiKey.trim()
      if (provider === 'ollama') updates.ollamaUrl = ollamaUrl.trim() || 'http://localhost:11434'
      await UpdateSettings({ projects: [], settings: updates } as any)
      const info: any = await CheckProviderHealth(provider)
      setTestResult({ ok: !!info?.ok, msg: info?.description || info?.error || (info?.ok ? 'Connected.' : 'Check failed.') })
    } catch (e: any) {
      setTestResult({ ok: false, msg: String(e?.message || e || 'probe failed') })
    } finally {
      setTesting(false)
    }
  }

  const handleStep1Next = async () => {
    setStep1Error(null)
    if (provider !== 'ollama' && !apiKey.trim()) {
      setStep1Error('Paste an API key to continue, or pick Ollama for local-only.')
      return
    }
    setKeySaving(true)
    try {
      const updates: any = { ...settings, defaultProvider: provider }
      if (provider === 'glm') { updates.glmKey = apiKey.trim(); updates.defaultModel = 'glm-5.2' }
      if (provider === 'minimax') { updates.minimaxKey = apiKey.trim(); updates.defaultModel = 'MiniMax-M2.7' }
      if (provider === 'kimi') { updates.kimiKey = apiKey.trim(); updates.defaultModel = 'kimi-for-coding' }
      if (provider === 'deepseek') { updates.deepseekKey = apiKey.trim(); updates.defaultModel = 'deepseek-v4-pro' }
      if (provider === 'ollama') { updates.ollamaUrl = ollamaUrl.trim() || 'http://localhost:11434'; updates.defaultModel = 'llama3.1' }
      await UpdateSettings({ projects: [], settings: updates } as any)
      // Update the local store so subsequent reads see fresh values.
      for (const k of ['defaultProvider', 'defaultModel', 'glmKey', 'minimaxKey', 'kimiKey', 'deepseekKey', 'ollamaUrl']) {
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

  return (
    <div className="onboarding-root" onClick={(e) => e.stopPropagation()}>
      <div className="onboarding-card" onClick={(e) => e.stopPropagation()}>
        <button className="onboarding-close" onClick={handleSkip} title="Skip onboarding (Esc)">
          <X size={16} />
        </button>

        <div className="onboarding-header">
          <Sparkles size={18} className="onboarding-spark" />
          <h2>Welcome to Gokin Studio</h2>
        </div>

        <div className="onboarding-steps">
          {[1, 2, 3].map((s) => (
            <div key={s} className={`onboarding-step-dot ${step >= s ? 'active' : ''} ${step === s ? 'current' : ''}`}>
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
                  onClick={() => { setProvider(p.id); setTestResult(null) }}
                >
                  <div className="onboarding-provider-name">{p.name}</div>
                  <div className="onboarding-provider-tag">{p.tag}</div>
                </button>
              ))}
            </div>

            {provider === 'ollama' ? (
              <div className="onboarding-field">
                <label htmlFor="ob-ollama"><Server size={13} /> Ollama base URL</label>
                <input
                  id="ob-ollama"
                  type="text"
                  value={ollamaUrl}
                  onChange={(e) => { setOllamaUrl(e.target.value); setTestResult(null) }}
                  placeholder="http://localhost:11434"
                  maxLength={300}
                />
                <div className="onboarding-hint">
                  Ollama must be running locally. <a href={PROVIDER_CHOICES.find((c) => c.id === 'ollama')!.signupUrl} target="_blank" rel="noreferrer">Get Ollama →</a>
                </div>
              </div>
            ) : (
              <div className="onboarding-field">
                <label htmlFor="ob-key"><Key size={13} /> API key</label>
                <div className="onboarding-key-row">
                  <input
                    id="ob-key"
                    type="password"
                    value={apiKey}
                    onChange={(e) => { setApiKey(e.target.value); setTestResult(null) }}
                    placeholder={`Paste your ${PROVIDER_CHOICES.find((c) => c.id === provider)?.name} key…`}
                    maxLength={500}
                  />
                  <button className="btn-secondary" type="button" onClick={handlePasteKey} title="Paste from clipboard">
                    Paste
                  </button>
                </div>
                <div className="onboarding-hint">
                  Don't have one? <a href={PROVIDER_CHOICES.find((c) => c.id === provider)?.signupUrl} target="_blank" rel="noreferrer">Get a {PROVIDER_CHOICES.find((c) => c.id === provider)?.name} key →</a>
                </div>
              </div>
            )}

            <div className="onboarding-test-row">
              <button
                className="btn-secondary"
                onClick={handleTestConnection}
                disabled={testing || (provider !== 'ollama' && !apiKey.trim())}
              >
                {testing ? <Loader2 size={13} className="spin" /> : <Wifi size={13} />}
                Test connection
              </button>
              {testResult && (
                <span className={`onboarding-test-result ${testResult.ok ? 'ok' : 'fail'}`}>
                  {testResult.ok ? <CheckCircle size={13} /> : <AlertCircle size={13} />}
                  {testResult.msg}
                </span>
              )}
            </div>

            {step1Error && <div className="onboarding-error"><AlertCircle size={13} /> {step1Error}</div>}

            <div className="onboarding-actions">
              <button className="btn-secondary" onClick={handleSkip}>Skip setup</button>
              <button className="btn-primary" onClick={handleStep1Next} disabled={keySaving}>
                {keySaving ? <Loader2 size={13} className="spin" /> : <ChevronRight size={14} />}
                Next: First project
              </button>
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
                onChange={(e) => setProjectName(e.target.value)}
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
                  onChange={(e) => setProjectDir(e.target.value)}
                  placeholder="/Users/you/projects/my-cli"
                />
                <button className="btn-secondary" type="button" onClick={handleBrowseDir}>
                  Browse…
                </button>
              </div>
            </div>

            {step2Error && <div className="onboarding-error"><AlertCircle size={13} /> {step2Error}</div>}

            <div className="onboarding-actions">
              <button className="btn-secondary" onClick={() => setStep(1)}>Back</button>
              <button className="btn-primary" onClick={handleStep2Next} disabled={projectAdding}>
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
              <ul className="onboarding-suggestions">
                <li><code>What is in this project?</code></li>
                <li><code>Show me the entry point.</code></li>
                <li><code>Find all TODOs and explain them.</code></li>
              </ul>
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
  )
}

// shouldShowOnboarding returns true if no projects are configured AND the
// user hasn't dismissed the wizard before. Pure function — call from the
// caller's useEffect to decide whether to mount the wizard.
export function shouldShowOnboarding(projectCount: number): boolean {
  if (projectCount > 0) return false
  try {
    return localStorage.getItem(ONBOARDING_DISMISSED_KEY) !== '1'
  } catch {
    return true // when localStorage is blocked, default to showing — gentlest for fresh installs
  }
}
