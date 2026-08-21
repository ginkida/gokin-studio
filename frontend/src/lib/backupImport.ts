// Older desktop bridges carry imports as base64 strings, so the compatibility
// path briefly needs both the original bytes and an approximately 33% larger
// string inside the WebView. Native restore uses the same user-facing bound so
// every manual export remains restorable through either bridge generation.
export const BACKUP_IMPORT_UI_MAX_BYTES = 100 * 1024 * 1024

// Keep each temporary binary string small while turning an older
// bridge-provided backup into Blob parts. The old one-shot atob() path held the
// 100 MiB archive, a 100 MiB binary string, and the 133 MiB base64 payload at
// the same time. This caps the extra binary-string allocation at 192 KiB.
export const BACKUP_EXPORT_DECODE_CHUNK_CHARS = 256 * 1024

export type DecodedBackupBase64 = {
  chunks: Uint8Array[]
  byteLength: number
}

type BackupImportFile = Pick<File, 'size'>

export function validateBackupImportFile(file: BackupImportFile): string | null {
  if (!Number.isSafeInteger(file.size) || file.size < 0) {
    return 'Backup file has an invalid size.'
  }
  if (file.size === 0) {
    return 'Backup file is empty.'
  }
  if (file.size > BACKUP_IMPORT_UI_MAX_BYTES) {
    const sizeMB = (file.size / (1024 * 1024)).toFixed(1)
    return `File too large (${sizeMB} MB). Import limit is 100 MB.`
  }
  return null
}

export function decodeBackupBase64Chunks(
  encoded: string,
  chunkChars = BACKUP_EXPORT_DECODE_CHUNK_CHARS,
  maxDecodedBytes = BACKUP_IMPORT_UI_MAX_BYTES,
): DecodedBackupBase64 {
  if (typeof encoded !== 'string' || encoded.length === 0) {
    throw new Error('Backup export returned an empty archive.')
  }
  if (!Number.isSafeInteger(chunkChars) || chunkChars < 4 || chunkChars % 4 !== 0) {
    throw new TypeError('Backup decode chunk size must be a positive multiple of 4.')
  }
  if (!Number.isSafeInteger(maxDecodedBytes) || maxDecodedBytes < 1) {
    throw new TypeError('Backup decode limit must be a positive safe integer.')
  }
  // ExportAllDataBase64 uses Go's padded StdEncoding. Enforcing that shape
  // keeps independent chunk decoding equivalent to one strict atob() call;
  // in particular, padding cannot terminate one chunk and hide later data.
  if (encoded.length % 4 !== 0) {
    throw new Error('Backup export returned invalid base64 data.')
  }
  const firstPadding = encoded.indexOf('=')
  if (
    firstPadding >= 0 &&
    (firstPadding < encoded.length - 2 ||
      (firstPadding === encoded.length - 2 && encoded[encoded.length - 1] !== '='))
  ) {
    throw new Error('Backup export returned invalid base64 data.')
  }

  const maxEncodedChars = 4 * Math.ceil(maxDecodedBytes / 3)
  if (encoded.length > maxEncodedChars) {
    throw new Error(`Backup export exceeds the ${Math.floor(maxDecodedBytes / (1024 * 1024))} MB download limit.`)
  }

  const chunks: Uint8Array[] = []
  let byteLength = 0
  try {
    for (let offset = 0; offset < encoded.length; offset += chunkChars) {
      const binary = atob(encoded.slice(offset, Math.min(offset + chunkChars, encoded.length)))
      byteLength += binary.length
      if (byteLength > maxDecodedBytes) {
        throw new Error(`Backup export exceeds the ${Math.floor(maxDecodedBytes / (1024 * 1024))} MB download limit.`)
      }
      const bytes = new Uint8Array(binary.length)
      for (let index = 0; index < binary.length; index++) {
        bytes[index] = binary.charCodeAt(index)
      }
      chunks.push(bytes)
    }
  } catch (error) {
    if (error instanceof Error && error.message.startsWith('Backup export exceeds')) throw error
    throw new Error('Backup export returned invalid base64 data.')
  }
  return { chunks, byteLength }
}
