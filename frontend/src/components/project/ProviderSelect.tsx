import { useState, useEffect, useRef } from 'react'
import { useProjectStore } from '../../stores/projectStore'
import { useSettingsStore } from '../../stores/settingsStore'
import { SetProjectProvider, SetProjectModelParams, SetProjectThinking, GetProject } from '../../../wailsjs/go/studio/Studio'
import { ChevronDown } from 'lucide-react'

interface ProviderSelectProps {
  projectId: string
  currentProvider: string
  currentModel: string
  currentTemperature?: number
  currentMaxTokens?: number
  currentThinkingMode?: string
  currentThinkingBudget?: number
}

export function ProviderSelect({ projectId, currentProvider, currentModel, currentTemperature, currentMaxTokens, currentThinkingMode, currentThinkingBudget }: ProviderSelectProps) {
  const [open, setOpen] = useState(false)
  const [provider, setProvider] = useState(currentProvider || 'glm')
  const [model, setModel] = useState(currentModel || 'glm-5.2')
  const [temperature, setTemperature] = useState(currentTemperature || 0)
  const [maxTokens, setMaxTokens] = useState(currentMaxTokens || 0)
  const [thinkingMode, setThinkingMode] = useState(currentThinkingMode || '')
  const [thinkingBudget, setThinkingBudget] = useState(currentThinkingBudget || 0)
  const [applyError, setApplyError] = useState<string | null>(null)
  const updateProject = useProjectStore((s) => s.updateProject)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  // Keep a stable ref to the cancel fn so the Escape handler doesn't need
  // to be re-registered every render.
  const cancelRef = useRef<() => void>(() => {})

  // Sync props to state when project data changes externally.
  useEffect(() => {
    setProvider(currentProvider || 'glm')
    setModel(currentModel || 'glm-5.2')
    setTemperature(currentTemperature || 0)
    setMaxTokens(currentMaxTokens || 0)
    setThinkingMode(currentThinkingMode || '')
    setThinkingBudget(currentThinkingBudget || 0)
  }, [currentProvider, currentModel, currentTemperature, currentMaxTokens, currentThinkingMode, currentThinkingBudget])

  // Close popup when the user switches to a different project.
  useEffect(() => {
    if (open) setOpen(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeProjectId])

  // Close on Escape (discard unsaved changes).
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      if (e.key === 'Escape') { e.preventDefault(); cancelRef.current() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])
  const providers = useSettingsStore((s) => s.providers)

  const getModels = (pid: string): string[] => {
    const p = providers.find((pr) => pr.id === pid)
    if (p && p.models.length > 0) {
      return p.models
    }
    return []
  }

  const handleProviderChange = (newProvider: string) => {
    setProvider(newProvider)
    const models = getModels(newProvider)
    const newModel = models[0] || ''
    setModel(newModel)
  }

  const handleApply = async () => {
    if (!model) { setApplyError('No model selected for this provider'); return }
    setApplyError(null)
    try {
      await SetProjectProvider(projectId, provider, model)
      if (temperature !== (currentTemperature || 0) || maxTokens !== (currentMaxTokens || 0)) {
        await SetProjectModelParams(projectId, temperature, maxTokens)
      }
      if (thinkingMode !== (currentThinkingMode || '') || thinkingBudget !== (currentThinkingBudget || 0)) {
        await SetProjectThinking(projectId, thinkingMode, thinkingBudget)
      }
      updateProject(projectId, { provider, model, temperature, maxTokens, thinkingMode, thinkingBudget })
      // Refresh full ProjectInfo to pick up the correct contextWindow for the
      // new provider/model — the gauge would stay at the old window size otherwise.
      GetProject(projectId).then((info: any) => {
        if (info?.contextWindow) updateProject(projectId, { contextWindow: info.contextWindow })
      }).catch(() => {})
      setOpen(false)
    } catch (e: any) {
      console.error('ProviderSelect apply error:', e)
      setApplyError(String(e?.message || e || 'Failed to save provider settings'))
    }
  }

  const handleCancel = () => {
    setProvider(currentProvider || 'glm')
    setModel(currentModel || 'glm-5.2')
    setTemperature(currentTemperature || 0)
    setMaxTokens(currentMaxTokens || 0)
    setThinkingMode(currentThinkingMode || '')
    setThinkingBudget(currentThinkingBudget || 0)
    setApplyError(null)
    setOpen(false)
  }
  cancelRef.current = handleCancel

  const models = getModels(provider)

  if (!open) {
    return (
      <button
        className="provider-badge provider-badge-clickable"
        onClick={(e) => {
          e.stopPropagation()
          setOpen(true)
        }}
        title="Change provider"
      >
        <span className={`provider-dot ${currentProvider || 'glm'}`} />
        {currentProvider || 'glm'} <ChevronDown size={8} />
      </button>
    )
  }

  return (
    <>
      <div className="provider-select-backdrop" onClick={handleCancel} />
      <div className="provider-select" onClick={(e) => e.stopPropagation()}>
        <div className="provider-select-field">
          <label>Provider</label>
          <select value={provider} onChange={(e) => handleProviderChange(e.target.value)}>
            {providers.map((p) => (
              <option key={p.id} value={p.id}>{p.name}</option>
            ))}
          </select>
        </div>
        <div className="provider-select-field">
          <label>Model</label>
          <select value={model} onChange={(e) => setModel(e.target.value)}>
            {models.map((m) => (
              <option key={m} value={m}>{m}</option>
            ))}
          </select>
        </div>
        <div className="provider-select-field">
          <label>Temperature (0 = default)</label>
          <input
            type="number"
            min="0"
            max="2"
            step="0.1"
            value={temperature}
            onChange={(e) => setTemperature(parseFloat(e.target.value) || 0)}
          />
        </div>
        <div className="provider-select-field">
          <label>Max tokens (0 = default)</label>
          <input
            type="number"
            min="0"
            max="128000"
            step="1024"
            value={maxTokens}
            onChange={(e) => setMaxTokens(parseInt(e.target.value) || 0)}
          />
        </div>
        <div className="provider-select-field">
          <label>Thinking</label>
          <select value={thinkingMode} onChange={(e) => setThinkingMode(e.target.value)}>
            <option value="">Auto (provider default)</option>
            <option value="enabled">Enabled</option>
            <option value="disabled">Disabled</option>
          </select>
        </div>
        {thinkingMode === 'enabled' && (
          <div className="provider-select-field">
            <label>Thinking budget tokens (0 = {provider === 'glm' ? 8192 : 4096})</label>
            <input
              type="number"
              min="0"
              max="32768"
              step="1024"
              value={thinkingBudget}
              onChange={(e) => setThinkingBudget(parseInt(e.target.value) || 0)}
            />
          </div>
        )}
        {applyError && <div className="provider-select-error">{applyError}</div>}
        <div className="provider-select-actions">
          <button className="btn-secondary" onClick={handleCancel}>Cancel</button>
          <button className="btn-primary" onClick={handleApply} disabled={!model}>Apply</button>
        </div>
      </div>
    </>
  )
}
