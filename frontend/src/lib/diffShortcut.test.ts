import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

test('official Diff shortcut owns one guarded workspace toggle route', () => {
  const workspace = readFileSync(new URL('../components/layout/SessionWorkspace.tsx', import.meta.url), 'utf8')
  for (const contract of [
    "window.addEventListener('gokin:toggle-diff-pane', toggleDiff)",
    "const toggleDiff = () => toggle('diff')",
  ]) {
    assert.ok(workspace.includes(contract), `missing Diff shortcut contract: ${contract}`)
  }

  const shortcuts = readFileSync(new URL('./paneShortcuts.ts', import.meta.url), 'utf8')
  for (const contract of [
    "event.code === 'KeyD'",
    "return 'diff'",
  ]) {
    assert.ok(shortcuts.includes(contract), `missing global Diff routing contract: ${contract}`)
  }

  const chat = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')
  const quickEntry = readFileSync(new URL('../components/quickentry/QuickEntryOverlay.tsx', import.meta.url), 'utf8')
  assert.doesNotMatch(chat, /shiftKey[^\n]+key\.toLowerCase\(\) !== 'd'/)
  assert.doesNotMatch(quickEntry, /shiftKey[^\n]+key\.toLowerCase\(\) === 'd'/)
})
