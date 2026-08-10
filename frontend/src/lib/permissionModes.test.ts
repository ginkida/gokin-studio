import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

test('composer exposes Accept edits as a distinct durable permission mode', () => {
  const source = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')
  for (const contract of [
    "type DurablePermissionMode = 'manual' | 'accept_edits' | 'auto' | 'skip'",
    "mode: 'accept_edits', label: 'Accept edits'",
    'Allow file edits; ask for shell and Git',
    "if (/^[1-5]$/.test(event.key))",
    "SetProjectPermissionMode(activeProjectId, nextMode)",
  ]) {
    assert.ok(source.includes(contract), `missing Accept edits composer contract: ${contract}`)
  }
})

test('scheduled tasks can retain the same Accept edits runtime policy', () => {
  const source = readFileSync(new URL('../components/chat/ScheduledTasksModal.tsx', import.meta.url), 'utf8')
  assert.ok(source.includes("'manual' | 'accept_edits' | 'auto' | 'skip'"))
  assert.ok(source.includes('<option value="accept_edits">Accept edits — allow file changes, ask for shell/Git</option>'))
})
