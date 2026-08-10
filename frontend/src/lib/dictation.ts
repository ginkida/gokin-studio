// Merge platform speech-recognition output into a stable reviewable draft.
// Interim results replace the previous interim segment because callers keep
// the pre-dictation base separately; final text is promoted by the browser on
// later result events without duplicating already-rendered words.
export function composeDictationDraft(
  base: string,
  finalTranscript: string,
  interimTranscript: string,
  maxChars: number,
): string {
  const speech = [finalTranscript, interimTranscript]
    .map((part) => part.replace(/\s+/g, ' ').trim())
    .filter(Boolean)
    .join(' ')
  if (!speech) return base
  const separator = base.length > 0 && !/\s$/.test(base) ? ' ' : ''
  let next = (base + separator + speech).slice(0, maxChars)
  // Avoid leaving an unmatched UTF-16 high surrogate when the hard composer
  // limit lands in the middle of an emoji or supplementary character.
  if (next.length > 0) {
    const lastCodeUnit = next.charCodeAt(next.length - 1)
    if (lastCodeUnit >= 0xD800 && lastCodeUnit <= 0xDBFF) next = next.slice(0, -1)
  }
  return next
}
