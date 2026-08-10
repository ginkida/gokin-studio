import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

test('approval UI exposes the bounded project-tool scope without weakening exact actions', () => {
  const chat = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')
  const store = readFileSync(new URL('../stores/chatStore.ts', import.meta.url), 'utf8')
  for (const contract of [
    "'current_turn_or_project_tool'",
    "'Turn or project'",
    'remember only the named tool in this project',
    'Hard-gated actions still ask',
  ]) {
    assert.ok(chat.includes(contract) || store.includes(contract), `missing approval scope contract: ${contract}`)
  }
})

test('context pane lists and revokes remembered tool permissions', () => {
  const context = readFileSync(new URL('../components/layout/ContextPanel.tsx', import.meta.url), 'utf8')
  for (const contract of [
    'ListProjectToolPermissions',
    'RevokeProjectToolPermission',
    'Always allowed tools',
    'Deletes, shell, network, connectors, SSH, browser, and computer actions still require exact review',
  ]) {
    assert.ok(context.includes(contract), `missing tool permission manager contract: ${contract}`)
  }
})
