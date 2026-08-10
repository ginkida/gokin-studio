export const WELCOME_METADATA_TIMEOUT_MS = 1500

export function welcomeMetadataReady(snapshotKey: string, languageReadyKey: string | null, gitReadyKey: string | null): boolean {
  return !!snapshotKey && languageReadyKey === snapshotKey && gitReadyKey === snapshotKey
}
