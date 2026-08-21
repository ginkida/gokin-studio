import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { clearOwnedAskUser, type AskUserQuestion } from '../stores/chatStore.ts'

const question = (questionID: string): AskUserQuestion => ({
  questionID,
  question: 'Proceed?',
  options: ['Allow', 'Deny'],
  default: 'Deny',
  askedAt: 1,
})

test('stale ask-user completion cannot clear a replacement question', () => {
  const current = { project_chat: question('new-owner') }
  const unchanged = clearOwnedAskUser(current, 'project_chat', 'old-owner')

  assert.equal(unchanged, current)
  assert.equal(unchanged.project_chat?.questionID, 'new-owner')

  const cleared = clearOwnedAskUser(current, 'project_chat', 'new-owner')
  assert.notEqual(cleared, current)
  assert.equal(cleared.project_chat, null)
})

test('ask-user lifecycle closes by exact question instead of chat completion', () => {
  const events = readFileSync(new URL('../hooks/useWailsEvents.ts', import.meta.url), 'utf8')
  const complete = events.slice(events.indexOf("EventsOn('chat:complete'"), events.indexOf("EventsOn('sessions:changed'"))
  const error = events.slice(events.indexOf("EventsOn('chat:error'"), events.indexOf("EventsOn('chat:ask_user'"))
  const ask = events.slice(events.indexOf("EventsOn('chat:ask_user'"), events.indexOf("EventsOn('chat:retry'"))

  assert.doesNotMatch(complete, /setAskUser\(key, null\)/)
  assert.doesNotMatch(error, /setAskUser\(key, null\)/)
  assert.match(ask, /chat:ask_user_closed/)
  assert.match(ask, /clearAskUser\(key, questionID\)/)
  assert.match(ask, /store\.askUser\[key\]\?\.questionID === questionID/)
})

test('answer and cancel callbacks clear only their captured question owner', () => {
  const panel = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')
  const card = panel.slice(panel.indexOf('{askUserQ && ('), panel.indexOf('{retryStatus && ('))

  assert.match(card, /await AnswerQuestion\(askUserQ\.questionID, answer\)[\s\S]*clearAskUser\(chatKey, askUserQ\.questionID\)/)
  assert.match(card, /await CancelQuestion\(askUserQ\.questionID\)[\s\S]*clearAskUser\(chatKey, askUserQ\.questionID\)/)
  assert.doesNotMatch(card, /setAskUser\(chatKey, null\)/)
})
