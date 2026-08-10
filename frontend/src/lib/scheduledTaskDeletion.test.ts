import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

test('scheduled task deletion defaults to retaining run chats and requires an explicit disk-data choice', () => {
  const source = readFileSync(new URL('../components/chat/ScheduledTasksModal.tsx', import.meta.url), 'utf8')
  for (const contract of [
    'GetScheduledTaskDeletionPreview',
    'DeleteScheduledTaskWithData',
    'deleteRunData: false',
    'Also delete run data from this device',
    'including transcripts, drafts, artifacts, and clean isolated worktrees',
    'uncommitted worktree changes blocks the entire deletion before anything is removed',
    'cannot authorize deleting a chat',
  ]) {
    assert.ok(source.includes(contract), `missing scheduled deletion contract: ${contract}`)
  }
})

test('scheduled task deletion sheet is a distinct modal above the scheduler', () => {
  const source = readFileSync(new URL('../components/chat/ScheduledTasksModal.tsx', import.meta.url), 'utf8')
  const css = readFileSync(new URL('../App.css', import.meta.url), 'utf8')
  assert.ok(source.includes('role="alertdialog"'))
  assert.ok(source.includes('aria-modal="true"'))
  assert.match(css, /\.scheduled-delete-backdrop\s*\{[^}]*z-index:\s*204/s)
})
