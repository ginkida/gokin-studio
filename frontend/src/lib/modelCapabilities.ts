// Model catalogs publish round-number context labels (1M) while some
// endpoints expose power-of-two limits (262,144). Keep both readable without
// turning GLM's documented 1,000,000-token window into a misleading "977K".
export function formatContextWindow(tokens: number): string {
  if (tokens >= 1_000_000) return '1M'
  if (tokens >= 1024 && tokens % 1024 === 0) return `${tokens / 1024}K`
  if (tokens >= 1000) return `${Math.round(tokens / 1000)}K`
  return tokens.toLocaleString()
}
