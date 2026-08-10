import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { paneShortcutAction } from './paneShortcuts.ts'

const chord = (overrides: Partial<Parameters<typeof paneShortcutAction>[0]> = {}) => ({
  altKey: false,
  ctrlKey: false,
  metaKey: true,
  shiftKey: true,
  key: 'd',
  code: 'KeyD',
  ...overrides,
})

test('pane shortcuts match the desktop contract and physical keys', () => {
  assert.equal(paneShortcutAction(chord()), 'diff')
  assert.equal(paneShortcutAction(chord({ key: 'и', code: 'KeyB' })), 'preview')
  assert.equal(paneShortcutAction(chord({ key: 'з', code: 'KeyP' })), 'preview')
  assert.equal(paneShortcutAction(chord({ key: 'ы', code: 'KeyS' })), 'select-preview-element')
  assert.equal(paneShortcutAction(chord({ metaKey: false, ctrlKey: true, shiftKey: false, key: '`', code: 'Backquote' })), 'terminal')
  assert.equal(paneShortcutAction(chord({ metaKey: true, ctrlKey: false, shiftKey: false, key: '`', code: 'Backquote' })), null)
  assert.equal(paneShortcutAction(chord({ altKey: true })), null)
})

test('app owns one capture-phase pane shortcut route', () => {
  const app = readFileSync(new URL('../App.tsx', import.meta.url), 'utf8')
  for (const contract of [
    'paneShortcutAction(event)',
    "window.addEventListener('keydown', handlePaneShortcut, true)",
    'event.stopImmediatePropagation()',
    "terminal: 'gokin:toggle-workspace-terminal'",
  ]) {
    assert.ok(app.includes(contract), `missing pane shortcut route: ${contract}`)
  }
})
