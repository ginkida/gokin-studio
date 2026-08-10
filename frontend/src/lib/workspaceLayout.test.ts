import assert from 'node:assert/strict'
import test from 'node:test'
import {
  collectWorkspacePanes,
  defaultWorkspaceLayout,
  legacyWorkspaceLayoutStorageKey,
  moveWorkspacePane,
  normalizeWorkspaceLayout,
  readWorkspaceLayout,
  resizeWorkspaceSplit,
  setWorkspacePaneOpen,
  workspaceLayoutStorageKey,
  type WorkspaceLayoutNode,
  type WorkspacePaneID,
} from './workspaceLayout.ts'

function parentOf(node: WorkspaceLayoutNode, pane: WorkspacePaneID): WorkspaceLayoutNode | null {
  if (node.kind === 'pane') return null
  if ((node.first.kind === 'pane' && node.first.pane === pane) || (node.second.kind === 'pane' && node.second.pane === pane)) return node
  return parentOf(node.first, pane) || parentOf(node.second, pane)
}

test('workspace split tree rejects duplicate/unknown panes and preserves chat', () => {
  const normalized = normalizeWorkspaceLayout({
    version: 2,
    root: {
      kind: 'split',
      axis: 'vertical',
      ratio: 99,
      first: { kind: 'pane', pane: 'preview' },
      second: {
        kind: 'split',
        axis: 'sideways',
        ratio: -4,
        first: { kind: 'pane', pane: 'preview' },
        second: { kind: 'pane', pane: 'unknown' },
      },
    },
  })
  assert.deepEqual(normalized.open, ['chat', 'preview'])
  assert.equal(normalized.root.kind, 'split')
  assert.equal(normalized.root.ratio, 0.68)
  assert.ok(setWorkspacePaneOpen(normalized, 'chat', false).open.includes('chat'))
})

test('panes dock on all axes, resize by split path, and collapse cleanly on close', () => {
  let layout = defaultWorkspaceLayout()
  layout = setWorkspacePaneOpen(layout, 'terminal', true, 'chat')
  const terminalParent = parentOf(layout.root, 'terminal')
  assert.ok(terminalParent?.kind === 'split')
  assert.equal(terminalParent.axis, 'vertical')

  layout = moveWorkspacePane(layout, 'context', 'terminal', 'top')
  const contextParent = parentOf(layout.root, 'context')
  assert.ok(contextParent?.kind === 'split')
  assert.equal(contextParent.axis, 'vertical')
  assert.deepEqual(collectWorkspacePanes(contextParent), ['context', 'terminal'])

  layout = resizeWorkspaceSplit(layout, [], 0.73)
  assert.equal(layout.root.kind, 'split')
  assert.equal(layout.root.ratio, 0.73)

  layout = setWorkspacePaneOpen(layout, 'terminal', false)
  assert.ok(!layout.open.includes('terminal'))
  assert.deepEqual(new Set(layout.open), new Set(['chat', 'context']))
})

test('legacy v1 layout migrates to a persisted 2D tree without losing open panes', () => {
  const project = 'project'
  const session = 'session'
  const legacy = JSON.stringify({
    order: ['terminal', 'chat', 'context'],
    open: ['terminal', 'context'],
    widths: { terminal: 500, chat: 700, context: 300 },
  })
  const storage = {
    getItem(key: string) {
      return key === legacyWorkspaceLayoutStorageKey(project, session) ? legacy : null
    },
  }
  const layout = readWorkspaceLayout(project, session, storage)
  assert.equal(layout.version, 2)
  assert.deepEqual(layout.open, ['terminal', 'chat', 'context'])
  assert.equal(layout.root.kind, 'split')
  assert.equal(layout.root.axis, 'horizontal')
})

test('workspace layout keys are versioned, scoped, and escaped per project/session', () => {
  assert.match(workspaceLayoutStorageKey('a', 'b'), /:v2:/)
  assert.notEqual(workspaceLayoutStorageKey('a:b', 'c'), workspaceLayoutStorageKey('a', 'b:c'))
  assert.notEqual(workspaceLayoutStorageKey('a', 'b'), legacyWorkspaceLayoutStorageKey('a', 'b'))
})
