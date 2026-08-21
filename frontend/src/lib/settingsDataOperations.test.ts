import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('../components/settings/SettingsPage.tsx', import.meta.url), 'utf8')
const dataSection = source.slice(
  source.indexOf('{/* Backup & Restore (iter 750+) */}'),
  source.indexOf('{/* Reset preferences (iter 630+) */}'),
)

test('backup restore file selection has a keyboard-operable button', () => {
  assert.match(dataSection, /<button[\s\S]*?onClick=\{handleChooseRestore\}[\s\S]*?Restore from backup…[\s\S]*?<\/button>/)
  assert.match(dataSection, /<input[\s\S]*?ref=\{restoreFileInputRef\}[\s\S]*?type="file"[\s\S]*?hidden/)
  assert.doesNotMatch(dataSection, /<label[^>]*btn-secondary[^>]*>[\s\S]*?Restore from backup…/)
})

test('config-tree operations share one visible interaction lock', () => {
  const busyDefinition = source.slice(
    source.indexOf('const dataOperationBusy ='),
    source.indexOf('const dataOperationLabel ='),
  )
  for (const state of [
    'backupBusy',
    'restoreSelectionBusy',
    'restoreBusy',
    'backupDeleteBusy',
    'restoreBackupBusy',
    'autoDeleteBusy',
    'autoRestoreBusy',
    'cleanupBusy',
  ]) {
    assert.match(busyDefinition, new RegExp(`\\b${state}\\b`), `${state} must participate in the shared lock`)
  }

  assert.match(dataSection, /settings-section-content" aria-busy=\{dataOperationBusy \|\| restoreFileReading !== null\}/)
  assert.ok(
    [...dataSection.matchAll(/disabled=\{dataOperationBusy\}/g)].length >= 10,
    'backup, restore, delete, and snapshot controls must honor the shared lock',
  )
})

test('data operation progress and outcomes are announced accessibly', () => {
  assert.match(dataSection, /dataOperationLabel \|\| restoreFileReading[\s\S]*?role="status"[\s\S]*?aria-live="polite"/)
  assert.match(dataSection, /reset-prefs-confirm-text" role="status" aria-live="polite"/)
  assert.match(dataSection, /settings-toast success" role="status"/)
  assert.match(dataSection, /settings-toast error" role="alert"/)
  assert.match(source, /aria-label="Diagnostics" aria-busy=\{diagLoading \|\| cleanupBusy\}/)
  assert.match(source, /if \(cleanupBusy\) return[\s\S]*?setShowDiag\(false\)/)
})
