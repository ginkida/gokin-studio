import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { sessionCycleShortcutDirection } from './sessionCycleShortcuts.ts'

function shortcut(overrides: Partial<Parameters<typeof sessionCycleShortcutDirection>[0]> = {}) {
  return {
    altKey: false,
    ctrlKey: true,
    metaKey: false,
    shiftKey: false,
    key: 'Tab',
    code: 'Tab',
    ...overrides,
  }
}

test('desktop session cycling supports official and compatibility chords', () => {
  assert.equal(sessionCycleShortcutDirection(shortcut()), 1)
  assert.equal(sessionCycleShortcutDirection(shortcut({ shiftKey: true })), -1)
  assert.equal(sessionCycleShortcutDirection(shortcut({ ctrlKey: false, metaKey: true, shiftKey: true, key: '{', code: 'BracketLeft' })), -1)
  assert.equal(sessionCycleShortcutDirection(shortcut({ ctrlKey: false, metaKey: true, shiftKey: true, key: 'ъ', code: 'BracketRight' })), 1)
  assert.equal(sessionCycleShortcutDirection(shortcut({ key: 'PageUp', code: 'PageUp' })), -1)
  assert.equal(sessionCycleShortcutDirection(shortcut({ key: 'PageDown', code: 'PageDown' })), 1)
  assert.equal(sessionCycleShortcutDirection(shortcut({ altKey: true })), null)
  assert.equal(sessionCycleShortcutDirection(shortcut({ ctrlKey: false, metaKey: true })), null)
})

test('app sends every session cycling chord through one modal-aware route', () => {
  const app = readFileSync(new URL('../App.tsx', import.meta.url), 'utf8')
  for (const contract of [
    'sessionCycleShortcutDirection(e)',
    "window.dispatchEvent(new CustomEvent('gokin:cycle-session'",
    'if (isGlobalShortcut || sessionCycleDirection !== null || isSessionShortcut) e.preventDefault()',
  ]) {
    assert.ok(app.includes(contract), `missing session-cycle contract: ${contract}`)
  }
})
