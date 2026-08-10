import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { workspaceMouseHistoryDirection } from './workspaceMouseNavigation.ts'

test('dedicated mouse buttons map only to workspace back and forward', () => {
  assert.equal(workspaceMouseHistoryDirection(3), -1)
  assert.equal(workspaceMouseHistoryDirection(4), 1)
  for (const button of [0, 1, 2, 5, -1]) {
    assert.equal(workspaceMouseHistoryDirection(button), null)
  }
})

test('app routes auxiliary mouse navigation through guarded workspace history', () => {
  const app = readFileSync(new URL('../App.tsx', import.meta.url), 'utf8')
  for (const contract of [
    "window.addEventListener('mouseup', handleMouseUp, true)",
    "window.addEventListener('auxclick', handleAuxClick, true)",
    'quickEntryOpenRef.current || hasOpenModal()',
    'navigateHistory(direction)',
    'event.preventDefault()',
  ]) {
    assert.ok(app.includes(contract), `missing mouse navigation contract: ${contract}`)
  }
})
