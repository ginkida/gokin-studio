import assert from 'node:assert/strict'
import test from 'node:test'
import { ONBOARDING_DISMISSED_KEY, shouldShowOnboarding } from './onboarding.ts'

test('onboarding is shown only for a fresh, non-dismissed workspace', () => {
  const freshStorage = { getItem: () => null }
  const dismissedStorage = {
    getItem: (key: string) => key === ONBOARDING_DISMISSED_KEY ? '1' : null,
  }

  assert.equal(shouldShowOnboarding(0, freshStorage), true)
  assert.equal(shouldShowOnboarding(0, dismissedStorage), false)
  assert.equal(shouldShowOnboarding(1, freshStorage), false)
})

test('onboarding fails open when WebView storage cannot be read', () => {
  const blockedStorage = {
    getItem: () => { throw new Error('storage blocked') },
  }

  assert.equal(shouldShowOnboarding(0, null), true)
  assert.equal(shouldShowOnboarding(0, blockedStorage), true)
})
