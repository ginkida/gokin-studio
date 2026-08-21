import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import {
  latestTurnHasMarkedError,
  type ChatMessage,
} from '../stores/chatStore.ts'

const message = (
  role: ChatMessage['role'],
  content: string,
  isError = false,
): ChatMessage => ({
  id: `${role}-${content}`,
  role,
  content,
  isError: isError || undefined,
  timestamp: 1,
})

test('chat completion deduplicates the terminal notification after chat error', () => {
  const failedTurn = [
    message('user', 'run it'),
    message('thinking', 'working'),
    message('assistant', 'partial output'),
    message('tool', 'tool failed'),
    message('assistant', 'Error: provider unavailable', true),
  ]
  assert.equal(latestTurnHasMarkedError(failedTurn), true)

  const newTurn = [...failedTurn, message('user', 'try again')]
  assert.equal(latestTurnHasMarkedError(newTurn), false)

  // Model-authored text may legitimately begin with "Error:"; only the
  // semantic event marker suppresses a terminal completion notification.
  assert.equal(latestTurnHasMarkedError([
    message('user', 'explain the log'),
    message('assistant', 'Error: means the operation failed'),
  ]), false)
})

test('Wails chat error marks its message and chat complete gates duplicate alerts', () => {
  const events = readFileSync(new URL('../hooks/useWailsEvents.ts', import.meta.url), 'utf8')
  const complete = events.slice(events.indexOf("EventsOn('chat:complete'"), events.indexOf("EventsOn('sessions:changed'"))
  const error = events.slice(events.indexOf("EventsOn('chat:error'"), events.indexOf("EventsOn('chat:ask_user'"))

  assert.match(error, /finalizeAssistant\(key, `Error: \$\{text\}`, true\)/)
  assert.match(complete, /latestTurnHasMarkedError/)
  assert.match(complete, /!completedTurnFailed[\s\S]*store\.bumpUnread\(key\)/)
  assert.match(complete, /!completedTurnFailed[\s\S]*notifyIfBlurred/)
})
