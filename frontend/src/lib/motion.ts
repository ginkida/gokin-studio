export function prefersReducedMotion(): boolean {
  return typeof window !== 'undefined'
    && typeof window.matchMedia === 'function'
    && window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

export function motionAwareBehavior(preferred: ScrollBehavior = 'smooth'): ScrollBehavior {
  return prefersReducedMotion() ? 'auto' : preferred
}

export function scrollIntoViewWithMotion(
  element: Element | null | undefined,
  options: Omit<ScrollIntoViewOptions, 'behavior'> = {},
): void {
  element?.scrollIntoView({ ...options, behavior: motionAwareBehavior() })
}

export function scrollToWithMotion(
  element: Element | null | undefined,
  options: Omit<ScrollToOptions, 'behavior'>,
): void {
  element?.scrollTo({ ...options, behavior: motionAwareBehavior() })
}
