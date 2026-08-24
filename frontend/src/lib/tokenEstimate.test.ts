import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { estimateTokens, estimateTokensFromBytes, utf8Length } from './tokenEstimate.ts'

test('utf8Length matches TextEncoder across scripts and astral planes', () => {
  const encoder = new TextEncoder()
  for (const sample of [
    '',
    'plain ascii text',
    'привет, как дела',
    '你好世界',
    'emoji 👍🏽 family 👨‍👩‍👧',
    'mixed: ascii + привет + 世界 + 🚀',
    '\u{10FFFF}',
    // A lone high surrogate is not a valid pair; it must still be counted once.
    '\ud800',
    'a\ud800b',
  ]) {
    assert.equal(utf8Length(sample), encoder.encode(sample).length, `byte length for ${JSON.stringify(sample)}`)
  }
})

test('ASCII estimates are unchanged by the switch to bytes', () => {
  // The old formula was chars/4 and ASCII is one byte per char, so every
  // pre-existing ASCII number must survive the change untouched.
  for (const sample of ['a', 'four', 'exactly eight!!', 'a'.repeat(4000)]) {
    assert.equal(estimateTokens(sample), Math.ceil(sample.length / 4))
  }
})

test('Cyrillic and CJK are no longer under-reported', () => {
  const russian = 'привет'
  // Old behaviour: 6/4 -> 2 tokens. Real cost is 3-4. Bytes give 12/4 = 3.
  assert.equal(Math.ceil(russian.length / 4), 2, 'guard: the old formula reported 2')
  assert.equal(estimateTokens(russian), 3)

  const chinese = '你好世界'
  // Old behaviour: 4/4 -> 1 token for four characters that cost ~4-6.
  assert.equal(Math.ceil(chinese.length / 4), 1, 'guard: the old formula reported 1')
  assert.equal(estimateTokens(chinese), 3)

  // The estimate must never come out below the old one, so a context gauge can
  // only move toward warning earlier, never later.
  for (const sample of ['привет мир', '你好', 'ascii', 'смешанный mixed 混合']) {
    assert.ok(
      estimateTokens(sample) >= Math.ceil(sample.length / 4),
      `estimate regressed downward for ${JSON.stringify(sample)}`,
    )
  }
})

test('empty and non-positive inputs estimate to zero, non-empty never does', () => {
  assert.equal(estimateTokens(''), 0)
  assert.equal(estimateTokensFromBytes(0), 0)
  assert.equal(estimateTokensFromBytes(-5), 0)
  // A single character must not display as "0 tokens".
  assert.equal(estimateTokens('a'), 1)
  assert.equal(estimateTokens('я'), 1)
})

// The whole point is that no call site keeps its own private ratio.
test('ChatPanel routes every token estimate through the shared helper', () => {
  const panel = readFileSync(new URL('../components/chat/ChatPanel.tsx', import.meta.url), 'utf8')
  assert.ok(panel.includes("from '../../lib/tokenEstimate'"), 'ChatPanel must import the shared estimator')
  const strays = panel.match(/\.length \/ 4\b/g) || []
  assert.deepEqual(strays, [], `open-coded chars/4 token estimates remain: ${strays.length}`)
})
