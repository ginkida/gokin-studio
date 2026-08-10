export type TranscriptMode = 'normal' | 'verbose' | 'summary'

export interface TranscriptModeOption {
  id: TranscriptMode
  label: string
  description: string
}

export const TRANSCRIPT_MODE_OPTIONS: readonly TranscriptModeOption[] = [
  {
    id: 'normal',
    label: 'Normal',
    description: 'Collapse tool activity into compact summaries.',
  },
  {
    id: 'verbose',
    label: 'Verbose',
    description: 'Show every tool call, dispatch, and reasoning step.',
  },
  {
    id: 'summary',
    label: 'Summary',
    description: 'Show prompts, final replies, and changed-file summaries only.',
  },
] as const

export function parseTranscriptMode(value: string | null): TranscriptMode | null {
  return value === 'normal' || value === 'verbose' || value === 'summary' ? value : null
}

export function nextTranscriptMode(mode: TranscriptMode): TranscriptMode {
  const index = TRANSCRIPT_MODE_OPTIONS.findIndex((option) => option.id === mode)
  return TRANSCRIPT_MODE_OPTIONS[(index + 1) % TRANSCRIPT_MODE_OPTIONS.length].id
}

export function transcriptModeLabel(mode: TranscriptMode): string {
  return TRANSCRIPT_MODE_OPTIONS.find((option) => option.id === mode)?.label || 'Normal'
}
