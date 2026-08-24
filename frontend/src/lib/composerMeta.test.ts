import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const css = readFileSync(new URL('../App.css', import.meta.url), 'utf8')
const panel = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')

// Returns the declaration body of the first rule whose selector matches exactly,
// so a later `@container` override of the same class cannot be mistaken for the
// base rule.
function ruleBody(selector: string): string {
  const start = css.indexOf(`\n${selector} {`)
  assert.ok(start >= 0, `no base rule for ${selector}`)
  const open = css.indexOf('{', start)
  const close = css.indexOf('}', open)
  assert.ok(close > open, `unterminated rule for ${selector}`)
  return css.slice(open + 1, close)
}

// The counter text ("N chars · ~N tokens · ≈$N input") changes length on every
// keystroke. While the meta row shrink-wrapped its content it sat flush right,
// which meant the counter grew leftward out of the send hint and every single
// character re-laid out the whole string — the row jittered as the user typed.
// Both edges must stay pinned: the row takes the remaining footer width, the
// counter is anchored left and grows into empty space, the hint stays right.
test('composer meta row pins both edges so typing cannot shift it', () => {
  const meta = ruleBody('.input-bar-meta')
  assert.match(meta, /\bflex:\s*1\b/, 'meta row must claim the remaining footer width')
  assert.match(meta, /\bjustify-content:\s*space-between\b/, 'counter and hint must be pushed to opposite edges')
  assert.match(meta, /\bmin-width:\s*0\b/, 'the row must still be allowed to shrink')
})

test('composer char counter keeps a stable run width', () => {
  const counter = ruleBody('.input-bar-meta .chat-char-count')
  assert.match(
    counter,
    /\bfont-variant-numeric:\s*tabular-nums\b/,
    'digits must be equal width so 1 -> 2 -> 3 does not resize the run',
  )
  assert.match(counter, /\bwhite-space:\s*nowrap\b/, 'the readout must never wrap onto a second line')
  assert.match(counter, /\btext-overflow:\s*ellipsis\b/, 'an overlong readout must ellipsize rather than push the hint')
})

// Order matters for which element is the anchor: the hint is the element the
// eye rests on between keystrokes, so it has to be the one flush against the
// right edge, with the mutable counter ahead of it.
test('the send hint is the right-anchored element of the meta row', () => {
  const row = panel.indexOf('className="input-bar-meta"')
  assert.ok(row >= 0, 'composer meta row not found in ChatPanel')
  const counter = panel.indexOf('chat-char-count', row)
  const hint = panel.indexOf('chat-send-hint', row)
  assert.ok(counter > row, 'counter must live inside the meta row')
  assert.ok(hint > counter, 'the send hint must come after the counter')
})
