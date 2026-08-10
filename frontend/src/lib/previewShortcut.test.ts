import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

test('Browser and Preview expose the official desktop shortcut with the legacy alias', () => {
  const shortcuts = readFileSync(new URL('./paneShortcuts.ts', import.meta.url), 'utf8')
  for (const contract of [
    "event.code === 'KeyB'",
    "event.code === 'KeyP'",
    "return 'preview'",
  ]) {
    assert.ok(shortcuts.includes(contract), `missing Browser shortcut contract: ${contract}`)
  }

  const palette = readFileSync(new URL('../components/palette/CommandPalette.tsx', import.meta.url), 'utf8')
  assert.ok(palette.includes("hint: 'Ctrl+Shift+B'"))
})
