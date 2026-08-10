import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { isNewSessionShortcut } from './newSessionShortcut.ts'

const chord = (overrides: Partial<Parameters<typeof isNewSessionShortcut>[0]> = {}) => ({
  altKey: false,
  ctrlKey: false,
  metaKey: true,
  shiftKey: false,
  key: 'n',
  code: 'KeyN',
  ...overrides,
})

test('new session accepts the official chord across platforms and layouts', () => {
  assert.equal(isNewSessionShortcut(chord()), true)
  assert.equal(isNewSessionShortcut(chord({ metaKey: false, ctrlKey: true })), true)
  assert.equal(isNewSessionShortcut(chord({ key: 'т' })), true)
  assert.equal(isNewSessionShortcut(chord({ shiftKey: true })), false)
  assert.equal(isNewSessionShortcut(chord({ altKey: true })), false)
  assert.equal(isNewSessionShortcut(chord({ key: 'm', code: 'KeyM' })), false)
})

test('app routes New session globally and coalesces native/WebView delivery', () => {
  const app = readFileSync(new URL('../App.tsx', import.meta.url), 'utf8')
  for (const contract of [
    'isNewSessionShortcut(e)',
    'if (!id || creatingChatRef.current) return',
    'creatingChatRef.current = true',
    "window.dispatchEvent(new CustomEvent('gokin:add-project'))",
  ]) {
    assert.ok(app.includes(contract), `missing New session contract: ${contract}`)
  }
})
