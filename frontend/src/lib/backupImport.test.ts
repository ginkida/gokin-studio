import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import {
  BACKUP_EXPORT_DECODE_CHUNK_CHARS,
  BACKUP_IMPORT_UI_MAX_BYTES,
  decodeBackupBase64Chunks,
  validateBackupImportFile,
} from './backupImport.ts'

test('backup import rejects invalid files before allocating a base64 copy', () => {
  assert.equal(validateBackupImportFile({ size: 0 }), 'Backup file is empty.')
  assert.equal(validateBackupImportFile({ size: -1 }), 'Backup file has an invalid size.')
  assert.equal(validateBackupImportFile({ size: Number.MAX_SAFE_INTEGER + 1 }), 'Backup file has an invalid size.')
  assert.equal(validateBackupImportFile({ size: BACKUP_IMPORT_UI_MAX_BYTES }), null)
  assert.match(
    validateBackupImportFile({ size: BACKUP_IMPORT_UI_MAX_BYTES + 1 }) ?? '',
    /File too large .* Import limit is 100 MB/,
  )
})

test('settings validates the selected backup before constructing FileReader', () => {
  const source = readFileSync(new URL('../components/settings/SettingsPage.tsx', import.meta.url), 'utf8')
  const handler = source.slice(source.indexOf('const handleRestoreFile'), source.indexOf('const handleConfirmRestore'))

  const validation = handler.indexOf('validateBackupImportFile(file)')
  const allocation = handler.indexOf('new FileReader()')
  assert.ok(validation >= 0, 'restore handler must validate the selected file')
  assert.ok(allocation > validation, 'restore handler must validate before FileReader allocation')
  assert.doesNotMatch(handler, /reader\.onload[\s\S]*file\.size > 100 \* 1024 \* 1024/)
})

test('restore selection is latest-file-wins and aborts stale readers', () => {
  const source = readFileSync(new URL('../components/settings/SettingsPage.tsx', import.meta.url), 'utf8')
  const handler = source.slice(source.indexOf('const handleRestoreFile'), source.indexOf('const handleConfirmRestore'))

  assert.match(source, /const restoreFileRequestRef = useRef\(0\)/)
  assert.match(handler, /const requestID = \+\+restoreFileRequestRef\.current/)
  assert.match(handler, /previousReader\?\.readyState === FileReader\.LOADING[\s\S]*previousReader\.abort\(\)/)
  assert.match(handler, /reader\.onload = \(\) => \{[\s\S]*restoreFileRequestRef\.current !== requestID/)
  assert.match(handler, /reader\.onerror = \(\) => \{[\s\S]*restoreFileRequestRef\.current !== requestID/)
  assert.match(source, /useEffect\(\(\) => \(\) => \{[\s\S]*activeReader\?\.readyState === FileReader\.LOADING[\s\S]*activeReader\.abort\(\)/)
})

test('backup export base64 is decoded in bounded chunks without changing bytes', () => {
  const original = Uint8Array.from({ length: 29 }, (_, index) => (index * 37) % 256)
  const encoded = btoa(String.fromCharCode(...original))
  const decoded = decodeBackupBase64Chunks(encoded, 8, original.length)

  assert.ok(decoded.chunks.length > 1)
  assert.equal(decoded.byteLength, original.length)
  assert.deepEqual(decoded.chunks.flatMap((chunk) => [...chunk]), [...original])
  assert.equal(BACKUP_EXPORT_DECODE_CHUNK_CHARS % 4, 0)
})

test('backup export decoder rejects malformed, oversized, or invalidly configured input', () => {
  const encoded = btoa('bounded backup payload')
  assert.throws(() => decodeBackupBase64Chunks('', 8), /empty archive/)
  assert.throws(() => decodeBackupBase64Chunks('YQ==Yg==', 4), /invalid base64/)
  assert.throws(() => decodeBackupBase64Chunks('abc', 4), /invalid base64/)
  assert.throws(() => decodeBackupBase64Chunks('!!!!', 4), /invalid base64/)
  assert.throws(() => decodeBackupBase64Chunks(encoded, 6), /multiple of 4/)
  assert.throws(() => decodeBackupBase64Chunks(encoded, 8, 0), /positive safe integer/)
  assert.throws(() => decodeBackupBase64Chunks(encoded, 8, 'bounded backup payload'.length - 1), /download limit/)
})

test('settings uses chunked export decode and the shared safe Blob downloader', () => {
  const source = readFileSync(new URL('../components/settings/SettingsPage.tsx', import.meta.url), 'utf8')
  const handler = source.slice(source.indexOf('const handleBackup'), source.indexOf('const handleRestoreFile'))

  assert.match(handler, /decodeBackupBase64Chunks\(result\.base64\)/)
  assert.match(handler, /result\.size !== decoded\.byteLength/)
  assert.doesNotMatch(handler, /atob\(result\.base64\)/)
  assert.match(handler, /downloadBlob\(blob, result\.filename\)/)
})

test('settings prefers direct native backup export and keeps base64 only as compatibility fallback', () => {
  const source = readFileSync(new URL('../components/settings/SettingsPage.tsx', import.meta.url), 'utf8')
  const handler = source.slice(source.indexOf('const handleBackup'), source.indexOf('const handleRestoreFile'))

  const capabilityCheck = handler.indexOf("typeof nativeBridge?.ExportAllDataToFile === 'function'")
  const nativeExport = handler.indexOf('await ExportAllDataToFile()')
  const fallbackExport = handler.indexOf('await ExportAllDataBase64()')
  assert.ok(capabilityCheck >= 0)
  assert.ok(nativeExport > capabilityCheck)
  assert.ok(fallbackExport > nativeExport)
  assert.match(handler, /if \(result\.canceled\) return/)
  assert.match(handler, /result\.path/)
})

test('settings stages native restore behind a single-use review token and retains file-reader fallback', () => {
  const source = readFileSync(new URL('../components/settings/SettingsPage.tsx', import.meta.url), 'utf8')
  const selection = source.slice(source.indexOf('const handleChooseRestore'), source.indexOf('const handleCancelRestore'))
  const confirmation = source.slice(source.indexOf('const handleConfirmRestore'), source.indexOf('// Keep a process-local draft'))

  assert.match(selection, /typeof nativeBridge\?\.SelectRestoreArchiveFile !== 'function'/)
  assert.match(selection, /await SelectRestoreArchiveFile\(\)/)
  assert.match(selection, /restoreFileInputRef\.current\?\.click\(\)/)
  assert.match(selection, /restoreNativeRequestRef\.current !== requestID[\s\S]*DiscardSelectedRestoreArchive\(review\.token\)/)
  assert.match(selection, /source: 'native'[\s\S]*token: review\.token/)
  assert.match(confirmation, /pending\.source === 'native'[\s\S]*ConfirmSelectedRestoreArchive\(pending\.token\)[\s\S]*ImportAllDataBase64\(pending\.base64\)/)
  assert.match(source, /restoreNativeTokenRef\.current[\s\S]*DiscardSelectedRestoreArchive\(nativeToken\)/)
})
