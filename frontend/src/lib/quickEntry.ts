export type QuickEntryComposerAction = 'capture-desktop' | 'capture-selection'

export interface PendingQuickEntryComposerAction {
  id: string
  projectID: string
  sessionID: string
  action: QuickEntryComposerAction
}
