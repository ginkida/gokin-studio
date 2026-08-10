import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { MAX_EXTERNAL_CHAT_LINK_BYTES, normalizeExternalHTTPLink } from './externalLinks.ts'

test('external chat links accept only bounded credential-free HTTP(S) URLs', () => {
  assert.deepEqual(normalizeExternalHTTPLink(' https://example.com/docs?q=1 '), {
    url: 'https://example.com/docs?q=1',
    display: 'https://example.com/docs?q=1',
    origin: 'https://example.com',
  })
  assert.equal(normalizeExternalHTTPLink('http://localhost:5173/')?.origin, 'http://localhost:5173')
  for (const value of [
    'file:///tmp/secret',
    'javascript:alert(1)',
    'https://user:secret@example.com/',
    'https://example.com/\nnext',
    `https://example.com/${'a'.repeat(MAX_EXTERNAL_CHAT_LINK_BYTES)}`,
    '',
  ]) {
    assert.equal(normalizeExternalHTTPLink(value), null, value)
  }
})

test('chat links offer an explicit in-app or default-browser chooser with modifier bypass', () => {
  const chat = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')
  const workspace = readFileSync(new URL('../components/layout/SessionWorkspace.tsx', import.meta.url), 'utf8')
  for (const contract of [
    'event.metaKey || event.ctrlKey',
    'BrowserOpenURL(externalLink.url)',
    "new CustomEvent('gokin:open-external-browser'",
    'bubbles: true',
  ]) assert.ok(chat.includes(contract), `missing chat link contract: ${contract}`)
  for (const contract of [
    "title: 'Open external link'",
    "value: 'app', label: 'Open in app'",
    "value: 'system', label: 'Default browser'",
    'if (hasOpenModal()) return',
    'workspaceRef.current?.contains(event.target)',
    'BrowserOpenURL(link.url)',
  ]) assert.ok(workspace.includes(contract), `missing external chooser contract: ${contract}`)
})
