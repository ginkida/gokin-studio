import { useState, useEffect, useId, useRef } from 'react'
import { useProjectStore } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { useSettingsStore } from '../../stores/settingsStore'
import { ConfigureProjectModel, ModelSwitchWarning } from '../../../wailsjs/go/studio/Studio'
import { AlertTriangle, ChevronDown, Loader2, X } from 'lucide-react'
import { formatContextWindow } from '../../lib/modelCapabilities'
import { formatModelLabel } from '../../lib/providerCatalog'
import { studioReasoningControlKind } from '../../lib/studioModelIds'
import { useConfirmDialog } from '../common/AppDialog'

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
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const [open, setOpen] = useState(false)
  const [provider, setProvider] = useState(currentProvider || 'glm')
  const [model, setModel] = useState(currentModel || 'glm-5.2')
  const [temperature, setTemperature] = useState(currentTemperature || 0)
  const [maxTokens, setMaxTokens] = useState(currentMaxTokens || 0)
  const [thinkingMode, setThinkingMode] = useState(currentThinkingMode || '')
  const [thinkingBudget, setThinkingBudget] = useState(currentThinkingBudget || 0)
  const [applyError, setApplyError] = useState<string | null>(null)
  const [switchWarning, setSwitchWarning] = useState<string | null>(null)
  const [adjustmentNotice, setAdjustmentNotice] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const fieldPrefix = useId()
  const dialogRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const savingRef = useRef(false)
  const updateProject = useProjectStore((s) => s.updateProject)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const projectBusy = useChatStore((state) => {
    const prefix = `${projectId}_`
    return Object.entries(state.sessionActive).some(([key, active]) => active && key.startsWith(prefix))
  })
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
    setApplyError(null)
    setSwitchWarning(null)
    setAdjustmentNotice(null)
  }, [currentProvider, currentModel, currentTemperature, currentMaxTokens, currentThinkingMode, currentThinkingBudget])

  // Close popup when the user switches to a different project.
  useEffect(() => {
    if (open) setOpen(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeProjectId])

  // Escape follows the guarded close path so parameter edits are not lost.
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
  const providerCapabilities = useSettingsStore((s) => s.providerCapabilities)
  const settings = useSettingsStore((s) => s.settings)
  const providerCredentialSources = useSettingsStore((s) => s.providerCredentialSources)
  const hasProviderCredential = (providerID: string) => {
    const savedKey = providerID === 'kimi' ? settings.kimiKey : settings.glmKey
    if (savedKey.trim()) return true
    const source = providerCredentialSources[providerID]
    if (source === 'env') return true
    return source === undefined ? null : false
  }
  const hasChanges = provider !== (currentProvider || 'glm')
    || model !== (currentModel || 'glm-5.2')
    || temperature !== (currentTemperature || 0)
    || maxTokens !== (currentMaxTokens || 0)
    || thinkingMode !== (currentThinkingMode || '')
    || thinkingBudget !== (currentThinkingBudget || 0)

  const getModels = (pid: string): string[] => {
    const p = providers.find((pr) => pr.id === pid)
    if (p && p.models.length > 0) {
      return p.models
    }
    return []
  }

  const handleProviderChange = (newProvider: string) => {
    const models = getModels(newProvider)
    const advertised = providerCapabilities[newProvider]?.availableModels || []
    const recommended = providerCapabilities[newProvider]?.recommendedModel
    const recommendedAvailable = recommended
      && models.includes(recommended)
      && (advertised.length === 0 || advertised.includes(recommended))
    const newModel = recommendedAvailable
      ? recommended!
      : advertised.length > 0
        ? models.find((candidate) => advertised.includes(candidate)) || ''
        : models[0] || ''
    applyModelChange(newProvider, newModel)
  }

  const applyModelChange = (newProvider: string, newModel: string) => {
    const target = providers
      .find((item) => item.id === newProvider)
      ?.modelDetails?.find((item) => item.id === newModel)
    const adjustments: string[] = []
    if (thinkingMode !== '' || thinkingBudget !== 0) {
      setThinkingMode('')
      setThinkingBudget(0)
      adjustments.push('Reasoning reset to Auto for the new model.')
    }
    if (maxTokens > 0 && target?.maxOutputTokens && maxTokens > target.maxOutputTokens) {
      setMaxTokens(0)
      adjustments.push(`Max output reset to the ${target.defaultMaxOutputTokens.toLocaleString()}-token model default.`)
    }
    setProvider(newProvider)
    setModel(newModel)
    setAdjustmentNotice(adjustments.join(' ') || null)
    setApplyError(null)
    setSwitchWarning(null)
  }

  const handleApply = async (switchConfirmed = false) => {
    if (savingRef.current) return
    if (projectBusy) {
      setApplyError('Stop all running chats in this project before changing model settings.')
      return
    }
    if (!model) { setApplyError('No model selected for this provider'); return }
    if (provider !== currentProvider && hasProviderCredential(provider) === false) {
      setApplyError(`Connect ${provider === 'kimi' ? 'Kimi' : 'GLM'} before switching this project.`)
      return
    }
    if (!getModels(provider).includes(model)) {
      setApplyError(`${model} is no longer in the current ${provider.toUpperCase()} account catalog. Re-test the connection or choose another model.`)
      return
    }
    const tested = providerCapabilities[provider]?.availableModels || []
    if (tested.length > 0 && !tested.includes(model)) {
      setApplyError(`${model} was not advertised for the tested ${provider.toUpperCase()} key. Choose an available model or re-test the account in Settings.`)
      return
    }
    savingRef.current = true
    setSaving(true)
    setApplyError(null)
    try {
      if (!switchConfirmed && (provider !== currentProvider || model !== currentModel)) {
        const warning = await ModelSwitchWarning(projectId, provider, model)
        if (warning) {
          setSwitchWarning(warning)
          return
        }
      }
      const info = await ConfigureProjectModel(projectId, provider, model, temperature, maxTokens, thinkingMode, thinkingBudget)
      if (!info?.id || info.id !== projectId || !info.provider || !info.model) {
        throw new Error('Backend returned an incomplete project model snapshot.')
      }
      // The mutation returns the authoritative resolved snapshot so context,
      // effective reasoning state, and every visible control update together.
      updateProject(projectId, info)
      setOpen(false)
      requestAnimationFrame(() => triggerRef.current?.focus())
    } catch (e: any) {
      console.error('ProviderSelect apply error:', e)
      setApplyError(String(e?.message || e || 'Failed to save provider settings'))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  const closeWithoutSaving = () => {
    if (saving) return
    setProvider(currentProvider || 'glm')
    setModel(currentModel || 'glm-5.2')
    setTemperature(currentTemperature || 0)
    setMaxTokens(currentMaxTokens || 0)
    setThinkingMode(currentThinkingMode || '')
    setThinkingBudget(currentThinkingBudget || 0)
    setApplyError(null)
    setSwitchWarning(null)
    setAdjustmentNotice(null)
    setOpen(false)
    requestAnimationFrame(() => triggerRef.current?.focus())
  }

  const requestCancel = async () => {
    if (saving) return
    if (hasChanges && !(await requestConfirmation({
      title: 'Discard model setting changes?',
      message: 'The unsaved provider, model, output, temperature, and reasoning settings will return to the values currently applied to this project.',
      confirmLabel: 'Discard changes',
      cancelLabel: 'Keep editing',
      danger: true,
    }))) return
    closeWithoutSaving()
  }
  cancelRef.current = () => { void requestCancel() }

  const models = getModels(provider)
  const testedModels = providerCapabilities[provider]?.availableModels || []
  const hasTestedCatalog = testedModels.length > 0
  const selectedMissingFromCatalog = !!model && !models.includes(model)
  const selectedUnavailable = selectedMissingFromCatalog
    || (hasTestedCatalog && !!model && !testedModels.includes(model))
  const switchingToUnconfiguredProvider = provider !== currentProvider && hasProviderCredential(provider) === false
  const selectedModelInfo = providers
    .find((item) => item.id === provider)
    ?.modelDetails?.find((item) => item.id === model)
  const reasoningControl = studioReasoningControlKind(provider, model, selectedModelInfo?.reasoningControl)
  const isKimiNativeEffort = reasoningControl === 'kimi-effort'
  const isGLMNativeEffort = reasoningControl === 'glm-effort'
  const outputTokenLimit = selectedModelInfo?.maxOutputTokens || 131072
  const currentProviderLabel = currentProvider === 'kimi' ? 'Kimi' : 'GLM'

  if (!open) {
    return (
      <button
        ref={triggerRef}
        type="button"
        className="provider-badge provider-badge-clickable"
        onClick={(e) => {
          e.stopPropagation()
          setOpen(true)
        }}
        title={`Change model · ${currentProvider || 'glm'} / ${currentModel || 'default'}`}
        aria-haspopup="dialog"
        aria-expanded={false}
        aria-label={`Change model. Current: ${currentProvider || 'glm'} ${currentModel || 'default'}`}
      >
        <span className={`provider-dot ${currentProvider || 'glm'}`} />
        <span className="provider-badge-provider">{currentProviderLabel}</span>
        <span className="provider-badge-model">{formatModelLabel(currentModel)}</span>
        <ChevronDown size={9} />
      </button>
    )
  }

  return (
    <>
      <div className="provider-select-backdrop" onMouseDown={() => void requestCancel()} />
      <div
        ref={dialogRef}
        className="provider-select"
        role="dialog"
        aria-modal="true"
        aria-busy={saving}
        aria-labelledby={`${fieldPrefix}-title`}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.preventDefault()
            event.stopPropagation()
            void requestCancel()
            return
          }
          if (event.key !== 'Tab') return
          const focusable = Array.from(dialogRef.current?.querySelectorAll<HTMLElement>(
            'button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
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
        <div className="provider-select-header">
          <div>
            <h3 id={`${fieldPrefix}-title`}>Model settings</h3>
            <p className={hasChanges ? 'has-changes' : ''}>{hasChanges ? 'Unsaved changes' : 'Applied to this project'}</p>
          </div>
          <button type="button" className="icon-btn" onClick={() => void requestCancel()} disabled={saving} aria-label="Close model settings">
            <X size={14} />
          </button>
        </div>
        <div className="provider-select-field">
          <label htmlFor={`${fieldPrefix}-provider`}>Provider</label>
          <select id={`${fieldPrefix}-provider`} value={provider} disabled={saving} autoFocus onChange={(e) => handleProviderChange(e.target.value)}>
            {providers.map((p) => (
              <option key={p.id} value={p.id}>{p.name}</option>
            ))}
          </select>
          {switchingToUnconfiguredProvider && (
            <div className="provider-credential-warning" role="status">
              <AlertTriangle size={12} />
              <span>
                {provider === 'kimi' ? 'Kimi' : 'GLM'} is not connected. Add its API key before applying this switch.
              </span>
              <button
                type="button"
                onClick={() => {
                  closeWithoutSaving()
                  window.dispatchEvent(new CustomEvent('gokin:open-settings', { detail: { section: 'settings-connections' } }))
                }}
              >
                Connect
              </button>
            </div>
          )}
        </div>
        <div className="provider-select-field">
          <label htmlFor={`${fieldPrefix}-model`}>Model</label>
          <select id={`${fieldPrefix}-model`} value={model} disabled={saving} onChange={(e) => applyModelChange(provider, e.target.value)}>
            {models.map((m) => (
              <option key={m} value={m} disabled={hasTestedCatalog && !testedModels.includes(m)}>
                {formatModelLabel(m)} · {m}{providers.find((item) => item.id === provider)?.modelDetails?.find((item) => item.id === m)?.latest ? ' · latest' : ''}
                {hasTestedCatalog && !testedModels.includes(m) ? ' · unavailable for tested key' : ''}
              </option>
            ))}
          </select>
          {selectedModelInfo && (
            <div className="provider-model-detail">
              {selectedModelInfo.description}
              <span>
                {formatContextWindow(selectedModelInfo.contextWindow)} context
                {' · '}{selectedModelInfo.inputModalities.join(' + ')}
                {' · '}{selectedModelInfo.reasoningControl}
              </span>
            </div>
          )}
          {selectedUnavailable && (
            <div className="provider-model-unavailable" role="alert">
              <AlertTriangle size={12} />
              <span>{selectedMissingFromCatalog
                ? 'This model is no longer in the current account catalog. Re-test the connection or choose another model.'
                : 'This model was not advertised for the tested account. Choose another model or re-test the API key in Settings.'}</span>
            </div>
          )}
        </div>
        <div className="provider-select-field">
          <label htmlFor={`${fieldPrefix}-temperature`}>Temperature <span>(0 = default)</span></label>
          <input
            id={`${fieldPrefix}-temperature`}
            type="number"
            min="0"
            max="2"
            step="0.1"
            value={temperature}
            disabled={saving}
            onChange={(e) => { setTemperature(parseFloat(e.target.value) || 0); setApplyError(null) }}
          />
        </div>
        <div className="provider-select-field">
          <label htmlFor={`${fieldPrefix}-max-tokens`}>Max tokens <span>(0 = default)</span></label>
          <input
            id={`${fieldPrefix}-max-tokens`}
            type="number"
            min="0"
            max={outputTokenLimit}
            step="1024"
            value={maxTokens}
            disabled={saving}
            onChange={(e) => { setMaxTokens(parseInt(e.target.value) || 0); setApplyError(null); setAdjustmentNotice(null) }}
          />
          {selectedModelInfo && (
            <div className="provider-model-detail">
              Studio default {selectedModelInfo.defaultMaxOutputTokens.toLocaleString()} · maximum {outputTokenLimit.toLocaleString()}
            </div>
          )}
        </div>
        <div className="provider-select-field">
          <label htmlFor={`${fieldPrefix}-thinking`}>{isKimiNativeEffort || isGLMNativeEffort ? 'Reasoning effort' : 'Thinking'}</label>
          {isKimiNativeEffort ? (
            <select
              id={`${fieldPrefix}-thinking`}
              value={thinkingMode === '' ? 'auto' : thinkingMode === 'disabled' ? 'low' : thinkingBudget > 16384 ? 'max' : thinkingBudget > 4096 ? 'high' : 'low'}
              disabled={saving}
              onChange={(e) => {
                const value = e.target.value
                if (value === 'auto') {
                  setThinkingMode('')
                  setThinkingBudget(0)
                  setApplyError(null)
                  setAdjustmentNotice(null)
                  return
                }
                setThinkingMode('enabled')
                setThinkingBudget(value === 'max' ? 32768 : value === 'high' ? 8192 : 4096)
                setApplyError(null)
                setAdjustmentNotice(null)
              }}
            >
              <option value="auto">Auto · High (recommended)</option>
              <option value="low">Low</option>
              <option value="high">High</option>
              <option value="max">Max</option>
            </select>
          ) : isGLMNativeEffort ? (
            <select
              id={`${fieldPrefix}-thinking`}
              value={thinkingMode === '' ? 'auto' : thinkingMode === 'disabled' ? 'disabled' : thinkingBudget > 16384 ? 'max' : 'high'}
              disabled={saving}
              onChange={(e) => {
                const value = e.target.value
                if (value === 'auto') {
                  setThinkingMode('')
                  setThinkingBudget(0)
                  setApplyError(null)
                  setAdjustmentNotice(null)
                  return
                }
                if (value === 'disabled') {
                  setThinkingMode('disabled')
                  setThinkingBudget(0)
                  setApplyError(null)
                  setAdjustmentNotice(null)
                  return
                }
                setThinkingMode('enabled')
                setThinkingBudget(value === 'max' ? 32768 : 8192)
                setApplyError(null)
                setAdjustmentNotice(null)
              }}
            >
              <option value="auto">Auto · Max (recommended)</option>
              <option value="high">High</option>
              <option value="max">Max</option>
              <option value="disabled">Disabled</option>
            </select>
          ) : (
            <select id={`${fieldPrefix}-thinking`} value={thinkingMode} disabled={saving} onChange={(e) => { setThinkingMode(e.target.value); setApplyError(null); setAdjustmentNotice(null) }}>
              <option value="">Auto (provider default)</option>
              <option value="enabled">Enabled</option>
              <option value="disabled">Disabled</option>
            </select>
          )}
        </div>
        {thinkingMode === 'enabled' && !isKimiNativeEffort && !isGLMNativeEffort && (
          <div className="provider-select-field">
            <label htmlFor={`${fieldPrefix}-thinking-budget`}>{`Thinking budget tokens (0 = ${provider === 'glm' ? 8192 : 4096})`}</label>
            <input
              id={`${fieldPrefix}-thinking-budget`}
              type="number"
              min="0"
              max="32768"
              step="1024"
              value={thinkingBudget}
              disabled={saving}
              onChange={(e) => { setThinkingBudget(parseInt(e.target.value) || 0); setApplyError(null); setAdjustmentNotice(null) }}
            />
          </div>
        )}
        {adjustmentNotice && <div className="provider-select-adjustment" role="status">{adjustmentNotice}</div>}
        {projectBusy && (
          <div className="provider-select-warning" role="status">
            <strong>Model settings are locked</strong>
            <span>Stop every running chat or queued turn in this project, then apply the change.</span>
          </div>
        )}
        {applyError && <div className="provider-select-error" role="alert">{applyError}</div>}
        {switchWarning && (
          <div className="provider-select-warning" role="alert">
            <strong>Before switching</strong>
            <span>{switchWarning}</span>
          </div>
        )}
        <div className="provider-select-actions">
          <button type="button" className="btn-secondary" onClick={() => void requestCancel()} disabled={saving}>Cancel</button>
          <button
            type="button"
            className="btn-primary"
            onClick={() => handleApply(!!switchWarning)}
            disabled={!model || !hasChanges || saving || projectBusy || selectedUnavailable || switchingToUnconfiguredProvider}
          >
            {saving && <Loader2 size={12} className="spin" />}
            {saving ? 'Saving…' : switchWarning ? 'Switch model' : 'Apply'}
          </button>
        </div>
      </div>
      {confirmationDialog}
    </>
  )
}
