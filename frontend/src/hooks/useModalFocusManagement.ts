import { useEffect } from 'react'

const FOCUSABLE_SELECTOR = [
  'button:not([disabled]):not([tabindex="-1"])',
  'input:not([disabled]):not([type="hidden"]):not([tabindex="-1"])',
  'select:not([disabled]):not([tabindex="-1"])',
  'textarea:not([disabled]):not([tabindex="-1"])',
  'a[href]:not([tabindex="-1"])',
  '[contenteditable="true"]:not([tabindex="-1"])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

function focusableElements(modal: HTMLElement): HTMLElement[] {
  return Array.from(modal.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter((element) => {
    if (element.closest('[aria-hidden="true"], [inert]')) return false
    const style = window.getComputedStyle(element)
    return style.display !== 'none' && style.visibility !== 'hidden' && element.getClientRects().length > 0
  })
}

function topModal(): HTMLElement | null {
  const modals = Array.from(document.querySelectorAll<HTMLElement>('[aria-modal="true"]'))
    .filter((modal) => !modal.closest('[aria-hidden="true"], [inert]'))
  return modals[modals.length - 1] || null
}

export function hasOpenModal(): boolean {
  return topModal() !== null
}

// One application-level focus lifecycle for every modal surface. Individual
// dialogs still own Escape and submit semantics; this hook only guarantees the
// desktop invariants shared by all of them:
//   1. focus enters the top-most modal,
//   2. Tab/Shift+Tab cannot escape behind it,
//   3. stray programmatic focus is redirected back inside, and
//   4. closing returns focus to the control that opened the modal.
export function useModalFocusManagement() {
  useEffect(() => {
    let activeModal: HTMLElement | null = null
    let scheduledFrame = 0
    const openerByModal = new WeakMap<HTMLElement, HTMLElement | null>()
    const lastFocusByModal = new WeakMap<HTMLElement, HTMLElement>()
    let lastOutsideFocus = document.activeElement instanceof HTMLElement && document.activeElement !== document.body
      ? document.activeElement
      : null

    const scheduleFocus = (modal: HTMLElement, preferred?: HTMLElement | null) => {
      window.cancelAnimationFrame(scheduledFrame)
      scheduledFrame = window.requestAnimationFrame(() => {
        if (!modal.isConnected || topModal() !== modal) return
        if (preferred?.isConnected && modal.contains(preferred)) {
          preferred.focus()
          return
        }
        if (modal.contains(document.activeElement)) return
        const candidates = focusableElements(modal)
        const autoFocus = candidates.find((element) => element.hasAttribute('autofocus'))
        const primaryInput = candidates.find((element) => (
          element instanceof HTMLInputElement ||
          element instanceof HTMLTextAreaElement ||
          element instanceof HTMLSelectElement
        ))
        const target = autoFocus || primaryInput || candidates[0]
        if (target) {
          target.focus()
          return
        }
        modal.tabIndex = -1
        modal.focus()
      })
    }

    const syncModal = () => {
      const nextModal = topModal()
      if (nextModal === activeModal) return

      const previousModal = activeModal
      const previousOpener = previousModal ? openerByModal.get(previousModal) || null : null

      if (nextModal && !openerByModal.has(nextModal)) {
        // React autoFocus runs during commit, before MutationObserver delivers
        // the added modal. Reading document.activeElement here can therefore
        // already return a field *inside* the new dialog. Track focus history
        // independently so the opener is always the actual prior surface.
        const opener = previousModal
          ? lastFocusByModal.get(previousModal) || null
          : lastOutsideFocus
        openerByModal.set(nextModal, opener?.isConnected ? opener : null)
      }

      activeModal = nextModal
      if (nextModal) {
        const preferred = previousOpener && nextModal.contains(previousOpener) ? previousOpener : null
        scheduleFocus(nextModal, preferred)
      } else if (previousOpener?.isConnected) {
        window.cancelAnimationFrame(scheduledFrame)
        scheduledFrame = window.requestAnimationFrame(() => previousOpener.focus())
      }
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Tab' || !activeModal || topModal() !== activeModal) return
      const candidates = focusableElements(activeModal)
      if (candidates.length === 0) {
        event.preventDefault()
        activeModal.tabIndex = -1
        activeModal.focus()
        return
      }
      const first = candidates[0]
      const last = candidates[candidates.length - 1]
      const focused = document.activeElement
      if (!activeModal.contains(focused)) {
        event.preventDefault()
        ;(event.shiftKey ? last : first).focus()
      } else if (event.shiftKey && focused === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && focused === last) {
        event.preventDefault()
        first.focus()
      }
    }

    const onFocusIn = (event: FocusEvent) => {
      const target = event.target
      if (!(target instanceof HTMLElement)) return
      const containingModal = target.closest<HTMLElement>('[aria-modal="true"]')
      if (containingModal) lastFocusByModal.set(containingModal, target)
      else if (!topModal()) lastOutsideFocus = target

      if (!activeModal || topModal() !== activeModal || activeModal.contains(target)) return
      scheduleFocus(activeModal)
    }

    const observer = new MutationObserver(syncModal)
    observer.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['aria-modal', 'aria-hidden', 'inert'],
    })
    document.addEventListener('keydown', onKeyDown, true)
    document.addEventListener('focusin', onFocusIn, true)
    syncModal()

    return () => {
      observer.disconnect()
      document.removeEventListener('keydown', onKeyDown, true)
      document.removeEventListener('focusin', onFocusIn, true)
      window.cancelAnimationFrame(scheduledFrame)
      const opener = activeModal ? openerByModal.get(activeModal) : null
      if (opener?.isConnected) opener.focus()
    }
  }, [])
}
