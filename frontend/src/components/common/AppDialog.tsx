import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { AlertTriangle, X } from 'lucide-react'

type ConfirmOptions = {
  title: string
  message: string
  confirmLabel?: string
  cancelLabel?: string
  danger?: boolean
}

type PromptOptions = {
  title: string
  message?: string
  initialValue?: string
  placeholder?: string
  confirmLabel?: string
  cancelLabel?: string
}

type ChoiceOptions = {
  title: string
  message?: string
  choices: Array<{
    value: string
    label: string
    description?: string
    primary?: boolean
  }>
  cancelLabel?: string
}

function DialogFrame({
  title,
  message,
  children,
  onCancel,
  danger = false,
}: {
  title: string
  message?: string
  children: ReactNode
  onCancel: () => void
  danger?: boolean
}) {
  const titleID = useId()
  const descriptionID = useId()
  const dialogRef = useRef<HTMLElement>(null)
  return createPortal(
    <div
      className="app-dialog-backdrop"
      onMouseDown={(event) => { if (event.target === event.currentTarget) onCancel() }}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          event.preventDefault()
          event.stopPropagation()
          onCancel()
          return
        }
        if (event.key !== 'Tab') return
        const focusable = Array.from(dialogRef.current?.querySelectorAll<HTMLElement>(
          'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
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
      <section
        ref={dialogRef}
        className={`app-dialog ${danger ? 'danger' : ''}`}
        role={danger ? 'alertdialog' : 'dialog'}
        aria-modal="true"
        aria-labelledby={titleID}
        aria-describedby={message ? descriptionID : undefined}
      >
        <button type="button" className="app-dialog-close" onClick={onCancel} aria-label="Close dialog">
          <X size={15} />
        </button>
        <div className="app-dialog-heading">
          {danger && <span className="app-dialog-danger-icon"><AlertTriangle size={15} /></span>}
          <h3 id={titleID}>{title}</h3>
        </div>
        {message && <p id={descriptionID} className="app-dialog-message">{message}</p>}
        {children}
      </section>
    </div>,
    document.body,
  )
}

export function useConfirmDialog() {
  const [options, setOptions] = useState<ConfirmOptions | null>(null)
  const resolver = useRef<((accepted: boolean) => void) | null>(null)
  const trigger = useRef<HTMLElement | null>(null)

  const settle = useCallback((accepted: boolean) => {
    const resolve = resolver.current
    resolver.current = null
    setOptions(null)
    resolve?.(accepted)
    window.requestAnimationFrame(() => { if (trigger.current?.isConnected) trigger.current.focus() })
  }, [])

  const confirm = useCallback((next: ConfirmOptions) => {
    resolver.current?.(false)
    trigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    return new Promise<boolean>((resolve) => {
      resolver.current = resolve
      setOptions(next)
    })
  }, [])

  useEffect(() => () => resolver.current?.(false), [])

  const dialog = options ? (
    <DialogFrame title={options.title} message={options.message} danger={options.danger} onCancel={() => settle(false)}>
      <div className="app-dialog-actions">
        <button type="button" className="btn-secondary" onClick={() => settle(false)} autoFocus={!!options.danger}>{options.cancelLabel || 'Cancel'}</button>
        <button type="button" className={options.danger ? 'app-dialog-danger-button' : 'btn-primary'} onClick={() => settle(true)} autoFocus={!options.danger}>
          {options.confirmLabel || 'Continue'}
        </button>
      </div>
    </DialogFrame>
  ) : null

  return [confirm, dialog] as const
}

export function usePromptDialog() {
  const [options, setOptions] = useState<PromptOptions | null>(null)
  const [value, setValue] = useState('')
  const resolver = useRef<((value: string | null) => void) | null>(null)
  const trigger = useRef<HTMLElement | null>(null)

  const settle = useCallback((result: string | null) => {
    const resolve = resolver.current
    resolver.current = null
    setOptions(null)
    resolve?.(result)
    window.requestAnimationFrame(() => { if (trigger.current?.isConnected) trigger.current.focus() })
  }, [])

  const prompt = useCallback((next: PromptOptions) => {
    resolver.current?.(null)
    trigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    setValue(next.initialValue || '')
    return new Promise<string | null>((resolve) => {
      resolver.current = resolve
      setOptions(next)
    })
  }, [])

  useEffect(() => () => resolver.current?.(null), [])

  const dialog = options ? (
    <DialogFrame title={options.title} message={options.message} onCancel={() => settle(null)}>
      <form
        className="app-dialog-form"
        onSubmit={(event) => {
          event.preventDefault()
          if (value.trim()) settle(value)
        }}
      >
        <input
          value={value}
          onChange={(event) => setValue(event.target.value)}
          placeholder={options.placeholder}
          maxLength={80}
          autoFocus
          aria-label={options.title}
        />
        <div className="app-dialog-actions">
          <button type="button" className="btn-secondary" onClick={() => settle(null)}>{options.cancelLabel || 'Cancel'}</button>
          <button type="submit" className="btn-primary" disabled={!value.trim()}>{options.confirmLabel || 'Save'}</button>
        </div>
      </form>
    </DialogFrame>
  ) : null

  return [prompt, dialog] as const
}

export function useChoiceDialog() {
  const [options, setOptions] = useState<ChoiceOptions | null>(null)
  const resolver = useRef<((value: string | null) => void) | null>(null)
  const trigger = useRef<HTMLElement | null>(null)

  const settle = useCallback((result: string | null) => {
    const resolve = resolver.current
    resolver.current = null
    setOptions(null)
    resolve?.(result)
    window.requestAnimationFrame(() => { if (trigger.current?.isConnected) trigger.current.focus() })
  }, [])

  const choose = useCallback((next: ChoiceOptions) => {
    resolver.current?.(null)
    trigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    return new Promise<string | null>((resolve) => {
      resolver.current = resolve
      setOptions(next)
    })
  }, [])

  useEffect(() => () => resolver.current?.(null), [])

  const dialog = options ? (
    <DialogFrame title={options.title} message={options.message} onCancel={() => settle(null)}>
      <div className="app-dialog-choice-list">
        {options.choices.map((choice, index) => (
          <button
            type="button"
            className={choice.primary ? 'app-dialog-choice primary' : 'app-dialog-choice'}
            key={choice.value}
            onClick={() => settle(choice.value)}
            autoFocus={choice.primary || (!options.choices.some((candidate) => candidate.primary) && index === 0)}
          >
            <strong>{choice.label}</strong>
            {choice.description && <small>{choice.description}</small>}
          </button>
        ))}
      </div>
      <div className="app-dialog-actions">
        <button type="button" className="btn-secondary" onClick={() => settle(null)}>{options.cancelLabel || 'Cancel'}</button>
      </div>
    </DialogFrame>
  ) : null

  return [choose, dialog] as const
}
