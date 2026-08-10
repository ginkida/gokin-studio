import test from 'node:test'
import assert from 'node:assert/strict'
import { normalizePreviewElementSelection, previewElementDraft } from './previewSelection.ts'

test('preview selection keeps a fixed bounded metadata schema', () => {
  const selection = normalizePreviewElementSelection({
    selector: '#save-' + 'x'.repeat(900),
    element: {
      tag: 'button',
      text: 'Save '.repeat(300),
      rect: { x: 12.4, y: 20.7, width: 90.2, height: 31.8 },
      secret: 'must-not-survive',
    },
    ancestors: Array.from({ length: 10 }, (_, index) => ({ tag: 'div', id: `a-${index}`, rect: {} })),
    url: 'http://localhost/app?access_token=' + 'q'.repeat(4000) + '#private',
    title: 'Fixture',
    credential: 'must-not-survive',
    capturedAt: Number.POSITIVE_INFINITY,
  })
  assert.ok(selection)
  assert.equal(Array.from(selection.selector).length, 512)
  assert.equal(Array.from(selection.element.text).length, 500)
  assert.equal(selection.ancestors.length, 4)
  assert.equal(selection.url, 'http://localhost/app')
  assert.deepEqual(selection.element.rect, { x: 12, y: 21, width: 90, height: 32 })
  const serialized = JSON.stringify(selection)
  assert.doesNotMatch(serialized, /must-not-survive|credential|secret|access_token|private/)
})

test('preview selection rejects missing selector or element identity and formats a reviewable draft', () => {
  assert.equal(normalizePreviewElementSelection({ element: { tag: 'button' } }), null)
  assert.equal(normalizePreviewElementSelection({ selector: '#x', element: {} }), null)
  const selection = normalizePreviewElementSelection({
    selector: '#save',
    element: { tag: 'button', text: 'Save', rect: { x: 1, y: 2, width: 3, height: 4 } },
    ancestors: [],
    url: 'http://localhost/',
    title: 'Fixture',
    capturedAt: 123,
  })
  assert.ok(selection)
  const draft = previewElementDraft(selection)
  assert.match(draft, /#save/)
  assert.match(draft, /Requested change: $/)
  assert.match(draft, /Do not assume the DOM selector is a source-file path/)
})
