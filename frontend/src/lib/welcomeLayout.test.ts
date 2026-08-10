import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { WELCOME_METADATA_TIMEOUT_MS, welcomeMetadataReady } from './welcomeLayout.ts'

test('welcome metadata is session-keyed and bounded before the stable surface mounts', () => {
  assert.equal(welcomeMetadataReady('project:chat', 'project:chat', 'project:chat'), true)
  assert.equal(welcomeMetadataReady('project:chat', 'other:chat', 'project:chat'), false)
  assert.equal(welcomeMetadataReady('project:chat', 'project:chat', null), false)
  assert.ok(WELCOME_METADATA_TIMEOUT_MS >= 500 && WELCOME_METADATA_TIMEOUT_MS <= 2000)

  const source = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')
  assert.ok(source.includes('welcomeSnapshotKey'), 'readiness must include the active workspace path')
  const readinessBranch = source.indexOf('!welcomeMetadataIsReady')
  const stableSurface = source.indexOf('data-welcome-layout="stable-v1"')
  assert.ok(readinessBranch >= 0 && stableSurface > readinessBranch, 'metadata gate must precede the visible welcome surface')
})

test('stable welcome CSS fixes every async text-bearing geometry slot', () => {
  const css = readFileSync(new URL('../App.css', import.meta.url), 'utf8')
  for (const contract of [
    '.chat-welcome[data-welcome-layout="stable-v1"] .chat-welcome-title',
    '.chat-welcome[data-welcome-layout="stable-v1"] .chat-welcome-hint',
    'grid-auto-rows: 58px',
    'height: 58px',
    'transition: background-color 120ms ease',
    'transform: none',
    '.chat-welcome[data-welcome-layout="stable-v1"] .welcome-git-header',
    'scrollbar-gutter: stable both-edges',
  ]) {
    assert.ok(css.includes(contract), `missing welcome layout contract: ${contract}`)
  }
})
