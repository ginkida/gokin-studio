import { useCallback, useEffect, useRef, useState } from 'react'

function clampPanelWidth(value: number, minWidth: number, maxWidth: number) {
  return Math.min(maxWidth, Math.max(minWidth, Math.round(value)))
}

export function usePersistentPanelWidth(
  storageKey: string,
  defaultWidth: number,
  minWidth: number,
  maxWidth: number,
) {
  const initialWidth = () => {
    try {
      const stored = Number(localStorage.getItem(storageKey))
      if (Number.isFinite(stored) && stored > 0) {
        return clampPanelWidth(stored, minWidth, maxWidth)
      }
    } catch { /* localStorage unavailable */ }
    return clampPanelWidth(defaultWidth, minWidth, maxWidth)
  }

  const [width, setWidth] = useState(initialWidth)
  const widthRef = useRef(width)

  const updateWidth = useCallback((nextWidth: number, persist = true) => {
    const clamped = clampPanelWidth(nextWidth, minWidth, maxWidth)
    widthRef.current = clamped
    setWidth(clamped)
    if (persist) {
      try { localStorage.setItem(storageKey, String(clamped)) } catch { /* unavailable */ }
    }
  }, [maxWidth, minWidth, storageKey])

  useEffect(() => {
    const reset = () => updateWidth(defaultWidth, false)
    window.addEventListener('gokin:layout-reset', reset)
    return () => window.removeEventListener('gokin:layout-reset', reset)
  }, [defaultWidth, updateWidth])

  return { width, widthRef, updateWidth }
}
