import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const panel = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')

test('tool presentation follows current coordination capabilities', () => {
  assert.doesNotMatch(panel, /name === 'ask_agent'/)
  assert.doesNotMatch(panel, /case 'ask_agent'/)
  assert.doesNotMatch(panel, /name === 'request_tool'/)
  assert.doesNotMatch(panel, /case 'request_tool'/)

  assert.match(panel, /name === 'delegate'/)
  assert.match(panel, /name === 'session_agent'/)
  assert.match(panel, /case 'delegate': return 'Delegated'/)
  assert.match(panel, /case 'session_agent': return 'Coordinated'/)
})
