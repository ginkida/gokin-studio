// Token estimation for text the provider has not counted yet.
//
// The composer readout, the per-message chip, and the pre-first-response
// context gauge all used `text.length / 4`. That ratio only holds for ASCII:
// "4 characters per token" is an English rule of thumb, and character count is
// the wrong unit because BPE tokenizers operate on UTF-8 bytes. Cyrillic spends
// 2 bytes per character and CJK 3, so a Russian draft was reported at roughly
// half its real token count and a Chinese one at a third — under-reporting cost
// and, worse, under-reporting context usage, which is what drives the 75% / 90%
// warnings. A session could sit near its window while the gauge read comfortable.
//
// Counting bytes instead is exact for ASCII (identical to the old numbers) and
// materially closer for everything else. It errs high for Cyrillic — roughly
// 2 characters per token against a real 2-3 — which is the safe direction for a
// context gauge: warn slightly early rather than not at all.

/** UTF-8 byte length of `text`, computed without allocating an encoder buffer. */
export function utf8Length(text: string): number {
  let bytes = 0
  for (let i = 0; i < text.length; i++) {
    const code = text.charCodeAt(i)
    if (code < 0x80) {
      bytes += 1
    } else if (code < 0x800) {
      bytes += 2
    } else if (code >= 0xd800 && code <= 0xdbff && i + 1 < text.length) {
      // Surrogate pair: one astral code point, four UTF-8 bytes. Skip the low
      // half so it is not counted a second time as a lone 3-byte character.
      const low = text.charCodeAt(i + 1)
      if (low >= 0xdc00 && low <= 0xdfff) {
        bytes += 4
        i++
      } else {
        bytes += 3
      }
    } else {
      bytes += 3
    }
  }
  return bytes
}

/** Estimated tokens for an already-known UTF-8 byte total. */
export function estimateTokensFromBytes(bytes: number): number {
  if (bytes <= 0) return 0
  // Ceil, not round: non-empty text is never "0 tokens".
  return Math.ceil(bytes / 4)
}

/** Estimated tokens for `text`. Exact-equal to the old chars/4 for ASCII. */
export function estimateTokens(text: string): number {
  if (!text) return 0
  return estimateTokensFromBytes(utf8Length(text))
}
