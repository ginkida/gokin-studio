import { useRef } from 'react'
import type { KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent } from 'react'

type SplitViewResizerProps = {
  value: number
  min: number
  max: number
  minSecondary: number
  defaultValue: number
  label: string
  onChange: (value: number) => void
}

export function SplitViewResizer({
  value,
  min,
  max,
  minSecondary,
  defaultValue,
  label,
  onChange,
}: SplitViewResizerProps) {
  const rootRef = useRef<HTMLDivElement>(null)

  const effectiveMax = () => {
    const parentWidth = rootRef.current?.parentElement?.getBoundingClientRect().width
    return Math.max(min, Math.min(max, (parentWidth || max + minSecondary) - minSecondary))
  }

  const apply = (nextValue: number) => {
    onChange(Math.min(effectiveMax(), Math.max(min, nextValue)))
  }

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    const target = event.currentTarget
    const parent = target.parentElement
    if (!parent) return
    event.preventDefault()
    target.setPointerCapture(event.pointerId)
    target.classList.add('is-resizing')

    const handlePointerMove = (moveEvent: PointerEvent) => {
      const parentRect = parent.getBoundingClientRect()
      apply(moveEvent.clientX - parentRect.left)
    }
    const finish = () => {
      target.classList.remove('is-resizing')
      target.removeEventListener('pointermove', handlePointerMove)
      target.removeEventListener('pointerup', finish)
      target.removeEventListener('pointercancel', finish)
    }

    target.addEventListener('pointermove', handlePointerMove)
    target.addEventListener('pointerup', finish)
    target.addEventListener('pointercancel', finish)
  }

  const handleKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const step = event.shiftKey ? 24 : 8
    if (event.key === 'ArrowLeft') apply(value - step)
    else if (event.key === 'ArrowRight') apply(value + step)
    else if (event.key === 'Home') apply(min)
    else if (event.key === 'End') apply(effectiveMax())
    else return
    event.preventDefault()
  }

  return (
    <div
      ref={rootRef}
      className="split-view-resizer"
      role="separator"
      aria-label={label}
      aria-orientation="vertical"
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuenow={Math.round(value)}
      tabIndex={0}
      title={`${label}. Drag or use arrow keys. Double-click to reset.`}
      onPointerDown={handlePointerDown}
      onKeyDown={handleKeyDown}
      onDoubleClick={() => apply(defaultValue)}
    />
  )
}
