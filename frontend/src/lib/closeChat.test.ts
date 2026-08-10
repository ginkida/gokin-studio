import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { isCloseChatShortcut, resolveCloseChatSession } from './closeChat.ts'

function shortcut(overrides: Partial<Parameters<typeof isCloseChatShortcut>[0]> = {}) {
  return {
    altKey: false,
    ctrlKey: false,
    metaKey: true,
    shiftKey: false,
    key: 'w',
    code: 'KeyW',
    ...overrides,
  }
}

test('close chat accepts the desktop chord and physical key across layouts', () => {
  assert.equal(isCloseChatShortcut(shortcut()), true)
  assert.equal(isCloseChatShortcut(shortcut({ metaKey: false, ctrlKey: true })), true)
  assert.equal(isCloseChatShortcut(shortcut({ key: 'ц' })), true)
  assert.equal(isCloseChatShortcut(shortcut({ altKey: true })), false)
  assert.equal(isCloseChatShortcut(shortcut({ shiftKey: true })), false)
  assert.equal(isCloseChatShortcut(shortcut({ metaKey: false, ctrlKey: false })), false)
  assert.equal(isCloseChatShortcut(shortcut({ key: 'x', code: 'KeyX' })), false)
})

test('close chat resolves only the exact selected loaded session', () => {
  assert.equal(resolveCloseChatSession('chat-b', 'chat-a', ['chat-a', 'chat-b']), 'chat-b')
  assert.equal(resolveCloseChatSession('files', 'chat-a', ['chat-a', 'chat-b']), 'chat-a')
  assert.equal(resolveCloseChatSession('files', 'stale', ['chat-a', 'chat-b']), null)
  assert.equal(resolveCloseChatSession('chat-a', 'chat-a', ['chat-a']), null)
})

test('app routes close through guarded reversible archive semantics', () => {
  const app = readFileSync(new URL('../App.tsx', import.meta.url), 'utf8')
  for (const contract of [
    "window.dispatchEvent(new CustomEvent('gokin:archive-active-session'))",
    "window.addEventListener('gokin:archive-active-session', archiveActiveSession)",
    "e.code === 'KeyW'",
    'quickEntryOpenRef.current || hasOpenModal()',
    'archivingSessionIDsRef.current.has(operationKey)',
    'await ArchiveChatSession(projectID, sessionId)',
  ]) {
    assert.ok(app.includes(contract), `missing close-chat contract: ${contract}`)
  }
})
